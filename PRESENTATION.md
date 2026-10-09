---
marp: true
paginate: true
title: Running Go gRPC Services on ECS
---

<!--
Plain markdown. Reads fine on GitHub as a document.
To project it as slides:  npx @marp-team/marp-cli PRESENTATION.md -o slides.html

Slides are separated by ---. The speaker script is a SEPARATE file: PRESENTER.md

Every number on these slides came out of a terminal in this repo, or from a
source quoted on the slide. Where something is not implemented, the slide says
so.
-->

# Running Go gRPC services on ECS

### From `localhost:50051` to production, with one set of Terraform modules

**Lakhan Samani** · AWS Community Day, Vadodara

`github.com/lakhansamani/grpc-ecs-demo`

---

## Who this talk is for

| If you are… | The part for you |
|---|---|
| **New to gRPC** | Slides 3–13. You will leave able to argue the choice in a design review. |
| **Writing Go already** | Slides 14–22. The contract, and real ECS on your laptop. |
| **Running ECS already** | Slides 23–28. **Three tasks, and one of them gets all the traffic.** |

One system throughout. **Three live demos.** All the code is open.

---

# Part 1 · The problem

## It is sale season

Big Billion Days. Great Indian Festival. Pick whichever one is running right now
— the sale you and I both have a tab open for.

Midnight. The banner goes live.

Everyone opens the app **at the same time**.

---

## What everyone is actually doing

Be honest about what that crowd is doing in the first hour:

| What they do | How often | Does it write anything? |
|---|---|---|
| Search "headphones under 2000" | Constantly | **No** |
| Scroll, compare, open a product page | Constantly | **No** |
| Add to cart, then go look at one more thing | Very often | No |
| **Actually place the order** | Far less often | **Yes** |

Most of a sale is **people looking**. A small slice is **people buying**.

You already know this from your own behaviour: how many things did you open last
sale, and how many did you buy?

---

## Now suppose the store is one application

One deployment doing everything: search, product pages, login, cart, checkout.

Traffic multiplies, so you scale up. More copies of the application.

**And it works.** This is not a story about a bad decision.

It is a story about **what you paid for it**: to survive all that *searching*,
you also made more copies of the part that **writes orders** — the one part you
least want many copies of, because copies of a writer is how two people buy the
last phone.

> **Scale the reads. Not the writes.**
>
> That one sentence is why this talk has three services instead of one.

---

## So: three services

| Service | Job | Copies | Why that number |
|---|---|---|---|
| `userd` | Accounts, login, tokens | **3** | Reads only. Safe to copy. |
| `productsd` | Search, list, product pages | **3** | Reads only. Safe to copy. |
| `orderd` | Places orders | **1** | **It writes.** |

This is not "microservices because microservices". The boundary is drawn where
the **scaling needs differ**, and nowhere else.

Search gets hammered at midnight → add `productsd` copies. `orderd` does not
move.

> Three services is also the *honest* number for a talk. If your system needs
> two, build two.

---

## But now they have to talk to each other

Placing one order needs two questions answered by **other services**:

1. `userd` — *who is this person?*
2. `productsd` — *what do these items cost, and are they in stock?*

Inside one application those were function calls. Now they cross a network.

**So: how should services call each other?**

That is the next question, and it is where gRPC comes in.

---

# Part 2 · Why gRPC

## What gRPC is

**gRPC lets you call a function that lives on another machine, as if it were
local.**

You write the function down — its name, its inputs, its outputs — in a file.
A compiler turns that file into real code for both sides.

```
RPC  =  Remote Procedure Call        (calling a function somewhere else)
g    =  gRPC Remote Procedure Calls  (yes, the acronym contains itself)
```

The `g` has never officially stood for Google. It gets reassigned every release
as a running joke.

---

## The actual difference from REST

**REST** makes you think about *resources* and *verbs*:

```
POST /v1/orders        { "items": [...] }
```
*"What's the right noun? POST or PUT? Which status code?"*

**gRPC** makes you think about *functions*:

```protobuf
rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse);
```
*"What does it take? What does it give back?"*

That is the whole mental shift. Everything else — HTTP/2, binary encoding, code
generation — is machinery serving that one idea.

---

## You write the contract. A compiler writes the code.

This is the **only** hand-written interface code in the project:

```protobuf
service OrderService {
  rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse);
}

message RequestedItem {
  string product_id = 1;
  int32  quantity   = 2;
}
```

One command (`make proto`) turns it into:

- Go **server** interfaces — forget an RPC and it **will not compile**
- Go **client** stubs
- A **REST/JSON gateway**
- An **OpenAPI** spec for the frontend team
- A **TypeScript** client

