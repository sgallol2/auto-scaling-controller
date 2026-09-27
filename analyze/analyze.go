package analyze

import "autoscaler-controller/types"

type Analyzer struct {
	cfg types.Config
}

func New(cfg types.Config) *Analyzer {
	return &Analyzer{cfg: cfg}
}

func (a *Analyzer) Smooth(rawValue float64, previousWindow []float64) (smoothedValue float64, updatedWindow []float64) {
	if !a.cfg.UseMovingAverage || a.cfg.MovingAverageWindow <= 0 {
		return rawValue, previousWindow // MA desactivado o ventana inválida: pass-through
	}

	window := append(previousWindow, rawValue)
	if len(window) > a.cfg.MovingAverageWindow {
		window = window[len(window)-a.cfg.MovingAverageWindow:] // recorta al tamaño configurado
	}

	sum := 0.0
	for _, v := range window {
		sum += v
	}
	return sum / float64(len(window)), window
}

func (a *Analyzer) Interpret(cpuValue float64, snapshot types.MetricSnapshot, state types.KnowledgeState) (types.Signal, string) {
	if !snapshot.HasCPUData {
		return types.MAINTAIN_CAPACITY, "no hay datos de CPU disponibles, se espera antes de decidir"
	}

	// Si hay targets unhealthy, prioriza mantener capacidad, no reducirla.
	if snapshot.TotalTargets > 0 && snapshot.HealthyTargets < snapshot.TotalTargets {
		return types.MAINTAIN_CAPACITY, "hay targets unhealthy, se espera antes de decidir"
	}

	switch {
	case cpuValue >= a.cfg.ScaleOutThreshold:
		return types.INCREASE_CAPACITY, "CPU (posiblemente suavizado) por encima del umbral de scale-out"
	case cpuValue <= a.cfg.ScaleInThreshold:
		return types.REDUCE_CAPACITY, "CPU (posiblemente suavizado) por debajo del umbral de scale-in"
	default:
		return types.MAINTAIN_CAPACITY, "CPU dentro de la banda de histéresis"
	}
}

func (a *Analyzer) Confirm(signal types.Signal, state types.KnowledgeState) bool {
	if signal != state.LastSignal {
		return false
	}
	return state.ConsecutiveCount+1 >= a.cfg.EvaluationPeriods
}
