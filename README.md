# grpc-ecs-payments

Two Go gRPC microservices taken from `localhost:50051` to AWS Fargate with **one
set of Terraform modules** that applies to a local emulator and to real AWS.

Demo repo for the talk *"Running Go gRPC Services on ECS: From LocalStack to
Production Cloud"* — AWS Community Day Vadodara, October 2026.

```
 demo-client / Postman ──unary──> paymentd ──────> SQLite (task-local)
                                     │
                                     ├──gRPC────> identityd ──> SQLite (baked into the image)
                                     │            VerifyToken
                                     └──────────> explain: template (Bedrock opt-in)

        both ──OTLP──> Jaeger    both ──:909x/metrics──> Prometheus
```

| Service | Role | Tasks |
|---|---|---|
| `identityd` | users, passwords, JWTs. Stateless — its database is baked into the image | **3** (the one you scale) |
| `paymentd` | issuer-side authorization: rules decide approve/decline | **1** (holds writable state) |

Every RPC is unary. [Measured across 14 real projects](docs/evidence), streaming
is the minority almost everywhere — and every ECS lesson here survives without
it, because the load-balancing bug is connection-level, not stream-level.

## Quick start

**You do not need the emulator to develop.** Fastest loop first — two terminals,
no Docker, no AWS:

```sh
make dev-seed        # once
make dev-identityd   # terminal 1 — :50051
make dev-paymentd    # terminal 2 — :50052
make dev-smoke       # terminal 3
```

That exercises both services and the real gRPC hop between them. Restart is
about two seconds. Reach for the emulator when you need ECS itself:

```sh
make test          # 7 packages, fully offline
make local-up      # the AWS emulator + jaeger

docker build --platform linux/arm64 -f build/Dockerfile.identityd \
  -t identityd:0.1.0 -t localhost:4566/identityd:0.1.0 .
docker build --platform linux/arm64 -f build/Dockerfile.paymentd \
  -t paymentd:0.1.0 -t localhost:4566/paymentd:0.1.0 .

make tf-local-apply   # terraform apply -> real ECS tasks, then re-aliases DNS
make ps               # prove it is ECS, not docker compose
make demo             # approved + declined + explained, end to end
make forward          # publish ports so Postman/grpcurl can reach the tasks
```

Full walkthrough, including what is **not** testable locally:
[`docs/RUNBOOK.md`](docs/RUNBOOK.md).

## What this demonstrates

| Topic | Where |
|---|---|
| Why gRPC cannot run on Lambda | ALB gRPC target groups accept only `instance` and `ip` targets |
| **The headline bug** — 3 tasks, 100% of traffic on one | [`internal/platform/grpcclient`](internal/platform/grpcclient) |
| Graceful shutdown wired to ECS `stopTimeout` | [`internal/platform/grpcserver`](internal/platform/grpcserver) |
| `grpc.health.v1` at three layers | same, plus [`cmd/healthcheck`](cmd/healthcheck) |
| One `.proto` → Go **and** TypeScript | [`buf.gen.yaml`](buf.gen.yaml), [`clients/node`](clients/node) |
| One module set, two targets | [`terraform/stack`](terraform/stack) vs. the two `terraform/envs/*/provider.tf` |
| Secrets Manager injection, no keys in the image | [`terraform/modules/secrets`](terraform/modules/secrets) |

### The load-balancing bug, in one place

grpc-go defaults to `pick_first`: it resolves the target, connects to **one**
address, and multiplexes every RPC over that one HTTP/2 connection. Scale to
three tasks and all traffic still lands on one. Both halves of the fix are
required:

```go
// client: balance per RPC, and resolve every A record (a bare host:port
// uses the passthrough resolver and yields ONE address)
grpc.NewClient("dns:///identityd.ecom.local:50051",
    grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`))

// server: recycle connections so clients re-resolve after a scale-out
grpc.KeepaliveParams(keepalive.ServerParameters{
    MaxConnectionAge:      30 * time.Second,
    MaxConnectionAgeGrace: 5 * time.Second,
})
```

## Deliberate decisions

Each of these is a trade-off, not an accident. `SPEC.md` has the reasoning.

- **SQLite for the demo only — RDS is what you actually want.** RDS costs 5–10
  minutes of every apply and ~$12–15/month if you forget to destroy it, so
  dropping it takes the AWS apply from ~10 minutes to ~2. That is the only
  reason it is not here; for anything handling real payments, use RDS. The
  module is written and kept in `terraform/modules/rds`, just not applied by
  default — `DB_DRIVER=postgres` is the switch. Two things worth keeping even
  if you never ship SQLite: it needs the **pure-Go** driver
  (`glebarez/sqlite`), because `gorm.io/driver/sqlite` requires CGO and breaks
  the static distroless build; and **EFS is not a workaround** — SQLite's own
  docs warn that network filesystems lead to database corruption.
- **A template, not an LLM, writes decline explanations.** Deterministic rules
  decide; the explainer only phrases it. For a regulated decline that is the
  better engineering choice — auditable, instant, and incapable of inventing a
  reason. The Bedrock client is there, opt-in, and its endpoint comes from the
  SDK's own `AWS_ENDPOINT_URL_BEDROCK_RUNTIME`, so **there is no `if local`
  branch anywhere in the codebase**.
- **No NAT Gateway.** ~$32/month plus data is the fastest way to bleed a demo
  account. Public subnets with `assign_public_ip`; VPC endpoints in production.
- **`int64` minor units for money**, never a float.

## Docs

| | |
|---|---|
| [`SPEC.md`](SPEC.md) | the full specification, with every claim marked verified or not |
| [`docs/RUNBOOK.md`](docs/RUNBOOK.md) | step-by-step local demo, and the local/AWS capability matrix |
| [`docs/AWS_PERMISSIONS.md`](docs/AWS_PERMISSIONS.md) | exactly what IAM you need, and what to skip |
| [`docs/DEMO_ACCESS.md`](docs/DEMO_ACCESS.md) | reaching the services from Postman: public IP vs. SSM port forwarding vs. ALB |

## Requirements

Go 1.27 · Docker · Terraform 1.9+ · `grpcurl` or Postman · Node 22 (TypeScript
client only) · `buf` (only to regenerate — generated code is committed)

Built for **arm64/Graviton**: cheaper on Fargate, native on Apple silicon.

## Local emulator

Uses [Ministack](https://github.com/ministackorg/ministack) (MIT) rather than
LocalStack. LocalStack retired its free Community edition in March 2026, and
ECS, ECR, ELB and Cloud Map were **never** in the free image — verified by
listing `localstack/services/` at tags v1.4.0 through v4.0.0. Ministack runs ECS
tasks as real Docker containers, and emulates ECR, Cloud Map, Secrets Manager,
SSM and Bedrock.

Its limits are documented rather than hidden — see the matrix in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md).

## License

MIT
