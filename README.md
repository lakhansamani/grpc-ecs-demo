# grpc-ecs-demo

Four Go services taken from `localhost:50051` to AWS Fargate with **one set of
Terraform modules** that applies to a local emulator and to real AWS.

Demo repo for the talk *"Running Go gRPC Services on ECS: From LocalStack to
Production Cloud"* — AWS Community Day Vadodara, October 2026.

```
  browser / Postman ──REST──> gatewayd ──gRPC──┬──> userd ──────> SQLite (baked into the image)
                              (generated,      │
                               not written)    ├──> productsd ──> SQLite + FTS5 (baked in)
                                               │
                                               └──> orderd ─────> SQLite (task-local, writable)
                                                      │
       grpcurl / Postman ──gRPC──────────────────────>┤
                                                      ├──gRPC──> userd.VerifyToken
                                                      └──gRPC──> productsd.CheckAvailability

        all ──OTLP──> Jaeger        all ──:909x/metrics──> Prometheus
```

| Service | Role | Tasks | Why that count |
|---|---|---|---|
| `userd` | accounts, passwords, JWTs | **3** | stateless, database baked into the image — **this is the one you scale** |
| `productsd` | search, list, get, internal availability check | **3** | same shape: read-only, identical on every task |
| `orderd` | places orders; calls the other two | **1** | it *writes*, so N tasks would mean N divergent databases |
| `gatewayd` | REST → gRPC, generated from the protos | 1–N | stores nothing |

**The point of three services:** during a sale, browsing goes up far more than
buying. `productsd` and `userd` can scale with the reads; `orderd` must not.
Scale the reads, not the writes.

Every RPC is unary. [Measured across 14 real projects](docs/evidence), streaming
is the minority almost everywhere — and every ECS lesson here survives without
it, because the load-balancing bug is connection-level, not stream-level.

## Quick start

**You do not need the emulator to develop.** Fastest loop first — no Docker, no
AWS, restart in about two seconds:

```sh
make dev-seed        # once: ./data/user.db and ./data/product.db
make dev-userd       # terminal 1 — :50051
make dev-productsd   # terminal 2 — :50053
make dev-orderd      # terminal 3 — :50052, dials the other two
make dev-gatewayd    # terminal 4 — :8080 REST
make dev-smoke       # terminal 5 — the gRPC flow
make dev-rest        #            — the same flow over curl
```

Reach for the emulator when you need ECS itself:

```sh
make test             # 8 packages, fully offline
make local-up         # emulator :4566, jaeger :16686, prometheus :9090
make images           # four ARM64 distroless images
make tf-local-apply   # terraform apply -> real ECS tasks, then re-aliases DNS
make ps               # prove it is ECS, not docker compose
make demo             # browse, log in, order, get rejected — end to end
make forward          # publish ports so Postman/grpcurl can reach the tasks
make api-coverage     # every RPC and every REST route, and what was missed
```

Full walkthrough — local inspection of every AWS component, manual tests and
the stage-by-stage demo: [`docs/DEMO_GUIDE.md`](docs/DEMO_GUIDE.md).
Architecture and what each AWS component is for:
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

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
grpc.NewClient("dns:///userd.ecom.local:50051",
    grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`))

// server: recycle connections so clients re-resolve after a scale-out
grpc.KeepaliveParams(keepalive.ServerParameters{
    MaxConnectionAge:      30 * time.Second,
    MaxConnectionAgeGrace: 5 * time.Second,
})
```

Both halves ship in [`internal/platform/grpcclient`](internal/platform/grpcclient)
and [`internal/platform/grpcserver`](internal/platform/grpcserver). To show the
bug live rather than describe it, redeploy `orderd` with the client reverted to
grpc-go's defaults:

```sh
terraform -chdir=terraform/envs/local apply -auto-approve -var lb_policy=pick_first
make dns && make scale N=3 && make forward && make demo-load   # one task takes everything
terraform -chdir=terraform/envs/local apply -auto-approve      # back to the fix
```

## What each service stores

Three storage shapes, one Dockerfile each — because the shape is the
interesting part, not the service name.

| Service | Storage | Dockerfile | Tasks | Why |
|---|---|---|---|---|
| `userd` | SQLite, **baked into the image** at build time | `Dockerfile.seeded` | 3 | every task ships an identical file, so all reads agree and it scales |
| `productsd` | SQLite + **FTS5 search index**, baked in | `Dockerfile.seeded` | 3 | same, and the index is built by the seeder so nothing is indexed at boot |
| `orderd` | SQLite, **empty and writable**, on the task filesystem | `Dockerfile.stateful` | **1** | it writes, so N tasks would mean N divergent databases |
| `gatewayd` | **nothing at all** | `Dockerfile.stateless` | 3 | no database, no `/data`, no `DB_DRIVER` — pure translation |

So `orderd` very much does use SQLite; it is the only service that *writes* to
one. The difference between it and the other two is **baked vs. empty**, not
present vs. absent. And `gatewayd` is the only one with no database — which is
also why it is the easiest to scale and the right thing to put an ALB in front of.

## Deliberate decisions

Each of these is a trade-off, not an accident. `SPEC.md` has the reasoning.

- **SQLite for the demo only — RDS is what you actually want.** RDS costs 5–10
  minutes of every apply, and it bills by the hour if you forget to destroy it, so
  dropping it takes the AWS apply from ~10 minutes to ~2. That is the only
  reason it is not here; for anything handling real payments, use RDS. The
  application side is already driver-agnostic — `DB_DRIVER=postgres` plus a DSN
  in `DB_URL` is the whole switch (see
  [`internal/platform/store`](internal/platform/store)); there is no RDS
  Terraform module in this repo, because adding one is the easy half. Two
  things worth keeping even
  if you never ship SQLite: it needs the **pure-Go** driver
  (`glebarez/sqlite`), because `gorm.io/driver/sqlite` requires CGO and breaks
  the static distroless build; and **EFS is not a workaround** — SQLite's own
  docs warn that network filesystems lead to database corruption.
- **No NAT Gateway.** At about **$33/month** before traffic ($0.045 per hour in AWS's own pricing example, plus $0.045 per GB processed), it is the fastest way to bleed a demo
  account. Public subnets with `assign_public_ip`; VPC endpoints in production.
- **`int64` minor units for money**, never a float.

## Docs

| | |
|---|---|
| [`PRESENTATION.md`](PRESENTATION.md) | the talk itself — 21 slides with speaker notes. Renders with Marp, reads fine on GitHub |
| [`SPEC.md`](SPEC.md) | the full specification, with every claim marked verified or not |
| [`docs/DEMO_GUIDE.md`](docs/DEMO_GUIDE.md) | **start here.** Fresh-laptop command sequence, inspecting every AWS component locally, manual tests, and the demo beat by beat |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | the diagram, and what ECS / Fargate / task definition / Route 53 each are, why they are here and how they are used |
| [`docs/DEPLOY_AWS.md`](docs/DEPLOY_AWS.md) | step by step onto a real AWS account, and how to tear it down |
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
tasks as real Docker containers, and emulates ECR, Cloud Map, Secrets Manager
and SSM.

Its limits are documented rather than hidden — see
[`docs/DEMO_GUIDE.md`](docs/DEMO_GUIDE.md) §3, which also shows how to inspect
each emulated service with the ordinary `aws` CLI.

## License

MIT
