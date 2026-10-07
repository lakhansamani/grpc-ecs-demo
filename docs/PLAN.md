> **SUPERSEDED by `../SPEC.md`** wherever the two disagree. Kept for the research trail; it still
> names RDS as the database and carries the pre-SQLite day plan.


# Go gRPC on ECS — talk rebuild plan

**Talk:** Running Go gRPC Services on ECS: From LocalStack to Production Cloud
**Speaker:** Lakhan Samani · AWS Community Day Vadodara · week of 2026-10-06
**Repo today:** `apis` + `userd` + `orderd` + `k8s` (kind) + `docker` (prom/alertmanager)
**Target:** monorepo, `identityd` + `paymentd` (all unary), Terraform that runs locally *and* on AWS, no Kubernetes.

---

## 1. What the current repo does (read, not assumed)

### How the gRPC APIs are defined

`apis/` is a standalone Go module (`github.com/lakhansamani/ecom-grpc-apis`) holding only `.proto`
files and generated code. Two packages, versioned by directory:

- `apis/user/v1/user.proto` → `service UserService { Register, Login, Me }`
- `apis/order/v1/order.proto` → `service OrderService { CreateOrder, GetOrder }`

Generation is raw `protoc` in `apis/Makefile`, with `paths=source_relative` and
`require_unimplemented_servers=false`. Every RPC is **unary** — there is no streaming anywhere in
the current demo.

### How the services are connected

```
client ──gRPC :50051──> userd ──> postgres (userdb)
                          ▲
                          │ gRPC Me() with forwarded "authorization" metadata
                          │
client ──gRPC :50052──> orderd ──> postgres (orderdb)
                     (both ──OTLP :4317──> jaeger,  /metrics :9091/:9092 ──> prometheus)
```

The auth pattern is the interesting part and worth keeping on stage:

1. `userd.Login` issues an HS256 JWT (`userd/utils/jwt.go`).
2. Client calls `orderd.CreateOrder` with `authorization: Bearer <jwt>` in gRPC metadata.
3. `orderd.authorize()` (`orderd/service/service.go:55`) reads that metadata, re-attaches it with
   `metadata.AppendToOutgoingContext`, and calls `userd.Me()` over gRPC to resolve the user ID.
4. `userd.Me` parses the token itself and loads the user.

So `orderd` never validates a JWT — it delegates to `userd`. That is a real microservice trust
boundary and a genuine second network hop, which is exactly what makes service discovery and load
balancing demo-able. Keep this shape.

Wiring style is clean and worth preserving: each service has `service.Config` /
`service.Dependencies` structs, a `db.Provider` interface, and `New(cfg, deps)` — constructor
injection, no globals beyond metrics. `orderd` builds its client with `grpc.NewClient` (correct,
modern) plus OTel stats handler and Prometheus client interceptors.

---

## 2. Defects verified in the current code

These are evidence for the "write it better" half of the talk — each one is a real bug, not style.

| # | Where | Problem |
|---|-------|---------|
| 1 | `userd/db/db.go:25` | `gorm.Open(..., &gorm.Config{})` omits `TranslateError: true`. GORM only returns `ErrDuplicatedKey` when that flag is on, so the `errors.Is(err, gorm.ErrDuplicatedKey)` branch in `userd/service/register.go:40` is **dead code**. Duplicate email returns a raw pgx error and the `user_exists` metric never increments. |
| 2 | all handlers | Errors are `errors.New(...)`, so every failure reaches the client as `codes.Unknown`. "user not found", "invalid password" and "unauthorized" are indistinguishable from a panic. Must be `status.Error(codes.NotFound/Unauthenticated/PermissionDenied, ...)`. |
| 3 | `userd/utils/jwt.go` | `github.com/golang-jwt/jwt` v3.2.2+incompatible — unmaintained line (CVE-2020-26160). `jwt.Parse` never checks the signing method, so a token with `alg` swapped is accepted → algorithm-confusion. Both `!ok` and `!token.Valid` branches `return "", err` where `err` is **nil** — an invalid token returns no error. `claims["user_id"].(string)` is an unchecked assertion that panics on a malformed token. |
| 4 | `userd/db/user.go:31` | `BeforeSave` comment says "hash only if it's changed" but it hashes unconditionally. GORM runs `BeforeSave` on updates too, so any future user update re-bcrypts the stored hash and locks the account out. |
| 5 | `orderd/service/metrics.go` | `ordersFetchedMetrics` is labelled with the **order ID**. Unbounded label cardinality — this is the classic way to kill a Prometheus server. |
| 6 | `orderd/main.go:39` | `grpcClientMetrics` is created but never `prometheus.MustRegister`-ed, so all outbound client metrics are silently dropped. |
| 7 | both `main.go` | No signal handling and no `server.GracefulStop()`. On ECS, a deploy sends SIGTERM and then SIGKILLs after `stopTimeout`; in-flight RPCs are cut. **This is the single most ECS-relevant bug in the repo.** |
| 8 | both services | No `grpc.health.v1.Health` service. ECS/ALB target health and `grpc-health-probe` both need it. |
| 9 | both `db.go` | `db.AutoMigrate(...)` return value ignored — a failed migration starts a broken service. Also, auto-migrate on boot races when ECS starts 2+ tasks at once. |
| 10 | both `main.go` | `semconv/v1.7.0` (very old), `tracerProvider.Shutdown` error dropped, and the `defer` never runs because `server.Serve` blocks until `log.Fatalf` — traces are lost on exit. |
| 11 | `apis` | `require_unimplemented_servers=false` removes the compile-time safety net that catches a service missing a newly-added RPC. |
| 12 | module drift | `userd` pins `ecom-grpc-apis v0.2.0`, `orderd` pins `v0.3.0`. The two services compile against **different versions of the shared contract**. |
| 13 | Dockerfiles | `FROM alpine:latest` (unpinned), `GOARCH=amd64` hardcoded, runs as root, no `ca-certificates` in a scratch/distroless layer, no healthcheck. amd64 also means QEMU emulation on your M-series laptop and misses Graviton's ~20% cost saving. |
| 14 | `.env` committed | `JWT_SECRET=secret` in `userd/.env`, in-tree. Becomes the Secrets Manager / SSM demo. |

