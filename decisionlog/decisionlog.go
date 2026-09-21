// Package decisionlog cumple el requisito de "toda decisión debe ser
// registrada y explicable". Escribe a stdout en JSON estructurado, que el
// agente de logs de la EC2 puede enviar a CloudWatch Logs.
package decisionlog

import (
	"encoding/json"
	"log"
	"time"

	"autoscaler-controller/types"
)

type Context struct {
	DetectedSignal       types.Signal
	SignalConfirmed      bool
	EvaluatedCPU         float64
	CurrentInstanceCount int
	NextInstanceCount    int
	MinInstances         int
	MaxInstances         int
	CooldownActive       bool
	ConsecutiveCount     int
	ExecutionStatus      string
	ExecutionError       string
}

type Entry struct {
	Timestamp            time.Time `json:"timestamp"`
	AvgCPU               float64   `json:"avg_cpu"`
	EvaluatedCPU         float64   `json:"evaluated_cpu"`
	HasCPUData           bool      `json:"has_cpu_data"`
	HealthyTargets       int       `json:"healthy_targets"`
	TotalTargets         int       `json:"total_targets"`
	DetectedSignal       string    `json:"detected_signal"`
	SignalConfirmed      bool      `json:"signal_confirmed"`
	DecisionSignal       string    `json:"decision_signal"`
	DeltaCount           int       `json:"delta_count"`
	CurrentInstanceCount int       `json:"current_instance_count"`
	NextInstanceCount    int       `json:"next_instance_count"`
	MinInstances         int       `json:"min_instances"`
	MaxInstances         int       `json:"max_instances"`
	CooldownActive       bool      `json:"cooldown_active"`
	ConsecutiveCount     int       `json:"consecutive_count"`
	ExecutionStatus      string    `json:"execution_status"`
	ExecutionError       string    `json:"execution_error,omitempty"`
	Reason               string    `json:"reason"`
}

func Log(snapshot types.MetricSnapshot, decision types.Decision, context Context) {
	entry := BuildEntry(snapshot, decision, context)
	b, err := json.Marshal(entry)
	if err != nil {
		log.Printf("error serializando decisión: %v", err)
		return
	}
	log.Println(string(b))
}

func BuildEntry(snapshot types.MetricSnapshot, decision types.Decision, context Context) Entry {
	return Entry{
		Timestamp:            decision.DecidedAt,
		AvgCPU:               snapshot.AvgCPUUtil,
		EvaluatedCPU:         context.EvaluatedCPU,
		HasCPUData:           snapshot.HasCPUData,
		HealthyTargets:       snapshot.HealthyTargets,
		TotalTargets:         snapshot.TotalTargets,
		DetectedSignal:       context.DetectedSignal.String(),
		SignalConfirmed:      context.SignalConfirmed,
		DecisionSignal:       decision.Signal.String(),
		DeltaCount:           decision.DeltaCount,
		CurrentInstanceCount: context.CurrentInstanceCount,
		NextInstanceCount:    context.NextInstanceCount,
		MinInstances:         context.MinInstances,
		MaxInstances:         context.MaxInstances,
		CooldownActive:       context.CooldownActive,
		ConsecutiveCount:     context.ConsecutiveCount,
		ExecutionStatus:      context.ExecutionStatus,
		ExecutionError:       context.ExecutionError,
		Reason:               decision.Reason,
	}
}
