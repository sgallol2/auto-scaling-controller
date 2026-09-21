// Punto de entrada del controlador. El proceso corre continuamente en una EC2
// dedicada y mantiene el KnowledgeState en memoria entre ciclos.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"autoscaler-controller/analyze"
	"autoscaler-controller/decisionlog"
	"autoscaler-controller/execute"
	"autoscaler-controller/monitor"
	"autoscaler-controller/plan"
	"autoscaler-controller/state"
	"autoscaler-controller/types"
)

func loadConfig() types.Config {
	useMovingAverage := parseBoolEnv("AUTOSCALER_USE_MOVING_AVERAGE", false)
	movingAverageWindow := parseIntEnv("AUTOSCALER_MOVING_AVERAGE_WINDOW", 3)

	return types.Config{
		AutoScalingGroupTag:   os.Getenv("AUTOSCALER_INSTANCE_TAG"),
		TargetGroupARN:        os.Getenv("AUTOSCALER_TARGET_GROUP_ARN"),
		LaunchTemplateID:      os.Getenv("AUTOSCALER_LAUNCH_TEMPLATE_ID"),
		StateFilePath:         valueOrDefault(os.Getenv("AUTOSCALER_STATE_FILE"), "autoscaler-state.json"),
		MinInstances:          1,
		MaxInstances:          3,
		ScaleOutThreshold:     30.0,
		ScaleInThreshold:      10.0,
		EvaluationPeriods:     2,
		CooldownDuration:      120_000_000_000, // bloquea por 2 minutos scale-out/scale-in consecutivos
		MetricWindow:          300_000_000_000, // los ultimos 5 minutos de CPUUtilization
		InstanceReadyTimeout:  5 * time.Minute,
		HealthCheckTimeout:    5 * time.Minute,   // tiempo máximo que esperamos a que un target pase a healthy
		DeregistrationDelay:   30 * time.Second,  // tiempo que tarda un target en pasar a "draining" y dejar de recibir tráfico
		OperationPollInterval: 10 * time.Second,  // cada 10s se consulta si la instancia está ready y healthy
		PollInterval:          30 * time.Second,  // cada 30 segundos se ejecuta un ciclo completo MAPE-K
		UseMovingAverage:      useMovingAverage,
		MovingAverageWindow:   movingAverageWindow,
	}
}

func parseBoolEnv(name string, fallback bool) bool {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Printf("warning: %s=%q no es un booleano válido; se usa %t", name, value, fallback)
		return fallback
	}
	return parsed
}

func parseIntEnv(name string, fallback int) int {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("warning: %s=%q no es un entero válido; se usa %d", name, value, fallback)
		return fallback
	}
	return parsed
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func validateConfig(cfg types.Config) error {
	missing := make([]string, 0, 3)
	if cfg.AutoScalingGroupTag == "" {
		missing = append(missing, "AUTOSCALER_INSTANCE_TAG")
	}
	if cfg.TargetGroupARN == "" {
		missing = append(missing, "AUTOSCALER_TARGET_GROUP_ARN")
	}
	if cfg.LaunchTemplateID == "" {
		missing = append(missing, "AUTOSCALER_LAUNCH_TEMPLATE_ID")
	}
	if len(missing) > 0 {
		return fmt.Errorf("faltan variables obligatorias: %s", strings.Join(missing, ", "))
	}
	if cfg.MinInstances < 1 {
		return fmt.Errorf("MinInstances debe ser >= 1")
	}
	if cfg.MaxInstances < cfg.MinInstances {
		return fmt.Errorf("MaxInstances debe ser >= MinInstances")
	}
	if cfg.ScaleInThreshold >= cfg.ScaleOutThreshold {
		return fmt.Errorf("ScaleInThreshold debe ser menor que ScaleOutThreshold")
	}
	if cfg.EvaluationPeriods < 1 {
		return fmt.Errorf("EvaluationPeriods debe ser >= 1")
	}
	if cfg.CooldownDuration < 0 || cfg.MetricWindow <= 0 || cfg.PollInterval <= 0 ||
		cfg.InstanceReadyTimeout <= 0 || cfg.HealthCheckTimeout <= 0 ||
		cfg.DeregistrationDelay < 0 || cfg.OperationPollInterval <= 0 {
		return fmt.Errorf("las duraciones y el intervalo de polling deben tener valores válidos")
	}
	if cfg.UseMovingAverage && cfg.MovingAverageWindow < 1 {
		return fmt.Errorf("MovingAverageWindow debe ser >= 1")
	}
	return nil
}

func discoverManagedInstances(ctx context.Context, client *ec2.Client, tagValue string) ([]string, error) {
	input := &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:role"), Values: []string{tagValue}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
		},
	}

	var instanceIDs []string
	for {
		output, err := client.DescribeInstances(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("ec2 DescribeInstances: %w", err)
		}
		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				if instance.InstanceId != nil && *instance.InstanceId != "" {
					instanceIDs = append(instanceIDs, *instance.InstanceId)
				}
			}
		}
		if output.NextToken == nil || *output.NextToken == "" {
			break
		}
		input.NextToken = output.NextToken
	}

	sort.Strings(instanceIDs)
	return instanceIDs, nil
}