> Nobody should write a client from a wiki page that was last accurate four
> months ago.

---

## Why teams actually adopt gRPC

Not for speed. Not for streaming. **For the typed contract.**

I counted the RPCs in the real `.proto` files of 14 well-known projects:

| | RPCs | Use streaming |
|---|---|---|
| Temporal | 121 | **0** |
| Milvus | 154 | 2 |
| Qdrant | 30 | **0** |
| containerd | 17 | **0** |
| OpenTelemetry (OTLP) | 1 | **0** |
| Envoy xDS | 2 | **2** (100%) |
| **All 14 together** | **611** | **50 — about 8%** |

Protos and the full table: `docs/evidence/`. The counts reproduce with one
`grep` — please check them.

**Two conclusions:** streaming is a specialist tool, not the point. And **not
one** of these 14 exposes gRPC to the public internet.

---

## When gRPC, and when REST

| Use gRPC when | Use REST when |
|---|---|
| Services call **each other**, inside your network | A **browser** is the client |
| You want one contract in several languages | A third party integrates with you |
| Breaking a field should fail **CI**, not production | `curl` and a browser tab must just work |
| The call is a **function** | Public, cacheable, bookmarkable URLs |

**Usually you need both.** It is not a competition — it is a question of *where*.

And a browser **cannot** speak gRPC: it cannot open a raw HTTP/2 connection and
control trailers the way the protocol requires. That is a genuine limitation,
and it is why the next slide exists.

---

## So we need a REST door: `gatewayd`

```
browser ──REST/JSON──> gatewayd ──gRPC──> userd / productsd / orderd
```

`gatewayd` is **generated from the same protos.** Nobody wrote it.

Add four lines to an RPC and it gets a REST route:

```protobuf
rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse) {
  option (google.api.http) = { post: "/v1/orders" body: "*" };
}
```

**Leave them out and it has no REST route at all.** We use that deliberately —
slide 18.

---

# Part 3 · Where does it run?

## Four options. One is ruled out by mechanics, not taste.

| Option | Can it serve gRPC? | What you operate |
|---|---|---|
| **Lambda** | **No** | Nothing |
| **EC2** | Yes | AMIs, patching, scaling groups, your own deploy tooling |
| **EKS** (Kubernetes) | Yes, very well | Control plane, nodes, upgrades, CNI, ingress, RBAC |
| **ECS + Fargate** | Yes | A task definition and a service. **Chosen.** |

Lambda is not a preference call. gRPC needs a **process that stays listening**,
holding an HTTP/2 connection. Lambda has none.

From the AWS load balancer docs, on gRPC target groups, **verbatim**:

> "The only supported target types are `instance` and `ip`."
> "**You can't use Lambda functions as targets.**"

---

## Why ECS *for now* — and when to leave

The honest question is not "serverless or containers?" It is **how much
orchestration do four services actually need?**

- Four services = four task definitions. That is the whole requirement.
- No nodes to patch, no quarterly cluster upgrade to own.
- An ECS **task role is just an IAM role** — nothing new to learn first.

**Kubernetes is genuinely excellent at this.** Move to it when you have dozens
of services and several teams, or you need a real service mesh, or the same
manifests must run on-prem too.

> Moving later is **not a rewrite.** The container, the contract, the health
> check, the graceful shutdown and the load-balancing fix all come with you.
> Only the YAML around them changes.

---

## Three words you need for the rest of this talk

| Word | What it means |
|---|---|
| **Task** | One running container with its own private IP. Think "one copy of your service." |
| **Task definition** | The recipe for a task: image, CPU, memory, ports, secrets, health check. Versioned — editing it creates **revision 2**; revision 1 never changes. |
| **Fargate** | "Run this task, and don't make me manage a server." No EC2 instance to patch. |

That is enough to follow everything after this. I will name anything else as it
appears.

---

## Demo 1 — "How is that different from docker compose?"

```sh
make ps
```

Seven escalating proofs. Two of them settle it.

**The task describes itself.** Nothing in our code sets this — the platform
injects it:

```
ECS_CONTAINER_METADATA_URI_V4=http://.../v4/Iw-s54...
  Cluster : arn:aws:ecs:us-east-1:...:cluster/ecom-local
  Family  : userd rev 1
```

**And credentials arrive without any key existing:**

```
AWS_CONTAINER_CREDENTIALS_FULL_URI=http://.../v2/credentials/46e1d1eb-...
```

The AWS SDK reads that variable on its own. **There is no access key anywhere**
— not in the image, not in git, not in a `.env` file.

---

# Part 4 · The contract decisions

## Two choices in the proto worth arguing about

**1. The order request carries no price.**