---

## 3. The LocalStack problem — and the answer to your question

**You asked: can we just use an older LocalStack image?**

No. I checked the open-source tree directly rather than trusting the docs or blog posts, listing
`localstack/services/` at four historical tags:

| Tag | `ecs` | `elbv2` | `servicediscovery` | `ecr` |
|-----|-------|---------|--------------------|-------|
| v1.4.0 | ✗ | ✗ | ✗ | ✗ |
| v2.3.2 | ✗ | ✗ | ✗ | ✗ |
| v3.8.1 | ✗ | ✗ | ✗ | ✓ (CFN resource plumbing only) |
| v4.0.0 | ✗ | ✗ | ✗ | ✓ (CFN resource plumbing only) |

ECS, ELBv2 and Cloud Map were **never** in the free LocalStack image — they have always been Pro.
Rolling back gains nothing. And since **23 Mar 2026** LocalStack retired the Community edition
entirely: the single image is now licence-gated and even the free path needs an auth token.

**You then asked: or can I just create a Hobby account?** You can — it is free — but it does not
buy this talk. Checked against the per-service tier table in the LocalStack docs:

| Service this talk needs | Hobby (free) | Base (~$39/mo) | Ultimate |
|---|---|---|---|
| **ECS** | ❌ | ✅ | ✅ |
| **ECR** | ❌ | ✅ | ✅ |
| **ELB / ALB** | ❌ | ✅ | ✅ |
| **Cloud Map** (servicediscovery) | ❌ | ❌ | ✅ |
| **RDS** | ❌ | ✅ | ✅ |
| **Bedrock / Bedrock Runtime** | ❌ | ❌ | ✅ |
| Secrets Manager, SSM, S3, SQS, Logs, IAM | ✅ | ✅ | ✅ |

So Hobby gives you Secrets Manager and SSM — the two *least* interesting things in the demo — and
none of ECS, ECR, Cloud Map, RDS or Bedrock. Worse, **even paying $39/mo for Base still gets you no
Cloud Map and no Bedrock**; both need Ultimate. There is no LocalStack tier under Ultimate that can
run this talk's local path.

Verdict: create the Hobby account anyway (it is free, 10 minutes, and makes a fine backup for the
Secrets Manager slide), but do not build the demo on it.

### The replacement: Ministack

The community forked around the paywall. **Ministack** (`ministackorg/ministack`, MIT, ~4.8k stars,
pushed today) emulates what this talk actually needs, free:

- `ecs.py` — **3,563 lines that really run containers via Docker.** Not an API-shaped stub: it has
  a rollout state machine, health-check grace windows, task networks, port bindings, volume mounts
  from `mountPoints`, and per-task platform selection.
- `ecs_metadata.py` — emulates `ECS_CONTAINER_METADATA_URI_V4`, so code that reads task metadata
  behaves as it does on Fargate.
- `ecr.py`, `alb.py`, `servicediscovery.py` (Cloud Map, backed by Route53 hosted zones),
  `rds.py`, `secretsmanager.py`, `ssm.py`, `s3.py`.
- `rds.py` — **starts real Postgres containers** (`postgres:16-alpine`, image picked from the
  requested engine version), exposed on host ports from `RDS_BASE_PORT` (default 15432). So the
  Terraform `rds` module really does apply locally.
- `bedrock_runtime.py` — supports `Converse`, `ConverseStream`, `InvokeModel` and
  `InvokeModelWithResponseStream` with real eventstream encoding, deterministic canned responses,
  and an optional `MINISTACK_BEDROCK_PROXY_URL` to point at Ollama or real Bedrock. Verified that
  the deterministic path emits a proper
  `messageStart → contentBlockDelta* → contentBlockStop → messageStop → metadata` sequence and
  deliberately splits text into ~5 deltas "so streaming consumers see multiple events" — so the
  live-token visual works offline with no model at all.
- Confirmed it handles the **real Fargate task-def shape**, not just EC2-style: `FARGATE` and
  `FARGATE_SPOT` launch types, `requiresCompatibilities`, `networkMode: awsvpc`, and
  `runtimePlatform.cpuArchitecture: ARM64`.
- Gateway is on **port 4566** (same as LocalStack, so endpoint overrides port-match), and it needs a
  **Redis** sidecar plus the Docker socket mounted.

That last one is what makes the AI example free to demo — note that Bedrock is **Ultimate-only** on
LocalStack, so Ministack covers more of this talk for $0 than LocalStack does for $39/mo. Floci
(`floci-io/floci`, ~26k stars) is the other active fork; it's Java and I did not confirm its ECS
depth, so Ministack is the pick.

