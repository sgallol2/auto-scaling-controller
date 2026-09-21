// Package analyze implementa la fase "A" del MAPE-K. No llama a ningún API
// de AWS: es lógica pura, fácil de testear con tabla de casos, lo que te da
// la explicabilidad que pide el reto.
package analyze

import "autoscaler-controller/types"

type Analyzer struct {
	cfg types.Config
}

func New(cfg types.Config) *Analyzer {
	return &Analyzer{cfg: cfg}
}

// Smooth aplica (o no) Moving Average según la config, y devuelve tanto el
// valor a usar en la decisión como la ventana actualizada para persistir.
// Al ser una función separada, puedes desactivar MA (UseMovingAverage=false)
// y el resto del pipeline sigue funcionando idéntico a como estaba antes:
// threshold-based e histéresis no dependen de si el valor fue suavizado o no.
func (a *Analyzer) Smooth(rawValue float64, previousWindow []float64) (smoothedValue float64, updatedWindow []float64) {
	if !a.cfg.UseMovingAverage {
		return rawValue, previousWindow // MA desactivado: pass-through
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

// Interpret traduce una métrica (ya suavizada o no, según Smooth) en una
// señal, usando doble umbral (histéresis) para que el sistema no oscile
// entre subir y bajar con pequeñas variaciones alrededor de un único punto
// de corte. Esta función NO sabe ni le importa si el valor viene crudo o
// suavizado -- por eso threshold+histéresis y Moving Average son
// composables sin acoplarse entre sí.
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

// Confirm exige que la misma señal se repita N veces consecutivas
// (EvaluationPeriods) antes de considerarla confirmada -- esto absorbe
// picos cortos de tráfico que no ameritan escalar.
func (a *Analyzer) Confirm(signal types.Signal, state types.KnowledgeState) bool {
	if signal != state.LastSignal {
		return false
	}
	return state.ConsecutiveCount+1 >= a.cfg.EvaluationPeriods
}
