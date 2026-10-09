# grpc-ecs-demo — Specification

**Purpose:** demo repo for the talk *"Running Go gRPC Services on ECS: From LocalStack to Production Cloud"*
**Speaker:** Lakhan Samani · AWS Community Day Vadodara · week of 2026-10-06
**Status:** spec for review. **No application code written yet.** Nothing here is built until this is approved.

Every factual claim marked ✅ was verified by running it on 2026-10-06/07, not recalled. Evidence in §14.

---

## 1. Goal

Teach a room of AWS practitioners how to take a Go gRPC microservice from `localhost:50051` to
Fargate, using **one set of Terraform modules** that applies to a local emulator and to real AWS.
The payments domain is a vehicle; the lessons are gRPC-on-ECS lessons.

Three things the audience should leave able to do:

1. Explain why a gRPC service **cannot** run on Lambda, and what that implies.
2. Diagnose the single most common gRPC-on-ECS bug: all traffic pinned to one task.
3. Run the same Terraform locally and against AWS, differing only in a provider block.

## 2. Non-goals

No Kubernetes. No card network or PSP integration. No capture or settlement. No ledger. No vector
DB. No RAG. No streaming in the core build (§5.4). No third service. No real money, no real cards,
no PII.

---

## 3. Architecture

Two services, **all RPCs unary**, one real network hop between them.

```
                    ┌───────────────────────────────────────────────────┐
 demo-client ─────> │  paymentd  ──────> SQLite (task-local, ephemeral) │
  Authorize         │     │                                             │
  (unary)           │     ├──gRPC────> identityd ──> SQLite (baked-in)  │
                    │     │             VerifyToken                     │
                    │     └──────────> explain: template (Bedrock opt-in)│
                    └───────────────────────────────────────────────────┘
      both ──OTLP──> Jaeger (local) / ADOT→X-Ray (AWS)
      both ──:909x/metrics──> Prometheus
```

### 3.1 `identityd` — stateless, **scaled to 3 tasks**

Port of the existing `userd`. This is the service used for the load-balancing demo, because its
read paths are identical on every task.

| RPC | Notes |
|---|---|
| `Register` | writes — see §6.4 for the honest caveat |
| `Login` | read; verifies bcrypt hash, issues HS256 JWT |
| `VerifyToken` | read; was `Me()`, renamed to say what it does |

### 3.2 `paymentd` — **1 task**, holds writable state

Port of the existing `orderd`. Issuer-side: it *makes* the approve/decline decision.

| RPC | Notes |
|---|---|
| `Authorize` | the hot path; fans out to `identityd.VerifyToken`, then rules, then persist |
| `GetTransaction` | ownership-checked read |
| `ListTransactions` | paginated read |
| `ExplainDecision` | rules already decided; Bedrock only phrases it |

### 3.3 What `Authorize` means

Card payments have three steps people conflate:

| Step | What happens | Money moves? |
|---|---|---|
| **Authorization** | issuer checks validity, funds, fraud; places a **hold**; approve/decline | No |
| Capture | merchant says "take it" | Yes |
| Settlement | batch transfer between banks | Already moved |

`Authorize` is **step 1 only**, from the **issuer** side. There is no gateway/PSP call anywhere.
Deliberate: it keeps the demo offline, and it keeps the latency budget *ours*, which §11.2 depends
on — an external PSP call would dominate the latency and hide the lesson.

---

## 4. Proto contracts

Package-per-service, versioned by directory. Generated code **committed**, so a clone builds
without `buf`.

```protobuf
// proto/identity/v1/identity.proto
syntax = "proto3";
package identity.v1;
option go_package = "github.com/lakhansamani/grpc-ecs-demo/gen/go/identity/v1;identityv1";

service IdentityService {
  rpc Register    (RegisterRequest)    returns (RegisterResponse);
  rpc Login       (LoginRequest)       returns (LoginResponse);
  rpc VerifyToken (VerifyTokenRequest) returns (VerifyTokenResponse);
}

message User { string id = 1; string name = 2; string email = 3; }

message RegisterRequest  { string name = 1; string email = 2; string password = 3; }
message RegisterResponse { string user_id = 1; }
message LoginRequest     { string email = 1; string password = 2; }
message LoginResponse    { string token = 1; int64 expires_at = 2; }
// Token travels in gRPC metadata ("authorization: Bearer <jwt>"), not in the body.
message VerifyTokenRequest  {}
message VerifyTokenResponse { User user = 1; }
```