**One Postgres, not two.** Because `rds.py` starts real Postgres containers, the Terraform `rds`
module applies in both envs and `compose.yaml` must **not** also run a `postgres` service — that
would give you two databases and an asterisk on the "same modules, two targets" slide. Compose runs
only `ministack` + `redis` + `jaeger` + `prometheus`; Postgres comes from `terraform apply`.

**The networking seam to watch.** Ministack launches ECS tasks and RDS containers on the Docker
network named by its `DOCKER_NETWORK` env var, while Jaeger and Prometheus live on the compose
network. Set `DOCKER_NETWORK` to the compose network so everything shares one, otherwise a task
cannot reach Jaeger on `jaeger:4317` and falls back to the Docker-Desktop-only
`host.docker.internal`. Pin this down in Step 0.

**Known gap:** ECS reads `serviceRegistries` and `loadBalancers`/`targetGroupArn`, and clusters
accept `serviceConnectDefaults`, but I found **no full `serviceConnectConfiguration` + Envoy sidecar
emulation** — expected, since that would mean shipping Envoy. So *in-container DNS resolution of
`identityd.ecom.local` is the one thing I will not promise before testing it.* Hence Step 0 below.

**Decision: Ministack as primary, with a documented fallback.** This is a strict upgrade over the
original premise and it is *more* topical, not less — "LocalStack just paywalled ECS; here is what
the community built, and here is how to keep your Terraform portable either way" is a better talk
than a vendor walkthrough. The fallback (compose for compute, emulator for dependencies only) is a
slide you keep in your pocket, because Terraform modules are identical in both.

---

## 4. Target architecture: `identityd` + `paymentd` (BFSI, **all unary**)

**Decision: no streaming in the core demo.** Driven by the evidence in §7.5 — streaming is the
minority in almost every real gRPC project — and by the §7.0 finding that the "why ECS, not Lambda"
argument does not need streaming at all. It is also the lowest-risk build, because your existing
`userd`/`orderd` code is *already* all-unary: this becomes "modernise, fix, deploy" rather than
"write a new streaming service." One streaming RPC sits above the cut line as a bonus.

```
                     ┌──────────────────────────────────────────────┐
  client ──unary────>│  paymentd ──> RDS postgres (payments, rules) │
   Authorize         │      │                                       │
                     │      ├──> identityd  (VerifyToken, gRPC hop) │
                     │      │        └──> RDS postgres (identitydb)  │
                     │      └──> Bedrock Converse (explain decline)  │
                     └──────────────────────────────────────────────┘
        both ──OTLP──> Jaeger/ADOT  ·  /metrics ──> Prometheus
```

### Proto contract

`proto/identity/v1/identity.proto` — the existing user service, renamed and corrected:

```protobuf
service IdentityService {
  rpc Register    (RegisterRequest)    returns (RegisterResponse);
  rpc Login       (LoginRequest)       returns (LoginResponse);
  rpc VerifyToken (VerifyTokenRequest) returns (VerifyTokenResponse); // was Me()
}
```

`proto/payment/v1/payment.proto` — every RPC unary, on a hard latency budget:

```protobuf
service PaymentService {
  // THE hot path. Card networks give you a few hundred ms, end to end.
  rpc Authorize       (AuthorizeRequest)       returns (AuthorizeResponse);
  rpc GetTransaction  (GetTransactionRequest)  returns (GetTransactionResponse);
  rpc ListTransactions(ListTransactionsRequest) returns (ListTransactionsResponse);
  // Rules decline; Bedrock only explains the decline that already happened.
  rpc ExplainDecision (ExplainDecisionRequest) returns (ExplainDecisionResponse);
}
```

### What `Authorize` means here — say this on stage, the term is overloaded

Card payments have three distinct steps that people routinely conflate:

| Step | What happens | Money moves? |
|---|---|---|
| **Authorization** | Issuer checks validity, funds and fraud, places a **hold**, returns approve/decline | No |
| **Capture** | Merchant says "take it" | Yes |
| **Settlement** | Batch transfer between banks | Already moved |

`paymentd.Authorize` is **step 1 only**, and `paymentd` sits on the **issuer side**: it *makes* the
decision about its own customer (velocity, per-transaction limit, merchant category, available
balance). **It does not relay to a gateway** — there is no Razorpay/Stripe/PSP call anywhere in the
demo. This is the service a bank or NBFC runs.

That is a deliberate scoping choice, for three reasons:

1. **Self-contained and offline** — no third-party API, no sandbox account, no key, nothing to
   rate-limit you on conference wifi.
2. **The latency budget stays yours.** §7.1 only works if `Authorize` has a real internal budget.
   If `paymentd` merely forwarded to an external PSP, that call would dominate the latency and the
   load-balancing lesson would be invisible underneath it.
3. **It is the shortest path from the existing code** — `orderd` already validates and writes a
   row; this is a rules table away.

**Cut-line variant:** making `paymentd` an acquirer/gateway that calls an external PSP has exactly
one merit — an outbound call forces the question *how does a Fargate task in a private subnet reach
the internet?*, which turns the NAT Gateway cost trap (§6) into a live demo instead of a warning.
It costs a stub PSP service and a failure mode that teaches nothing about gRPC or ECS, so build it
only if Days 1-5 finish early.

### Why this works without streaming

