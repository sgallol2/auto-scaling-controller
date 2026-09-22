// Package execute implementa la fase "E" del MAPE-K: es el único módulo
// con permiso de escritura sobre EC2/ELB. Aísla aquí el radio de impacto
// de errores y facilita aplicar mínimo privilegio en el rol IAM.
package execute

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"autoscaler-controller/types"
)

type Executor struct {
	ec2Client ec2Client
	elbClient elbClient
	cfg       types.Config
	sleep     func(context.Context, time.Duration) error
}

type ec2Client interface {
	RunInstances(context.Context, *ec2.RunInstancesInput, ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
}

type elbClient interface {
	RegisterTargets(context.Context, *elasticloadbalancingv2.RegisterTargetsInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.RegisterTargetsOutput, error)
	DeregisterTargets(context.Context, *elasticloadbalancingv2.DeregisterTargetsInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeregisterTargetsOutput, error)
	DescribeTargetHealth(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func New(ec2c ec2Client, elbc elbClient, cfg types.Config) *Executor {
	return &Executor{
		ec2Client: ec2c,
		elbClient: elbc,
		cfg:       cfg,
		sleep:     defaultSleep,
	}
}

func buildTargetDescriptions(ids []string) []elbtypes.TargetDescription {
	targets := make([]elbtypes.TargetDescription, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, elbtypes.TargetDescription{Id: aws.String(id)})
	}
	return targets
}

// Apply ejecuta la decisión y devuelve el nuevo set de instancias conocidas
// (para que se persista en el Knowledge Base).
func (e *Executor) Apply(ctx context.Context, d types.Decision, current []string) ([]string, error) {
	switch {
	case d.DeltaCount > 0:
		return e.scaleOut(ctx, current, d.DeltaCount)
	case d.DeltaCount < 0:
		return e.scaleIn(ctx, current, -d.DeltaCount)
	default:
		return current, nil // Hold: no-op
	}
}

func (e *Executor) scaleOut(ctx context.Context, current []string, n int) ([]string, error) {
	runOut, err := e.ec2Client.RunInstances(ctx, &ec2.RunInstancesInput{
		MinCount: aws.Int32(int32(n)),
		MaxCount: aws.Int32(int32(n)),
		LaunchTemplate: &ec2types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(e.cfg.LaunchTemplateID),
		},
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeInstance,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("role"),
						Value: aws.String(e.cfg.AutoScalingGroupTag),
					},
				},
			},
		},
	})
	if err != nil {
		return current, fmt.Errorf("ec2 RunInstances: %w", err)
	}
	if runOut == nil {
		return current, fmt.Errorf("ec2 RunInstances devolvió output nulo")
	}

	var newIDs []string
	for _, inst := range runOut.Instances {
		if inst.InstanceId == nil || aws.ToString(inst.InstanceId) == "" {
			return e.rollbackInstances(ctx, current, newIDs, fmt.Errorf("ec2 RunInstances devolvió una instancia sin ID"))
		}
		newIDs = append(newIDs, aws.ToString(inst.InstanceId))
	}
	if len(newIDs) != n {
		return e.rollbackInstances(ctx, current, newIDs, fmt.Errorf("ec2 RunInstances creó %d de %d instancias solicitadas", len(newIDs), n))
	}

	if err := e.waitForInstancesRunning(ctx, newIDs); err != nil {
		return e.rollbackInstances(ctx, current, newIDs, err)
	}

	targets := buildTargetDescriptions(newIDs)
	_, err = e.elbClient.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(e.cfg.TargetGroupARN),
		Targets:        targets,
	})
	if err != nil {
		return e.rollbackInstances(ctx, current, newIDs, fmt.Errorf("elbv2 RegisterTargets: %w", err))
	}
	if err := e.waitForTargetsHealthy(ctx, newIDs); err != nil {
		_, deregisterErr := e.elbClient.DeregisterTargets(ctx, &elasticloadbalancingv2.DeregisterTargetsInput{
			TargetGroupArn: aws.String(e.cfg.TargetGroupARN),
			Targets:        targets,
		})
		_, terminateErr := e.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: newIDs})
		return current, errors.Join(
			err,
			wrapRollbackError("elbv2 DeregisterTargets", deregisterErr),
			wrapRollbackError("ec2 TerminateInstances", terminateErr),
		)
	}

	return append(current, newIDs...), nil
}