```protobuf
// proto/payment/v1/payment.proto
syntax = "proto3";
package payment.v1;
option go_package = "github.com/lakhansamani/grpc-ecs-demo/gen/go/payment/v1;paymentv1";

service PaymentService {
  rpc Authorize        (AuthorizeRequest)        returns (AuthorizeResponse);
  rpc GetTransaction   (GetTransactionRequest)   returns (GetTransactionResponse);
  rpc ListTransactions (ListTransactionsRequest) returns (ListTransactionsResponse);
  rpc ExplainDecision  (ExplainDecisionRequest)  returns (ExplainDecisionResponse);
}

enum Decision {
  DECISION_UNSPECIFIED = 0;
  DECISION_APPROVED    = 1;
  DECISION_DECLINED    = 2;
}

enum DeclineReason {
  DECLINE_REASON_UNSPECIFIED      = 0;
  DECLINE_REASON_LIMIT_EXCEEDED   = 1;
  DECLINE_REASON_VELOCITY         = 2;
  DECLINE_REASON_MERCHANT_BLOCKED = 3;
  DECLINE_REASON_INSUFFICIENT     = 4;
}

message Transaction {
  string   id                = 1;
  string   user_id           = 2;
  int64    amount_minor      = 3;  // paise. NEVER float for money.
  string   currency          = 4;  // ISO-4217, "INR"
  string   merchant_id       = 5;
  string   merchant_category = 6;  // MCC
  Decision decision          = 7;
  DeclineReason decline_reason = 8;
  int64    created_at        = 9;  // unix millis
}

message AuthorizeRequest {
  int64  amount_minor      = 1;
  string currency          = 2;
  string merchant_id       = 3;
  string merchant_category = 4;
  string idempotency_key   = 5;  // retries must not double-authorize
}
message AuthorizeResponse { Transaction transaction = 1; }

message GetTransactionRequest   { string id = 1; }
message GetTransactionResponse  { Transaction transaction = 1; }
message ListTransactionsRequest { int32 page_size = 1; string page_token = 2; }
message ListTransactionsResponse { repeated Transaction transactions = 1; string next_page_token = 2; }
message ExplainDecisionRequest  { string transaction_id = 1; }
message ExplainDecisionResponse { string explanation = 1; string model_id = 2; }
```

**Deliberate choices:** `int64` minor units for money, never `double` (the current `orderd` uses
`double unit_price` — a defect). Enums for decisions so the contract is self-documenting.
`idempotency_key` because any payment API without one is wrong. Codegen keeps
`require_unimplemented_servers` **on** (the current repo disables it, losing a compile-time check).

---

## 5. Decisions already settled

### 5.1 Local emulator: Ministack, not LocalStack ✅

LocalStack retired its free Community edition on 2026-03-23. ECS/ECR/ELB/Cloud Map were **never**
in the free image (verified by listing `localstack/services/` at tags v1.4.0, v2.3.2, v3.8.1,
v4.0.0 — no `ecs`, no `elbv2`, no `servicediscovery` at any of them). A Hobby account does not help:
ECS, ECR, ALB, RDS and Bedrock are all excluded, and even paid Base lacks Cloud Map and Bedrock.

**Ministack** (`ministackorg/ministack`, MIT) emulates ECS with real Docker containers, plus ECR,
ALB, Cloud Map, Secrets Manager, SSM and Bedrock — free. §14 has the verification run.

### 5.2 One Go module, one new repo

The four existing repos (`ecom-grpc-apis`, `-userd`, `-orderd`, `ecom-k8s`) are separately published
with consumed tags and "blog series" READMEs. They stay untouched. This is a **new** repo, a
**single** Go module — so `git clone && go build ./...` works with no `go.work` and no `replace`,
and the two binaries cannot drift onto different contract versions.

### 5.3 Domain: BFSI payment authorization

Chosen over ride hailing, trading, voice AI and e-commerce. Scored on *technical necessity* (§11.0,
§11.2) rather than relatability.

### 5.4 No streaming in the core build

Streaming is the minority in real gRPC projects (§14.3: Temporal 121 RPCs / 0 streaming; OTLP 1
unary RPC). More importantly, **every** §11 lesson survives unary — the sticky-connection bug is
connection-level, not stream-level. One optional server-streaming RPC sits above the cut line as a
visual only.