| Talk lesson | Needs streaming? |
|---|---|
| Lambda cannot serve gRPC at all (§7.0) | **No** — stronger without it |
| Sticky HTTP/2 connection → one hot task (§7.1) | **No** — it is a *connection*-level bug that hits unary hardest |
| `MaxConnectionAge` so scale-out is discovered | **No** — mainly a unary concern |
| Cloud Map / Service Connect discovery | No |
| Graceful shutdown of in-flight RPCs | No |
| `grpc.health.v1` + ALB health checks | No |
| OTel traces across the service hop | No |
| ALB gRPC target group, HTTPS listener, ACM | No |

Every lesson in §7 survives. The only thing streaming bought was a livelier stage visual, and §7.0
more than pays for it.

### The latency budget is the "why gRPC" story

`Authorize` fans out — verify token (gRPC hop to `identityd`), velocity check, rules evaluation,
persist — inside a few hundred milliseconds. That makes the §7.1 load-balancing failure
**consequential**: when all traffic pins to one task, the p99 breaches and the payment *times out*.
Not a graph; a declined transaction.

### The AI element, kept honest

Deterministic rules decide: velocity, amount limits, merchant category, balance. **Bedrock never
approves or declines** — it only explains the decision the rules already made ("declined because
this is the 4th attempt in 10 minutes and ₹45,000 exceeds your ₹25,000 per-transaction limit").
That is the correct architecture for anything a customer can dispute or a regulator can audit, and
it is a one-shot unary `Converse` call — no streaming needed. Local = Ministack's mock Bedrock;
AWS = real Bedrock via the **task IAM role**, so no key enters the repo.

### Bedrock locally: available, with one caveat worth knowing

Ministack **does** emulate `bedrock-runtime` — `Converse`, `ConverseStream`, `InvokeModel` and
`InvokeModelWithResponseStream` — verified live on 2026-10-06. The API shape is real: correct
request/response fields, token usage, proper eventstream framing. **The generated text is a
deterministic canned reply**, so a local decline would read
`[ministack mock nova ...] reply for prompt#6d5f6d46` rather than a sentence.

Three ways to handle it, in order of preference:

1. **Point it at Ollama (recommended, wired up in `compose.yaml`).**
   `MINISTACK_BEDROCK_PROXY_URL` accepts any OpenAI-compatible `/chat/completions` endpoint;
   Ministack translates Converse → OpenAI → back to Converse. You get real LLM prose offline, for
   free, **through the real AWS SDK code path** — the same `bedrock-runtime` client that talks to
   real Bedrock on AWS, so nothing is special-cased. `make llm-up` starts it and pulls
   `llama3.2:1b` (small, fast, plenty for one explanatory sentence). It is a compose `profile`, so
   `make local-up` stays quick for anyone who does not want the model download.
   **The demo-safe property:** if Ollama is unreachable, Ministack falls back to the mock
   *silently* rather than erroring. Your demo degrades to a placeholder instead of crashing.
2. **Accept the mock locally** and show the real sentence only in the AWS run. Honest, zero setup,
   and it makes a fair point: the local emulator is for exercising the *wiring*, not the model.
3. Do not hard-code a canned explanation in your own `internal/llm`; that bypasses the SDK path and
   proves nothing about the AWS integration.

Note that token counts from the mock are a heuristic (`chars/4`), so do not build a cost slide on
local numbers.

Scope discipline: no card network integration, no settlement, no ledger. Rules are a small
table-driven evaluator.

## 5. Repo layout (one Go module, monorepo)

```
ecom-grpc/                        # rename later to grpc-ecs-demo if you like
├── go.mod                        # ONE module — clone once, `go build ./...` just works
├── buf.yaml  buf.gen.yaml  buf.lock
├── proto/
│   ├── identity/v1/identity.proto
│   └── payment/v1/payment.proto
├── gen/go/...                    # generated, committed (audience does not need buf)
├── build/                        # Dockerfile per service (shared base, TARGETARCH)
├── cmd/
│   ├── identityd/                # main.go
│   ├── paymentd/                 # main.go
│   └── demo-client/              # `feed` + `trader` modes = the stage visual
├── internal/
│   ├── identity/                 # service + db for identityd
│   ├── payment/                  # service + db + rules + llm (Bedrock behind an iface)
│   └── platform/                 # shared across both services
│       ├── grpcserver/           # server build + health + graceful shutdown + interceptors
│       ├── observability/        # OTel + Prometheus setup, one function per concern
│       └── config/               # env parsing, fail fast with all errors at once
├── terraform/
│   ├── modules/                  # network, ecr, ecs-cluster, ecs-service, rds, secrets, alb
│   └── envs/{local,aws}/         # same modules, two provider configs
├── compose.yaml                  # ministack + redis + jaeger + prometheus (NO postgres)
├── Makefile                      # the only interface you type on stage
└── docs/DEMO.md                  # the runbook in §9
```

**One Go module, not a `go.work` workspace.** You picked "monorepo" for the reason that a clone
should just build — and a single module delivers that better than a workspace does: `git clone &&
go build ./...` with no `go.work`, no `replace` directives, and no chance of the two binaries
compiling against different versions of the contract (defect #12 becomes structurally impossible).
A workspace only earns its complexity when modules are released on independent version timelines,
which is the opposite of what a talk repo wants. `internal/platform/` then kills the duplication
between the two `main.go` files (the OTel block is currently copy-pasted verbatim), and `internal/`
is enforceable precisely because it is all one module.

### Toolchain (all versions confirmed against the module proxy / registry today)

| Thing | Now | Target |
|---|---|---|
| Go | 1.23.1 | **1.27** (`go 1.27.1` locally; single `go.mod`, `toolchain go1.27.1`) |
| grpc-go | v1.71.0 | **v1.84.0** |
| protobuf-go | v1.36.5 | **v1.36.12** |
| protoc-gen-go-grpc | — | **v1.6.2** |
| golang-jwt | v3.2.2+incompatible | **v5.3.1** |
| gorm | v1.25.12 / pg v1.5.11 | **v1.31.2 / v1.6.3** |
| otel | v1.35.0 | **v1.47.0** (otelgrpc **v0.72.0**) |
| prometheus/client_golang | v1.21.1 / **v1.14.0** in orderd | **v1.24.1** (unify) |
| aws-sdk-go-v2 | — | **v1.47.1** |
| codegen | raw `protoc` 27.3 | **buf** (already installed) with pinned remote plugins |
| Terraform | — | **1.14.5** CLI, **AWS provider 6.67.0** |
| Base image | `alpine:latest` | pinned `distroless/static` + `TARGETARCH`, arm64/Graviton |

buf replaces the Makefile `protoc` lines and gets you `buf lint` + **`buf breaking`** — a 15-second
stage moment where you rename a proto field and CI rejects it. That is the single best argument for
Protobuf in the whole talk, and raw `protoc` cannot make it.

---

## 6. Terraform: one module set, two targets

The whole local-to-cloud claim lives or dies on *not* having two copies of the infra code.

`terraform/envs/local/provider.tf` — hand-written endpoint overrides, no wrapper script, because on
stage you want the audience to *see* why this works:

```hcl
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    ecs = "http://localhost:4566"   # every service → the emulator
    ecr = "http://localhost:4566"
    # ... rds, secretsmanager, ssm, servicediscovery, elbv2, logs, iam, sts
  }
}
```

`terraform/envs/aws/provider.tf` is the same provider block **minus** `endpoints` and the skips.
Modules are byte-identical. That contrast is your core slide.

Modules: `network` (VPC, subnets, SGs, VPC endpoints), `ecr`, `ecs-cluster`, `ecs-service`
(parameterised, instantiated twice), `rds`, `secrets`, `alb`, `observability`.

**Cost guardrails, deliberate:**
- **No NAT Gateway.** ~$32/mo plus data is the #1 way a demo account bleeds money. Use public
  subnets with `assign_public_ip = true`, or VPC endpoints for ECR/Secrets/Logs. Say this out loud
  on stage — the audience will recognise their own bill.
- Fargate Spot for the demo services; `desired_count = 1` by default.
- RDS `db.t4g.micro`, single-AZ, `skip_final_snapshot = true`, 7-day backups off.
- `terraform destroy` as a documented Makefile target, and **run it before you leave the venue.**

---

## 7. The gRPC-on-ECS content that is actually worth the slot

This is the technical heart, and none of it exists in the repo today.

### 7.0 Why ECS at all? Because Lambda structurally cannot serve gRPC

Open with this. It is the cleanest justification for the whole talk and it needs no streaming —
verified against primary AWS documentation, not blog posts:

- **Lambda cannot run a listening server.** It is event-driven; there is no process holding a port.
- **API Gateway REST API does not support gRPC** — it strips the binary framing and headers gRPC
  needs. HTTP API speaks HTTP/2 but is not a gRPC endpoint.
- **ALB gRPC target groups exclude Lambda.** The ELB docs, under "Considerations for the gRPC
  protocol version", state it outright: *"The only supported target types are `instance` and `ip`.
  … You can't use Lambda functions as targets."*

So on AWS, a gRPC service runs on ECS, EKS or EC2 — and between those, ECS is the one that does not
hand you a control plane to operate. **That is the thesis of the talk, and it is true for a 100%
unary service.** The workarounds that exist (gRPC-gateway behind API Gateway, or a Unix-socket shim
inside Lambda) all terminate gRPC at the edge and speak JSON onward — which means you have paid
gRPC's cost and kept none of its benefit.

Two more details worth a line each, both from the same doc: a gRPC target group **requires an HTTPS
listener** (so ACM, so a certificate — see the §7.1 blocker), and you must give it a custom health
check method of the form `/package.service/method` plus the gRPC status codes that count as healthy.

### 7.1 Service discovery: three options, one right answer

Show them in order and let the audience feel the problem:

1. **Cloud Map DNS (`serviceRegistries`)** → `identityd.ecom.local` resolves to task IPs.
   Then demonstrate the failure: scale `identityd` to 3 tasks and watch **100% of traffic pin to
   one task**. gRPC opens one HTTP/2 connection and multiplexes every RPC over it, so DNS-level
   round-robin never gets consulted again. Prometheus per-task counters make this visible on screen.
2. **The fix, client + server side** — this is the slide people will photograph:
   ```go
   // client: resolve all A records and balance per-RPC, not per-connection
   grpc.NewClient("dns:///authd.ecom.local:50051",
       grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`))

   // server: force clients to re-resolve, so new tasks get traffic after a scale-out
   grpc.KeepaliveParams(keepalive.ServerParameters{
       MaxConnectionAge:      30 * time.Second,
       MaxConnectionAgeGrace: 5 * time.Second,
   })
   ```
   Without `MaxConnectionAge`, a client that connected before a scale-out never discovers the new
   tasks. This is the most common real-world gRPC-on-ECS bug and almost nobody covers it.
3. **ECS Service Connect** (`appProtocol: grpc`) → per-request balancing via the Envoy sidecar, no
   client config. Mention the caveat honestly: there is a long-standing community report of
   Service Connect not distributing gRPC traffic (`aws/amazon-ecs-service-connect-agent#78`,
   filed Apr 2024) — **re-check its current status before you cite it on stage**; with 1–2 tasks it
   will not affect your demo either way. And note that Service Connect is the piece Ministack does
   not emulate, so this one is cloud-only.
