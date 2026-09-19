# Despliegue manual en AWS Academy

Esta guía utiliza recursos creados manualmente desde la consola de AWS y comandos puntuales de AWS CLI para verificar el despliegue. No es necesario ejecutar `infra/deploy.ps1` ni usar Terraform o CloudFormation.

## Arquitectura

```text
k6 -> ALB público (2 AZ)
                            |
                            v
             Target Group -> instancias web privadas
                                                            ^
                                                            |
                                        EC2 controladora privada
                                        -> CloudWatch, EC2 y ELBv2
```

El controlador no usa Auto Scaling Groups ni políticas gestionadas de AWS. Decide manualmente cuándo ejecutar `RunInstances` y `TerminateInstances`.

### Topología recomendada para el MVP

- **Dos Availability Zones**: necesarias para un Application Load Balancer
    internet-facing.
- **Dos subnets públicas**, una por AZ: solo para las interfaces del ALB. Deben
    tener una ruta hacia un Internet Gateway.
- **Una subnet privada de aplicación**: para la instancia web inicial y las
    instancias creadas por el Launch Template. El código actual usa una única
    `APP_SUBNET_ID`, por lo que todas las instancias web del MVP se lanzan en esa
    subnet y, por tanto, en una sola AZ.
- **Una subnet privada de controladora**: para la EC2 que ejecuta el proceso.
    Debe tener salida HTTPS mediante NAT Gateway o VPC Endpoints para acceder a
    CloudWatch, EC2 y ELBv2.

Esta topología permite demostrar el autoescalado, pero no proporciona alta
disponibilidad completa de la flota web porque el Launch Template apunta a una
sola subnet/AZ. Para producción real habría que distribuir las instancias en
varias AZ, por ejemplo usando varios Launch Templates o una estrategia de
selección de subnet.

### Alternativa simplificada para AWS Academy

Si el laboratorio no ofrece NAT Gateway ni VPC Endpoints, puedes colocar la
controladora y la subnet de aplicación en subnets públicas con salida por
Internet Gateway. Mantén el puerto 80 de las instancias web permitido solo
desde el Security Group del ALB y limita SSH a tu IP. Esta alternativa es
válida para la demostración académica, pero es menos segura que la topología
privada.

## Reglas de seguridad

- No se guardan access keys, secret keys, session tokens ni contraseñas en el repositorio.
- Usa la sesión temporal de AWS Academy o el instance profile de la EC2.
- No pegues credenciales en `/etc/autoscaler-controller.env`.
- Los valores marcados como `<REEMPLAZAR>` son datos de tu cuenta y no deben convertirse en valores fijos del repositorio.

## 1. Preparar las AMI

Necesitas dos imágenes Linux.

### AMI de aplicación

Debe ejecutar una aplicación HTTP en el puerto 80 y responder con estado 2xx. Comprueba desde la propia instancia:

```bash
curl http://localhost/
```

La AMI debe incluir todo lo necesario para arrancar la aplicación sin intervención humana. El Launch Template añadirá la etiqueta:

```text
role=web-fleet
```

### AMI de la controladora

Compila el binario desde tu equipo, sin incluir credenciales:

```powershell
$env:GOOS="linux"
$env:GOARCH="amd64"
go build -o controller .
Remove-Item Env:GOOS
Remove-Item Env:GOARCH
```

En la EC2 controladora prepara las rutas:

```bash
sudo useradd --system --home /opt/autoscaler-controller autoscaler
sudo install -d -o autoscaler -g autoscaler /opt/autoscaler-controller
sudo install -d -o autoscaler -g autoscaler /var/lib/autoscaler-controller
sudo install -m 0755 controller /opt/autoscaler-controller/controller
```

Copia `infra/autoscaler-controller.service` a:

```bash
sudo install -m 0644 autoscaler-controller.service /etc/systemd/system/autoscaler-controller.service
```

## 2. Crear Security Groups

Crea manualmente tres Security Groups en la VPC elegida.

### Security Group del ALB