---

## 6. Data: SQLite for the demo — RDS in production

**Framing for the talk: say plainly that RDS is the right answer and SQLite is a
conference-demo shortcut.** The `rds` module stays in the repo, unapplied, and
`DB_DRIVER=postgres` is the whole switch.

### 6.1 Why the shortcut

RDS is the slowest and most expensive part of the demo: ~5–10 min to create on AWS, ~80s on
Ministack ✅, plus a subnet group, a security group rule, and ~$12–15/mo if left running. Dropping it
takes the AWS apply from ~10 minutes to ~2, which is the single biggest stage-risk reduction
available. It also deletes a whole class of "is the DB reachable" failure on conference wifi.

### 6.2 Driver: pure Go, mandatory ✅

| Driver | Backend | CGO | Verdict |
|---|---|---|---|
| `gorm.io/driver/sqlite` | `mattn/go-sqlite3` | **required** | ❌ breaks `CGO_ENABLED=0` + distroless/static and ARM64 cross-compilation |
| `github.com/glebarez/sqlite` v1.11.0 | `modernc.org/sqlite` v1.60.1 | none | ✅ **use this** |

Verified: static ARM64 ELF built with `CGO_ENABLED=0`, running SQLite 3.53.4 under GORM with
`TranslateError: true`. Evidence in §14.2.

### 6.3 EFS is ruled out, on SQLite's own advice ✅

