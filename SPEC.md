# grpc-ecs-demo — Specification

**Purpose:** demo repo for the talk *"Running Go gRPC Services on ECS: From LocalStack to Production Cloud"*
**Speaker:** Lakhan Samani · AWS Community Day Vadodara · October 2026
**Status:** **built and verified.** Deployed to a real AWS account on 2026-10-09 and torn down; the
local path was re-run cold on 2026-10-09. Evidence in §13.

Claims marked ✅ were verified by running them, not recalled.

> **Scope of this document.** This is the specification for what the repo *is*. For how to run it
> see [`docs/DEMO_GUIDE.md`](docs/DEMO_GUIDE.md); for what each AWS component does see
> [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). [`docs/PLAN.md`](docs/PLAN.md) is the design-phase
> research trail and is **superseded by this file wherever they disagree** — it still describes a
> two-service payments example with RDS, which is not what was built.

---

## 1. Goal

Teach a room of AWS practitioners how to take a Go gRPC microservice from `localhost:50051` to
Fargate, using **one set of Terraform modules** that applies to a local emulator and to real AWS.
The e-commerce domain is a vehicle; the lessons are gRPC-on-ECS lessons.

Three things the audience should leave able to do:

1. Explain why a gRPC service **cannot** run on Lambda, and what that implies.
2. Diagnose the single most common gRPC-on-ECS bug: all traffic pinned to one task.
3. Run the same Terraform locally and against AWS, differing only in a provider block.

## 2. Non-goals

No Kubernetes. No payment processing. No real money and no PII. No streaming in the core build
(§4.4). No ALB (§11). No vector DB, no RAG, **no LLM** — an order rejection carries a reason enum,
so there is nothing for a model to phrase.

---

## 3. Architecture

Four services, **all RPCs unary**, two real network hops on the write path.

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

        all ──OTLP──> Jaeger (local) / ADOT→X-Ray (AWS)
        all ──:909x/metrics──> Prometheus
