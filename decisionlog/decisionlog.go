// Package decisionlog cumple el requisito de "toda decisión debe ser
// registrada y explicable". Escribe a stdout en JSON estructurado, que el
// agente de logs de la EC2 puede enviar a CloudWatch Logs.
package decisionlog

import (
	"encoding/json"
	"log"

	"autoscaler-controller/types"
)

func Log(snapshot types.MetricSnapshot, decision types.Decision) {
	entry := map[string]interface{}{
		"timestamp":       decision.DecidedAt,
		"avg_cpu":         snapshot.AvgCPUUtil,
		"healthy_targets": snapshot.HealthyTargets,
		"total_targets":   snapshot.TotalTargets,
		"signal":          decision.Signal.String(),
		"delta_count":     decision.DeltaCount,
		"reason":          decision.Reason,
	}
	b, _ := json.Marshal(entry)
	log.Println(string(b))
}
