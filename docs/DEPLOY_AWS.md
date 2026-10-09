# Deploying to a real AWS account

Follow this **before the talk, not on the day.** A partial apply leaves a
half-built VPC you then have to unpick by hand.

> **Use a personal or sandbox account.** This creates a VPC, an ECS cluster, four
> ECR repositories, IAM roles and a Secrets Manager secret. Do not point it at a
> company production, staging or development account.

---

## 0 · Decide the account and region

```sh
# Personal account with a static key pair:
export AWS_PROFILE=my-personal
# ...or AWS IAM Identity Center (SSO):
aws sso login --profile my-sandbox
export AWS_PROFILE=my-sandbox

# Confirm WHICH account you are about to build in. Read the number out loud.
aws sts get-caller-identity
```

The stack defaults to **`us-east-1`**. Override with
`-var aws_region=...` — Fargate ARM64 is available in every commercial region,
so any of them works.

**Signing in without an access key** (preferred — nothing long-lived is stored):

```sh
aws login --profile awsug-bdq      # opens a browser; sign in with the console
                                   # username and password
aws configure set region us-east-1 --profile awsug-bdq
export AWS_PROFILE=awsug-bdq
```

`aws login` exchanges your console session for short-lived credentials and
refreshes them automatically. If it is unavailable for your account, AWS
CloudShell is the other no-key route — it now supports Docker, so the whole
build-and-push flow runs there.

## 1 · Check you have the permissions

Full ECS + ECR + EC2 is **not enough**. See
[`AWS_PERMISSIONS.md`](AWS_PERMISSIONS.md) for the exact policy; the short
version is you also need IAM (including `iam:PassRole`), CloudWatch Logs,
Secrets Manager, and Cloud Map **plus Route 53**.

```sh
aws iam list-roles --max-items 1                     >/dev/null && echo "iam ok"
aws logs describe-log-groups --limit 1               >/dev/null && echo "logs ok"
aws secretsmanager list-secrets --max-results 1      >/dev/null && echo "secrets ok"
aws servicediscovery list-namespaces --max-results 1 >/dev/null && echo "cloudmap ok"
aws route53 list-hosted-zones --max-items 1          >/dev/null && echo "route53 ok"
```

**Read access succeeding does not prove you can create.** Step 4's plan is the
real check.

If something is refused, `terraform/envs/aws/minimal.tfvars` switches off the
parts that need the permissions you may not have. What you lose is in
`AWS_PERMISSIONS.md`.

## 2 · Create the ECR repositories first

Terraform creates them, but you need them to exist before you can push images —
so apply just that target first.

```sh
cd terraform/envs/aws
terraform init

terraform apply \
  -target=module.stack.module.ecr \
  -var user_image=placeholder -var product_image=placeholder \
  -var order_image=placeholder -var gateway_image=placeholder

terraform output -json ecr_repository_urls
```

## 3 · Build and push the four images

ARM64, because Fargate Graviton is cheaper and it is native on Apple silicon.

```sh
cd ../../..                     # back to the repo root

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
  --build-arg SERVICE=orderd -t "$ECR/orderd:0.1.0" .
docker push "$ECR/orderd:0.1.0"

docker build --platform linux/arm64 -f build/Dockerfile.stateless \
  --build-arg SERVICE=gatewayd -t "$ECR/gatewayd:0.1.0" .
docker push "$ECR/gatewayd:0.1.0"
```

## 4 · Plan, read it, then apply

```sh
cd terraform/envs/aws

cat > terraform.tfvars <<EOF
user_image    = "$ECR/userd:0.1.0"
product_image = "$ECR/productsd:0.1.0"
order_image   = "$ECR/orderd:0.1.0"
gateway_image = "$ECR/gatewayd:0.1.0"

# Your public IP, so the demo client can reach gatewayd. Keep it tight.
# curl ifconfig.me
operator_ingress_cidrs = ["YOUR.IP.HERE/32"]
EOF

terraform plan -out=tf.plan
```

**Read the plan.** You are looking for roughly: 1 VPC, 2 subnets, 1 IGW, 1 route
table + 2 associations, 1 security group + rules, 4 ECR repos, 1 ECS cluster,
1 Cloud Map namespace, 4 Cloud Map services, 1 log group, 2 IAM roles, 1 secret,
4 task definitions, 4 ECS services.

```sh
terraform apply tf.plan
terraform output
```

Expect **~2 minutes**. There is no RDS, which is the main reason it is that fast.

## 5 · Verify

```sh
cd ../../..
make ps-aws
```

Then reach `gatewayd`. Two options — the second is better, see
[`DEMO_ACCESS.md`](DEMO_ACCESS.md).

**a) Public IP** (needs `operator_ingress_cidrs` set above):

```sh
CLUSTER=payments-aws
TASK=$(aws ecs list-tasks --cluster $CLUSTER --service-name gatewayd \
  --query 'taskArns[0]' --output text)
ENI=$(aws ecs describe-tasks --cluster $CLUSTER --tasks "$TASK" \
  --query 'tasks[0].attachments[0].details[?name==`networkInterfaceId`].value' --output text)
IP=$(aws ec2 describe-network-interfaces --network-interface-ids "$ENI" \
  --query 'NetworkInterfaces[0].Association.PublicIp' --output text)

curl -s "http://$IP:8080/healthz"
REST_BASE="http://$IP:8080" bash scripts/rest-smoke.sh
```

**b) SSM port forwarding** — no public IP, no inbound rule:

```sh
RUNTIME=$(aws ecs describe-tasks --cluster $CLUSTER --tasks "$TASK" \
  --query 'tasks[0].containers[0].runtimeId' --output text)

aws ssm start-session \
  --target "ecs:${CLUSTER}_${TASK##*/}_${RUNTIME}" \
  --document-name AWS-StartPortForwardingSession \
  --parameters '{"localPortNumber":["8080"],"portNumber":["8080"]}'

# in another terminal
bash scripts/rest-smoke.sh
```

## 6 · The load-balancing demo, on real AWS

This is the only segment that needs more than one task, and on AWS **Cloud Map
does the DNS for real** — no alias shim.

```sh
aws ecs update-service --cluster payments-aws --service userd --desired-count 3
aws ecs describe-services --cluster payments-aws --services userd \
  --query 'services[0].{Desired:desiredCount,Running:runningCount}'
```

Then drive load and watch per-task metrics. Scale `userd` or `productsd`.
**Never `orderd`** — Terraform refuses:

```sh
make show-guard
```

## 7 · Tear it down

```sh
cd terraform/envs/aws
terraform destroy
```

**Do this before you leave the venue.** Left running, the four Fargate tasks are
the only real cost — there is no NAT Gateway and no RDS by design.

If `destroy` leaves ECR repositories behind because they still hold images, the
`force_delete = true` on the repos should prevent that; if it does not:

```sh
for r in userd productsd orderd gatewayd; do
  aws ecr delete-repository --repository-name "$r" --force
done
```

---

## Stage rules

- **Pre-provision.** Never run a cold `terraform apply` on stage. Deploy the day
  before and live-demo a *delta*: `make scale N=3`, or killing a task.
- **Check your egress IP at the venue.** `curl ifconfig.me` from the room — the
  conference NAT may not be the IP you allow-listed. Keep a wider rule ready to
  paste, or use SSM port forwarding and avoid the problem entirely.
- **Pre-pull images** the morning of, and pin every tag.
- **Record the demo** as insurance, and keep `terraform plan` output in a file.
- **Use a seeded user** (`demo@example.com` / `demo-password`) once `userd` is at
  more than one task. A freshly registered user exists on exactly one task.