```

**Why three services and not two.** During a sale, browsing rises far more than buying, and the two
workloads want opposite things: the read path is huge, spiky and safe to copy; the write path is
small and must not be copied. One application cannot answer that, because scaling for the browsing
also scales the part that writes orders. **Scale the reads, not the writes** — that sentence is why
the service boundary falls where it does, and every later decision follows from it.

### 3.1 `userd` — stateless, the one that scales

Accounts and tokens. Called by `gatewayd` **and** by `orderd`, which makes it the busiest hop.

| RPC | REST | Notes |
|---|---|---|
| `Register` | `POST /v1/users` | writes — see §5.3 for the honest caveat |
| `Login` | `POST /v1/sessions` | read; verifies a bcrypt hash, issues an HS256 JWT |
| `VerifyToken` | `GET /v1/users/me` | read; token travels in metadata, not the body |

### 3.2 `productsd` — stateless, read-only, FTS5 search

| RPC | REST | Notes |
|---|---|---|
| `ListProducts` | `GET /v1/products` | paginated, optional category filter |
| `GetProduct` | `GET /v1/products/{id}` | |
| `SearchProducts` | `GET /v1/products:search` | SQLite **FTS5** full-text index, built by the seeder |
| `CheckAvailability` | **none, deliberately** | internal: prices and stock for a cart |

### 3.3 `orderd` — **1 task**, holds writable state

| RPC | REST | Notes |
|---|---|---|
| `CreateOrder` | `POST /v1/orders` | two outbound hops, then persists |
| `GetOrder` | `GET /v1/orders/{id}` | ownership-checked read |
| `ListOrders` | `GET /v1/orders` | paginated; authenticates, so it is what the load demo drives |

### 3.4 `gatewayd` — REST in, gRPC out, stores nothing

`grpc-gateway` generated from the same protos. Not hand-written, and that is the point: the REST
surface is a **side effect of the contract**, so it cannot drift from it.

### 3.5 Two contract decisions worth a slide each

**`CreateOrderRequest` carries no price.** A client sends product ids and quantities; `orderd` asks
`productsd` what things cost and computes the total from the catalogue. A client must never be able
to ask "is this price real?" and then submit a different number.

```protobuf
message RequestedItem { string product_id = 1; int32 quantity = 2; }
```

**`CheckAvailability` has no `google.api.http` option**, so it is reachable over gRPC and returns
**404 over REST** ✅. Four lines of annotation are the whole difference between an internal and a
public API — a far better demonstration of the gateway than exposing everything.

A rejection is **not an error**: `CreateOrder` returns `OK` with
`ORDER_STATUS_REJECTED` plus a `RejectionReason` enum. Reserving gRPC error codes for
*transport and auth* failures keeps business outcomes out of the status code, where clients would
have to parse strings.

---

## 4. Decisions already settled

### 4.1 Local emulator: Ministack, not LocalStack ✅

LocalStack retired its free Community edition on 2026-03-23. ECS/ECR/ELB/Cloud Map were **never**
in the free image (verified by listing `localstack/services/` at tags v1.4.0, v2.3.2, v3.8.1,
v4.0.0 — no `ecs`, no `elbv2`, no `servicediscovery` at any of them). A Hobby account does not help:
ECS, ECR, ALB and RDS are all excluded, and even paid Base lacks Cloud Map.

**Ministack** (`ministackorg/ministack`, MIT) emulates ECS with real Docker containers, plus ECR,
ALB, Cloud Map, Secrets Manager and SSM — free. §13 has the verification runs.

### 4.2 One Go module, one new repo

The four existing repos (`ecom-grpc-apis`, `-userd`, `-orderd`, `ecom-k8s`) are separately published
with consumed tags and "blog series" READMEs. They stay untouched. This is a **new** repo and a
**single** Go module — so `git clone && go build ./...` works with no `go.work` and no `replace`,
and the services cannot drift onto different contract versions (defect #12, §8).

### 4.3 Domain: e-commerce, chosen for the scaling asymmetry

Chosen over BFSI payments, ride hailing, trading and voice AI. The earlier payments example was
rejected as **too complex to explain in the time available**: it needed authorization-vs-capture-vs-
settlement framing, issuer-vs-acquirer framing, and MCC/velocity jargon before the ECS lesson could
start. E-commerce needs one sentence — *browsing scales, ordering must not* — and that sentence is
itself the architecture. Scored on *technical necessity*, not relatability.

### 4.4 No streaming in the core build

Streaming is the minority in real gRPC projects (§13.5: Temporal 121 RPCs / 0 streaming; OTLP 1
unary RPC). More importantly, **every** §9 lesson survives unary — the sticky-connection bug is
connection-level, not stream-level.

---

## 5. Data: SQLite for the demo — RDS in production

**Framing for the talk: say plainly that RDS is the right answer and SQLite is a conference-demo
shortcut.** The application is already driver-agnostic; `DB_DRIVER=postgres` plus a DSN in `DB_URL`
is the whole switch (§5.5). There is **no RDS Terraform module in this repo** — writing one is the
easy half, and leaving it out keeps the apply fast.

### 5.1 Why the shortcut

RDS is the slowest and most expensive part of the demo: ~5–10 min to create on AWS, ~80s on
Ministack ✅, plus a subnet group, a security-group rule, and ~$12–15/mo if left running. Dropping it
takes the AWS apply from ~10 minutes to **~2** ✅, which is the single biggest stage-risk reduction
available. It also deletes a whole class of "is the DB reachable" failure on conference wifi.

### 5.2 Driver: pure Go, mandatory ✅

| Driver | Backend | CGO | Verdict |
|---|---|---|---|
| `gorm.io/driver/sqlite` | `mattn/go-sqlite3` | **required** | ❌ breaks `CGO_ENABLED=0` + distroless/static and ARM64 cross-compilation |
| `github.com/glebarez/sqlite` v1.11.0 | `modernc.org/sqlite` v1.60.1 | none | ✅ **use this** |

Verified: static ARM64 ELF built with `CGO_ENABLED=0`, running SQLite 3.53.4 under GORM with
`TranslateError: true`. Evidence in §13.4. **FTS5 works in the pure-Go driver** ✅, which is what
makes `SearchProducts` real rather than a `LIKE` query.

### 5.3 EFS is ruled out, on SQLite's own advice ✅

SQLite's official position on network filesystems: *"SQLite relies on exclusive locks for write
operations, and those have been known to operate incorrectly for some network filesystems. This has
led to database corruption."* — and *"Rely upon it at your (and your customers') peril."*

Not a cost decision, a correctness one. The database lives on the task's own filesystem.

### 5.4 Three storage shapes — one Dockerfile each

Fargate task storage is **ephemeral and dies with the task**, and N tasks means N independent
databases. That is not hidden; it shapes the design. **There is one Dockerfile per storage shape,
not per service**, because the shape is the interesting part:

| Service | Storage | Dockerfile | Tasks | Consequence |
|---|---|---|---|---|
| `userd` | **baked into the image at build time**, seeded | `Dockerfile.seeded` | **3** | every task has a byte-identical DB, so all *read* paths behave identically → **the load-balancing demo works perfectly** |
| `productsd` | baked in, plus the FTS5 index | `Dockerfile.seeded` | **3** | same; the index is built by the seeder, so nothing is indexed at boot |
| `orderd` | task-local, **writable** | `Dockerfile.stateful` | **1** | orders persist for the life of the task and vanish when it is replaced |
| `gatewayd` | **none at all** | `Dockerfile.stateless` | 1–N | no database, no `/data`, no `DB_DRIVER` — pure translation |

> **Demo-flow rule, do not violate:** anything run at `userd` N>1 must authenticate as a **seeded**
> user. A user created by `Register` exists on exactly one task, so after scale-out two of three
> tasks will fail `VerifyToken` for them — which looks **exactly** like the load-balancing fix not
> working, and would wreck the §9.2 segment. `scripts/load.sh` therefore always logs in as a seeded
> user. `Register` is demonstrated only at N=1, or shown deliberately at N=3 as "watch this *not*
> survive scale-out" — a fine lesson, but announce which one you are doing.

Terraform **refuses** to scale the writer, so the rule is enforced rather than remembered ✅:

```hcl
validation {
  condition     = var.order_desired_count == 1
  error_message = "orderd keeps its database on the task filesystem, so N tasks would mean N divergent databases. Scale userd or productsd instead."
}
```

`make show-guard` shows the refusal on screen. **This is a feature, not a compromise.** The closing
beat:

> "Kill the `orderd` task — your orders are gone. *This* is what people mean when they say don't
> keep state in the task. One env var moves it to Postgres."

### 5.4a Three implementation details that will bite otherwise

- **File ownership.** The runtime image is `distroless/static:nonroot`. The baked SQLite file must be
  `COPY --chown=65532:65532`, and its **directory** must be writable — SQLite needs to create
  `-journal`/`-wal` siblings, so a writable file in a read-only directory still fails with
  `attempt to write a readonly database`.
- **Prometheus needs service discovery**, not static targets: task IPs are assigned at runtime and
  change on every deploy. Locally `docker_sd_configs`; on AWS `dns_sd_configs` against
  `userd.ecom.local`. Acceptance criterion 4 depends on this. Two non-obvious parts, both found by
  running it ✅ — the image runs as `nobody` and **cannot read the Docker socket** (discovery fails
  with `permission denied` while the container looks perfectly healthy, so `user: root` is
  required), and `awsvpc` task containers **expose no ports**, so the metrics port cannot be
  discovered and must be supplied per service.
- **`TranslateError: true`** on the GORM config, or `ErrDuplicatedKey` is never returned and the
  idempotency branch is dead code (defect #1, §8).

### 5.5 Driver abstraction

`DB_DRIVER` ∈ {`sqlite`, `postgres`} plus `DB_URL`. GORM supports both, so this is ~10 lines and one
`switch` in [`internal/platform/store`](internal/platform/store). It keeps the spec honest about the
production answer. The SQLite pool is capped at **one** connection, and WAL plus `busy_timeout`
pragmas are set.

### 5.6 The JWT-secret trap — must not be missed

With `userd` on 3 tasks, **all three must share one `JWT_SECRET`**, or a token minted by task A
fails verification on task B and the demo breaks intermittently and confusingly. The secret comes
from **Secrets Manager** (AWS) / Ministack Secrets Manager (local), injected via the task
definition's `secrets` block — never baked into the image, never per-task. **Verified ✅: Ministack's
ECS resolves `secrets[].valueFrom` against its Secrets Manager, so the local and AWS paths are
identical here** — no emulator gap, and the Secrets Manager segment demos locally. This is the
concrete motivation for that segment rather than a bolted-on aside: get it wrong and the demo
breaks.

### 5.7 Schema

Migrations run at boot via GORM `AutoMigrate` with the **error checked** (the old repo discards it).
Safe here because each task owns its own file — no concurrent-migration race, which is itself worth
one sentence on stage.

```
users:        id TEXT pk, name TEXT, email TEXT UNIQUE, password_hash TEXT, created_at INT
products:     id TEXT pk, title TEXT, description TEXT, category TEXT idx,
              price_minor INT, currency TEXT, stock INT, created_at INT
              + products_fts  (FTS5 virtual table over title/description)