```protobuf
message RequestedItem { string product_id = 1; int32 quantity = 2; }
```

The client sends *what* and *how many*. `orderd` asks `productsd` what it costs.
A client must never be able to ask "is this sale price real?" and then submit a
different number.

**2. One RPC is deliberately not on the internet.**

```protobuf
// NOTE: deliberately NO google.api.http option.
rpc CheckAvailability(CheckAvailabilityRequest) returns (CheckAvailabilityResponse);
```

Reachable over gRPC. **404 over REST.** Four lines of annotation are the entire
difference between an internal and a public API.

---

## And "out of stock" is not an error

Out of stock is a **business outcome**, not a transport failure:

```
CreateOrder → OK, status = REJECTED, reason = OUT_OF_STOCK
```

Not `codes.Internal`. Not an HTTP 500.

gRPC status codes stay for what they are for: *unauthenticated*, *invalid
argument*, *service unavailable*. Business outcomes live in the response, as an
**enum** — so clients branch on a value, never on an error message string.

> If your client does `if strings.Contains(err.Error(), "stock")`, then someone
> rephrasing a log message breaks production.

---

## Demo 2 — Browse, then buy

```sh
make demo        # over gRPC
make dev-rest    # the same flow over REST
```

Real output from this repo:

```
search  -> Wireless Noise Cancelling Headphones, Noise Cancelling Earbuds Ultra
login   -> token acquired
order   -> ORDER_STATUS_CONFIRMED  total=36997.0 INR  (2 lines priced by productsd)
replay  -> same order returned (replay=True)
reject  -> ORDER_STATUS_REJECTED | REJECTION_REASON_OUT_OF_STOCK
reject  -> ORDER_STATUS_REJECTED | REJECTION_REASON_INSUFFICIENT_STOCK
no token-> Unauthenticated, as expected
```

Then the same internal RPC, two ways:

```
REST  -> 404
gRPC  -> works
```

---

# Part 5 · Can I scale this?

## Storage decides. Not traffic.

"Can I run three copies of this?" is answered by **where its data lives** —
nothing else.

| Service | Where its data is | Copies | Why |
|---|---|---|---|
| `userd` | Database **baked into the image** | **3** | Every copy byte-identical. All reads agree. |
| `productsd` | Baked in, plus a search index | **3** | Same. |
| `orderd` | **Writable file in the task** | **1** | 3 copies = **3 different databases.** |
| `gatewayd` | Nothing at all | any | Nothing to diverge. |

A Fargate task's filesystem **dies with the task.** That is not hidden here — it
is the lesson. Kill `orderd` on stage and the orders are gone.

---

## So the code refuses to let you get it wrong

```hcl
validation {
  condition     = var.order_desired_count == 1
  error_message = "orderd keeps its database on the task filesystem, so N tasks
                   would mean N divergent databases. Scale userd or productsd."
}
```

```sh
make show-guard   # Terraform refuses, and tells you why
```

**For a real system, use a managed database.** SQLite on the task is a
conference-demo shortcut; dropping RDS took the AWS deploy from ~10 minutes to
~2. `DB_DRIVER=postgres` is the whole application-side switch.

**And not EFS.** From SQLite's own documentation, verbatim:

> "file locking logic is buggy in many network filesystem implementations …
> resulting in corruption … **there is nothing SQLite can do to prevent it.**"

---

# Part 6 · What bites in production

## Midnight. Three tasks. One of them gets everything.

You scale `userd` to 3. All three are healthy. DNS returns all three addresses.

**One task serves 100% of the traffic.**

Measured in this repo — 120 requests, three healthy tasks:

```
task 10e77597   120 calls     <- all of them
task 90c7be3b     0
task 02e3010a     0
```

People conclude service discovery is broken. **It is not.** Discovery did its
job perfectly.

> The client never asked to balance.

---

## Why: `pick_first`

gRPC's default load-balancing policy is `pick_first`:

1. Resolve the name → get three addresses
2. Connect to **one** of them
3. Send **every** request down that one connection, forever

And that default is reasonable! With HTTP/2, one connection multiplexes many
requests, so opening more looks wasteful. It optimises for a world where the
other end is a single load balancer.

On ECS the other end is **three tasks with three IPs.** The default is wrong
here, and **nothing warns you** — no error, no log line, no failed health check.

---

## The fix is two halves. Either alone does nothing.

```go
// CLIENT — ask for balancing, AND use a resolver that returns every address.
grpc.NewClient("dns:///userd.ecom.local:50051",
    grpc.WithDefaultServiceConfig(
        `{"loadBalancingConfig":[{"round_robin":{}}]}`))
```

