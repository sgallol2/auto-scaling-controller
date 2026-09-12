// Punto de entrada del controlador. El proceso corre continuamente en una EC2
// dedicada y mantiene el KnowledgeState en memoria entre ciclos.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"autoscaler-controller/analyze"
	"autoscaler-controller/decisionlog"
	"autoscaler-controller/execute"
	"autoscaler-controller/monitor"
	"autoscaler-controller/plan"
	"autoscaler-controller/types"
)

func loadConfig() types.Config {
	instanceIDs := strings.FieldsFunc(os.Getenv("AUTOSCALER_INSTANCE_IDS"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})

	return types.Config{
		AutoScalingGroupTag: os.Getenv("AUTOSCALER_INSTANCE_TAG"),
		TargetGroupARN:      os.Getenv("AUTOSCALER_TARGET_GROUP_ARN"),
		LaunchTemplateID:    os.Getenv("AUTOSCALER_LAUNCH_TEMPLATE_ID"),
		InitialInstanceIDs:  instanceIDs,
		MinInstances:        1,
		MaxInstances:        3,
		ScaleOutThreshold:   70.0,
		ScaleInThreshold:    30.0,
		EvaluationPeriods:   2,
		CooldownDuration:    120_000_000_000, // 2 minutos en nanosegundos
		MetricWindow:        300_000_000_000, // 5 minutos en nanosegundos
		PollInterval:        120 * time.Second,

		// Suavizado desactivado por defecto = comportamiento idéntico al
		// threshold-based original. Actívalo para la variante experimental
		// con Moving Average sin tocar el resto del pipeline.
		UseMovingAverage:    false,
		MovingAverageWindow: 3,
	}
}

// runCycle ejecuta un ciclo completo: Monitor -> Analyze -> Plan -> Execute.
func runCycle(ctx context.Context, mon *monitor.Monitor, analyzer *analyze.Analyzer, planner *plan.Planner, executor *execute.Executor, knowledge *types.KnowledgeState) error {
	// M: observar (le pasamos las instancias activas conocidas, ya que no
	// hay ASG que agregue la métrica por nosotros)
	snapshot, err := mon.Collect(ctx, knowledge.CurrentInstanceIDs)
	if err != nil {
		return err
	}

	// A: suavizar (si UseMovingAverage está activo) + interpretar + confirmar
	smoothedCPU, updatedWindow := analyzer.Smooth(snapshot.AvgCPUUtil, knowledge.RecentCPUReadings)
	signal, reason := analyzer.Interpret(smoothedCPU, snapshot, *knowledge)
	confirmed := analyzer.Confirm(signal, *knowledge)

	effectiveSignal := types.MAINTAIN_CAPACITY
	if confirmed {
		effectiveSignal = signal
	}

	// P: decidir
	decision := planner.Decide(effectiveSignal, reason, len(knowledge.CurrentInstanceIDs), *knowledge)

	// E: actuar
	newInstanceIDs, err := executor.Apply(ctx, decision, knowledge.CurrentInstanceIDs)
	if err != nil {
		return err
	}

	// Registrar la decisión (explicabilidad)
	decisionlog.Log(snapshot, decision)

	// K: persistir el nuevo estado
	newKnowledge := types.KnowledgeState{
		CurrentInstanceIDs: newInstanceIDs,
		LastSignal:         signal,
		RecentCPUReadings:  updatedWindow,
	}
	if decision.DeltaCount != 0 {
		newKnowledge.LastScaleAction = decision.DecidedAt
	} else {
		newKnowledge.LastScaleAction = knowledge.LastScaleAction
	}
	if signal == knowledge.LastSignal {
		newKnowledge.ConsecutiveCount = knowledge.ConsecutiveCount + 1
	} else {
		newKnowledge.ConsecutiveCount = 1
	}

	*knowledge = newKnowledge
	return nil
}

func main() {
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("cargar configuración AWS: %v", err)
	}

	mon := monitor.New(cloudwatch.NewFromConfig(awsCfg), elasticloadbalancingv2.NewFromConfig(awsCfg), cfg)
	analyzer := analyze.New(cfg)
	planner := plan.New(cfg)
	executor := execute.New(ec2.NewFromConfig(awsCfg), elasticloadbalancingv2.NewFromConfig(awsCfg), cfg)
	knowledge := types.KnowledgeState{
		CurrentInstanceIDs: append([]string(nil), cfg.InitialInstanceIDs...),
		LastSignal:         types.MAINTAIN_CAPACITY,
	}

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := runCycle(ctx, mon, analyzer, planner, executor, &knowledge); err != nil {
			log.Printf("error en ciclo MAPE-K: %v", err)
		}

		select {
		case <-ctx.Done():
			log.Println("controlador detenido")
			return
		case <-ticker.C:
		}
	}
}