orders:       id TEXT pk, user_id TEXT idx, total_minor INT, currency TEXT,
              status INT, rejection_reason INT,
              idempotency_key TEXT UNIQUE, created_at INT
order_lines:  id pk, order_id TEXT fk ON DELETE CASCADE, product_id TEXT,
              title TEXT, unit_price_minor INT, quantity INT
```

`int64` **minor units** for money everywhere, never a float (the old `orderd` used
`double unit_price` — defect #15). The idempotency key is namespaced per user by the service, and
unique, so a retried request cannot place a second order; the service pre-checks **and** falls back
to the stored row on `gorm.ErrDuplicatedKey`, because the pre-check alone loses a race.

---

## 6. Repo layout

```
grpc-ecs-demo/
├── go.mod                      # ONE module
├── buf.yaml  buf.gen.yaml      # four outputs from one source
├── proto/{user,product,order}/v1/*.proto   # the only hand-written contract
├── gen/go/...                  # generated, committed
├── gen/openapi/                # generated, committed
├── clients/node/src/gen/       # generated TypeScript
├── cmd/{userd,productsd,orderd,gatewayd,seed,healthcheck}/
├── internal/
│   ├── user/  product/  order/           # service + store each
│   └── platform/
│       ├── grpcserver/         # server, health, graceful shutdown, interceptors
│       ├── grpcclient/         # THE load-balancing fix
│       ├── observability/      # OTel + Prometheus
│       ├── store/             # driver switch, pragmas, migrate
│       └── config/             # env parsing, reports ALL missing vars at once
├── build/Dockerfile.{seeded,stateful,stateless}   # one per STORAGE SHAPE
├── terraform/
│   ├── modules/{network,ecr,ecs-cluster,ecs-service,secrets,iam}
│   ├── stack/                  # the whole deployment, shared VERBATIM
│   └── envs/{local,aws}/       # differ ONLY in provider.tf
├── docker/prometheus.yml
├── compose.yaml                # ministack + redis + jaeger + prometheus
├── scripts/{smoke,rest-smoke,api-coverage,load,ps,forward}.sh
├── Makefile
├── PRESENTATION.md
└── docs/{ARCHITECTURE,DEMO_GUIDE,DEPLOY_AWS,AWS_PERMISSIONS,DEMO_ACCESS,PLAN}.md
```

**One proto, four outputs** — `protocolbuffers/go`, `grpc/go`, `grpc-ecosystem/gateway`,
`grpc-ecosystem/openapiv2` and `bufbuild/es` (TypeScript). Generated code is **committed**, so a
clone builds without `buf` installed.

---

## 7. Toolchain — versions verified against the module proxy / registry ✅

| Component | Version | vs. the old repo |
|---|---|---|
| Go | **1.27.1** | was 1.23.1 |
| grpc-go | **v1.84.0** | was v1.71.0 |
| protobuf-go | **v1.36.12** | was v1.36.5 |
| protoc-gen-go-grpc | **v1.6.2** | raw protoc 27.3 |
| grpc-gateway | **v2.31.0** | new |
| golang-jwt | **v5.3.1** | was v3.2.2+incompatible (CVE-2020-26160) |
| gorm | **v1.31.2** | was v1.25.12 |
| sqlite driver | **glebarez/sqlite v1.11.0** → modernc v1.60.1 | new |
| otel / otelgrpc | **v1.47.0 / v0.72.0** | was v1.35.0 / v0.60.0 |
| prometheus/client_golang | **v1.24.1** | v1.21.1 and v1.14.0 — drifted |
| aws-sdk-go-v2 | **v1.47.1** | new |
| Terraform / AWS provider | **1.14.5 / 6.67.0** | new |
| @bufbuild/protoc-gen-es | **2.16.0** | new |
| codegen | **buf** | raw `protoc` |
| base image | pinned `distroless/static:nonroot`, `TARGETARCH` | `alpine:latest`, hardcoded amd64, root |

Target **arm64/Graviton**: cheaper on Fargate and native on the speaker's M-series laptop (no QEMU).
**Verified ✅ Fargate Spot supports ARM64** (GA since Oct 2024, Fargate platform version **1.4.0+**,
all commercial regions). So `cpu_architecture = ARM64` + `FARGATE_SPOT` is a valid combination and
the `ecs-service` module needs no per-service special-casing — the "byte-identical modules" claim
holds without an asterisk.

---

## 8. Defects from the existing repo that this fixes

| # | Where | Problem |
|---|---|---|
| 1 | `userd/db/db.go` | `gorm.Config{}` lacks `TranslateError: true`, so the `ErrDuplicatedKey` branch in `register.go` is **dead code** |
| 2 | all handlers | `errors.New` → everything is `codes.Unknown`; must be `status.Error` with real codes |
| 3 | `userd/utils/jwt.go` | jwt v3; **signing method never checked** (alg confusion); returns `"", nil` on invalid token; unchecked `.(string)` panics |
| 4 | `userd/db/user.go` | `BeforeSave` re-bcrypts on every save despite the comment; any user update locks the account out |
| 5 | `orderd/service/metrics.go` | order ID used as a Prometheus **label** — unbounded cardinality |
| 6 | `orderd/main.go` | `grpcClientMetrics` created but never registered — silently dropped |
| 7 | both `main.go` | **no SIGTERM handling, no `GracefulStop`** — the most ECS-relevant bug |
| 8 | both | no `grpc.health.v1` |
| 9 | both | `AutoMigrate` error ignored |
| 10 | both | `semconv/v1.7.0`; `Shutdown` error dropped; `defer` never runs because `Serve` blocks until `log.Fatalf` → traces lost |
| 11 | `apis` | `require_unimplemented_servers=false` removes a compile-time safety net |
| 12 | modules | `userd` pins apis v0.2.0, `orderd` v0.3.0 — **different contract versions** |
| 13 | Dockerfiles | `alpine:latest`, `GOARCH=amd64` hardcoded, runs as root |
| 14 | `.env` committed | `JWT_SECRET=secret` in-tree → becomes the Secrets Manager demo |
| 15 | `order.proto` | `double unit_price` for money |

---

## 9. Talk content this repo must support

- **9.0 Why ECS, not Lambda.** Lambda runs no listening server; API Gateway REST strips gRPC
  framing; and the ELB docs state it outright for gRPC target groups: *"The only supported target
  types are `instance` and `ip`. … You can't use Lambda functions as targets."* ✅ Needs no streaming.
- **9.1 Service discovery:** Cloud Map DNS → the failure → the fix → Service Connect → ALB.
- **9.2 The headline bug.** grpc-go defaults to **`pick_first`**: even when three tasks are healthy
  and DNS returns three addresses, one task takes 100% of traffic. **Both halves are required** —
  client `grpc.WithDefaultServiceConfig('{"loadBalancingConfig":[{"round_robin":{}}]}')` **and** a
  `dns:///` target prefix (a bare `host:port` uses the **passthrough** resolver and yields exactly
  one address, so `round_robin` has nothing to balance over), plus server
  `keepalive.ServerParameters{MaxConnectionAge: 30s, MaxConnectionAgeGrace: 5s}` so clients
  re-resolve after a scale-out. For an ALB, set
  `load_balancing.algorithm.type = least_outstanding_requests` — with multiplexed HTTP/2, connection
  counts say nothing about task load.

  **Measured at three `userd` tasks** ✅: `pick_first` put **120 of 120** `VerifyToken` calls on one
  task; `round_robin` gave **39 / 40 / 41**. `-var lb_policy=pick_first` redeploys `orderd` with the
  bug, so this is shown live rather than described. The toggle disables **both** halves on purpose,
  because half the fix looks like it works and does not.
- **9.3 Health checks** at three layers: `grpc.health.v1`, container healthCheck, target group.
  ALB gRPC needs a custom health check method `/package.service/method` plus healthy status codes ✅.
  The runtime image is distroless — no shell, no `curl`, no `grpc-health-probe` — so `/healthcheck`
  is a small Go binary baked in, with an HTTP mode for `gatewayd` and a gRPC mode for the rest.
- **9.4 Graceful shutdown** wired to `stopTimeout`. Order matters: flip the health service to
  `NOT_SERVING` **first**, then a bounded `GracefulStop`, then `Stop()`. And `stopTimeout` (30s)
  **must exceed** the app's own `SHUTDOWN_TIMEOUT` (15s), or ECS SIGKILLs mid-drain and the
  graceful-shutdown work is wasted.
- **9.5 Observability**: Jaeger locally, ADOT → X-Ray on AWS. One `CreateOrder` trace shows three
  spans ✅.
- **9.6 `buf breaking`** rejecting a renamed field — the strongest argument for Protobuf, and
  impossible to show with raw `protoc`.
- **9.7 Stateless vs stateful on ECS** — §5.4, delivered by killing the `orderd` task.
- **9.8 One proto, two protocols.** `CheckAvailability` over gRPC works; over REST it is a 404 ✅.

### How the laptop reaches the AWS deployment

ALB is blocked on ACM and sits above the cut line, so the demo needs another path.
**Decision: `gatewayd` runs in a public subnet with `assign_public_ip = true` and a security group
allowing :8080 from the speaker's IP only.** `userd` and `productsd` stay private — only `orderd`
calls them, over Cloud Map. This also avoids a NAT Gateway (§ cost guardrails). SSM port forwarding
is the better option where it is available, because it needs no public IP and no inbound rule at
all; see [`docs/DEMO_ACCESS.md`](docs/DEMO_ACCESS.md).

> **Check the egress IP at the venue** (`curl ifconfig.me`). The conference NAT is probably not the
> IP allow-listed from home, and an **IPv6** address in a `cidr_ipv4` field fails the apply partway
> through ✅ — caught in a plan, which is why the plan gets read.

### Cost guardrails

**No NAT Gateway** (~$32/mo + data — the #1 demo-account bill killer): public subnets with
`assign_public_ip`. For production the opposite is correct — private subnets plus VPC endpoints for
`ecr.api`, `ecr.dkr`, `s3`, `logs` and `secretsmanager`, which is cheaper than NAT at this scale and
keeps traffic off the internet. **Fargate has no free tier**; ARM64 in us-east-1 is $0.03238 per
vCPU-hour + $0.00356 per GB-hour ≈ **1¢ per task-hour** at 0.25 vCPU / 0.5 GB. Container Insights
off, log retention 1 day, `desired_count` 1 except `userd`=3 during §9.2. **`terraform destroy`
before leaving the venue.**

---

## 10. Proving it is really ECS (`make ps`)

The audience's fair question is "how is that different from docker compose?"
`scripts/ps.sh` answers it in seven escalating steps, and the last four are the convincing ones:

| # | Shows | Why it convinces |
|---|---|---|
| 1 | `describe-services`: desired / running / pending | there is a control plane reconciling state |
| 2 | tasks with the task-definition **revision** | deploys are immutable revisions, not restarts |
| 3 | `networkMode: awsvpc`, `FARGATE`, `ARM64`, execution role | this is a Fargate task definition |
| 4 | the task **self-describing** via `ECS_CONTAINER_METADATA_URI_V4` | nothing in our code sets this; the platform does |
| 5 | `AWS_CONTAINER_CREDENTIALS_FULL_URI` | **this is how task roles deliver credentials** — no key anywhere |
| 6 | `secrets[].valueFrom` is an ARN | the secret never entered git or the image |
| 7 | login as each seeded user | the baked database is identical on every task |

Step 4 is the one to linger on. Verified output ✅:

```
Cluster  : arn:aws:ecs:us-east-1:000000000000:cluster/ecom-local
TaskARN  : arn:aws:ecs:us-east-1:000000000000:task/ecom-local/90c7be3b-...
Family   : userd rev 1
AZ       : us-east-1a
```

**Emulator fidelity — say this out loud, do not hope nobody reads the table.** `healthStatus` comes
back `UNKNOWN` and `launchType` empty, because Ministack does not echo those back even though the
task definition requests `FARGATE` and the service is created with `launch_type = FARGATE`. Real ECS
reports `HEALTHY` and `FARGATE`. Naming the emulator's limits yourself is more credible than being
caught by them, and it is precisely why the talk also deploys to AWS.

---

## 11. What is deliberately NOT built

| Missing | Why |
|---|---|
| **ALB** | a gRPC target group **requires an HTTPS listener** ✅ → ACM certificate → a domain. Above the cut line. Cloud Map covers the internal hops; `gatewayd` is the one thing an ALB belongs in front of, and that is a slide, not a demo |
| **RDS** | §5.1. The application-side switch exists; the Terraform module does not |
| **EFS** | §5.3 — corruption, not cost |
| **NAT Gateway** | cost guardrails above |
| **Container Insights** | costs money per metric; Prometheus and Jaeger cover the demo |
| **Autoscaling policies** | `make scale N=3` is more honest on stage than waiting for a CloudWatch alarm |
| **Streaming** | §4.4 |
| **An LLM** | §2. The payments example needed prose for a decline reason; an order rejection is an enum |

---

## 12. Acceptance criteria

All eight pass as of 2026-10-09 ✅.

1. `git clone && go build ./...` succeeds on a clean machine with only Go 1.27 installed.
2. `go test ./...` passes offline — **8 packages**, plus `go vet` clean and
   `npm run typecheck` clean in `clients/node`.
3. `make local-up && make images && make tf-local-apply && make demo` yields a confirmed order and
   all three rejection reasons, end to end.
4. `make demo-load` (authenticating as a **seeded** user — §5.4) with `userd` at 3 tasks shows
   traffic on **one** task before the fix and spread across all three after, visible in Prometheus
   per task. Requires `make dns` to have aliased **every** `userd` task, not just the first.
5. Killing a `userd` task mid-load drops **zero** in-flight RPCs (graceful shutdown works).
6. `terraform plan` in `envs/aws` succeeds, and its module set is byte-identical to `envs/local` —
   `diff` of the two `provider.tf` files is the only difference.
7. One Jaeger trace shows `orderd.CreateOrder` → `userd.VerifyToken` and →
   `productsd.CheckAvailability` as child spans.
8. `make api-coverage` reports **20/20**: all 10 RPCs, all 9 REST routes, and the negative assertion
   that `CheckAvailability` is *not* exposed over REST.

---

## 13. Verified evidence

### 13.1 Ministack Step 0 spike — PASSED (2026-10-06)

Distroless ARM64 Go 1.27 / grpc-go 1.84.0 binary, Terraform 1.14.5 / AWS provider 6.67.0:

| Check | Result |
|---|---|
| ECR repo via Terraform + `docker push` | ✅ pushes to `localhost:4566/<repo>` |
| ECS starts a real container from `FARGATE` + `awsvpc` + `ARM64` task def | ✅ task `RUNNING` |
| gRPC reflection + `grpc.health.v1` | ✅ `{"status":"SERVING"}` |
| RDS module → Postgres reachable from a task | ✅ PostgreSQL 16.14 (now unused — §5) |
| Task → `jaeger:4317` | ✅ via `DOCKER_NETWORK` |
| Cloud Map DNS between tasks | ❌ natively; ✅ via Docker-alias shim |

Gotcha found and fixed: publishing the RDS port range on the ministack container makes
`CreateDBInstance` fail with `port is already allocated` and then silently report a dead endpoint.

### 13.2 Terraform → Ministack ECS, end to end — PASSED (2026-10-08, re-run cold 2026-10-09)

`terraform apply` in `envs/local` creates VPC, subnets, security group, ECR repos, ECS cluster with
FARGATE + FARGATE_SPOT, Cloud Map namespace, log group, Secrets Manager secret, IAM roles, and all
four ECS services. Tasks start, and `scripts/smoke.sh` passes against them. `make api-coverage`
reports 20/20 **on a cold start** ✅, and `destroy` removed 36 resources leaving no `ecom` networks.

**Secrets Manager injection works on the emulator.** `userd` requires `JWT_SECRET` and refuses to
start without it, so the task reaching SERVING is proof that `secrets[].valueFrom` resolved.

Bugs this deployment caught that no local `go test` would have:

1. **`count` cannot depend on a resource attribute.** `count = var.namespace_id == "" ? 0 : 1` fails
   with "Invalid count argument" because the namespace does not exist at plan time. Replaced with a
   statically-known `enable_service_discovery` flag.
2. **OTel semconv version mismatch crash-looped every task.** `resource.Merge(resource.Default(),
   ...)` rejects a resource whose schema URL differs from the SDK's, so importing `semconv/v1.37.0`
   against otel sdk v1.47.0 (which uses v1.43.0) is fatal. **It is invisible locally**, because with
   no `OTEL_EXPORTER_OTLP_ENDPOINT` the tracer short-circuits to a no-op and never builds a
   resource. Fixed, and pinned by a regression test that passes an endpoint.
3. **Ministack cannot create Cloud Map services via Terraform.** Its `CreateService` requires a
   top-level `NamespaceId`; the AWS provider sends it nested in `DnsConfig`, which is what real AWS
   accepts. Confirmed by calling both shapes directly. So `enable_service_discovery = false` locally
   and the `make dns` alias shim stands in; `true` on AWS.
4. **`docker ps` ORs multiple `--filter name=` values.** The first `make dns` therefore aliased the
   wrong container as `userd.ecom.local` — a silent misroute that would have broken the stage demo
   in a baffling way. Fixed with one anchored regex filter, plus a force-disconnect pass, because
   `docker network rm` fails while containers are attached and left stale aliases behind.
5. **`capacity_provider_strategy` produced a perpetual diff** against the emulator, which does not
   echo the strategy back; the provider then refuses with *"force_new_deployment should be true"*.
   Spot is now opt-in and the default is plain `launch_type = "FARGATE"`.
6. **`grpc.NewClient` is lazy**, so the first cold `api-coverage` run scored 10/20: the first RPC
   pays for resolution and the handshake and fails fast if the upstream is not up yet. Fixed with
   `grpcclient.Warm`, which connects and waits for `Ready` before serving. Now 20/20 cold ✅.
7. **Prometheus was never actually scraping** — see §5.4a. Two independent causes, both silent.
8. **Local boot order.** Task containers start before `make dns` can attach them to the alias
   network, so `orderd`'s upstream `Warm` fails and its first RPCs return
   `Unavailable: lookup userd.ecom.local ... server misbehaving` until the grpc DNS resolver retries.
   It self-heals within seconds; it is an artifact of the alias shim, not of the services, and on
   AWS Cloud Map registers the task before it is reachable anyway. **Re-run the command.**

Lesson for the talk, worth saying out loud: items 2, 4, 6 and 7 were only findable by actually
running it. Neither unit tests nor `terraform validate` would have surfaced them.

### 13.3 Real AWS — PASSED, then destroyed (2026-10-09)

Account `272639014758`, **us-east-1**, ~2 minutes to apply.

| Check | Result |
|---|---|
| All four tasks RUNNING on Fargate ARM64 | ✅ |
| Full REST smoke through `gatewayd`'s public IP | ✅ `POST /v1/orders` → `CONFIRMED total=29999.0` |
| `CheckAvailability` over REST | ✅ 404 |
| `userd` scaled to 3 tasks | ✅ 3 Cloud Map A records across 2 AZs (`10.0.1.91, 10.0.0.172, 10.0.0.135`) |
| `terraform destroy` | ✅ 45 resources; verified 0 clusters, repos, namespaces, secrets, VPCs, ENIs and IAM roles remaining in us-east-1 **and** ap-south-1 |

Three things real AWS caught that the emulator could not:

1. **ap-south-1 had a Fargate vCPU quota of zero**, and ECS reported only *"your account is
   currently blocked"* — not a quota error. Quota 30 was available in us-east-1, us-west-2 and
   eu-central-1, so the region moved. **Check `service-quotas` before choosing a region.**
2. **The ECS service-linked role had not propagated.** The first apply failed with *"Unable to
   assume the service linked role"* although the role already existed. A retry fixed it.
3. **THE BIG ONE — an empty `health_check_custom_config {}` block broke Cloud Map DNS.** It looks
   like the clean way to silence a deprecation warning on `failure_threshold`, but it makes the
   provider send *no* custom health config at all. Cloud Map then never accepts ECS's health
   reports, every instance stays `AWS_INIT_HEALTH_STATUS=UNHEALTHY`, unhealthy instances are
   **excluded from DNS answers**, and clients fail with `code 14: "no children to pick from"` —
   while all four tasks, all four Cloud Map services and VPC DNS all look healthy in the console.
   AWS forces the value to `1` regardless, so the deprecation warning is cosmetic but **the block is
   required**. The fix also needed the ECS services drained to 0 first, because `DeleteService`
   returns `ResourceInUse`. There is a prominent comment on it in
   `terraform/modules/ecs-service/main.tf`; **do not "clean it up".**

### 13.4 SQLite pure-Go static build (2026-10-07)

Run in a throwaway package that is **not** in the repo — the point was to settle the driver choice
before any service code was written (§5.2):

```
$ CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags probe ./sqlitecheck
$ file probe → ELF 64-bit LSB executable, ARM aarch64, statically linked
$ go run  → OK pure-go sqlite=3.53.4 row="probe"
```

The standing proof is now the build itself: `build/Dockerfile.seeded` builds with `CGO_ENABLED=0`
onto `distroless/static:nonroot`, which simply would not run if the driver needed CGO. FTS5 is
exercised by `make demo`, whose first step is a `SearchProducts` call ✅.

### 13.5 When gRPC makes sense — measured from 14 projects' real `.proto` files

Temporal 121 RPCs / **0** streaming · Milvus 154 / 2 · TiKV 73 / 9 · Dapr 74 / 6 · k8s CRI 43 / 7 ·
etcd 42 / 4 · Qdrant 30 / **0** · CockroachDB 28 / 10 · containerd 17 / **0** · Bazel RE 13 / 5 ·
Vitess 9 / 4 · Thanos 4 / 1 · **Envoy xDS 2 / 2 (100%)** · **OTLP 1 / 0**.

Conclusion: gRPC is chosen for a **typed, versioned, polyglot contract on internal traffic**;
streaming is a capability for specific cases (config push, watch, blob transfer). Not one of the 14
is a public consumer API — which is the honest answer to "should our mobile app speak gRPC?".
Protos in [`docs/evidence/`](docs/evidence).

### 13.6 The TypeScript segment (~60 seconds)

`clients/node` is generated by the **same protos** that produce the Go stubs — one extra plugin,
nothing hand-written.

> TypeScript codegen lives in a **separate template**, `buf.gen.ts.yaml`, run with
> `--include-imports`. `protoc-gen-es` emits a real import for every proto dependency, so a proto
> importing `google/api/annotations.proto` generates `import { file_google_api_annotations }` — and
> that file only exists if imports are generated too. `--include-imports` is a CLI flag applying to
> every plugin in a template, and generating googleapis into `gen/go` would collide with what
> grpc-gateway already provides. Hence two templates; `make proto` runs both. The plugin also needs
> `import_extension=js`, because the client is an ESM package on `moduleResolution: NodeNext` and
> Node ESM requires explicit extensions on relative imports ✅. It exists because "a typed contract with free
polyglot codegen" is the reason real projects adopt gRPC (§13.5), and claiming that is weaker than
showing it.

What to say while `make ts-demo` runs:

1. "Same `.proto`. I added one plugin. I wrote no types."
2. `int64` → Go `int64`, TypeScript **`bigint`** — because a JS `number` cannot hold int64 safely.
   The generator gets that right so you cannot silently truncate a timestamp or a price. Hand-written
   clients get this wrong constantly.
3. Status codes survive the language boundary: `AlreadyExists`, `Unauthenticated`,
   `InvalidArgument` — so the client branches on a code, not on a message string.
4. The browser caveat, which sets up §9.0: this is **real gRPC over HTTP/2**, and it works because
   Node can open an HTTP/2 connection and send trailers. A browser cannot — hence gRPC-Web, Connect,
   and `gatewayd`. Not one of the 14 surveyed projects exposes gRPC publicly.

Verified output (2026-10-09) ✅ — needs `make forward` first, since `awsvpc` tasks have no host port:

```
search       -> Wireless Noise Cancelling Headphones, Noise Cancelling Earbuds Ultra
get          -> Wireless Noise Cancelling Headphones ₹29999.00
logged in    -> expires 2026-10-10T13:49:51.722Z
verified     -> demo@example.com
ordered      -> CONFIRMED ₹29999.00 (1 line priced by productsd)
idempotent   -> same order (replay=true)
rejected     -> REJECTED OUT_OF_STOCK
bad password -> Unauthenticated
bad token    -> Unauthenticated
empty cart   -> InvalidArgument
```

---

## 14. Stage rules

```bash
make test              # 8 packages, offline
make local-up          # ministack + jaeger + prometheus
make images            # four ARM64 distroless images
make tf-local-apply    # terraform apply → ECS tasks (auto-runs make dns)
make ps                # PROVE it is ECS: control plane, task metadata, task role
make demo              # browse → login → order → three rejections
make dev-rest          # the same flow over REST
make ts-demo           # the SAME flow from TypeScript   ← polyglot segment
make forward           # publish ports for Postman
make scale N=3         # userd 1→3
make demo-load         # sustained load; watch per-task metrics  ← §9.2
make show-guard        # terraform refusing to scale the writer
make api-coverage      # 20/20
```

- **Pre-provision AWS.** Never run a cold `terraform apply` on stage; deploy the day before and
  live-demo a *delta* — a scale-out, or killing a task.
- **Pin every image tag** and pre-pull the morning of.
- **Use a seeded user** once `userd` is at more than one task (§5.4).
- **Keep a screen recording and a saved `terraform plan`** as insurance.
- **`terraform destroy` before leaving the venue.**

---

## 15. Resolved questions

| | Resolution |
|---|---|
| Domain | **e-commerce** (§4.3). Payments was built first and rejected as too complex to explain |
| Database | **SQLite on the task**, three storage shapes (§5.4). RDS named as the production answer |
| ALB / ACM / domain | **cut.** External ingress is `gatewayd` on a public IP restricted to one `/32`, or SSM port forwarding |
| Bedrock / LLM | **removed entirely** (§2). The code existed, was imported by nothing, and is deleted |
| Repo name | `grpc-ecs-demo`, module `github.com/lakhansamani/grpc-ecs-demo` |
| Public repo | https://github.com/lakhansamani/grpc-ecs-demo |
| Region | **us-east-1** — ap-south-1 had zero Fargate vCPU quota (§13.3) |