The `dns:///` prefix is **not decoration.** A bare `host:port` uses the
*passthrough* resolver, which hands the name straight to the dialer and yields
exactly **one** address — so `round_robin` has nothing to balance over.

```go
// SERVER — recycle connections so clients re-resolve after a scale-out.
grpc.KeepaliveParams(keepalive.ServerParameters{
    MaxConnectionAge:      30 * time.Second,
    MaxConnectionAgeGrace: 5 * time.Second,
})
```

Without the server half, a client connected **before** you scaled out never
learns the new tasks exist. Which is exactly when you scale: during the sale.

---

## Demo 3 — Break it, then fix it

```sh
# 1. Deploy the client with gRPC's DEFAULTS. Three tasks; watch one work.
terraform -chdir=terraform/envs/local apply -auto-approve -var lb_policy=pick_first
make dns && make scale N=3 && make forward && make demo-load
```

```sh
# 2. Apply the fix. Same command, variable dropped.
terraform -chdir=terraform/envs/local apply -auto-approve
make dns && make forward && make demo-load
```

In Prometheus: `sum by (task) (grpc_server_started_total{service="userd"})`

| | Result |
|---|---|
| `pick_first` | **120 / 0 / 0** |
| `round_robin` + `dns:///` | **40 / 40 / 40** |

Then **kill a task mid-load** → zero failed requests.

---

## Why zero failed requests: shutdown order

ECS sends `SIGTERM`, waits, then `SIGKILL`. What you do in between matters, and
the **order** matters:

1. Mark health **`NOT_SERVING`** first → stop being given new work
2. **Then** `GracefulStop()` → let in-flight requests finish
3. Hard `Stop()` as a backstop

And two numbers have to agree:

```hcl
stopTimeout      = 30     # ECS: how long it waits before SIGKILL
SHUTDOWN_TIMEOUT = 15s    # the app: how long it drains
```

> If `stopTimeout` is **shorter** than your drain, ECS kills you mid-drain and
> all that graceful-shutdown code did nothing at all.

---

## Four bugs only a real deploy found

| What broke | Why it was invisible until then |
|---|---|
| **Every task crash-looped** on an OpenTelemetry schema mismatch | With no tracing endpoint set, the tracer short-circuits. **Locally it never built the thing that crashes.** |
| **Cloud Map returned no addresses** — `code 14: "no children to pick from"` | An *empty* `health_check_custom_config {}` block — which looks like tidying a deprecation warning — makes the provider send no health config, so Cloud Map never accepts ECS's health reports. Every task, service and DNS setting looked healthy. |
| **The first request after every deploy failed** | `grpc.NewClient` is **lazy** — it returns before connecting, so the first call pays for DNS + handshake and fails fast if the other side isn't up yet. |
| **Prometheus was scraping nothing** | It runs as `nobody` and cannot read the Docker socket. Discovery failed silently while the container looked perfectly healthy. |

Unit tests and `terraform validate` would not have found **any** of these.

---

# Part 7 · The payoff

## One set of modules. Two environments. One differing file.

```sh
diff terraform/envs/local/provider.tf terraform/envs/aws/provider.tf
```

Local has fake credentials and an `endpoints` block pointing at an emulator.
AWS has:

```hcl
provider "aws" {
  region = var.aws_region
}
```

**Everything describing the deployment is shared verbatim.** Not a copy, not a
simplified local version — literally the same files.

That is the argument for emulating locally instead of keeping a second, simpler
set of local manifests: **there is no second set to drift.**

---

## What to take away

1. **Draw service boundaries where scaling needs differ.** Not by noun.
2. **gRPC is for service-to-service.** A browser needs a REST door — and you can
   generate it instead of writing it.
3. **Lambda cannot serve gRPC.** Mechanics, not preference.
4. **`pick_first` is the default.** Three tasks and one gets everything is the
   *normal* outcome. Fix **both** halves.
5. **Storage decides whether you can scale**, not traffic.
6. **Graceful shutdown is two numbers agreeing** — yours and `stopTimeout`.
7. **Deploy it once before you trust it.** Four of my worst bugs were invisible
   locally.

---

## Clone it and break it yourself

### `github.com/lakhansamani/grpc-ecs-demo`

```sh
make test          # no Docker, no AWS needed
make local-up      # real ECS tasks on your laptop
make ps            # prove it is really ECS
make demo          # the whole flow
make scale N=3     # then break it, then fix it
```

- `docs/DEMO_GUIDE.md` — every command, and how to inspect each AWS component
- `docs/ARCHITECTURE.md` — what ECS, Fargate, task definitions and Route 53 do
- `SPEC.md` — every decision, and what was verified versus assumed

**Thank you. Questions?**