Regla de entrada:

| Protocolo | Puerto | Origen |
|---|---:|---|
| TCP | 80 | `0.0.0.0/0` para el experimento |

Las reglas de salida deben permitir tráfico hacia el Security Group de las instancias web.

### Security Group de las instancias web

Regla de entrada:

| Protocolo | Puerto | Origen |
|---|---:|---|
| TCP | 80 | Security Group del ALB |

No abras el puerto 80 de las instancias web directamente a Internet.

### Security Group de la controladora

Permite SSH únicamente desde tu IP de laboratorio si necesitas acceder por terminal. Permite salida HTTPS para las APIs de AWS.

Guarda estos IDs:

```text
ALB_SECURITY_GROUP_ID=<REEMPLAZAR>
APP_SECURITY_GROUP_ID=<REEMPLAZAR>
CONTROLLER_SECURITY_GROUP_ID=<REEMPLAZAR>
```

## 3. Crear Target Group

En EC2 > Load Balancing > Target Groups:

1. Tipo de target: **Instances**.
2. Protocolo: **HTTP**.
3. Puerto: `80`.
4. VPC: la VPC seleccionada.
5. Health check path: `/` o el endpoint real de la aplicación.
6. Healthy threshold: por ejemplo `2`.
7. Unhealthy threshold: por ejemplo `2`.
8. Deregistration delay: `30` segundos, coherente con el controlador.

Registra una instancia web inicial que tenga `role=web-fleet` y espera a que aparezca como `healthy`.

Guarda:

```text
TARGET_GROUP_ARN=<REEMPLAZAR>
```

## 4. Crear Application Load Balancer

En EC2 > Load Balancers:

1. Tipo: **Application Load Balancer**.
2. Scheme: `Internet-facing` para la demo desde tu equipo.
3. Selecciona al menos dos subnets de Availability Zones distintas.
4. Asigna el Security Group del ALB.
5. Crea un listener HTTP en el puerto `80`.
6. Reenvía las peticiones al Target Group anterior.

Guarda el DNS del ALB:

```text
ALB_DNS_NAME=<REEMPLAZAR>
```

Comprueba:

```powershell
curl.exe http://<ALB_DNS_NAME>/
```

## 5. Crear Launch Template

En EC2 > Launch Templates crea un template para las instancias web:

1. AMI: la AMI de aplicación.
2. Instance type: uno permitido por AWS Academy, por ejemplo `t2.micro` o `t3.micro` según la región.
3. Key pair: el necesario para el laboratorio, si aplica.
4. Security Group: `APP_SECURITY_GROUP_ID`.
5. Subnet: la subnet de aplicación elegida.
6. IAM instance profile: el de la aplicación, si la aplicación lo necesita.
7. User data: solo el arranque de la aplicación, sin secretos.
8. Resource tag:

```text
Key: role
Value: web-fleet
```

Guarda:

```text
LAUNCH_TEMPLATE_ID=<REEMPLAZAR>
APP_SUBNET_ID=<REEMPLAZAR>
APP_AMI_ID=<REEMPLAZAR>
```

La subnet del template es obligatoria: el controlador llama a `RunInstances` usando únicamente el Launch Template.

## 6. Crear el IAM role de la controladora

En IAM:

1. Crea un role para EC2.
2. Como entidad confiable selecciona `EC2`.
3. Crea una política inline usando `infra/controller-policy.json.template` como referencia.
4. Sustituye `REGION`, `ACCOUNT_ID`, `SUBNET_ID`, `APP_SECURITY_GROUP_ID`, `AMI_ID`, `LAUNCH_TEMPLATE_ID`, `TARGET_GROUP_NAME` y `TARGET_GROUP_ID` por valores de tu cuenta.
5. Revisa la política antes de guardarla.
6. Asocia el role al instance profile de la EC2 controladora.

Permisos necesarios:

