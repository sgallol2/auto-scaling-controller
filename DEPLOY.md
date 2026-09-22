````markdown
# Despliegue con Terraform en AWS Academy

Este es el único procedimiento de despliegue de infraestructura del proyecto.
Terraform crea la VPC, las subredes, el NAT Gateway, los Security Groups, el
ALB, el Target Group, el Launch Template, el IAM Instance Profile, la
instancia web inicial y la EC2 controladora.

## Topología

```text
                        Availability Zone A       Availability Zone B
                      +-----------------------+  +-----------------------+
Internet -> ALB        | public subnet A       |  | public subnet B       |
                      +-----------------------+  +-----------------------+
                            | NAT Gateway
                            v
                      +-----------------------+
                      | private web subnet A  | -> instancias web
                      | private controller A  | -> EC2 controladora
                      +-----------------------+
```
````

La aplicación web y la controladora están en dos subredes privadas distintas,
pero ambas en `availability_zone_a`, según el diseño solicitado. El ALB usa
subredes públicas en dos AZ porque un Application Load Balancer necesita como
mínimo dos AZ. El NAT Gateway permite que la controladora privada acceda a las
APIs de AWS.

Esta topología es adecuada para el MVP, pero la flota web queda en una sola
AZ. Terraform no crea Auto Scaling Groups ni políticas gestionadas: el
controlador sigue decidiendo manualmente cuándo llamar a EC2 y ELBv2.

## Requisitos

- Cuenta AWS Academy activa.
- Credenciales temporales configuradas fuera del repositorio, o una sesión
  equivalente usada por el proveedor AWS de Terraform.
- Terraform >= 1.5.
- Una AMI de aplicación que escuche HTTP en el puerto 80.
- Una AMI de controladora que contenga:
- `/opt/autoscaler-controller/controller`
- `/etc/autoscaler-controller.env`
- `/etc/systemd/system/autoscaler-controller.service`

- Un key pair existente en la región elegida.

No guardes access keys, secret keys, session tokens, contraseñas ni archivos
`.tfvars` reales en Git. `terraform.tfvars` está excluido por `.gitignore`.

## Preparar las AMI

Compila el controlador para Linux desde la raíz del proyecto:

```powershell
$env:GOOS="linux"
$env:GOARCH="amd64"
$env:CGO_ENABLED="0"
go build -trimpath -o controller .
Remove-Item Env:GOOS
Remove-Item Env:GOARCH
Remove-Item Env:CGO_ENABLED

```

Prepara una AMI Linux de controladora con el binario, las variables de entorno y el servicio:

```bash
sudo useradd --system --home /opt/autoscaler-controller autoscaler
sudo install -d -o autoscaler -g autoscaler /opt/autoscaler-controller
sudo install -d -o autoscaler -g autoscaler /var/lib/autoscaler-controller
sudo install -m 0755 controller /opt/autoscaler-controller/controller
sudo install -m 0644 autoscaler-controller.service /etc/systemd/system/autoscaler-controller.service

```

### Configuración del controlador y variables de entorno (`loadConfig`)

El controlador gestiona sus parámetros bajo dos esquemas (definidos en la función `loadConfig()`):

#### 1. Variables dinámicas (Leídas desde `/etc/autoscaler-controller.env`)

Deben coincidir con los nombres exactos leídos por `os.Getenv`:

```bash
sudo tee /etc/autoscaler-controller.env > /dev/null <<'EOF'
# Firma de API y región implícita de AWS SDK Go v2
AWS_REGION=us-east-1
AWS_DEFAULT_REGION=us-east-1

# Parámetros leídos por loadConfig()
AUTOSCALER_INSTANCE_TAG=web-fleet
AUTOSCALER_TARGET_GROUP_ARN=arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/sample/xxxx
AUTOSCALER_LAUNCH_TEMPLATE_ID=lt-xxxxxxxxxxxxxxxxx
AUTOSCALER_STATE_FILE=/var/lib/autoscaler-controller/autoscaler-state.json

# Opciones con valor por defecto
AUTOSCALER_USE_MOVING_AVERAGE=false
AUTOSCALER_MOVING_AVERAGE_WINDOW=3
EOF

sudo chmod 0600 /etc/autoscaler-controller.env
sudo chown autoscaler:autoscaler /etc/autoscaler-controller.env

```

#### 2. Parámetros Hardcodeados en el Binario Go

Cualquier ajuste de los siguientes valores requiere **recompilar el binario** en Go:

| Parámetro               | Valor Hardcodeado              | Descripción                                           |
| ----------------------- | ------------------------------ | ----------------------------------------------------- |
| `MinInstances`          | `1`                            | Mínimo de instancias en la flota                      |
| `MaxInstances`          | `3`                            | Límite máximo de escalado horizontal                  |
| `ScaleOutThreshold`     | `5.0` (% CPU)                  | Umbral para disparar un _Scale-Out_                   |
| `ScaleInThreshold`      | `2.0` (% CPU)                  | Umbral para disparar un _Scale-In_                    |
| `EvaluationPeriods`     | `2`                            | Muestras consecutivas requeridas para confirmar señal |
| `CooldownDuration`      | `120s` (`120_000_000_000` ns)  | Tiempo de espera entre acciones de escalado           |
| `MetricWindow`          | `5 min` (`300_000_000_000` ns) | Ventana temporal analizada en CloudWatch              |
| `InstanceReadyTimeout`  | `5 min`                        | Tiempo límite de espera a que EC2 esté `running`      |
| `HealthCheckTimeout`    | `5 min`                        | Tiempo límite para que el target pase a `healthy`     |
| `DeregistrationDelay`   | `30s`                          | Tiempo de espera en estado `draining`                 |
| `OperationPollInterval` | `10s`                          | Frecuencia de polling de estado durante operaciones   |
| `PollInterval`          | `30s`                          | Frecuencia de ejecución del ciclo completo MAPE-K     |

