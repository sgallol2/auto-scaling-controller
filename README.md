# autoscaler-controller

Controlador de autoescalado horizontal en Go para ejecutarse como un proceso
continuo en una única instancia EC2. Implementa un ciclo MAPE-K: Monitor,
Analyze, Plan, Execute y Knowledge. El estado se mantiene en memoria y se
respalda en un archivo JSON local; no se utiliza Lambda ni DynamoDB.

## Estructura

```
main.go                      # daemon de EC2 y orquestación del ciclo MAPE-K
types/types.go               # configuración y modelos compartidos
monitor/monitor.go           # CloudWatch CPU y salud de targets del ALB
analyze/analyze.go           # histéresis, moving average y confirmación
plan/plan.go                 # cooldown, límites y delta de instancias
execute/execute.go           # operaciones EC2 y registro en el Target Group
state/state.go               # respaldo local del KnowledgeState en JSON
decisionlog/decisionlog.go   # decisiones JSON para CloudWatch Logs
infra/*.tf                   # VPC, subnets, ALB, IAM, EC2 y Launch Template
infra/controller-user-data.sh.tftpl   # configuración de la controladora
infra/autoscaler-controller.service    # servicio systemd de la EC2 controladora
load/k6/load-test.js         # generación de carga para la demostración
DEPLOY.md                    # despliegue, ejecución y demostración 10.2/10.5
```

La guía de despliegue con Terraform está en [DEPLOY.md](DEPLOY.md). La
infraestructura se define como código, no guarda credenciales ni valores
reales de la cuenta, y crea la VPC completa del experimento: dos subredes
públicas en dos AZ para el ALB y dos subredes privadas separadas en una misma
AZ para la flota web y la controladora.

## Decisiones

Las señales del controlador son:

- `MAINTAIN_CAPACITY`: no modificar la capacidad.
- `INCREASE_CAPACITY`: añadir una instancia.
- `REDUCE_CAPACITY`: retirar una instancia.

## Ejecución en EC2

El proceso se inicia una vez en la instancia EC2 y ejecuta un ciclo cada dos
minutos. El estado se conserva mientras el proceso permanezca activo.

La configuración se define mediante variables de entorno:

```powershell
$env:AUTOSCALER_INSTANCE_TAG="web-fleet"
$env:AUTOSCALER_TARGET_GROUP_ARN="arn:aws:elasticloadbalancing:region:account:targetgroup/name/id"
$env:AUTOSCALER_LAUNCH_TEMPLATE_ID="lt-0123456789abcdef0"
$env:AUTOSCALER_STATE_FILE="C:\ProgramData\autoscaler-controller\state.json"
$env:AUTOSCALER_USE_MOVING_AVERAGE="false"
$env:AUTOSCALER_MOVING_AVERAGE_WINDOW="3"
go run .
```

`AUTOSCALER_USE_MOVING_AVERAGE` acepta `true` o `false`, y
`AUTOSCALER_MOVING_AVERAGE_WINDOW` indica cuántas lecturas se promedian. Estas
variables se leen al arrancar el proceso. Cambiarlas en el shell o en un
`EnvironmentFile` de systemd no modifica el entorno de un proceso ya iniciado;
hay que reiniciar el servicio para aplicar el nuevo valor. Para modificarlo
sin reiniciar habría que añadir un archivo de configuración recargable o un
manejador de `SIGHUP`, no usar únicamente variables de entorno.

Al arrancar, el controlador valida las variables y límites de configuración,
y consulta `DescribeInstances` para descubrir instancias `pending` o `running`
con el tag `role=web-fleet`. Si AWS no está disponible, el controlador se
detiene de forma segura y no ejecuta decisiones de escalado basadas en un
inventario local potencialmente obsoleto. El archivo de estado
por defecto es `autoscaler-state.json`; en producción debe ubicarse en un
directorio persistente y protegido, por ejemplo `/var/lib/autoscaler-controller/state.json`.

En Linux, la configuración equivalente puede definirse en el servicio
systemd que ejecute el binario. La instancia EC2 necesita un instance profile
con permisos para CloudWatch, `ec2:DescribeInstances`, EC2 y Elastic Load
Balancing v2.

## Flujo de un ciclo

1. `Monitor` consulta CPU promedio en CloudWatch y targets saludables en el ALB.
2. Si no hay datapoints de CPU, el snapshot se marca como `HasCPUData=false`.
3. `Analyze` devuelve `MAINTAIN_CAPACITY` cuando faltan métricas; nunca interpreta la ausencia como CPU `0%`.
4. `Analyze` aplica moving average opcional e histéresis cuando sí hay datos.
5. `Analyze` confirma la misma señal durante `EvaluationPeriods` ciclos.
6. `Plan` respeta cooldown y los límites mínimo/máximo.
7. `Execute` crea o elimina instancias y las registra o desregistra del ALB.
8. `decisionlog` escribe la señal y el motivo en JSON.
9. El `KnowledgeState` actualizado queda en memoria y se guarda en el archivo
  local para poder recuperarlo tras un reinicio.

Cada evento de decisión incluye `avg_cpu`, `evaluated_cpu`, `has_cpu_data`,
targets saludables y totales, señal detectada, confirmación de la señal,
decisión final, delta, capacidad anterior y siguiente, límites mínimo/máximo,
estado del cooldown, contador de evaluaciones consecutivas y estado de la
ejecución. Si una operación AWS falla, también incluye `execution_error`.

Durante un `scale-out`, el controlador comprueba que las nuevas instancias
estén `running` antes de registrarlas en el Target Group y espera a que estén
`healthy`. Si el registro o el health check falla, desregistra y termina las
instancias nuevas como rollback. Aunque falle el deregistro, el controlador
intenta igualmente terminar las instancias y devuelve todos los errores para
intervención operativa.

Durante un `scale-in`, primero desregistra los targets y hace polling hasta que
queden en estado `unused`. Ese polling usa `DeregistrationDelay` como timeout
máximo, por lo que no añade una segunda espera fija antes de terminar las
instancias.

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
tu entorno para aplicar un mínimo privilegio más estricto. Todas las
instancias gestionadas deben tener el tag `role=web-fleet` para que puedan
descubrirse y terminarse durante un scale-in.

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

- Reconciliación avanzada de operaciones parcialmente completadas, incluyendo
  recuperación manual si falla también el rollback.
- Ampliar los tests unitarios de `analyze` y `plan` con más casos de frontera.
- Métricas propias (`cloudwatch:PutMetricData`) si quieres complementar CPU
  con latencia percibida de la app.
- Recuperación externa del estado si también se necesita tolerar la pérdida del
  disco local de la EC2 controladora.
