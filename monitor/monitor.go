// Package monitor implementa la fase "M" del MAPE-K: solo lee, nunca decide
// ni actúa. Su única responsabilidad es producir un types.MetricSnapshot.
package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"autoscaler-controller/types"
)

type Monitor struct {
	cw  cloudWatchClient
	elb elbClient
	cfg types.Config
}

type cloudWatchClient interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

type elbClient interface {
	DescribeTargetHealth(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

func New(cw cloudWatchClient, elb elbClient, cfg types.Config) *Monitor {
	return &Monitor{cw: cw, elb: elb, cfg: cfg}
}

// Collect llama a CloudWatch (GetMetricData) para CPU promedio y a ELBv2
// (DescribeTargetHealth) para saber cuántos targets están realmente sanos
// -- esto último es clave para no contar instancias que aún están en
// warm-up o unhealthy como capacidad disponible.
//
// currentInstanceIDs viene del KnowledgeState en memoria, no de una
// dimensión "AutoScalingGroupName" -- como no usamos un Auto Scaling Group
// gestionado, no existe esa agregación automática. Pedimos CPUUtilization
// por cada InstanceId en la misma llamada (GetMetricData admite hasta 500
// queries por request) y promediamos nosotros mismos.
func (m *Monitor) Collect(ctx context.Context, currentInstanceIDs []string) (types.MetricSnapshot, error) {
	end := time.Now()
	start := end.Add(-m.cfg.MetricWindow)

	if len(currentInstanceIDs) == 0 {
		// No hay instancias activas todavía: no hay CPU que consultar.
		return types.MetricSnapshot{Timestamp: end}, nil
	}

	queries := make([]cwtypes.MetricDataQuery, 0, len(currentInstanceIDs))
	for i, instanceID := range currentInstanceIDs {
		queries = append(queries, cwtypes.MetricDataQuery{
			Id: aws.String(fmt.Sprintf("cpu%d", i)), // ids deben ser únicos y alfanuméricos
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/EC2"),
					MetricName: aws.String("CPUUtilization"),
					Dimensions: []cwtypes.Dimension{
						{Name: aws.String("InstanceId"), Value: aws.String(instanceID)},
					},
				},
				Period: aws.Int32(60),
				Stat:   aws.String("Average"),
			},
		})
	}

	out, err := m.cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(end),
		MetricDataQueries: queries,
	})
	if err != nil {
		return types.MetricSnapshot{}, fmt.Errorf("cloudwatch GetMetricData: %w", err)
	}

	// Promediamos primero en el tiempo (por instancia) y luego entre
	// instancias
	var sumOfInstanceAverages float64
	var instancesWithData int
	for _, result := range out.MetricDataResults {
		if len(result.Values) == 0 {
			continue // instancia recién lanzada, aún sin datapoints (warm-up)
		}
		sum := 0.0
		for _, v := range result.Values {
			sum += v
		}
		sumOfInstanceAverages += sum / float64(len(result.Values))
		instancesWithData++
	}

	var avgCPU float64
	if instancesWithData > 0 {
		avgCPU = sumOfInstanceAverages / float64(instancesWithData)
	}

	healthOut, err := m.elb.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(m.cfg.TargetGroupARN),
	})
	if err != nil {
		return types.MetricSnapshot{}, fmt.Errorf("elbv2 DescribeTargetHealth: %w", err)
	}

	healthy, total := 0, len(healthOut.TargetHealthDescriptions)
	for _, t := range healthOut.TargetHealthDescriptions {
		if t.TargetHealth.State == "healthy" {
			healthy++
		}
	}

	return types.MetricSnapshot{
		Timestamp:      end,
		AvgCPUUtil:     avgCPU,
		HasCPUData:     instancesWithData > 0,
		HealthyTargets: healthy,
		TotalTargets:   total,
	}, nil
}