func (e *Executor) scaleIn(ctx context.Context, current []string, n int) ([]string, error) {
	if n <= 0 || len(current) == 0 {
		return current, nil
	}
	if n > len(current) {
		n = len(current)
	}
	toRemove := current[:n]
	remaining := current[n:]

	// 1) Desregistrar primero del target group (deja drenar conexiones)
	targets := buildTargetDescriptions(toRemove)
	_, err := e.elbClient.DeregisterTargets(ctx, &elasticloadbalancingv2.DeregisterTargetsInput{
		TargetGroupArn: aws.String(e.cfg.TargetGroupARN),
		Targets:        targets,
	})
	if err != nil {
		return current, fmt.Errorf("elbv2 DeregisterTargets: %w", err)
	}

	if err := e.waitForTargetsUnused(ctx, toRemove); err != nil {
		return current, err
	}

	// 2) waitForTargetsUnused ya confirma que terminó el drenaje; no se añade
	// otra espera fija del mismo DeregistrationDelay.
	_, err = e.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: toRemove,
	})
	if err != nil {
		return current, fmt.Errorf("ec2 TerminateInstances: %w", err)
	}

	return remaining, nil
}

func (e *Executor) rollbackInstances(ctx context.Context, current, instanceIDs []string, cause error) ([]string, error) {
	if len(instanceIDs) == 0 {
		return current, cause
	}
	_, rollbackErr := e.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: instanceIDs})
	if rollbackErr != nil {
		return current, fmt.Errorf("%w; rollback TerminateInstances: %v", cause, rollbackErr)
	}
	return current, cause
}

func wrapRollbackError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("rollback %s: %w", operation, err)
}

func (e *Executor) waitForInstancesRunning(ctx context.Context, instanceIDs []string) error {
	deadline := time.Now().Add(e.cfg.InstanceReadyTimeout)
	for {
		output, err := e.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: instanceIDs})
		if err != nil {
			return fmt.Errorf("ec2 DescribeInstances esperando running: %w", err)
		}
		if output == nil {
			return fmt.Errorf("ec2 DescribeInstances devolvió output nulo")
		}
		running := 0
		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				if instance.State != nil && instance.State.Name == ec2types.InstanceStateNameRunning {
					running++
				}
			}
		}
		if running == len(instanceIDs) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout esperando instancias running")
		}
		if err := e.sleepContext(ctx, e.cfg.OperationPollInterval); err != nil {
			return fmt.Errorf("esperar instancias running: %w", err)
		}
	}
}

func (e *Executor) waitForTargetsHealthy(ctx context.Context, instanceIDs []string) error {
	deadline := time.Now().Add(e.cfg.HealthCheckTimeout)
	for {
		output, err := e.elbClient.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(e.cfg.TargetGroupARN),
		})
		if err != nil {
			return fmt.Errorf("elbv2 DescribeTargetHealth esperando healthy: %w", err)
		}
		if output == nil {
			return fmt.Errorf("elbv2 DescribeTargetHealth devolvió output nulo")
		}
		healthy := make(map[string]bool, len(instanceIDs))
		for _, description := range output.TargetHealthDescriptions {
			if description.Target != nil && description.Target.Id != nil && description.TargetHealth != nil && description.TargetHealth.State == elbtypes.TargetHealthStateEnumHealthy {
				healthy[aws.ToString(description.Target.Id)] = true
			}
		}
		allHealthy := true
		for _, id := range instanceIDs {
			if !healthy[id] {
				allHealthy = false
				break
			}
		}
		if allHealthy {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout esperando targets healthy")
		}
		if err := e.sleepContext(ctx, e.cfg.OperationPollInterval); err != nil {
			return fmt.Errorf("esperar targets healthy: %w", err)
		}
	}
}

func (e *Executor) waitForTargetsUnused(ctx context.Context, instanceIDs []string) error {
	deadline := time.Now().Add(e.cfg.DeregistrationDelay)
	for {
		output, err := e.elbClient.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{
			TargetGroupArn: aws.String(e.cfg.TargetGroupARN),
		})
		if err != nil {
			return fmt.Errorf("elbv2 DescribeTargetHealth esperando drenaje: %w", err)
		}
		if output == nil {
			return fmt.Errorf("elbv2 DescribeTargetHealth devolvió output nulo")
		}
		active := make(map[string]bool, len(instanceIDs))
		for _, description := range output.TargetHealthDescriptions {
			if description.Target != nil && description.Target.Id != nil && description.TargetHealth != nil {
				state := description.TargetHealth.State
				if state != elbtypes.TargetHealthStateEnumUnused {
					active[aws.ToString(description.Target.Id)] = true
				}
			}
		}
		if len(active) == 0 {
			return nil
		}
		allRemoved := true
		for _, id := range instanceIDs {
			if active[id] {
				allRemoved = false
				break
			}
		}
		if allRemoved {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout esperando drenaje de targets")
		}
		if err := e.sleepContext(ctx, e.cfg.OperationPollInterval); err != nil {
			return fmt.Errorf("esperar drenaje de targets: %w", err)
		}
	}
}

func (e *Executor) sleepContext(ctx context.Context, d time.Duration) error {
	if e.sleep != nil {
		return e.sleep(ctx, d)
	}
	return defaultSleep(ctx, d)
}