4. **ALB with a gRPC target group** for external ingress. Set
   `load_balancing.algorithm.type = least_outstanding_requests` rather than the `round_robin`
   default — with multiplexed HTTP/2 a connection count tells you nothing about how loaded a task
   actually is.
   ⚠️ **Blocker to resolve early:** an ALB gRPC target group requires `protocol_version = "GRPC"`
   *and* an **HTTPS listener**, which means an ACM certificate, which means a domain. No cert → no
   external gRPC ingress demo. Either use a Route53 domain you already own, import a self-signed
   cert into ACM, or cut this section and keep ingress internal-only.

### 7.2 Health checks, three layers
`grpc.health.v1` in-process · `grpc-health-probe` as the task-definition container healthCheck ·
ALB target-group health. Show what breaks when you skip each.

### 7.3 Graceful shutdown, end to end
SIGTERM → stop accepting → drain in-flight streams → `GracefulStop()` with a timeout → exit, wired
to ECS `stopTimeout` and the ALB deregistration delay. Kill a task mid-stream and show the stream
finish cleanly. Defect #7 made visible.

### 7.4 Observability on ECS without Jaeger-in-a-box
Local: Jaeger all-in-one in compose. AWS: ADOT sidecar → X-Ray, or OTLP to a collector service.
Replace the ancient `semconv/v1.7.0` and set `service.name`/`deployment.environment` from task
metadata.