> **Nota:** En el despliegue con Terraform, el bloque `user_data` inyecta dinámicamente los valores reales de `AUTOSCALER_TARGET_GROUP_ARN` y `AUTOSCALER_LAUNCH_TEMPLATE_ID` obtenidos durante la creación de la infraestructura dentro de `/etc/autoscaler-controller.env`.

Crea una AMI a partir de esa instancia y usa su ID como `controller_ami_id`. La AMI de aplicación debe estar preparada de forma similar, pero con el servidor HTTP de la aplicación.

## Configuración Terraform

Desde `infra/`, crea un archivo local:

```powershell
Copy-Item .\terraform.tfvars.example .\terraform.tfvars

```

Edita `terraform.tfvars` y reemplaza los valores de ejemplo:

```hcl
aws_region          = "us-east-1"
availability_zone_a = "us-east-1a"
availability_zone_b = "us-east-1b"

app_ami_id        = "ami-REPLACE_APP"
controller_ami_id = "ami-REPLACE_CONTROLLER"
key_name          = "REPLACE_KEY_PAIR"

app_instance_type        = "t2.micro"
controller_instance_type = "t2.micro"
managed_instance_tag     = "web-fleet"
use_moving_average       = false
moving_average_window    = 3

```

Los bloques CIDR predeterminados crean:

- VPC: `10.20.0.0/16`.
- Public A: `10.20.1.0/24` en AZ A.
- Public B: `10.20.2.0/24` en AZ B.
- Private web A: `10.20.11.0/24` en AZ A.
- Private controller A: `10.20.12.0/24` en AZ A.

Si esos rangos chocan con la red del laboratorio, cambia las variables CIDR.

## Inicializar y revisar

Desde la raíz del repositorio:

```powershell
terraform -chdir=infra init
terraform -chdir=infra fmt -recursive
terraform -chdir=infra validate
terraform -chdir=infra plan -out=tfplan

```

Revisa especialmente el plan antes de aplicar. Debe crear una VPC completa y
no modificar recursos ajenos al proyecto.

## Aplicar

```powershell
terraform -chdir=infra apply tfplan

```

Terraform mostrará los outputs principales:

- `load_balancer_dns_name`.
- `target_group_arn`.
- `launch_template_id`.
- `initial_web_instance_id`.
- `controller_instance_id`.
- IDs de subredes públicas y privadas.

La instancia web inicial se registra automáticamente en el Target Group. La EC2 controladora recibe mediante `user_data` el ARN del Target Group y el ID del Launch Template, configurando `AUTOSCALER_TARGET_GROUP_ARN` y `AUTOSCALER_LAUNCH_TEMPLATE_ID` en `/etc/autoscaler-controller.env` antes de activar `systemd`.

## Verificar

Consulta los outputs:

```powershell
terraform -chdir=infra output

```

Comprueba la aplicación mediante el DNS del ALB:

```powershell
$alb = terraform -chdir=infra output -raw load_balancer_dns_name
curl.exe "http://$alb/"

```

Comprueba los targets:

```powershell
$targetGroup = terraform -chdir=infra output -raw target_group_arn
aws elbv2 describe-target-health --target-group-arn $targetGroup --region us-east-1

```

Conéctate a la instancia controller usando el key pair y revisa:

```bash
sudo systemctl status autoscaler-controller
sudo journalctl -u autoscaler-controller -f

```

La controladora debe descubrir la instancia inicial mediante `DescribeInstances`
y producir eventos JSON con campos como `detected_signal`,
`signal_confirmed`, `avg_cpu`, `current_instance_count`,
`next_instance_count` y `execution_status`.

## Demostración con k6

Desde el equipo que tenga conectividad al ALB:

```powershell
$env:TARGET_URL="http://<ALB_DNS_NAME>"
$env:RAMP_SECONDS="120"
$env:PEAK_VUS="20"
k6 run .\load\k6\load-test.js

```

Observa simultáneamente:

1. Aumento de carga en k6.
2. Aumento de `CPUUtilization` en CloudWatch por encima del **5.0%** (`ScaleOutThreshold`).
3. Confirmación tras **2 periodos de evaluación consecutivos** (`EvaluationPeriods=2`).
4. Decisión `INCREASE_CAPACITY` en los logs.
5. Nueva instancia creada con la etiqueta especificada en `AUTOSCALER_INSTANCE_TAG`.
6. Estado `running` y luego `healthy`.
7. Entrada en periodo de **cooldown de 120 segundos** (`CooldownDuration`).
8. Reducción de carga por debajo del **2.0%** (`ScaleInThreshold`).
9. Decisión `REDUCE_CAPACITY` tras confirmación y cooldown.
10. Drenaje (`DeregistrationDelay=30s`), desregistro y terminación de la instancia.

El controlador ejecuta un ciclo completo MAPE-K cada **30 segundos** (`PollInterval=30s`), por lo que la demostración completa puede tardar varios minutos.

## Destruir el entorno

Después del experimento:

```powershell
terraform -chdir=infra destroy

```

Revisa el plan de destrucción y confirma que corresponde únicamente a los
recursos del proyecto. El destroy elimina también el NAT Gateway y su Elastic
IP, el ALB, el Target Group, las instancias, los Security Groups, el IAM role
y la VPC.

No subas `terraform.tfvars`, `.terraform/`, `*.tfstate` ni `tfplan` al
repositorio.

```

```
