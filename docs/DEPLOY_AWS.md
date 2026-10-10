# Deploying to real AWS — bare IP, no ALB, no domain

Reaching the services needs **no load balancer, no Route 53 record and no TLS
certificate**. Tasks get public IPs, and a security group that allows only your
own address. [Why, and what you give up](../INSTRUCTIONS.md#1-no-alb-no-domain--does-that-work).

**Do this the day before the talk, not on the day.** A partial apply leaves a
half-built VPC to unpick by hand.

> **Use a personal or sandbox account.** This creates a VPC, an ECS cluster,
> four ECR repositories, IAM roles and a Secrets Manager secret. Do not point it
> at a company production, staging or development account.

**Time:** ~15 minutes the first time, of which `apply` is ~2.
**Cost:** cents per hour — [§7](#7--what-this-costs).

---

## 0 · Pick the account, and prove it

Getting this wrong is the most expensive mistake available here.

```sh
export AWS_PROFILE=demo          # whichever profile is your sandbox
aws sts get-caller-identity
```

**Read the `Account` number out loud and check it is the one you meant.**

Credentials, lowest risk first:

```sh
aws sso login --profile demo      # IAM Identity Center. Nothing stored. Preferred.
aws login --profile demo          # browser flow, temporary credentials
aws configure --profile demo      # long-lived keys. Last resort; rotate afterwards.
```

If you have been running the local stack, **clear the emulator overrides** —
otherwise every command below quietly goes to your laptop and you will think
nothing deployed:

```sh
unset AWS_ENDPOINT_URL AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
aws sts get-caller-identity       # must still work, via the profile
```

### Check the region has Fargate quota

A region with a Fargate vCPU quota of **0** reports only *"your account is
currently blocked"* — which reads like a billing problem and is not. This cost
me a region switch on the first run.

List them by name rather than by quota code, so there is no code to get wrong:

```sh
export AWS_REGION=us-east-1
aws service-quotas list-service-quotas --service-code fargate \
  --query 'Quotas[].{Name:QuotaName,Value:Value}' --output table
```

Look for **"Fargate On-Demand vCPU resource count"**. This deployment needs
**1 vCPU** (four tasks at 0.25), so anything above ~8 is plenty. If it is **0**,
request an increase or pick another region — us-east-1, us-west-2 and
eu-central-1 all had quota when I checked; ap-south-1 was 0.

---

## 1 · Check your permissions

Full ECS + ECR + EC2 is **not enough.** You also need IAM (including
`iam:PassRole`), CloudWatch Logs, Secrets Manager, and Cloud Map **plus Route
53** — Cloud Map creates a private hosted zone, so it needs Route 53 even
though this deployment publishes no public DNS.

```sh
aws iam list-roles --max-items 1                     >/dev/null && echo "iam ok"
aws logs describe-log-groups --limit 1               >/dev/null && echo "logs ok"
aws secretsmanager list-secrets --max-results 1      >/dev/null && echo "secrets ok"
aws servicediscovery list-namespaces --max-results 1 >/dev/null && echo "cloudmap ok"
aws route53 list-hosted-zones --max-items 1          >/dev/null && echo "route53 ok"
aws ecs list-clusters                                >/dev/null && echo "ecs ok"
aws ecr describe-repositories --max-results 1        >/dev/null && echo "ecr ok"
```

**Read access succeeding does not prove you can create.** Step 4's plan is the
real test. [`AWS_PERMISSIONS.md`](AWS_PERMISSIONS.md) has the exact policy, and
`terraform/envs/aws/minimal.tfvars` switches off the parts needing permissions
you may not have.

---

## 2 · Create the ECR repositories first

Terraform creates them, but the images have to exist before the services can
start — so apply that one module first.

```sh
cd terraform/envs/aws
terraform init

terraform apply \
  -target=module.deployment.module.ecr \
  -var user_image=placeholder -var product_image=placeholder \
  -var order_image=placeholder -var gateway_image=placeholder
```

---

## 3 · Build and push, ARM64

One command. It reads the account and region from your *current* credentials,
so what it pushes to is what Terraform will deploy from.

```sh
cd ../../..
make images-push
```

It prints the four image URIs to paste into the next step. ARM64 because
Fargate Graviton is cheaper, and on Apple silicon it is a native build with no
emulation.

<details>
<summary>The same thing by hand, if you prefer</summary>

```sh
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
REGION=${AWS_REGION:-us-east-1}
ECR="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com"
aws ecr get-login-password --region "$REGION" \
  | docker login --username AWS --password-stdin "$ECR"

for svc in userd productsd; do
  case $svc in
    userd)     FLAG=-user-db;    DB=user.db ;;
    productsd) FLAG=-product-db; DB=product.db ;;
  esac
  docker build --platform linux/arm64 -f build/Dockerfile.seeded \
    --build-arg SERVICE=$svc --build-arg SEED_FLAG=$FLAG --build-arg DB_FILE=$DB \
    -t "$ECR/$svc:0.1.0" .
  docker push "$ECR/$svc:0.1.0"
done

docker build --platform linux/arm64 -f build/Dockerfile.stateful \
  --build-arg SERVICE=orderd -t "$ECR/orderd:0.1.0" . && docker push "$ECR/orderd:0.1.0"
docker build --platform linux/arm64 -f build/Dockerfile.stateless \
  --build-arg SERVICE=gatewayd -t "$ECR/gatewayd:0.1.0" . && docker push "$ECR/gatewayd:0.1.0"
```
</details>

---

## 4 · Allow-list your IP, read the plan, then apply

**This step decides whether you can reach anything.** With no load balancer,
access is one security-group rule for your own address.

```sh
curl -s ifconfig.me; echo
```

It **must be IPv4.** An IPv6 address in a `cidr_ipv4` field fails the apply
partway through — I caught that in a plan once, which is why the plan gets read.

```sh
cd terraform/envs/aws
cat > terraform.tfvars <<'EOF'
user_image    = "<paste from make images-push>"
product_image = "<paste>"
order_image   = "<paste>"
gateway_image = "<paste>"

# Your public IPv4 only. EMPTY BY DEFAULT, which means nothing is reachable.
operator_ingress_cidrs = ["YOUR.IPV4.HERE/32"]
EOF

terraform plan -out=tf.plan
```

**Read the plan.** Roughly: 1 VPC, 2 subnets, 1 internet gateway, 1 route table
+ 2 associations, 1 security group + rules, 4 ECR repositories, 1 ECS cluster,
1 Cloud Map namespace, 4 Cloud Map services, 1 log group, 2 IAM roles, 1
secret, 4 task definitions, 4 ECS services.

```sh
terraform apply tf.plan
```

**~2 minutes**, and it is that fast because there is no RDS and no NAT Gateway.

> **If the first apply fails with "Unable to assume the service linked role"**,
> the ECS service-linked role exists but has not propagated yet in a fresh
> account. **Retry the apply.** It worked second time for me.

---

## 5 · Verify

```sh
cd ../../..
make ps-aws
```

Seven proofs, now against real ECS — and this time `healthStatus` reports
`HEALTHY` and `launchType` reports `FARGATE`, neither of which the local
emulator echoes back.

### Get the addresses

```sh
make aws-ip
```

```
SERVICE     PORT  PUBLIC IP    TRY THIS
userd       50051 54.x.x.x     grpcurl -plaintext 54.x.x.x:50051 list
productsd   50053 3.x.x.x      grpcurl -plaintext 3.x.x.x:50053 list
orderd      50052 44.x.x.x     grpcurl -plaintext 44.x.x.x:50052 list
gatewayd    8080  18.x.x.x     curl http://18.x.x.x:8080/healthz
```

### Test it

```sh
U=54.x.x.x:50051; O=44.x.x.x:50052; P=3.x.x.x:50053
BASE=http://18.x.x.x:8080

curl -s "$BASE/healthz"
grpcurl -plaintext $P list
grpcurl -plaintext -d '{"service":"userd"}' $U grpc.health.v1.Health/Check
grpcurl -plaintext -d '{"query":"cancelling"}' $P product.v1.ProductService/SearchProducts

TOKEN=$(grpcurl -plaintext -d '{"email":"demo@example.com","password":"demo-password"}' \
  $U user.v1.UserService/Login | jq -r .token)

grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"aws-1"}' \
  $O order.v1.OrderService/CreateOrder | jq '.order | {status, totalMinor}'

# the internal-only RPC: works over gRPC, 404 over REST
grpcurl -plaintext -d '{"items":[{"product_id":"p-1001","quantity":2}]}' \
  $P product.v1.ProductService/CheckAvailability | jq -r '.results[0].availability'
curl -s -o /dev/null -w 'REST -> %{http_code}\n' \
  -X POST "$BASE/v1/products:checkAvailability" -d '{}'

# the whole REST flow in one go
REST_BASE="$BASE" bash scripts/rest-smoke.sh
```

Every command in [`INSTRUCTIONS.md`](../INSTRUCTIONS.md) works here — just
repoint the variables.

**If it hangs, it is the security group about 95% of the time.** Your egress IP
changed, or you are on a different network:

```sh
curl -s ifconfig.me; echo
terraform -chdir=terraform/envs/aws apply -var 'operator_ingress_cidrs=["NEW.IPV4/32"]'
```

> **Re-run `make aws-ip` after every deploy or scale event.** `awsvpc` gives
> each task its own ENI, so the IP changes whenever the task is replaced.

### Scale something and watch Cloud Map

```sh
aws ecs update-service --cluster ecom-aws --service userd --desired-count 3
sleep 60

SVC=$(aws servicediscovery list-services \
  --query 'Services[?Name==`userd`].Id' --output text)
aws servicediscovery get-instances-health-status --service-id "$SVC"
```

**All three must be `HEALTHY`.** Unhealthy instances are excluded from DNS
answers, and the symptom is `code 14: "no children to pick from"` — see the
note on `health_check_custom_config` in
[`ARCHITECTURE.md`](ARCHITECTURE.md#cloud-map-and-route-53).

---

## 6 · Optional: a real database

By default each service keeps SQLite on its own task filesystem, which is why
`orderd` is pinned to one task. Turn RDS on and that lifts — **one**
`db.t4g.micro` with a **separate database per service**.

```sh
terraform -chdir=terraform/envs/aws apply -var use_rds=true
```

Adds **5–10 minutes** to the apply, so do it the day before. Then:

```sh
aws ecs update-service --cluster ecom-aws --service orderd --desired-count 3
```

The services create their own databases at boot and seed themselves, so there
is no migration step to run.

```sh
aws rds describe-db-instances \
  --query 'DBInstances[].{Id:DBInstanceIdentifier,Class:DBInstanceClass,Status:DBInstanceStatus,MultiAZ:MultiAZ}'
aws secretsmanager list-secrets --query 'SecretList[].Name'   # one DSN per service
```

---

## 7 · What this costs

us-east-1, on-demand. These figures come from third-party pricing data, because
AWS's pricing pages are JavaScript-rendered — treat them as close, not
authoritative, and check <https://aws.amazon.com/fargate/pricing/> and
<https://aws.amazon.com/rds/pricing/> for your region.

| | Rate | 3-hour window | A month, if you forget |
|---|---|---|---|
| 4 Fargate tasks, 0.25 vCPU / 0.5 GB, ARM64 | — | **a few cents** | ~$10 |
| RDS `db.t4g.micro`, Single-AZ (optional) | $0.016/hr | ~5¢ | ~$11.68 |
| + 20 GiB gp3, the minimum | $0.115/GB-mo | ~1¢ | ~$2.30 |
| CloudWatch Logs, 1-day retention | — | ~0 | ~0 |
| **NAT Gateway** | $0.045/hr | — | **~$33 — there is none here** |
| **ALB** | $0.0225/hr + LCUs | — | **~$16 — there is none here** |

The two expensive line items are deliberately absent. **The real risk is
forgetting to destroy**, not the hourly rate.

---

## 8 · Tear it down

**Do this before you leave the venue.**

```sh
# Drain first: ECS must deregister from Cloud Map before the namespace can go,
# or DeleteService returns ResourceInUse.
for s in userd productsd orderd gatewayd; do
  aws ecs update-service --cluster ecom-aws --service $s --desired-count 0
done
sleep 75

terraform -chdir=terraform/envs/aws destroy
```

Then **verify**, because "Destroy complete" is not the same as "nothing is
billing":

```sh
aws ecs list-clusters
aws ecr describe-repositories        --query 'repositories[].repositoryName'
aws servicediscovery list-namespaces --query 'Namespaces[].Name'
aws secretsmanager list-secrets      --query 'SecretList[].Name'
aws rds describe-db-instances        --query 'DBInstances[].DBInstanceIdentifier'
aws ec2 describe-vpcs --filters Name=tag:Project,Values=ecom --query 'Vpcs[].VpcId'
aws iam list-roles --query 'Roles[?starts_with(RoleName,`ecom-`)].RoleName'
```

All should be empty. If ECR repositories survive because they still hold
images, `force_delete = true` should have handled it; if not:

```sh
for r in userd productsd orderd gatewayd; do
  aws ecr delete-repository --repository-name "$r" --force
done
```

> **Check every region you deployed to**, not just the current one. On the first
> run of this project I had to verify us-east-1 **and** ap-south-1 after a
> region switch.

---

## Stage rules

- **Pre-provision.** Never a cold `terraform apply` on stage. Deploy the day
  before and demo a *delta* — `make ps-aws`, or a scale-out.
- **Check your egress IP from the venue.** `curl ifconfig.me` on the conference
  wifi; it is probably not the address you allow-listed at home. Keep the
  one-line `apply` that updates it ready to paste.
- **Pin every tag**, and pre-pull images the morning of.
- **Run `make aws-ip` right before you present** and keep the output in a file
  you can paste from.
- **Use a seeded user** (`demo@example.com` / `demo-password`) once `userd` is
  at more than one task on the SQLite setup — a `Register`ed user lives on
  exactly one task.
- **Record the demo** as insurance, and keep the `terraform plan` output.
