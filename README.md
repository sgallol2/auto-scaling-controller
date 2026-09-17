# autoscaler-controller

Controlador de autoescalado horizontal en Go para ejecutarse como un proceso
continuo en una única instancia EC2. Implementa un ciclo MAPE-K: Monitor,
Analyze, Plan, Execute y Knowledge. El estado Knowledge vive en memoria del
proceso; no se utiliza Lambda ni DynamoDB.

## Estructura

```
main.go                      # daemon de EC2 y orquestación del ciclo MAPE-K
types/types.go               # configuración y modelos compartidos
monitor/monitor.go           # CloudWatch CPU y salud de targets del ALB
analyze/analyze.go           # histéresis, moving average y confirmación
plan/plan.go                 # cooldown, límites y delta de instancias
execute/execute.go           # operaciones EC2 y registro en el Target Group
decisionlog/decisionlog.go   # decisiones JSON para CloudWatch Logs
```

## Decisiones

Las señales del controlador son:

- `MAINTAIN_CAPACITY`: no modificar la capacidad.
- `INCREASE_CAPACITY`: añadir una instancia.
- `REDUCE_CAPACITY`: retirar una instancia.

## Ejecución en EC2

El proceso se inicia una vez en la instancia EC2 y ejecuta un ciclo cada dos
minutos. El estado se conserva mientras el proceso permanezca activo.

Las instancias gestionadas inicialmente se configuran mediante variables de
entorno separadas por comas:

```powershell
$env:AUTOSCALER_INSTANCE_IDS="i-0123456789abcdef0,i-abcdef0123456789"
$env:AUTOSCALER_INSTANCE_TAG="web-fleet"
$env:AUTOSCALER_TARGET_GROUP_ARN="arn:aws:elasticloadbalancing:region:account:targetgroup/name/id"
$env:AUTOSCALER_LAUNCH_TEMPLATE_ID="lt-0123456789abcdef0"
go run .
```

En Linux, la configuración equivalente puede definirse en el servicio
systemd que ejecute el binario. La instancia EC2 necesita un instance profile
con permisos para CloudWatch, EC2 y Elastic Load Balancing v2.

## Flujo de un ciclo

1. `Monitor` consulta CPU promedio en CloudWatch y targets saludables en el ALB.
2. `Analyze` aplica moving average opcional e histéresis.
3. `Analyze` confirma la misma señal durante `EvaluationPeriods` ciclos.
4. `Plan` respeta cooldown y los límites mínimo/máximo.
5. `Execute` crea o elimina instancias y las registra o desregistra del ALB.
6. `decisionlog` escribe la señal y el motivo en JSON.
7. El `KnowledgeState` actualizado queda en memoria para el siguiente ciclo.

## Política IAM de mínimo privilegio (sugerida)

El instance profile de la EC2 solo necesita:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "cloudwatch:GetMetricData"
      ],
      "Resource": "*"
    },
    {
      "Sid": "LaunchTaggedInstances",
      "Effect": "Allow",
      "Action": "ec2:RunInstances",
      "Resource": "arn:aws:ec2:*:*:instance/*",
      "Condition": {
        "StringEquals": { "aws:RequestTag/role": "web-fleet" }
      }
    },
    {
      "Sid": "LaunchDependencies",
      "Effect": "Allow",
      "Action": "ec2:RunInstances",
      "Resource": [
        "arn:aws:ec2:*:*:network-interface/*",
        "arn:aws:ec2:*:*:subnet/*",
        "arn:aws:ec2:*:*:security-group/*",
        "arn:aws:ec2:*::image/*",
        "arn:aws:ec2:*:*:launch-template/*"
      ]
    },
    {
      "Sid": "TagInstancesDuringLaunch",
      "Effect": "Allow",
      "Action": "ec2:CreateTags",
      "Resource": "arn:aws:ec2:*:*:instance/*",
      "Condition": {
        "StringEquals": {
          "ec2:CreateAction": "RunInstances",
          "aws:RequestTag/role": "web-fleet"
        }
      }
    },
    {
      "Sid": "TerminateManagedInstances",
      "Effect": "Allow",
      "Action": "ec2:TerminateInstances",
      "Resource": "arn:aws:ec2:*:*:instance/*",
      "Condition": {
        "StringEquals": { "ec2:ResourceTag/role": "web-fleet" }
      }
    },
    {
      "Sid": "DescribeInstances",
      "Effect": "Allow",
      "Action": "ec2:DescribeInstances",
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:RegisterTargets",
        "elasticloadbalancing:DeregisterTargets",
        "elasticloadbalancing:DescribeTargetHealth"
      ],
      "Resource": "arn:aws:elasticloadbalancing:*:*:targetgroup/*"
    }
  ]
}
```

El código añade `role=web-fleet` mediante `TagSpecifications` en cada llamada
a `RunInstances`. Sustituye los comodines de región, cuenta, subnet,
security group, AMI, Launch Template y Target Group por los ARN concretos de
tu entorno para aplicar un mínimo privilegio más estricto. Las instancias
iniciales configuradas en `AUTOSCALER_INSTANCE_IDS` también deben tener el
tag `role=web-fleet` para que puedan terminarse durante un scale-in.

## Estrategias intercambiables/combinables en `analyze`

- `Smooth()`: aplica Moving Average sobre `MetricSnapshot.AvgCPUUtil` si `cfg.UseMovingAverage = true`; si está en `false`, es un pass-through (comportamiento idéntico al threshold-based original).
- `Interpret()`: umbrales con histéresis. No sabe ni le importa si el valor que recibe fue suavizado — por diseño, ambas técnicas son composables, no mutuamente excluyentes.
- Para comparar experimentalmente "crudo + histéresis" vs "Moving Average + histéresis", cambia solo `cfg.UseMovingAverage` y `cfg.MovingAverageWindow`; el resto del pipeline no se toca.
- La ventana de lecturas recientes (`KnowledgeState.RecentCPUReadings`) se mantiene en memoria junto con el resto del estado del proceso.

## Métricas de CloudWatch utilizadas

- `AWS/EC2` → `CPUUtilization`, dimensión `InstanceId` (una consulta por cada instancia activa conocida en el Knowledge Base — no existe agregación automática porque no usamos Auto Scaling Group).
- `AWS/ApplicationELB` → `DescribeTargetHealth` (vía API de ELBv2, no CloudWatch directamente) para `HealthyTargets`/`TotalTargets`.
- Opcionales para extender: `TargetResponseTime`, `RequestCountPerTarget`, `HTTPCode_Target_5XX_Count` (todas en `AWS/ApplicationELB`), o métricas de memoria vía CloudWatch Agent (`CWAgent` namespace) si se decide incorporar memoria al criterio de escalado.


## Qué falta para producción (fuera del alcance del reto, pero vale mencionarlo)

- Manejo de errores parciales en `scaleOut`/`scaleIn` (p. ej. si `RunInstances`
  crea 2 de 3 instancias pedidas).
- Esperar el estado `running` + health check antes de `RegisterTargets`
  (usar un waiter o reintentos cortos).
- Tests unitarios de `analyze` y `plan` (son funciones puras, ideales para
  tabla de casos con `testing.T`).
- Métricas propias (`cloudwatch:PutMetricData`) si quieres complementar CPU
  con latencia percibida de la app.
- Persistencia o recuperación externa del estado si la EC2 se reinicia y se
  necesita conservar cooldown, confirmaciones y la lista de instancias.
