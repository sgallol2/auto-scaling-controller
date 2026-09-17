// Package execute implementa la fase "E" del MAPE-K: es el único módulo
// con permiso de escritura sobre EC2/ELB. Aísla aquí el radio de impacto
// de errores y facilita aplicar mínimo privilegio en el rol IAM.
package execute

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"autoscaler-controller/types"
)

type Executor struct {
	ec2Client *ec2.Client
	elbClient *elasticloadbalancingv2.Client
	cfg       types.Config
}

func New(ec2c *ec2.Client, elbc *elasticloadbalancingv2.Client, cfg types.Config) *Executor {
	return &Executor{ec2Client: ec2c, elbClient: elbc, cfg: cfg}
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

	var newIDs []string
	for _, inst := range runOut.Instances {
		newIDs = append(newIDs, aws.ToString(inst.InstanceId))
	}

	// Nota: aquí normalmente esperarías el estado "running" + health checks
	// del target group (con un waiter o un polling corto) antes de
	// registrar en el ALB, para no mandar tráfico a una instancia en warm-up.
	targets := make([]elbtypes.TargetDescription, 0, len(newIDs))
	for _, id := range newIDs {
		targets = append(targets, elbtypes.TargetDescription{Id: aws.String(id)})
	}
	_, err = e.elbClient.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(e.cfg.TargetGroupARN),
		Targets:        targets,
	})
	if err != nil {
		return current, fmt.Errorf("elbv2 RegisterTargets: %w", err)
	}

	return append(current, newIDs...), nil
}

func (e *Executor) scaleIn(ctx context.Context, current []string, n int) ([]string, error) {
	if n > len(current) {
		n = len(current)
	}
	toRemove := current[:n]
	remaining := current[n:]

	// 1) Desregistrar primero del target group (deja drenar conexiones)
	targets := make([]elbtypes.TargetDescription, 0, len(toRemove))
	for _, id := range toRemove {
		targets = append(targets, elbtypes.TargetDescription{Id: aws.String(id)})
	}
	_, err := e.elbClient.DeregisterTargets(ctx, &elasticloadbalancingv2.DeregisterTargetsInput{
		TargetGroupArn: aws.String(e.cfg.TargetGroupARN),
		Targets:        targets,
	})
	if err != nil {
		return current, fmt.Errorf("elbv2 DeregisterTargets: %w", err)
	}

	// 2) Terminar las instancias
	_, err = e.ec2Client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: toRemove,
	})
	if err != nil {
		return current, fmt.Errorf("ec2 TerminateInstances: %w", err)
	}

	return remaining, nil
}