---

### 7.5 Evidence slide: when does gRPC actually make sense?

Measured, not asserted — I pulled the real `.proto` files from 14 well-known projects on 2026-10-06
and counted RPC shapes:

| Project | RPCs | Streaming RPCs |
|---|---|---|
| Temporal | 121 | **none** |
| Milvus | 154 | bidi 1, server 1 |
| TiKV | 73 | bidi 2, client 3, server 4 |
| Dapr | 74 | bidi 4, server 2 |
| Kubernetes CRI | 43 | server 7 |
| etcd | 42 | bidi 2, server 2 |
| Qdrant | 30 | **none** |
| CockroachDB | 28 | bidi 4, server 6 |
| containerd | 17 | **none** |
| Bazel Remote Execution | 13 | client 1, server 4 |
| Vitess | 9 | server 4 |
| Thanos StoreAPI | 4 | server 1 |
| **Envoy xDS (ADS)** | **2** | **bidi 2 — 100%** |
| **OpenTelemetry OTLP** | **1** | **none** |

**The counterintuitive finding, and the honest headline for this slide: streaming is the minority
almost everywhere.** Temporal runs 121 RPCs with zero streaming. Qdrant and containerd: zero.
OpenTelemetry — the project everyone *describes* as "streaming telemetry" — exposes a **single
unary `Export`** RPC. So nobody should leave your talk believing streaming is the reason to pick
gRPC.

The real reason these projects chose gRPC is **a typed, versioned contract with free codegen across
languages**, for traffic that is internal. Streaming is a *capability* you reach for in specific
situations, not the justification.

**The six situations where gRPC genuinely earns its complexity**, each with its witness from the
table above:

1. **Two separately-released binaries must agree on a contract.** Kubernetes CRI is the poster
   child: kubelet and containerd/CRI-O ship on independent release cycles and *must* interoperate.
   43 RPCs, mostly unary. This is the single most common real reason.
2. **Polyglot clients.** Codegen in N languages beats N hand-written SDKs (Dapr, Temporal, Milvus).
3. **Internal, high-volume, latency-sensitive paths.** Database internode traffic, where protobuf
   plus HTTP/2 measurably beats JSON (CockroachDB, TiKV, Vitess, etcd).
4. **Large or numeric payloads.** Vector search ships big float arrays; JSON is the wrong encoding
   (Qdrant, Milvus — and note both are almost entirely unary).
5. **Push / watch semantics.** This is where REST genuinely cannot follow: **Envoy xDS is 2 RPCs and
   both are bidi**, because the control plane pushes config and the proxy acks. `etcd Watch` is the
   same shape. If you need push, you need streaming.
6. **Large blob transfer.** Bazel Remote Execution uses client-streaming upload and server-streaming
   download for build artifacts.

**And when it does not make sense** — worth saying out loud, it buys credibility:

- **Public, third-party-facing APIs.** Stripe, GitHub and Twilio are REST/GraphQL, not gRPC.
  Browsers cannot speak gRPC natively; you need gRPC-web or Connect and a proxy. Note that **not
  one project in the table above is a public consumer API** — every single one is infrastructure.
- **A handful of CRUD endpoints with one consumer.** The codegen toolchain costs more than it saves.
- **When `curl`-ability and human-readable payloads matter more** than throughput.
- **When you want HTTP caching / CDN intermediaries.** gRPC bypasses that whole layer.

