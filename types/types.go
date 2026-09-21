// Package types define las estructuras compartidas por las 4 fases del
// ciclo MAPE-K (Monitor, Analyze, Plan, Execute) y por el Knowledge Base.
package types

import "time"

// Config son los parámetros que definiste en tu matriz de decisión
// (umbrales, cooldown, min/max instancias). Cárgalos desde env vars o SSM
// Parameter Store, nunca hardcodeados.
type Config struct {
	AutoScalingGroupTag string // tag usado para identificar tus instancias, ej. "role=web"
	TargetGroupARN      string
	LaunchTemplateID    string
	StateFilePath       string
	SubnetIDs           []string
	SecurityGroupIDs    []string
	PollInterval        time.Duration

	MinInstances int
	MaxInstances int

	// Umbrales con histéresis (dimensión "método" de tu matriz)
	ScaleOutThreshold float64 // ej. CPU > 70% -> considerar subir
	ScaleInThreshold  float64 // ej. CPU < 30% -> considerar bajar
	EvaluationPeriods int     // cuántas ventanas consecutivas deben cumplirse

	CooldownDuration      time.Duration // anti-oscilación
	MetricWindow          time.Duration // ventana de agregación en CloudWatch
	InstanceReadyTimeout  time.Duration
	HealthCheckTimeout    time.Duration
	DeregistrationDelay   time.Duration
	OperationPollInterval time.Duration

	// Estrategia de suavizado (independiente de la estrategia de umbral).
	// Permite comparar "crudo + histéresis" vs "moving average + histéresis"
	// sin duplicar el resto del pipeline.
	UseMovingAverage    bool
	MovingAverageWindow int // cuántas lecturas recientes promediar
}

// MetricSnapshot es la salida de la fase Monitor.
type MetricSnapshot struct {
	Timestamp             time.Time
	AvgCPUUtil            float64
	HasCPUData            bool
	RequestCountPerTarget float64
	HealthyTargets        int
	TotalTargets          int
}

// Signal es la salida de la fase Analyze: una interpretación de las
// métricas, no todavía una decisión numérica.
type Signal int

const (
	MAINTAIN_CAPACITY Signal = iota
	INCREASE_CAPACITY
	REDUCE_CAPACITY
)

func (s Signal) String() string {
	switch s {
	case INCREASE_CAPACITY:
		return "INCREASE_CAPACITY"
	case REDUCE_CAPACITY:
		return "REDUCE_CAPACITY"
	default:
		return "MAINTAIN_CAPACITY"
	}
}

// Decision es la salida de la fase Plan: la acción concreta a ejecutar.
type Decision struct {
	Signal     Signal
	DeltaCount int    // +N o -N instancias
	Reason     string // texto explicable, para el log de decisiones
	DecidedAt  time.Time
}

// KnowledgeState es el estado que el proceso mantiene entre ciclos en la EC2
// dedicada que ejecuta el controlador.
type KnowledgeState struct {
	CurrentInstanceIDs []string
	LastScaleAction    time.Time
	LastSignal         Signal
	ConsecutiveCount   int // cuántas evaluaciones seguidas dieron la misma señal

	// RecentCPUReadings es la ventana deslizante usada por Moving Average.
	RecentCPUReadings []float64
}