- `cloudwatch:GetMetricData`.
- `ec2:DescribeInstances`.
- `ec2:RunInstances` para el Launch Template y sus dependencias.
- `ec2:CreateTags` solo durante `RunInstances`.
- `ec2:TerminateInstances` solo para `role=web-fleet`.
- Operaciones del Target Group.

No añadas permisos de Auto Scaling, Application Auto Scaling o políticas de escalado gestionadas.

## 7. Crear la EC2 controladora

Lanza una instancia usando la AMI de la controladora:

1. Selecciona la misma región y VPC.
2. Asigna el Security Group de la controladora.
3. Asocia el instance profile creado en el paso anterior.
4. Usa una subnet con salida a Internet o conectividad hacia los endpoints de CloudWatch, EC2 y ELBv2.
5. Conéctate por SSH desde tu IP de laboratorio.

Configura el entorno en `/etc/autoscaler-controller.env`:

```bash
sudo tee /etc/autoscaler-controller.env >/dev/null <<'EOF'
AUTOSCALER_INSTANCE_TAG=web-fleet
AUTOSCALER_TARGET_GROUP_ARN=<TARGET_GROUP_ARN>
AUTOSCALER_LAUNCH_TEMPLATE_ID=<LAUNCH_TEMPLATE_ID>
AUTOSCALER_STATE_FILE=/var/lib/autoscaler-controller/state.json
AUTOSCALER_USE_MOVING_AVERAGE=false
AUTOSCALER_MOVING_AVERAGE_WINDOW=3
EOF
sudo chmod 600 /etc/autoscaler-controller.env
```

Activa el servicio:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now autoscaler-controller
sudo systemctl status autoscaler-controller
sudo journalctl -u autoscaler-controller -f
```

El proceso debe descubrir la instancia inicial mediante `DescribeInstances`. Si no encuentra ninguna instancia `pending` o `running` con `role=web-fleet`, se detendrá de forma segura.

## 8. Verificar manualmente

Con AWS CLI configurado en tu sesión temporal:

```powershell
aws sts get-caller-identity
aws ec2 describe-instances --filters "Name=tag:role,Values=web-fleet" "Name=instance-state-name,Values=pending,running" --region <REGION>
aws elbv2 describe-target-health --target-group-arn <TARGET_GROUP_ARN> --region <REGION>
```

En los logs de la controladora debes ver eventos JSON con campos como:

```text
detected_signal, signal_confirmed, avg_cpu, current_instance_count, next_instance_count, execution_status
```

## 9. Demostración 10.5 con k6

Instala k6 fuera del repositorio y ejecuta desde tu equipo:

```powershell
$env:TARGET_URL="http://<ALB_DNS_NAME>"
$env:RAMP_SECONDS="120"
$env:PEAK_VUS="20"
k6 run .\load\k6\load-test.js
```

Durante la demostración observa simultáneamente:

1. La carga aumenta mediante k6.
2. CloudWatch muestra el aumento de `CPUUtilization`.
3. El log muestra `INCREASE_CAPACITY`.
4. EC2 crea una nueva instancia etiquetada `role=web-fleet`.
5. El controlador espera `running` y `healthy`.
6. El ALB empieza a usar el nuevo target.
7. Al reducirse la carga, aparece `REDUCE_CAPACITY`.
8. El target se desregistra, se drenan conexiones y la instancia termina.

No hagas cambios manuales durante la ejecución de la prueba. El intervalo normal es de dos minutos, la confirmación necesita dos evaluaciones y el health check puede añadir varios minutos.

## Limpieza manual

Antes de terminar AWS Academy:

1. Detén el servicio en la EC2 controladora.
2. Termina la EC2 controladora.
3. Desregistra y termina las instancias web restantes.
4. Elimina el Launch Template.
5. Elimina el listener y el ALB.
6. Elimina el Target Group.
7. Elimina los Security Groups.
8. Elimina el instance profile, role y política inline.
9. Elimina volúmenes EBS o Elastic IPs que ya no uses.

Los scripts de `infra/` y el archivo k6 permanecen en el repositorio como artefactos opcionales, pero este procedimiento manual no depende de ellos.