SQLite's official position on network filesystems: *"SQLite relies on exclusive locks for write
operations, and those have been known to operate incorrectly for some network filesystems. This has
led to database corruption."* — and *"Rely upon it at your (and your customers') peril."*

So no EFS. The database lives on the task's own filesystem.

### 6.4 Consequences — stated plainly, then used as a teaching point

Fargate task storage is **ephemeral and dies with the task**, and N tasks means N independent
databases. That is not hidden; it shapes the design:

| Service | DB | Tasks | Consequence |
|---|---|---|---|
| `identityd` | **baked into the image at build time**, seeded with demo users | **3** | every task has a byte-identical DB, so all *read* paths (`Login`, `VerifyToken`) behave identically → **the load-balancing demo works perfectly**. `Register` writes only to the task that served it, and is explicitly non-persistent. |

> **Demo-flow rule, do not violate:** anything run at `identityd` N>1 must authenticate as a
> **seeded** user. A user created by `Register` exists on exactly one task, so after scale-out two
> of three tasks will fail `VerifyToken` for them — which looks exactly like the load-balancing fix
> not working, and would wreck the §11.2 segment. `make demo-load` therefore always logs in as a
> seeded user. `Register` is demonstrated only at N=1, or shown deliberately at N=3 as "watch this
> *not* survive scale-out" — a fine lesson, but announce which one you are doing.
| `paymentd` | task-local, writable | **1** | authorizations persist for the life of the task and vanish when it is replaced |

**This is a feature, not a compromise.** The closing beat of the talk:

> "Kill the `paymentd` task — your authorizations are gone. *This* is what people mean when they say
> don't keep state in the task. One env var moves it to RDS."

The `rds` Terraform module is written and kept, **not applied by default**, so flipping
`DB_DRIVER=postgres` is a 30-second finale if time allows.

### 6.4a Two implementation details that will bite otherwise

- **File ownership.** The runtime image is `distroless/static:nonroot`. The baked SQLite file must be
  `COPY --chown=nonroot:nonroot`, and its **directory** must be writable — SQLite needs to create
  `-journal`/`-wal` siblings, so a writable file in a read-only directory still fails with
  `attempt to write a readonly database`.
- **Prometheus needs service discovery**, not static targets: task IPs are assigned at runtime.
  Locally use `docker_sd_configs` (the Docker socket is already mounted); on AWS use
  `dns_sd_configs` against `identityd.ecom.local`. Acceptance criterion 4 depends on this.

### 6.8 Explanations without Bedrock access — the design

The deployed environment has **no Bedrock access**, so the explanation path must work with nothing
behind it. `internal/payment/explain` therefore ships three pieces:

| Piece | Role |
|---|---|
| `Template` | Pure Go, no network, no credentials, deterministic. **The default, including on ECS.** |
| `Bedrock` | The real AWS SDK `bedrockruntime` Converse client. Opt-in via `LLM_PROVIDER=bedrock`. |
| `Fallback` | Wraps any provider; on error *or* empty output it degrades to `Template`. Always applied. |

**Why the template is not a cop-out.** For a card decline, a deterministic template is arguably the
*correct* production choice: auditable, instant, free, translatable, and structurally incapable of
inventing a reason the rules did not give. The honest line for the stage is:

> "The rules decide. The model only phrases it. And for a regulated decline reason, a template does
> that better than a model — so that is what ships. Here is the interface, so swapping it is a
> config change, not a rewrite."

**How the Bedrock path is mocked when it is enabled.** No custom endpoint code exists. The AWS SDK
already honours `AWS_ENDPOINT_URL_BEDROCK_RUNTIME` (its generic `AWS_ENDPOINT_URL_<SDK_ID>`
mechanism, verified ✅), so the deployment alone decides where the client points:

| Environment | `AWS_ENDPOINT_URL_BEDROCK_RUNTIME` | Result |
|---|---|---|
| local | `http://ministack:4566` | Ministack's emulated Converse (mock text, or real prose via the Ollama proxy) |
| ECS, no access | a stub endpoint, or simply leave `LLM_PROVIDER=template` | template text |
| real Bedrock | unset | real model via the task role |

**This is the talk's thesis for the third time**: the same endpoint-override trick as Terraform's
`endpoints` block and the same boundary discipline as the database driver. One env var, and
**not a single `if local` branch anywhere in the codebase**. Verified by a test that stands a stub
HTTP server in for Bedrock and asserts the SDK routes to it ✅.

**Consequence for §11:** the "task IAM role instead of API keys" lesson now hangs on **Secrets
Manager** (the shared `JWT_SECRET`, §6.6), not on Bedrock. That is a better example anyway, because
it is load-bearing — get it wrong with 3 `identityd` tasks and the demo breaks.

### 6.5 Driver abstraction

`DB_DRIVER` ∈ {`sqlite`, `postgres`} plus `DB_URL`. GORM supports both, so this is ~10 lines and one
`switch`. It keeps the finale cheap and keeps the spec honest about the production answer.

### 6.6 The JWT-secret trap — must not be missed

With `identityd` on 3 tasks, **all three must share one `JWT_SECRET`**, or a token minted by task A
fails verification on task B and the demo breaks intermittently and confusingly. The secret comes
from **Secrets Manager** (AWS) / Ministack Secrets Manager (local), injected via the task
definition's `secrets` block — never baked into the image, never per-task. **Verified ✅: Ministack's
ECS resolves `secrets[].valueFrom` against its Secrets Manager, so the local and AWS paths are
identical here** — no emulator gap, and the Secrets Manager segment demos locally. This is the concrete
motivation for the Secrets Manager section rather than a bolted-on aside.

### 6.7 Schema

Migrations run at boot via GORM `AutoMigrate` with the **error checked** (the current repo discards
it). Safe here because each task owns its own file — no concurrent-migration race, which is itself
worth one sentence on stage.

```
users:        id TEXT pk, name TEXT, email TEXT UNIQUE, password_hash TEXT, created_at INT
transactions: id TEXT pk, user_id TEXT idx, amount_minor INT, currency TEXT,
              merchant_id TEXT, merchant_category TEXT, decision INT, decline_reason INT,
              idempotency_key TEXT UNIQUE, created_at INT
```

---

## 7. Repo layout

```
grpc-ecs-demo/
├── go.mod                      # ONE module
├── buf.yaml  buf.gen.yaml
├── proto/{identity,payment}/v1/*.proto
├── gen/go/...                  # generated, committed
├── cmd/
│   ├── identityd/main.go
│   ├── paymentd/main.go
│   └── demo-client/main.go     # -mode=once | load
├── internal/
│   ├── identity/               # service + store
│   ├── payment/                # service + store + rules + llm
│   └── platform/
│       ├── grpcserver/         # server, health, graceful shutdown, interceptors
│       ├── observability/      # OTel + Prometheus
│       ├── db/                 # driver switch, migrate
│       └── config/             # env parsing, reports ALL missing vars at once
├── build/
│   ├── Dockerfile.identityd    # includes `seed` step that bakes the SQLite file
│   └── Dockerfile.paymentd
├── terraform/
│   ├── modules/{network,ecr,ecs-cluster,ecs-service,secrets,observability}
│   │                              # + alb, rds — both ABOVE the cut line (§15.1)
│   └── envs/{local,aws}/
├── compose.yaml                # ministack + redis + jaeger + prometheus [+ ollama profile]
├── Makefile
└── docs/DEMO.md                # §12 runbook
```

---

## 8. Toolchain — versions verified against the module proxy / registry ✅

| Component | Version | vs. current repo |
|---|---|---|
| Go | **1.27.1** | was 1.23.1 |
| grpc-go | **v1.84.0** | was v1.71.0 |
| protobuf-go | **v1.36.12** | was v1.36.5 |
| protoc-gen-go-grpc | **v1.6.2** | raw protoc 27.3 |
| golang-jwt | **v5.3.1** | was v3.2.2+incompatible (CVE-2020-26160) |
| gorm | **v1.31.2** | was v1.25.12 |
| sqlite driver | **glebarez/sqlite v1.11.0** → modernc v1.60.1 | new |
| otel / otelgrpc | **v1.47.0 / v0.72.0** | was v1.35.0 / v0.60.0 |
| prometheus/client_golang | **v1.24.1** | v1.21.1 and v1.14.0 — drifted |
| aws-sdk-go-v2 | **v1.47.1** | new |
| Terraform / AWS provider | **1.14.5 / 6.67.0** | new |
| codegen | **buf** | raw `protoc` |
| base image | pinned `distroless/static:nonroot`, `TARGETARCH` | `alpine:latest`, hardcoded amd64, root |

Target **arm64/Graviton**: cheaper on Fargate and native on the speaker's M-series laptop (no QEMU).
**Verified ✅ Fargate Spot supports ARM64** (GA since Oct 2024, Fargate platform version **1.4.0+**,
all commercial regions, up to ~70% off). So `cpu_architecture = ARM64` + `FARGATE_SPOT` is a valid
combination and the `ecs-service` module needs no per-service capacity-provider special-casing —
the "byte-identical modules" claim holds without an asterisk.

---

## 9. Defects from the existing repo that this fixes

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

## 10. Local environment

`compose.yaml`: `ministack` (:4566) + `redis` + `jaeger` + `prometheus`, all on the `ecom-infra`
Docker network, with `DOCKER_NETWORK=ecom-infra` so Ministack-launched ECS tasks join it. **No
postgres service** — there is no RDS now, and SQLite lives inside the task.

`ollama` is a compose **profile** (`make llm-up`): Ministack's `MINISTACK_BEDROCK_PROXY_URL`
forwards Converse to any OpenAI-compatible endpoint and translates back, giving real LLM prose
offline **through the real AWS SDK path**. If unreachable it falls back to the canned mock
*silently* — the correct failure mode on stage. ✅

Known local gaps, with the agreed workaround:

| Gap | Workaround |
|---|---|
| Cloud Map stores registrations but serves **no DNS** ✅ | `make dns` adds Docker network aliases — service discovery *is* just DNS. Verified resolving. **Must alias every task, not one**: Docker's embedded DNS returns all A records for a shared alias, which is what makes the local §11.2 demo possible. |
| `awsvpc` tasks have **no host port** ✅ | correct AWS behaviour; `demo-client` runs as a container on the task network |
| ECS Service Connect / Envoy not emulated | that comparison is AWS-only |
| Bedrock text is a deterministic mock | optional Ollama proxy above |

---

## 11. Talk content this repo must support

- **11.0 Why ECS, not Lambda.** Lambda runs no listening server; API Gateway REST strips gRPC
  framing; and the ELB docs state it outright for gRPC target groups: *"The only supported target
  types are `instance` and `ip`. … You can't use Lambda functions as targets."* ✅ Needs no streaming.
- **11.1 Service discovery:** Cloud Map DNS → the failure → the fix → Service Connect → ALB.
- **11.2 The headline bug.** grpc-go defaults to **`pick_first`**: even when `dns:///` resolves 3
  task IPs, one task takes 100% of traffic. Fix is two parts —
  client `grpc.WithDefaultServiceConfig('{"loadBalancingConfig":[{"round_robin":{}}]}')`, and
  server `keepalive.ServerParameters{MaxConnectionAge: 30s, MaxConnectionAgeGrace: 5s}` so clients
  re-resolve after a scale-out. For ALB, set
  `load_balancing.algorithm.type = least_outstanding_requests` — with multiplexed HTTP/2, connection
  counts say nothing about task load.
- **11.3 Health checks** at three layers: `grpc.health.v1`, container healthCheck, target group.
  ALB gRPC needs a custom health check method `/package.service/method` plus healthy status codes. ✅
- **11.4 Graceful shutdown** wired to `stopTimeout` and deregistration delay.
- **11.5 Observability**: Jaeger locally, ADOT → X-Ray on AWS.
- **11.6 `buf breaking`** rejecting a renamed field — the strongest argument for Protobuf, and
  impossible to show with raw `protoc`.
- **11.7 Stateless vs stateful on ECS** — §6.4, delivered by killing the `paymentd` task.

### How the laptop reaches the AWS deployment

ALB is blocked on ACM and sits above the cut line, so `make demo AWS=1` needs another path.
**Decision: `paymentd` runs in a public subnet with `assign_public_ip = true` and a security group
allowing :50052 from the speaker's IP only.** `identityd` stays private — only `paymentd` calls it,
over Cloud Map. This also avoids a NAT Gateway (§ cost guardrails). The alternative, running
`demo-client` as a one-off `run-task` in-VPC, is more faithful but gives no live terminal output,
so it is the fallback if the venue IP is unpredictable.

### ⚠️ Blocker to resolve before the talk

ALB gRPC target groups **require an HTTPS listener** ✅ → ACM certificate → a domain. Without one,
cut external gRPC ingress and keep traffic internal. Needs an answer early, not on Friday.

### Cost guardrails

**No NAT Gateway** (~$32/mo + data — the #1 demo-account bill killer): public subnets with
`assign_public_ip`, or VPC endpoints for ECR/Secrets/Logs. Fargate Spot, `desired_count` 1 except
`identityd`=3 during §11.2. `make tf-aws-destroy` before leaving the venue.

---

## 11.8 Proving it is really ECS (`make ps`)

The audience's fair question is "how is that different from docker compose?"
`scripts/ps.sh` answers it in seven escalating steps, and the last three are the
convincing ones:

| # | Shows | Why it convinces |
|---|---|---|
| 1 | `describe-services`: desired / running / pending | there is a control plane reconciling state |
| 2 | tasks with the task-definition **revision** | deploys are immutable revisions, not restarts |
| 3 | `networkMode: awsvpc`, `FARGATE`, `ARM64`, execution role | this is a Fargate task definition |
| 4 | the task **self-describing** via `ECS_CONTAINER_METADATA_URI_V4` | nothing in our code sets this; the platform does |
| 5 | `AWS_CONTAINER_CREDENTIALS_FULL_URI` | **this is how task roles deliver credentials** — no key anywhere |
| 6 | `secrets[].valueFrom` is an ARN | the secret never entered git or the image |
| 7 | login as each seeded user | the baked database is identical on every task |

Step 4 is the one to linger on. Verified output:

```
Cluster  : arn:aws:ecs:us-east-1:000000000000:cluster/payments-local
TaskARN  : arn:aws:ecs:us-east-1:000000000000:task/payments-local/6cd5fea5-...
Family   : identityd rev 1
AZ       : us-east-1a
```

Step 5 is worth a sentence too: the AWS SDK picks those two variables up on its
own. That is the whole "no API keys on ECS" story in one `docker inspect`.

**Emulator fidelity — say this out loud, do not hope nobody reads the table.**
`healthStatus` comes back `UNKNOWN` and `LaunchType` as `None`/`EC2`, because
Ministack does not echo those back even though the task definition requests
`FARGATE` and the service uses the FARGATE capacity provider. Real ECS reports
`HEALTHY` and `FARGATE`. Naming the emulator's limits yourself is more credible
than being caught by them, and it is precisely why the talk also deploys to AWS.

## 12. Stage runbook

```bash
make local-up          # ministack + jaeger (+ make llm-up for real LLM text)
make tf-local-apply    # terraform apply → ECS tasks on the emulator
make dns               # Cloud Map alias shim
make ps                # PROVE it is ECS: control plane, task metadata, task role
make demo              # login → approved + declined authorize + explanation
make ts-demo           # the SAME flow from TypeScript  ← polyglot segment
make demo-load         # sustained Authorize; watch per-task metrics   ← §11.2
make scale N=3         # identityd 1→3: show the failure, then the fix
make tf-aws-plan       # same modules, no endpoints block              ← the money slide
make demo AWS=1        # identical client, real Fargate, real Bedrock
make tf-aws-destroy
```

### The TypeScript segment (~60 seconds, confirmed in scope)

`clients/node` is generated by the **same `buf generate`** that produces the Go stubs — one extra
plugin in `buf.gen.yaml`, nothing hand-written. It exists because "a typed contract with free
polyglot codegen" is the reason real projects adopt gRPC (§14.3), and claiming that is weaker than
showing it.

What to say while `make ts-demo` runs:

1. "Same `.proto`. I added one plugin. I wrote no types."
2. `int64` → Go `int64`, TypeScript **`bigint`** — because a JS `number` cannot hold int64 safely.
   The generator gets that right so you cannot silently truncate a timestamp. Hand-written clients
   get this wrong constantly.
3. Status codes survive the language boundary: `AlreadyExists`, `Unauthenticated`,
   `InvalidArgument` — so the client branches on a code, not on a message string.
4. The browser caveat, which sets up §11.0: this is **real gRPC over HTTP/2**, and it works because
   Node can open an HTTP/2 connection and send trailers. A browser cannot — hence gRPC-Web and
   Connect. One line (`createConnectTransport`) and the same generated types run in a browser. Not
   one of the 14 surveyed projects exposes gRPC publicly.

Verified output (2026-10-08):

```
approved     -> ₹1200.00 APPROVED
idempotent   -> same transaction (replay=true)
declined     -> ₹45000.00 DECLINED LIMIT_EXCEEDED
explanation  -> "Declined: ₹45000.00 exceeds your per-transaction limit of ₹25000.00." [template]
duplicate    -> AlreadyExists
bad token    -> Unauthenticated
```

**Rules:** never `terraform apply` from scratch on stage — pre-provision and demo a *delta*. Pin
every image tag. Pre-pull images the morning of. Keep a screen recording and a saved
`terraform plan` as insurance.

---

## 13. Acceptance criteria

1. `git clone && go build ./...` succeeds on a clean machine with only Go 1.27 installed.
2. `go test ./...` passes offline.
3. `make local-up && make tf-local-apply && make dns && make demo` yields an approved **and** a
   declined authorization, with the decline explained in prose.
4. `make demo-load` (authenticating as a **seeded** user — see §6.4) with `identityd` at 3 tasks
   shows traffic on **one** task before the fix and spread across all three after, visible in
   Prometheus via service discovery. Requires `make dns` to have aliased **every** identityd task,
   not just the first.
5. Killing the `identityd` task mid-load drops **zero** in-flight RPCs (graceful shutdown works).
6. `terraform plan` in `envs/aws` succeeds, and its module set is byte-identical to `envs/local`.
7. One Jaeger trace shows `paymentd.Authorize` → `identityd.VerifyToken` as parent/child spans.
8. `buf breaking` fails on a renamed proto field.

---

## 14. Verified evidence

### 14.1 Ministack Step 0 spike — PASSED (2026-10-06)

Distroless ARM64 Go 1.27 / grpc-go 1.84.0 binary, Terraform 1.14.5 / AWS provider 6.67.0:

| Check | Result |
|---|---|
| ECR repo via Terraform + `docker push` | ✅ pushes to `localhost:4566/<repo>` |
| ECS starts a real container from `FARGATE` + `awsvpc` + `ARM64` task def | ✅ task `RUNNING` |
| gRPC reflection + `grpc.health.v1` | ✅ `{"status":"SERVING"}` |
| RDS module → Postgres reachable from a task | ✅ PostgreSQL 16.14 (now unused — §6) |
| Task → `jaeger:4317` | ✅ via `DOCKER_NETWORK` |
| Cloud Map DNS between tasks | ❌ natively; ✅ via Docker-alias shim |
| Bedrock `Converse` | ✅ mock reply + token usage |

Gotcha found and fixed: publishing the RDS port range on the ministack container makes
`CreateDBInstance` fail with `port is already allocated` and then silently report a dead endpoint.

### 14.1b Terraform -> Ministack ECS, end to end — PASSED (2026-10-08)

`terraform apply` in `envs/local` creates VPC, subnets, security group, ECR
repos, ECS cluster with FARGATE + FARGATE_SPOT, Cloud Map namespace, log group,
Secrets Manager secret, IAM roles, and both ECS services. Tasks start, and
`scripts/smoke.sh` passes against them: approved, idempotent replay, over-limit
decline with a rendered explanation, blocked-category decline, and an
unauthenticated call rejected through the identityd hop.

**Secrets Manager injection works on the emulator.** identityd requires
`JWT_SECRET` and refuses to start without it, so the task reaching SERVING is
proof that `secrets[].valueFrom` resolved.

Four real bugs this deployment caught that no local run would have:

1. **`count` cannot depend on a resource attribute.** `count = var.namespace_id
   == "" ? 0 : 1` fails with "Invalid count argument" because the namespace does
   not exist at plan time. Replaced with a statically-known
   `enable_service_discovery` flag.
2. **OTel semconv version mismatch crash-looped both tasks.**
   `resource.Merge(resource.Default(), ...)` rejects a resource whose schema URL
   differs from the SDK's, so importing `semconv/v1.37.0` against otel sdk
   v1.47.0 (which uses v1.43.0) is fatal. **It is invisible locally**, because
   with no `OTEL_EXPORTER_OTLP_ENDPOINT` the tracer short-circuits to a no-op and
   never builds a resource. Fixed, and pinned by a regression test that passes an
   endpoint.
3. **Ministack cannot create Cloud Map services via Terraform.** Its
   `CreateService` requires a top-level `NamespaceId`; the AWS provider sends it
   nested in `DnsConfig`, which is what real AWS accepts. Confirmed by calling
   both shapes directly. So `enable_service_discovery = false` locally and the
   `make dns` alias shim stands in; `true` on AWS.
4. **`docker ps` ORs multiple `--filter name=` values.** The first `make dns`
   therefore aliased *paymentd's* container as `identityd.ecom.local` — a silent
   misroute that would have broken the stage demo in a baffling way. Fixed with
   one anchored regex filter, plus a force-disconnect pass, because
   `docker network rm` fails while containers are attached and left stale
   aliases behind.

Lesson for the talk, worth saying out loud: items 2 and 4 were only findable by
actually deploying. Neither unit tests nor `terraform validate` would have
surfaced them.

### 14.2 SQLite pure-Go static build (2026-10-07)

```
$ CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags probe ./internal/sqlitecheck
$ file probe → ELF 64-bit LSB executable, ARM aarch64, statically linked
$ go run  → OK pure-go sqlite=3.53.4 row="probe"
```

### 14.3 When gRPC makes sense — measured from 14 projects' real `.proto` files

Temporal 121 RPCs / **0** streaming · Milvus 154 / 2 · TiKV 73 / 9 · Dapr 74 / 6 · k8s CRI 43 / 7 ·
etcd 42 / 4 · Qdrant 30 / **0** · CockroachDB 28 / 10 · containerd 17 / **0** · Bazel RE 13 / 5 ·
Vitess 9 / 4 · Thanos 4 / 1 · **Envoy xDS 2 / 2 (100%)** · **OTLP 1 / 0**.

Conclusion: gRPC is chosen for a **typed, versioned, polyglot contract on internal traffic**;
streaming is a capability for specific cases (config push, watch, blob transfer). Not one of the 14
is a public consumer API.

---

## 14.4 Note on `docs/PLAN.md`

That file is the working document from the design phase and is **superseded by this spec** wherever
they disagree — in particular it still names RDS as the database and carries the pre-SQLite day
plan. It is kept only for the research trail; `docs/evidence/` holds the 14 projects' protos.

---

## 15. Open questions — need answers before/while building

1. **Talk date.** The day plan assumes 5 working days. A Saturday talk leaves 3, which means cutting
   to the floor: *both services on Ministack ECS via Terraform + one service on real Fargate*, with
   ALB, Secrets, observability and §11.2 as stretch.
2. **Domain + ACM cert** for the ALB gRPC listener — or agree now to cut external ingress.
3. ~~Bedrock model access~~ — **RESOLVED 2026-10-08: there is no Bedrock access on the ECS
   deployment.** See §6.8. The explanation provider defaults to `template` everywhere; the Bedrock
   client stays in the repo as reference code and is reachable via an endpoint override. No IAM
   permission, no model opt-in, no region constraint, no cost.
4. **Repo name** — `grpc-ecs-demo` used throughout; one `git mv` + module rename to change.
5. **Push to GitHub?** Public repo for the audience to clone, and under which account.