**How this frames your own demo, honestly:** the talk shows streaming because it is what REST cannot
do *and* because long-lived connections are what force every interesting ECS lesson — graceful
shutdown, `MaxConnectionAge`, idle timeouts, sticky load balancing. But tell the audience the truth
from this table: their first gRPC service will probably be 90% unary, and that is completely normal
and still the right choice, for reason #1.

## 8. Execution plan (talk is this week — ordered by what you cannot cut)

**Step 0 — the spike. Do this first, today, before anything else. ~45 min.**
Do not build a talk on an untested emulator.
`docker compose up ministack redis` (port 4566, Docker socket mounted, `DOCKER_NETWORK` set to the
compose network). Then `terraform apply` in `envs/local` with a **hello-world gRPC service carrying
the production task-def shape** — `requires_compatibilities = ["FARGATE"]`, `network_mode =
"awsvpc"`, `runtime_platform { cpu_architecture = "ARM64" }`. Do *not* spike with a simplified
EC2-style task def: the whole point is to find out now whether the local and AWS modules can be the
same file, and that is exactly where they would diverge.

Checklist, in order — write down which pass:
1. ECR: `terraform apply` creates the repo, `docker push` succeeds.
2. ECS: `RunTask`/`CreateService` with the Fargate+awsvpc+ARM64 task def actually starts a container.
3. The container is reachable on its gRPC port from the host (`grpcurl`).
4. `rds` module starts a Postgres container **that an ECS task can connect to** (not just the host).
5. A task can reach `jaeger:4317` for traces.
6. **A second task can resolve the first via Cloud Map DNS** (`identityd.ecom.local`).

**Decision gate:** 1–3 pass → full Ministack path, build everything. 4 or 5 fail → a networking
fix (`DOCKER_NETWORK`), not a redesign. 6 fails → keep Terraform + Ministack for
ECS/ECR/RDS/Secrets/Bedrock and inject the peer address as an env var locally while Cloud Map stays
in the AWS env only — one variable differs, the modules do not. *No later step depends on the
outcome, so the rest of this plan is safe either way.*

Already resolved by code inspection, so **not** in Step 0: Fargate/awsvpc/ARM64 support, real
Postgres containers, and multi-delta `ConverseStream` (so the local streaming visual needs no model
and no Ollama).

### Step 0 RESULT — run 2026-10-06, **PASSED**, full Ministack path is go

Executed for real against `ministackorg/ministack:latest` with a distroless **ARM64** Go **1.27** /
grpc-go **v1.84.0** binary and Terraform **1.14.5** / AWS provider **6.67.0**:

| # | Check | Result |
|---|---|---|
| 1 | ECR repo via Terraform + `docker push` | ✅ pushes to `localhost:4566/<repo>` |
| 2 | ECS starts a real container from a `FARGATE` + `awsvpc` + `ARM64` task def | ✅ `ministack-ecs-<task>-<ctr>` running, task `RUNNING` |
| 3 | gRPC reachable, reflection + `grpc.health.v1` | ✅ `{"status":"SERVING"}` |
| 4 | `rds` module → Postgres an ECS task can reach | ✅ real `postgres:16-alpine`, PostgreSQL 16.14 aarch64 |
| 5 | Task can reach `jaeger:4317` | ✅ via `DOCKER_NETWORK=ecom-local` |
| 6 | Cloud Map DNS between tasks | ❌ natively — ✅ with a 1-line shim (below) |
| 7 | Bedrock `Converse` | ✅ returns a mock reply + token usage |

Three findings that change the build, all of which would have cost stage time:

1. **`awsvpc` means no host port binding** — correct AWS behaviour, and it means you **cannot
   `grpcurl` a task from the Mac host**. The demo client must run as a container on the task
   network (`docker run --network ecom-local ...`). Build `cmd/demo-client` as a container from
   day one; do not plan on calling tasks from the host shell.
2. **Do not publish the RDS port range on the ministack container.** Doing so cost 20 minutes:
   `CreateDBInstance` fails with `Bind for 0.0.0.0:15432 failed: port is already allocated`, and
   Ministack silently falls back to reporting `localhost:15432` with no database behind it. Each
   RDS instance binds its own host port. Fixed in `compose.yaml` with a comment.
   With it fixed, the endpoint is a container IP (`172.22.0.4:5432`) — reachable from tasks, which
   is what matters.
3. **Cloud Map stores registrations but serves no DNS.** Verified by reading `ecs.py`:
   `serviceRegistries` is persisted and never acted on — no resolver, no network aliases. The shim
   is one line per service, run after `terraform apply`, and exploits the fact that Cloud Map *is*
   just DNS:
   ```sh
   docker network create ecom-dns
   docker network connect --alias identityd.ecom.local ecom-dns "$TASK_CONTAINER"
   ```
   Verified end to end: `grpcurl -plaintext spike.ecom.local:50051 grpc.health.v1.Health/Check`
   → `SERVING`. On AWS, Cloud Map does this for real and the shim is not applied. This is a good
   30-second stage aside: it shows service discovery is *only* DNS underneath.

Also noted: the deterministic Bedrock reply is a short one-liner, so splitting it into ~5 deltas
makes an underwhelming "tokens streaming in" visual. For the stage, point
`MINISTACK_BEDROCK_PROXY_URL` at a local Ollama so the local stream looks like the cloud one.

Spike artefacts live in `terraform/envs/local/spike.tf` — **delete it** once `ecs-service` is a
real module; it exists only as the evidence above.

