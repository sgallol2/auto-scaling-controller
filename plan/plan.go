// Package plan implementa la fase "P" del MAPE-K: decide CUÁNTAS instancias
// añadir o quitar, respetando límites (min/max) y el cooldown.
package plan

import (
	"time"

	"autoscaler-controller/types"
)

type Planner struct {
	cfg types.Config
}

func New(cfg types.Config) *Planner {
	return &Planner{cfg: cfg}
}

// Decide aplica las reglas de negocio de tu matriz de decisión:
// - respeta el cooldown (anti-oscilación temporal)
// - respeta MinInstances / MaxInstances
// - por defecto escala de a 1 instancia (puedes cambiar a step-scaling)
func (p *Planner) Decide(signal types.Signal, reason string, currentCount int, state types.KnowledgeState) types.Decision {
	now := time.Now()

	if now.Sub(state.LastScaleAction) < p.cfg.CooldownDuration {
		return types.Decision{
			Signal:     types.MAINTAIN_CAPACITY,
			DeltaCount: 0,
			Reason:     "en periodo de cooldown, se posterga la decisión",
			DecidedAt:  now,
		}
	}

	switch signal {
	case types.INCREASE_CAPACITY:
		if currentCount >= p.cfg.MaxInstances {
			return types.Decision{Signal: types.MAINTAIN_CAPACITY, DeltaCount: 0,
				Reason: "ya en MaxInstances, no se puede escalar más", DecidedAt: now}
		}
		return types.Decision{Signal: signal, DeltaCount: 1, Reason: reason, DecidedAt: now}

	case types.REDUCE_CAPACITY:
		if currentCount <= p.cfg.MinInstances {
			return types.Decision{Signal: types.MAINTAIN_CAPACITY, DeltaCount: 0,
				Reason: "ya en MinInstances, no se puede reducir más", DecidedAt: now}
		}
		return types.Decision{Signal: signal, DeltaCount: -1, Reason: reason, DecidedAt: now}

	default:
		return types.Decision{Signal: types.MAINTAIN_CAPACITY, DeltaCount: 0, Reason: reason, DecidedAt: now}
	}
}