func initializeKnowledge(ctx context.Context, client *ec2.Client, cfg types.Config, backup types.KnowledgeState) (types.KnowledgeState, error) {
	instanceIDs, err := discoverManagedInstances(ctx, client, cfg.AutoScalingGroupTag)
	if err != nil {
		return types.KnowledgeState{}, fmt.Errorf("no se pudo reconciliar EC2; se detiene de forma segura: %w", err)
	}
	if len(instanceIDs) == 0 {
		return types.KnowledgeState{}, fmt.Errorf("no se encontraron instancias pending/running con tag role=%q", cfg.AutoScalingGroupTag)
	}

	backup.CurrentInstanceIDs = instanceIDs
	return backup, nil
}

// runCycle ejecuta un ciclo completo: Monitor -> Analyze -> Plan -> Execute.
func runCycle(ctx context.Context, mon *monitor.Monitor, analyzer *analyze.Analyzer, planner *plan.Planner, executor *execute.Executor, cfg types.Config, knowledge *types.KnowledgeState) error {
	// M: observar (le pasamos las instancias activas conocidas, ya que no
	// hay ASG que agregue la métrica por nosotros)
	snapshot, err := mon.Collect(ctx, knowledge.CurrentInstanceIDs)
	if err != nil {
		return err
	}

	// A: suavizar (si UseMovingAverage está activo) + interpretar + confirmar
	smoothedCPU := snapshot.AvgCPUUtil
	updatedWindow := knowledge.RecentCPUReadings
	if snapshot.HasCPUData {
		smoothedCPU, updatedWindow = analyzer.Smooth(snapshot.AvgCPUUtil, knowledge.RecentCPUReadings)
	}
	signal, reason := analyzer.Interpret(smoothedCPU, snapshot, *knowledge)
	confirmed := analyzer.Confirm(signal, *knowledge)

	effectiveSignal := types.MAINTAIN_CAPACITY
	if confirmed {
		effectiveSignal = signal
	}

	// P: decidir
	decision := planner.Decide(effectiveSignal, reason, len(knowledge.CurrentInstanceIDs), *knowledge)

	// E: actuar
	currentInstanceCount := len(knowledge.CurrentInstanceIDs)
	newInstanceIDs, err := executor.Apply(ctx, decision, knowledge.CurrentInstanceIDs)
	if err != nil {
		decisionlog.Log(snapshot, decision, decisionlog.Context{
			DetectedSignal:       signal,
			SignalConfirmed:      confirmed,
			EvaluatedCPU:         smoothedCPU,
			CurrentInstanceCount: currentInstanceCount,
			NextInstanceCount:    currentInstanceCount,
			MinInstances:         cfg.MinInstances,
			MaxInstances:         cfg.MaxInstances,
			CooldownActive:       time.Since(knowledge.LastScaleAction) < cfg.CooldownDuration,
			ConsecutiveCount:     knowledge.ConsecutiveCount,
			ExecutionStatus:      "failed",
			ExecutionError:       err.Error(),
		})
		return err
	}

	// Registrar la decisión (explicabilidad)
	decisionlog.Log(snapshot, decision, decisionlog.Context{
		DetectedSignal:       signal,
		SignalConfirmed:      confirmed,
		EvaluatedCPU:         smoothedCPU,
		CurrentInstanceCount: currentInstanceCount,
		NextInstanceCount:    len(newInstanceIDs),
		MinInstances:         cfg.MinInstances,
		MaxInstances:         cfg.MaxInstances,
		CooldownActive:       time.Since(knowledge.LastScaleAction) < cfg.CooldownDuration,
		ConsecutiveCount:     knowledge.ConsecutiveCount,
		ExecutionStatus:      "succeeded",
	})

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
	if err := validateConfig(cfg); err != nil {
		log.Fatalf("configuración inválida: %v", err)
	}

	store := state.New(cfg.StateFilePath)
	backup, err := store.Load()
	if err != nil {
		log.Fatalf("cargar respaldo local: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("cargar configuración AWS: %v", err)
	}

	ec2Client := ec2.NewFromConfig(awsCfg)
	knowledge, err := initializeKnowledge(ctx, ec2Client, cfg, backup)
	if err != nil {
		log.Fatalf("reconciliar instancias gestionadas: %v", err)
	}
	if err := store.Save(knowledge); err != nil {
		log.Fatalf("guardar estado inicial: %v", err)
	}

	mon := monitor.New(cloudwatch.NewFromConfig(awsCfg), elasticloadbalancingv2.NewFromConfig(awsCfg), cfg)
	analyzer := analyze.New(cfg)
	planner := plan.New(cfg)
	executor := execute.New(ec2Client, elasticloadbalancingv2.NewFromConfig(awsCfg), cfg)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := runCycle(ctx, mon, analyzer, planner, executor, cfg, &knowledge); err != nil {
			log.Printf("error en ciclo MAPE-K: %v", err)
		} else if err := store.Save(knowledge); err != nil {
			log.Fatalf("guardar estado local: %v", err)
		}

		select {
		case <-ctx.Done():
			log.Println("controlador detenido")
			return
		case <-ticker.C:
		}
	}
}