**Day 1 — contract + skeleton (must have)**
Monorepo collapsed to one `go.mod`, buf setup, both protos, generated code committed.
`internal/platform/grpcserver` with health + graceful shutdown + interceptors.
Port `userd` → `identityd` and fix defects #1, #2, #3, #4, #9, #10.

**Day 2 — `paymentd` (must have)**
Port `orderd` → `paymentd`: `Authorize`, `GetTransaction`, `ListTransactions`, `ExplainDecision`.
All unary, so there is **no concurrency-sensitive stream state to write** — this is the single
biggest risk the no-streaming decision removes. A table-driven rules evaluator (velocity, amount
limit, merchant category) decides; `internal/llm` wraps Bedrock behind an interface and only
*explains* the decision. Fix defects #5, #6.

`cmd/demo-client` — a small CLI that registers, logs in, then fires `Authorize` calls: one approved,
one declined with a streamed-in-text explanation, and a burst to show p99 under load. It must run as
a container (`docker run --network ecom-local`) because `awsvpc` tasks have no host port — see
Step 0 finding 1. Pair it with `--rate` so you can drive load for the §7.1 demo.

**Day 3 — Terraform (must have)**
Modules + both envs. Everything runs locally end to end. `make local-up` works from a clean clone.

**Day 4 — AWS (must have)**
Apply to a real account. ECR push, RDS, Secrets Manager, task roles, Cloud Map, logs.
**Pre-provision everything and leave it running** — RDS takes 5–10 minutes and conference Wi-Fi is
not your friend.

**Day 5 — rehearse (must have)**
Full runbook twice, timed. Screen-record the whole thing as your demo-fails insurance.

**Cut line — build only if Days 1–5 finish early:**
**one streaming RPC** (`StreamTransactions`, server-stream) purely as a visual — the §7 lessons do
not need it ·
ALB + ACM gRPC ingress · Service Connect comparison · `buf breaking` in CI · load-balancing
demo with 3 tasks · Prometheus/Alertmanager (the existing `docker/` configs already cover this;
it is not what the audience came for) · the old `k8s/` dir (keep it, mention it in one line as the
contrast, do not maintain it).

---

## 9. Stage runbook

```bash
make local-up          # ministack + postgres + jaeger + prometheus
make tf-local-apply    # terraform apply → ECS on the emulator
make demo              # register → login → approved + declined authorize
make demo-load         # sustained Authorize burst; watch per-task metrics  ← §7.1
make tf-aws-plan       # same modules, no endpoints block  ← the money slide
make tf-aws-apply      # (pre-run before the talk)
make demo AWS=1        # identical client, real Bedrock, real Fargate
make scale N=3         # show the load-balancing problem, then the fix
make tf-aws-destroy    # do not forget
```

**Live-demo rules, learned the hard way:**
- Never `terraform apply` from scratch on stage. Pre-provision; live-demo a *delta* — scale 1→3
  tasks, or a task-definition revision rollout. Fast, visual, low risk.
- Have the screen recording ready. Have the `terraform plan` output in a text file too.
- Pin every image tag. A `:latest` pull on venue Wi-Fi mid-demo is an avoidable death.
- Pre-pull all images and pre-seed the DB the morning of.

---

## 10. Open items I need from you

0. **The exact talk date, and a floor.** Today is Tue 6 Oct; Community Days usually land on a
   Saturday, which would leave **three working days**, not five. Days 1–5 above are heavier than
   that: monorepo migration, buf, two protos, a platform package fixing 8 defects, a new streaming
   service, 7 Terraform modules, and an AWS deploy. So below the cut line, fix a **floor** — the
   version you can still give if everything slips:
   *`identityd` + `paymentd` on Ministack ECS via Terraform, plus one of the two services
   running on real Fargate.* The `alb`, `secrets` and `observability` modules, the
   load-balancing demo and `buf breaking` all sit above the floor. Tell me the date and I will
   re-cut the day plan against it.
1. ~~Step 0 result~~ — **done, passed.** See the results table in §8. Cloud Map needs the
   one-line Docker-alias shim locally; everything else works natively.
2. **ALB/ACM** — do you have a Route53 domain in the demo account? If not, cut external gRPC ingress
   (§7.1.4) now rather than discovering the cert requirement on Friday.
3. **Bedrock model access** — model access is per-region and per-model and needs enabling in the
   console *before* it works. Confirm Nova Micro or Claude Haiku is enabled in your demo region now.
4. **New repo — decided, needs only a name.** `apis`, `userd`, `orderd` and `k8s` each turned out
   to be their **own published GitHub repo** (`lakhansamani/ecom-grpc-*`, `lakhansamani/ecom-k8s`)
   with tagged releases that the services consume (`ecom-grpc-apis v0.2.0`/`v0.3.0`) and READMEs
   saying "for the blog series". So this is **not** a rename and **not** a history merge: rewriting
   them would break your existing blog readers for no gain. The talk gets a **new, empty repo**
   (suggest `grpc-ecs-trading`) that people clone once; the four blog repos stay exactly as they are.
   Tell me the name and I will set it up.
5. ~~Not a git repo yet~~ — **done.** `git init` run, `.gitignore` excludes the four sub-repos,
   Terraform state and provider binaries, and the plan + spike are committed. Note the current
   working directory nests the new repo alongside four unrelated ones; when you pick the repo name
   I will move the new files into their own directory so a clone is clean.
