# Runbook — local demo, start to finish

Everything here was executed on 2026-10-09 on macOS/arm64. Commands are copied
from a run that worked, not reconstructed.

## What IS and IS NOT testable locally

| Capability | Local | Notes |
|---|---|---|
| `terraform apply` of the whole stack | ✅ | same modules as AWS, different provider block |
| ECS tasks really running as containers | ✅ | Fargate + awsvpc + ARM64 task definition |
| ECR push/pull | ✅ | `localhost:4566/<repo>` |
| Secrets Manager injection (`secrets[].valueFrom`) | ✅ | identityd refuses to boot without it, so booting proves it |
| SQLite on the task filesystem | ✅ | baked for identityd, empty for paymentd |
| gRPC health, graceful shutdown, metrics | ✅ | |
| OTel traces to Jaeger | ✅ | tasks share the compose network |
| Postman / grpcurl from the host | ✅ | needs `make forward` — see below |
| Bedrock `Converse` | ✅ | canned text, or real prose via `make llm-up` |
| **Cloud Map service discovery** | ⚠️ | **Terraform CANNOT create it**: Ministack wants a top-level `NamespaceId`, the provider nests it in `DnsConfig`. `make dns` stands in with Docker aliases. |
| **SSM port forwarding / ECS Exec session** | ❌ | `ssmmessages` is not emulated. The Terraform applies, but no session can be opened. `make forward` is the local equivalent. |
| ALB / gRPC target group | ❌ | needs an HTTPS listener + ACM; above the cut line |
| `healthStatus`, `LaunchType` in the API | ⚠️ | emulator returns UNKNOWN / None even though the task def requests FARGATE |

## Four loops — use the cheapest one that catches your bug

You do **not** need the emulator to develop. Most of the time you should not run it.

| Loop | Command | Restart | Catches |
|---|---|---|---|
| **1. `go run`** | `make dev-userd` + `make dev-orderd` | ~2s | business logic, rules, validation, auth, the contract |
| **2. docker build** | `docker build -f build/Dockerfile.*` | ~30s | CGO creeping in, file ownership, CA bundle, architecture |
| **3. emulator** | `make local-up && make tf-local-apply` | ~60s | task definitions, env wiring, IAM, secret resolution, Terraform |
| **4. real AWS** | `terraform -chdir=terraform/envs/aws apply` | ~2min | everything the emulator does not model |

### Loop 1 in full — no Docker, no emulator

Three terminals. This is where most of the work happens.

```sh
make dev-seed        # once: creates ./data/user.db with the demo users

# terminal 1
make dev-userd   # :50051, sqlite at ./data/user.db

# terminal 2
make dev-orderd    # :50052, talks to localhost:50051

# terminal 3
make dev-smoke       # or grpcurl / Postman against localhost directly
```

Verified 2026-10-09:

```
login ok
authorize -> DECISION_APPROVED
explain   -> Declined: ₹45000.00 exceeds your per-transaction limit of ₹25000.00.
```

Why this works without any AWS at all: the only AWS-shaped dependencies are the
database (SQLite, a local file) and the explanation provider (the `template`
default, which needs no network). `make dev-clean` removes `./data`.

**What loop 1 does NOT exercise:** task definitions, Cloud Map, Secrets Manager
injection, the distroless image, `awsvpc` networking, graceful shutdown under a
real SIGTERM from ECS. That is what loop 3 is for.

## Prerequisites

```sh
go version        # 1.27+
docker --version  # 25+, and running
terraform version # 1.9+
buf --version     # only to regenerate protos; generated code is committed
grpcurl --version # or use Postman
node --version    # 22+, only for the TypeScript client segment
```

## First run

```sh
cd ~/projects/grpc-ecs-demo

# 1. tests first - 7 packages, all offline
make test

# 2. the emulator + jaeger  (ministack :4566, jaeger UI :16686)
make local-up

# 3. build the images. identityd bakes its seeded SQLite database in a
#    separate build stage, so every task ships an identical file.
docker build --platform linux/arm64 -f build/Dockerfile.identityd \
  -t identityd:0.1.0 -t localhost:4566/identityd:0.1.0 .
docker build --platform linux/arm64 -f build/Dockerfile.paymentd \
  -t paymentd:0.1.0 -t localhost:4566/paymentd:0.1.0 .

# 4. deploy. This also re-runs `make dns` for you, because terraform
#    replaces the task containers and a replaced container has no alias.
make tf-local-apply

# 5. prove it is ECS, not docker compose
make ps

# 6. the business flow, end to end through the tasks
make demo

# 7. Postman / grpcurl from the host
make forward
```

Optional:

```sh
make llm-up     # ollama, so explanations are real prose instead of canned text
make ts-demo    # the same flow from TypeScript, generated from the same proto
```

## Ordering rules that bite

1. **`make dns` after every deploy.** Terraform replaces task containers, and
   aliases live on the container. `tf-local-apply` and `forward` both do it for
   you; if you run `terraform apply` by hand, do it yourself.
2. **`make forward` before Postman.** `awsvpc` tasks have no host port — that is
   faithful to Fargate, not a bug. Without the relay there is no route from the
   Mac to the task.
3. **Rebuild images before deploying code changes.** Ministack pulls from the
   local Docker daemon; a stale tag silently redeploys the old binary.

## Postman setup

```
make forward     # publishes 127.0.0.1:50051 and 127.0.0.1:50052
```

1. New → gRPC request → `localhost:50052`
2. **Using server reflection** — both services register it, so no `.proto` import
3. Method: `payment.v1.PaymentService/Authorize`
4. Metadata: `authorization` = `Bearer <token from Login>`
5. Message:

```json
{
  "amount_minor": 120000,
  "currency": "INR",
  "merchant_id": "M-GROCER",
  "merchant_category": "5411",
  "idempotency_key": "postman-1"
}
```

Get the token first from `localhost:50051` →
`identity.v1.IdentityService/Login` with
`{"email":"demo@example.com","password":"demo-password"}`.

**Use a seeded user, not a freshly registered one.** A registered user exists on
exactly one `identityd` task; once you scale to three, two of them will not know
that user and it will look like a load-balancing bug.

Four things worth showing in Postman:

| Call | Shows |
|---|---|
| `amount_minor: 120000`, mcc `5411` | APPROVED |
| same `idempotency_key` twice | identical transaction, `idempotentReplay: true` |
| `amount_minor: 4500000` | DECLINED / `LIMIT_EXCEEDED` — and it returns **OK**, because a decline is a business outcome, not a transport error |
| `ExplainDecision` with that id | the sentence, plus which provider produced it |
| no `authorization` metadata | `Unauthenticated` — rejected via the hop to identityd |

## Teardown

```sh
make forward-stop
make tf-local-destroy
make local-down
```

## AWS

See `docs/AWS_PERMISSIONS.md` (what to request, and what to skip) and
`docs/DEMO_ACCESS.md` (public IP vs. SSM port forwarding vs. ALB).

```sh
# minimum-permission profile: no Secrets Manager, reuse an existing role
terraform -chdir=terraform/envs/aws apply -var-file=minimal.tfvars \
  -var identity_image=<acct>.dkr.ecr.ap-south-1.amazonaws.com/identityd:0.1.0 \
  -var payment_image=<acct>.dkr.ecr.ap-south-1.amazonaws.com/paymentd:0.1.0

make ps-aws
terraform -chdir=terraform/envs/aws destroy    # before you leave the venue
```

**Pre-provision AWS before the talk.** Live-demo a *delta* — a scale-out, a task
kill — never a cold apply on venue wifi.
