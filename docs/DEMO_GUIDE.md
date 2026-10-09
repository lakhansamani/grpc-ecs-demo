# Demo guide — from a cold laptop to a working ECS deployment

Everything here was run start to finish. Copy-paste in order.

**Contents**
1. [What you need](#1-what-you-need)
2. [Fresh start — every command in order](#2-fresh-start--every-command-in-order)
3. [Seeing each AWS component locally](#3-seeing-each-aws-component-locally)
4. [Manual tests](#4-manual-tests)
5. [The demo, in stage order](#5-the-demo-in-stage-order)
6. [Teardown](#6-teardown)
7. [When something breaks](#7-when-something-breaks)
8. [What is and is not testable locally](#8-what-is-and-is-not-testable-locally)

Architecture and what each AWS component is for:
[`ARCHITECTURE.md`](ARCHITECTURE.md).

---

## 1 · What you need

```sh
go version        # 1.27+
docker --version  # running, and Docker Desktop with 4GB+ allocated
terraform version # 1.9+
grpcurl --version # brew install grpcurl
jq --version      # optional, nicer output
```

Ports used: `4566` (emulator) · `8080` (REST) · `9090` (Prometheus) · `16686` (Jaeger) · `50051-50053` (gRPC) · `9091-9094` (metrics)

---

## 2 · Fresh start — every command in order

### 2.1 Clean slate

```sh
cd ~/projects/grpc-ecs-demo

docker compose down -v
docker ps -aq --filter "name=ministack-ecs-" | xargs -r docker rm -f
docker ps -aq --filter "name=forward-"       | xargs -r docker rm -f
docker network rm ecom-dns ecom-infra 2>/dev/null
rm -rf data
```

### 2.2 Tests first — no Docker, no AWS, nothing running

```sh
go build ./...
go test ./...
```

Expect **8 packages ok**. This is the fastest possible signal that the business logic is intact.

### 2.2b Four loops — use the cheapest one that catches your bug

You do **not** need the emulator to develop, and most of the time you should
not run it.

| Loop | Command | Restart | Catches |
|---|---|---|---|
| **1. `go run`** | `make dev-userd` etc. | ~2s | business logic, validation, auth, the contract |
| **2. docker build** | `make images` | ~30s | CGO creeping in, file ownership, CA bundle, architecture |
| **3. emulator** | `make local-up && make tf-local-apply` | ~60s | task definitions, env wiring, IAM, secret resolution, Terraform |
| **4. real AWS** | `terraform -chdir=terraform/envs/aws apply` | ~2min | everything the emulator does not model (§8) |

### 2.3 Loop 1 — run the services with no emulator at all

Most development happens here. Four terminals.

```sh
make dev-seed        # creates ./data/user.db and ./data/product.db
```

```sh
make dev-userd       # terminal 1 — :50051
make dev-productsd   # terminal 2 — :50053
make dev-orderd      # terminal 3 — :50052, dials the other two
make dev-gatewayd    # terminal 4 — :8080 REST
```

```sh
make dev-smoke       # terminal 5 — gRPC flow
make dev-rest        #            — the same thing over curl
```

**Why start here:** no Docker, no AWS, restart in ~2 seconds. It exercises both
services and the real gRPC hops between them. When you are writing a validation
rule, you do not want a 60-second deploy in the loop.

Stop them with `Ctrl-C` in each terminal when you move on.

### 2.4 Loop 2 — build the images

```sh
make images
```

Four images, three Dockerfiles (one per storage shape), all ARM64 and distroless:

```
userd:0.1.0       38.6MB
productsd:0.1.0   38.5MB
orderd:0.1.0      38.6MB
gatewayd:0.1.0    29.2MB
```

### 2.5 Loop 3 — the emulator, and real ECS tasks

```sh
make local-up            # ministack :4566, jaeger :16686, prometheus :9090
make tf-local-apply      # terraform apply -> ECS tasks; also runs `make dns`
make ps                  # prove it is really ECS
make demo                # the business flow
make forward             # publish ports so Postman/curl can reach the tasks
make dev-rest            # REST through gatewayd
```

> **`make dns` must run after every deploy.** Terraform replaces the task
> containers, and the Cloud Map stand-in is a Docker network alias that lives on
> the container. `tf-local-apply` and `forward` both call it for you.


---

## 3 · Seeing each AWS component locally

The emulator speaks the real AWS APIs, so **the ordinary `aws` CLI works** — you
just point it at `localhost:4566`. Set this once per terminal:

```sh
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:4566
```

Now every command below is **exactly** what you would run against real AWS,
minus that last line.

### Is the emulator up, and what does it emulate?

```sh
curl -s localhost:4566/_ministack/health | jq '.services | keys | length'
curl -s localhost:4566/_ministack/health | jq '.services | {ecs, ecr, servicediscovery, secretsmanager, logs, iam}'
```

### ECS — cluster, services, tasks

```sh
aws ecs list-clusters
aws ecs describe-clusters --clusters ecom-local

aws ecs describe-services --cluster ecom-local \
  --services userd productsd orderd gatewayd \
  --query 'services[].{Service:serviceName,Desired:desiredCount,Running:runningCount}' \
  --output table

aws ecs list-tasks --cluster ecom-local
aws ecs describe-tasks --cluster ecom-local \
  --tasks $(aws ecs list-tasks --cluster ecom-local --query 'taskArns[0]' --output text) \
  --query 'tasks[0].{Status:lastStatus,Def:taskDefinitionArn,CPU:cpu,Mem:memory}'
```

### Task definition — the thing that makes it Fargate, not compose

```sh
aws ecs describe-task-definition --task-definition userd \
  --query 'taskDefinition.{Family:family,Rev:revision,Net:networkMode,Compat:requiresCompatibilities,Arch:runtimePlatform.cpuArchitecture,ExecRole:executionRoleArn}'

# the container definition: ports, env, secrets, health check, stopTimeout
aws ecs describe-task-definition --task-definition userd \
  --query 'taskDefinition.containerDefinitions[0]' | jq
```

### ECR — the registry

```sh
aws ecr describe-repositories --query 'repositories[].repositoryName'
aws ecr list-images --repository-name userd
```

### Cloud Map — service discovery

```sh
aws servicediscovery list-namespaces --query 'Namespaces[].{Name:Name,Type:Type}'
aws servicediscovery list-services   --query 'Services[].Name'
```

> **Local limitation, stated plainly:** Ministack stores Cloud Map registrations
> but serves no DNS, and Terraform cannot even create the services (it wants a
> top-level `NamespaceId`; the provider nests it in `DnsConfig`). So locally
> `enable_service_discovery = false` and `make dns` adds Docker network aliases
> instead. That works because **service discovery is only DNS underneath** — a
> good thing to say out loud rather than hide.

### Secrets Manager

```sh
aws secretsmanager list-secrets --query 'SecretList[].Name'

# the task definition stores an ARN, never a value:
aws ecs describe-task-definition --task-definition userd \
  --query 'taskDefinition.containerDefinitions[0].secrets'
```

### CloudWatch Logs

```sh
aws logs describe-log-groups --query 'logGroups[].logGroupName'
aws logs tail /ecs/ecom-local --since 5m
```

### IAM — the two roles, and why there are two

```sh
aws iam list-roles --query 'Roles[?starts_with(RoleName, `ecom-`)].RoleName'

# execution role: used by the ECS AGENT before your code runs - pull the image,
# resolve secrets, create log streams
aws iam list-attached-role-policies --role-name ecom-local-execution

# task role: the credentials YOUR process gets. Empty here, because the
# services call no AWS API at runtime.
aws iam list-role-policies --role-name ecom-local-task
```

### The containers behind the tasks — what ECS actually did

```sh
docker ps --filter "name=ministack-ecs-" \
  --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'

# the task self-describes, via a variable nothing in our code sets:
CID=$(docker ps --filter "name=ministack-ecs-.*-userd$" --format '{{.Names}}' | head -1)
docker inspect "$CID" --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | grep -E "ECS_CONTAINER_METADATA|AWS_CONTAINER_CREDENTIALS"

URI=$(docker inspect "$CID" --format '{{range .Config.Env}}{{println .}}{{end}}' \
  | grep ECS_CONTAINER_METADATA_URI_V4 | cut -d= -f2-)
docker run --rm --network ecom-infra curlimages/curl:latest -s "$URI/task" | jq

docker logs "$CID" --tail 20
```

`AWS_CONTAINER_CREDENTIALS_FULL_URI` is the whole "no API keys on ECS" story in
one `docker inspect`: the AWS SDK reads it and gets temporary credentials.

### Web UIs

| What | URL | Shows |
|---|---|---|
| **Jaeger** | http://localhost:16686 | one order = three spans: gatewayd → orderd → userd + productsd |
| **Prometheus** | http://localhost:9090 | per-task metrics. The load-balancing query is `sum by (task) (grpc_server_started_total{service="userd", grpc_method="VerifyToken"})` |
| Ministack health | http://localhost:4566/_ministack/health | which services it emulates |
| Service metrics | http://localhost:9091–9094/metrics | raw, after `make forward` |

Confirm Prometheus found the tasks before you rely on it on stage — four
targets, all `up`:

```sh
curl -s localhost:9090/api/v1/targets \
  | jq -r '.data.activeTargets[] | "\(.labels.service)  task=\(.labels.task)  \(.health)"'
```

Targets are discovered through `docker_sd_configs`, not hardcoded, because task
IPs change on every deploy — the same reason you need Cloud Map on AWS. See
[`../docker/prometheus.yml`](../docker/prometheus.yml); two things there are
faithful to Fargate and worth knowing: `awsvpc` task containers expose **no**
ports, so the metrics port has to be supplied per service, and Prometheus needs
`user: root` to read the Docker socket or discovery fails silently while the
container looks healthy.

Ministack has **no web console** — inspection is the `aws` CLI plus Docker.
That is a fair question to expect, and the honest answer is that the CLI being
identical is the more useful property anyway.

---

## 4 · Manual tests

### Everything at once

```sh
make test            # 8 Go packages
make api-coverage    # all 10 RPCs + all 9 REST routes + the negative check
```

### gRPC by hand

`awsvpc` tasks have no host port, so either run `grpcurl` inside the task
network, or `make forward` first and use `localhost`.

```sh
# inside the network:
G() { docker run --rm --network ecom-dns fullstorydev/grpcurl:latest -plaintext "$@"; }

# reflection: grpcurl discovers the API with no .proto file
G userd.ecom.local:50051 list
G productsd.ecom.local:50053 describe product.v1.ProductService

# browse — no auth needed, this is the read path that scales
G -d '{"query":"cancelling"}' productsd.ecom.local:50053 \
  product.v1.ProductService/SearchProducts
G -d '{"category":"footwear"}' productsd.ecom.local:50053 \
  product.v1.ProductService/ListProducts
G -d '{"id":"p-1005"}' productsd.ecom.local:50053 \
  product.v1.ProductService/GetProduct

# log in as a SEEDED user
TOKEN=$(G -d '{"email":"demo@example.com","password":"demo-password"}' \
  userd.ecom.local:50051 user.v1.UserService/Login | jq -r .token)

# place an order — note there is no price field in the request
G -H "authorization: Bearer $TOKEN" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"m-1"}' \
  orderd.ecom.local:50052 order.v1.OrderService/CreateOrder

# the same key again -> the same order, idempotentReplay: true
G -H "authorization: Bearer $TOKEN" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"m-1"}' \
  orderd.ecom.local:50052 order.v1.OrderService/CreateOrder

# health
G -d '{"service":"userd"}' userd.ecom.local:50051 grpc.health.v1.Health/Check
```

### Rejections — the seed data has these on purpose

| Product | Stock | Try | Expect |
|---|---|---|---|
| `p-1003` JBL speaker | 0 | quantity 1 | `OUT_OF_STOCK` |
| `p-1005` Ultraboost | 1 | quantity 3 | `INSUFFICIENT_STOCK` |
| `p-9999` | — | quantity 1 | `PRODUCT_NOT_FOUND` |

```sh
for id in p-1003 p-1005 p-9999; do
  G -H "authorization: Bearer $TOKEN" \
    -d "{\"items\":[{\"product_id\":\"$id\",\"quantity\":3}],\"idempotency_key\":\"r-$id\"}" \
    orderd.ecom.local:50052 order.v1.OrderService/CreateOrder \
    | jq -r '.order | "\(.status) \(.rejectionReason // "")"'
done
```

### REST by hand

```sh
make forward                     # publishes 8080 and 50051-50053
BASE=http://localhost:8080

curl -s "$BASE/healthz"
curl -s "$BASE/v1/products:search?query=cancelling" | jq '.products[].title'
curl -s "$BASE/v1/products/p-1005" | jq '.product | {title, stock}'

TOKEN=$(curl -s -X POST "$BASE/v1/sessions" -H 'Content-Type: application/json' \
  -d '{"email":"demo@example.com","password":"demo-password"}' | jq -r .token)

curl -s "$BASE/v1/users/me" -H "Authorization: Bearer $TOKEN" | jq
curl -s -X POST "$BASE/v1/orders" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"items":[{"productId":"p-1001","quantity":1}],"idempotencyKey":"rest-1"}' | jq '.order.status'

# the internal-only RPC is NOT reachable over REST
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$BASE/v1/products:checkAvailability" -d '{}'
# -> 404
```

### Postman

`make forward`, then: **New → gRPC → `localhost:50053`**, tick **"Using server
reflection"** (no `.proto` import needed), pick a method. For `orderd`, add
metadata `authorization` = `Bearer <token>`.

For REST, import `gen/openapi/api.swagger.json` and you get all nine routes.

### The TypeScript client

```sh
make ts-demo
```

Same flow from generated TypeScript — proves one `.proto` serves both languages.

---

## 5 · The demo, in stage order

### Beat 1 — "how is this different from docker compose?" (2 min)

```sh
make ps
```

Seven proofs. Linger on two:
- the task **self-describes** through `ECS_CONTAINER_METADATA_URI_V4` — nothing
  in your code sets it
- `AWS_CONTAINER_CREDENTIALS_FULL_URI` — **this is how task roles work.** No key
  anywhere.

Then admit the gaps yourself: `healthStatus` is `UNKNOWN` and `LaunchType` is
not echoed back, even though the task definition requests `FARGATE`.

### Beat 2 — browse, then buy (3 min)

```sh
make demo
make dev-rest
```

Say while it runs: the order request **has no price field**; `orderd` asks
`productsd`. Then show one call in Postman so people see a human-facing client.

### Beat 3 — one proto, two protocols (1 min)

```sh
curl -s -o /dev/null -w 'REST  -> %{http_code}\n' \
  -X POST http://localhost:8080/v1/products:checkAvailability -d '{}'

docker run --rm --network ecom-dns fullstorydev/grpcurl:latest -plaintext \
  -d '{"items":[{"product_id":"p-1005","quantity":5}]}' \
  productsd.ecom.local:50053 product.v1.ProductService/CheckAvailability \
  | jq -r '.results[0].availability'
```

**404 over REST, works over gRPC.** Four missing lines of annotation.

### Beat 4 — the tracing payoff (1 min)

Open http://localhost:16686, pick `orderd`, find a `CreateOrder` trace.
**Three spans**: orderd → userd, and orderd → productsd.

### Beat 5 — the main event: scale out, break it, fix it (5 min)

Open Prometheus at http://localhost:9090 first, with this query:

```promql
sum by (task) (grpc_server_started_total{service="userd"})
```

**Step 1 — show the bug.** Redeploy `orderd` with grpc-go's *default* client
behaviour. One variable, and it turns off both halves of the fix at once:

```sh
terraform -chdir=terraform/envs/local apply -auto-approve -var lb_policy=pick_first
make dns
make scale N=3        # three userd tasks, all healthy
make forward
make demo-load        # 300 authenticated calls through orderd
```

Three tasks healthy. **One** line moving in Prometheus. Measured on this repo:

```
task=10e77597   120 VerifyToken calls     <- all of them
task=90c7be3b     0
task=02e3010a     0
```

Say the diagnosis out loud: DNS is fine — `make dns` aliased all three, and on
AWS `aws servicediscovery get-instances-health-status` shows three healthy
instances — the client simply never asked to balance.

**Step 2 — apply the fix.** Drop the variable and redeploy:

```sh
terraform -chdir=terraform/envs/local apply -auto-approve
make dns && make forward
make demo-load
```

Three lines now climb together — 39 / 40 / 41 out of 120 on the run above. The
diff is two settings:
`WithDefaultServiceConfig(round_robin)` on the client, and the `dns:///` prefix
so the resolver yields every A record instead of one. Half the fix looks like
it works and does not — which is why one variable controls both.

**Step 3 — kill a task mid-load.** Zero failed RPCs; graceful shutdown and
`stopTimeout` are doing their job.

```sh
make demo-load &
docker rm -f $(docker ps --filter "name=ministack-ecs-.*-userd$" -q | head -1)
```

**Step 4 — kill `orderd` instead.** The orders are gone. State on the task
filesystem, exactly as promised, and the reason `orderd` stays at one task.

> **Use a seeded user.** A user from `Register` exists on exactly one `userd`
> task; at three tasks two have never heard of them, and it fails in a way that
> looks exactly like the bug you just fixed.

### Beat 6 — the money slide (1 min)

```sh
diff terraform/envs/local/provider.tf terraform/envs/aws/provider.tf
make show-guard     # terraform refuses to scale the writer
```

### Beat 7 — real AWS

Pre-provision the day before. See [`DEPLOY_AWS.md`](DEPLOY_AWS.md). Live-demo a
**delta** — a scale-out — never a cold apply on venue wifi.

---

## 6 · Teardown

```sh
make forward-stop
make tf-local-destroy
docker compose down -v
docker ps -aq --filter "name=ministack-ecs-" | xargs -r docker rm -f
docker network rm ecom-dns ecom-infra 2>/dev/null
rm -rf data
```

And on AWS, **before you leave the venue**:

```sh
cd terraform/envs/aws
for s in userd productsd orderd gatewayd; do
  aws ecs update-service --cluster ecom-aws --service $s --desired-count 0
done
sleep 75                       # ECS must deregister from Cloud Map first
terraform destroy
```

---

## 7 · When something breaks

| Symptom | Cause | Fix |
|---|---|---|
| `connection refused` on `localhost:50051` | `awsvpc` tasks have no host port — faithful to Fargate | `make forward` |
| gRPC `code 14 "no children to pick from"` | the DNS name resolves to nothing | `make dns`. On AWS, check the Cloud Map instance is **HEALTHY** |
| A name resolves to the wrong service | aliases live on the container, and Terraform replaced it | `make dns` again |
| `CreateDBInstance ... port is already allocated` | the RDS port range was published on the ministack container | already fixed in `compose.yaml` — don't re-add it |
| Tasks crash-loop right after deploy | check the logs; an OTel schema mismatch does this and is invisible without an OTLP endpoint | `docker logs <task container>` |
| Intermittent auth failures at >1 task | a `Register`ed user exists on one task only | use a seeded user |
| First request after a deploy fails | `grpc.NewClient` is lazy | already fixed by `grpcclient.Warm` |
| AWS: `account is currently blocked` | Fargate **vCPU quota is 0** in that region | check `aws service-quotas`, request an increase, or pick a region that has quota |
| AWS: `Unable to assume the service linked role` | fresh account, IAM still propagating | retry the apply |
| Prometheus shows 0 targets | it runs as `nobody` and cannot read the Docker socket | already fixed with `user: root` in `compose.yaml`; check `docker logs prometheus` for `permission denied` |
| Prometheus graph double-counts a task | docker SD emits one entry per attached network, and tasks are on two | already fixed: targets are keyed on the container name, not the task IP |
| `make demo-load` logs in and then nothing moves | the socat relays were just recreated by a deploy | the script now retries for 20s; otherwise re-run `make forward` |

---

## 8 · What is and is not testable locally

Stated up front rather than discovered on stage.

| Capability | Local | Notes |
|---|---|---|
| `terraform apply` of the whole stack | ✅ | the same modules as AWS; only the provider block differs |
| ECS tasks really running as containers | ✅ | Fargate + `awsvpc` + ARM64 task definition |
| ECR push/pull | ✅ | `localhost:4566/<repo>` |
| Secrets Manager injection (`secrets[].valueFrom`) | ✅ | `userd` refuses to boot without `JWT_SECRET`, so booting proves it |
| SQLite on the task filesystem | ✅ | baked for `userd`/`productsd`, empty and writable for `orderd` |
| gRPC health, graceful shutdown, metrics | ✅ | |
| OTel traces to Jaeger | ✅ | tasks share the compose network |
| Prometheus scraping per task | ✅ | `docker_sd_configs`, see [`../docker/prometheus.yml`](../docker/prometheus.yml) |
| Postman / grpcurl from the host | ✅ | needs `make forward` |
| **Cloud Map service discovery** | ⚠️ | **Terraform cannot create it**: Ministack wants a top-level `NamespaceId`, the provider nests it in `DnsConfig`. `make dns` stands in with Docker network aliases, which works because service discovery is only DNS |
| **SSM port forwarding / ECS Exec** | ❌ | `ssmmessages` is not emulated. The Terraform applies, but no session opens. `make forward` is the local equivalent |
| ALB / gRPC target group | ❌ | needs an HTTPS listener plus ACM; above the cut line for this talk |
| `healthStatus`, `launchType` in the API | ⚠️ | the emulator returns `UNKNOWN` / empty even though the task definition requests `FARGATE` |

---

### Seeded demo data

| | |
|---|---|
| Users | `demo@example.com`, `asha@example.com`, `ravi@example.com` — all `demo-password` |
| Products | `p-1001` … `p-1012`. `p-1003` is out of stock, `p-1005` has exactly 1 left |
