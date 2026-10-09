---
marp: true
paginate: true
title: Running Go gRPC Services on ECS
---

<!--
Plain markdown. Reads fine on GitHub as a document.
To project it:  npx @marp-team/marp-cli PRESENTATION.md -o slides.html

DIAGRAMS: the two Mermaid blocks render on GitHub. Marp does NOT render
Mermaid. Either present slides 6 and 25 from the GitHub tab, or screenshot
them and paste the images in. Decide before you are on stage.

Slides marked [ASK] are audience-prediction beats — do not read the next
slide until someone has answered. Slides marked [BEAT] are single-line pace
changes; they take ten seconds.

Speaker script is a SEPARATE file: PRESENTER.md

Every number here came out of a terminal in this repo, or from a source quoted
on the slide. Where something is a shortcut, the slide says so.
-->

# Running Go gRPC services on ECS

### From `localhost:50051` to production — with one set of Terraform modules

**Lakhan Samani** · AWS Community Day, Vadodara

`github.com/lakhansamani/grpc-ecs-demo`

---

## Before we start, a promise

Later in this talk I will show you a system where:

- **Three servers are running.** All three healthy.
- **DNS is correct.** It returns all three addresses.
- **One of them is doing 100% of the work.**

No error. No log line. No failed health check. Nothing in your dashboards.

**I lost most of a day to this.** It is the single most common way a gRPC
service gets deployed wrong, and by the end of this talk you will recognise it
in about ten seconds.

Everything else is how we get there.

---

## Where we are going

| Part | What you get |
|---|---|
| **1 · The use case** | Why this is three services and not one |
| **2 · Why gRPC** | What it is, what is actually true about it, when *not* to use it |
| **3 · Feeding a browser** | Do we need REST? Do we need a gateway? |
| **4 · Where it runs** | Lambda, EC2, EKS, ECS — and what Fargate really gives you |
| **5 · The code** | One contract → three services |
| **6 · The Terraform** | **Every AWS component, and one pipeline for laptop and prod** |
| **7 · Running it** | Three demos, then that bug |

Parts 1–3 need no prior gRPC knowledge. **Part 6 is the heart of the talk.**

---

# Part 1 · The use case

## It is sale season

Big Billion Days. Great Indian Festival. Whichever one is running right now —
the sale you and I both have a tab open for.

Midnight. The banner goes live.

Everyone opens the app **at the same time**.

---

## [ASK] Think about your own last sale

> **How many products did you open?**
>
> **How many did you buy?**

Hold that ratio in your head. We are about to build the architecture it
implies.

---

## That ratio, as a table

| What they do | How often | Does it write anything? |
|---|---|---|
| Search "headphones under 2000" | Constantly | **No** |
| Scroll, compare, open a product page | Constantly | **No** |
| Add to cart, then go look at one more thing | Very often | No |
| **Actually place the order** | Far less often | **Yes** |

Most of a sale is **people looking**. A small slice is **people buying**.

I am not going to put somebody else's traffic graph on a slide. **Your own
browsing is the evidence**, and it is better evidence, because you trust it.

---

## Suppose the store is one application

One deployment: search, product pages, login, cart, checkout.

Traffic multiplies, so you scale up. More copies of the whole thing.

**And it works.** This is not a story about a bad decision.

It is a story about **what it costs you**:

- You are scaling **checkout** in order to survive **search** traffic
- A slow catalogue query and a checkout bug share one deploy, one rollback and
  one on-call page
- You cannot tune them separately, because they are the same process

---

## [BEAT]

# Two workloads.
# One deploy button.

---

## The overall picture

```mermaid
graph LR
    subgraph who["Who uses it"]
        shopper["Shopper<br/>(browser / app)"]
        ops["You<br/>(terraform)"]
    end

    subgraph reads["READ path - scales freely"]
        browse["Search and browse<br/>the catalogue"]
        login["Log in"]
    end

    subgraph writes["WRITE path"]
        order["Place an order"]
    end

    shopper --> browse
    shopper --> login
    shopper --> order

    browse --> productsd["productsd"]
    login --> userd["userd"]
    order --> orderd["orderd"]

    orderd -->|"who is this?"| userd
    orderd -->|"what does it cost?<br/>is it in stock?"| productsd

    ops -->|"one pipeline:<br/>laptop AND prod"| infra["ECS + Fargate"]
    infra -.-> productsd
    infra -.-> userd
    infra -.-> orderd

    classDef r fill:#2E7D8C,stroke:#1a4d57,color:#fff
    classDef w fill:#E8633A,stroke:#a8431f,color:#fff
    class browse,login,productsd,userd r
    class order,orderd w
```

Teal reads. Orange writes. **They do not grow at the same rate.**

---

## So: three services

| Service | Job | Copies |
|---|---|---|
| `userd` | Accounts, login, tokens | **3** |
| `productsd` | Search, list, product pages | **3** |
| `orderd` | Places orders | **1** |

**How do you know the boundary is right?** A test you can use at work tomorrow:

> Would these ever need a **different number of copies**, a **different deploy
> schedule**, or a **different on-call owner**?
>
> **No to all three → it is one service.** Put it back.

Here, search gets hammered at midnight and checkout does not. That is a yes.

**Each split costs you** a network hop, a new failure mode and another thing to
deploy. Three is what *this* use case pays for. If yours pays for two, build
two.

---

## But now they have to talk to each other

Placing one order needs two questions answered by **other services**:

1. `userd` — *who is this person?*
2. `productsd` — *what do these items cost, and are they in stock?*

Inside one application those were function calls. Now they cross a network.

**So: how should services call each other?**

---

# Part 2 · Why gRPC

## What gRPC is

**gRPC lets you call a function that lives on another machine, as if it were
local.**

You write the function down — name, inputs, outputs — in a file. A compiler
turns that file into real code for both sides.

```
RPC  =  Remote Procedure Call        (calling a function somewhere else)
g    =  gRPC Remote Procedure Calls  (yes, the acronym contains itself)
```

---

## The mental shift

**REST** makes you think about *resources* and *verbs*:

```
POST /v1/orders        { "items": [...] }
```
*"What is the right noun? POST or PUT? Which status code?"*

**gRPC** makes you think about *functions*:

```protobuf
rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse);
```
*"What does it take? What does it give back?"*

Everything else — HTTP/2, binary encoding, code generation — is machinery
serving that one idea.

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

`make proto` turns it into:

- Go **server** interfaces — forget an RPC and it **will not compile**
- Go **client** stubs · a **REST/JSON gateway** · an **OpenAPI** spec
- A **TypeScript** client

> Nobody should write a client from a wiki page that was last accurate four
> months ago.

---

## Let me be honest about performance

You will read that gRPC is "faster". Here is the accurate version.

**Real advantages, by design:**

- **Binary Protobuf** instead of JSON — smaller payloads, cheaper to parse
- **One multiplexed HTTP/2 connection** instead of a connection per call
- **Headers are not re-sent** in full on every request

**But:**

- For most internal services the network and your database dominate. Encoding
  is rarely the bottleneck.
- I have **not** benchmarked this repo, so I will not put a speed-up number on
  a slide and have you quote me on it.

> Performance is a genuine benefit. It is just **not usually why teams switch**.

---

## [ASK] So people say gRPC is for streaming

I counted every RPC in the real `.proto` files of **14 projects you have heard
of** — Temporal, etcd, containerd, Kubernetes CRI, Milvus, Qdrant, TiKV, Dapr,
CockroachDB, Vitess, Thanos, Bazel, Envoy, OpenTelemetry.

**611 RPCs in total.**

> ### What percentage use streaming?
>
> Shout out a number.

---

## About 8%

| | RPCs | Use streaming |
|---|---|---|
| Temporal | 121 | **0** |
| Milvus | 154 | 2 |
| Qdrant | 30 | **0** |
| containerd | 17 | **0** |
| OpenTelemetry (OTLP) | 1 | **0** |
| Envoy xDS | 2 | **2** (100%) |
| **All 14 together** | **611** | **50 — about 8%** |

Protos are in `docs/evidence/`. The counts reproduce with one `grep` — **please
go check me.**

**Two conclusions.** Streaming is a specialist tool, not the reason to adopt
gRPC. And **not one** of these 14 exposes gRPC to the public internet.

What you are most likely adopting is a **typed, versioned contract between your
own services**.

---

# Part 3 · Feeding a browser

## A browser cannot speak gRPC

Not a configuration problem. A browser cannot open a raw HTTP/2 connection and
control trailers the way gRPC requires.

gRPC is for the calls *inside* your network. The moment a client is a browser
or a third party, you need REST — so **usually you need both**, and the only
question is where the line sits.

So something has to translate. **Three real options:**

| Option | What it means | Cost |
|---|---|---|
| **1 · A gateway process** | One service translates REST→gRPC for everything behind it | One more thing to deploy and scale |
| **2 · Gateway in-process** | Each service serves gRPC *and* REST on its own ports | No extra hop; every service carries an HTTP stack |
| **3 · ConnectRPC** | The server speaks Connect, gRPC **and** gRPC-Web on **one port** | A different server library |

Connect's own docs say it interops *"with `grpc-web` frontends without the need
for an intermediary proxy (such as Envoy)."*

---

## So do we actually need `gatewayd`?

# No.

It is a choice. Here is the honest trade-off.

**Why I picked option 1:** it makes the lesson *visible*. `gatewayd` is a
separate ECS service with its own task definition, so you watch a stateless
service deploy next to stateful ones.

**If I were starting a product today**, option 3 is very attractive: one port,
no extra hop, nothing extra to operate. The TypeScript client in this repo
already uses `@connectrpc/connect`.

**When a gateway process still earns its keep:**

- You want **one** public door to audit, rate-limit and put a WAF in front of
- Your services must stay plain gRPC, because another team owns them

---

## And do we need REST for every API?

# Also no.

Of the 10 RPCs in this repo, **9 have a REST route. One does not.**

```protobuf
// NOTE: deliberately NO google.api.http option.
rpc CheckAvailability(CheckAvailabilityRequest) returns (CheckAvailabilityResponse);
```

`orderd` calls `CheckAvailability` to price a cart. **Nothing outside should.**

```
REST  -> 404
gRPC  -> works
```

So the proto itself is the access-control decision, and you can read it in a
code review.

**One nuance worth seeing:** `VerifyToken` is *both*. It is
`GET /v1/users/me` for the browser **and** the internal hop `orderd` makes on
every order. Same RPC, same implementation, two callers — which is fine. The
annotation decides who can *reach* it, not who it is *for*.

> **The rule:** a REST route exists for a client you do **not** control. Expose
> exactly those. Four lines of annotation are the entire difference between an
> internal and a public API.

---

# Part 4 · Where does it run?

## Four options. One is ruled out by mechanics, not taste.

| Option | Can it serve gRPC? | What you operate |
|---|---|---|
| **Lambda** | **No** | Nothing |
| **EC2** | Yes | AMIs, patching, scaling groups, your own deploys |
| **EKS** (Kubernetes) | Yes, very well | Control plane, nodes, upgrades, CNI, ingress, RBAC |
| **ECS + Fargate** | Yes | A task definition and a service. **Chosen.** |

gRPC needs a **process that stays listening**, holding an HTTP/2 connection.
Lambda has none. From the AWS load balancer docs on gRPC target groups,
**word for word**:

> "The only supported target types are `instance` and `ip`."
> "**You can't use Lambda functions as targets.**"

---

## Why ECS *for now* — and when to leave

The real question is not "serverless or containers?" It is **how much
orchestration do four services actually need?**

- Four services = four task definitions. That is the whole requirement.
- An ECS **task role is just an IAM role** — nothing new to learn first.

**Kubernetes is genuinely excellent at this.** Move when you have dozens of
services and several teams, need a real service mesh, or the same manifests
must run on-prem too.

> Moving later is **not a rewrite.** The container, the contract, the health
> check and the graceful shutdown all come with you. Only the YAML changes.
>
> *"For now"* is a legitimate engineering answer. *"Forever"* rarely is.

---

## [ASK] A question about Fargate

Your container is running on Fargate. There is a kernel underneath it.

> ### Who patches that kernel?
>
> And — **what happens to your running task when they do?**

---

## What Fargate actually gives you — and what it does not

**Fargate = run containers without managing EC2 instances.** AWS's words: *"you
no longer have to provision, configure, or scale clusters of virtual
machines."*

**AWS owns** the *platform version*, which AWS defines as *"a combination of
the kernel and container runtime versions."* So AWS patches it. But:

> "If a security issue is found that affects an existing platform version, AWS
> creates a new patched revision of the platform version **and retires tasks
> running on the vulnerable revision.**"

**AWS will stop your task to patch underneath you.** A task never upgrades in
place — a *new* task gets the new revision.

**And you still own** everything **inside** your image: your base image, your
packages, your CVEs.

> **Serverless does not mean nobody patches.** It means AWS patches their half,
> kills your task to do it, and you still patch yours.
>
> Which is why graceful shutdown is not optional. Remember that.

---

## Three words for the rest of this talk

| Word | What it means |
|---|---|
| **Task** | One running container with its own private IP. "One copy of your service." |
| **Task definition** | The recipe: image, CPU, memory, ports, secrets, health check. Versioned — editing it creates **revision 2**; revision 1 never changes. |
| **Service** | The controller that keeps *N* tasks of a task definition running and replaces the ones that die. |

---

# Part 5 · The code

## Three services, one contract

```mermaid
graph TB
    client["Browser / Postman / curl"]

    subgraph vpc["VPC"]
        gw["gatewayd :8080<br/>REST to gRPC<br/>generated, stores nothing"]

        subgraph svc["ECS services"]
            u["userd :50051<br/>accounts, JWT<br/>3 tasks"]
            p["productsd :50053<br/>search + catalogue<br/>3 tasks"]
            o["orderd :50052<br/>places orders<br/>1 task"]
        end

        cm["Cloud Map<br/>ecom.local<br/>names to task IPs"]
    end

    client -->|"REST/JSON"| gw
    client -->|"gRPC"| o
    gw -->|gRPC| u
    gw -->|gRPC| p
    gw -->|gRPC| o
    o ==>|"VerifyToken"| u
    o ==>|"CheckAvailability"| p
    cm -.->|resolves| o

    classDef s fill:#2E7D8C,stroke:#1a4d57,color:#fff
    classDef g fill:#E8633A,stroke:#a8431f,color:#fff
    class u,p,o s
    class gw,cm g
```

**Nobody in this diagram knows anybody's IP address.** They dial names.

---

## The request has no price in it

```protobuf
message RequestedItem { string product_id = 1; int32 quantity = 2; }
```

The client sends *what* and *how many*. `orderd` asks `productsd` what it
costs.

**Think about the alternative during a sale.** If the client sends the price,
then a client can ask *"is this ₹2,000 sale price real?"* — and then submit
₹200.

---

## [ASK] You tap "Buy". The spinner spins.

Midnight, sale traffic, patchy 4G. Your phone sends the order and the response
never comes back.

So your phone retries. Reasonably — it has no idea whether the server got it.

> ### Did you just buy one phone, or two?

---

## That is what an idempotency key is for

The client makes up a unique string per *intent to buy* and sends it with the
order:

```protobuf
message CreateOrderRequest {
  repeated RequestedItem items  = 1;
  string idempotency_key        = 2;   // required
}
```

**The server's deal:** *"Send me the same key twice and you get the same order
back — I will not create a second one."*

```
first call   -> Order abc123, idempotentReplay = false
same key     -> Order abc123, idempotentReplay = true    <- no second order
```

Three details that matter in the code:

- **Required.** No key → `InvalidArgument`. A retry-unsafe order API is a bug.
- **Namespaced per user** (`userID + ":" + key`), so two shoppers cannot
  collide on `"cart-1"`.
- **A unique index backs it**, not just an `if`. Two simultaneous retries race;
  one loses, catches the duplicate-key error, and returns the stored order.

> The check alone is not enough. **The database constraint is what makes it
> true.**

---

## And "out of stock" is not an error

```
CreateOrder → OK, status = REJECTED, reason = OUT_OF_STOCK
```

Not `codes.Internal`. Not a 500.

gRPC status codes stay for what they are for: *unauthenticated*, *invalid
argument*, *unavailable*. Business outcomes are an **enum** in the response, so
clients branch on a value — never on an error string.

> `if strings.Contains(err.Error(), "stock")` means that somebody rephrasing a
> message breaks production.
>
> **An enum cannot be rephrased.**

---

## One honest note on copies

`userd` and `productsd` run 3 copies because their database is **baked into the
image** — every copy is identical, so any copy can answer any read.

`orderd` runs **1**, and I want to be precise about why, because I used to say
this badly:

> **Not** "because it writes". Writers scale fine.
>
> **Because it writes to a file inside the task.** Three copies would be three
> different databases.

**Give it a managed database and `orderd` scales like the others.** SQLite on
the task is a demo shortcut that keeps the AWS deploy at ~2 minutes instead of
~10. Terraform refuses to scale it, so the shortcut cannot bite by accident:

```sh
make show-guard
```

---

# Part 6 · The Terraform

## Six modules, four services, two environments

```
terraform/
├── modules/
│   ├── network/        VPC, subnets, IGW, routes, security group
│   ├── ecr/            one image repository
│   ├── ecs-cluster/    cluster + capacity providers + Cloud Map + log group
│   ├── iam/            execution role, task role
│   ├── secrets/        the shared JWT secret
│   └── ecs-service/    task definition + service + Cloud Map registration
│
├── deployment/         THE WHOLE DEPLOYMENT — identical in both envs
│   └── main.tf         wires the modules; instantiates ecs-service x4
│
└── envs/
    ├── local/          provider.tf  <- the only difference
    └── aws/            provider.tf  <- the only difference
```

`ecs-service` is instantiated **four times**. One `protocol = "grpc" | "http"`
variable switches the port mapping and the health-check mode — so the three
gRPC services and the HTTP gateway come out of the **same module**.

---

## Every AWS component this creates

| Component | Resource | Why it is here |
|---|---|---|
| **VPC** | `aws_vpc` | `enable_dns_hostnames` is **required** for Cloud Map. Miss it and discovery silently resolves nothing. |
| **Subnets** ×2 | `aws_subnet` | Two availability zones. |
| **Internet Gateway** | `aws_internet_gateway` | Tasks reach ECR, Secrets Manager, CloudWatch. |
| **Route table** | `aws_route_table` + assoc | `0.0.0.0/0` → IGW. |
| **Security group** | `aws_security_group` + 3 rules | A **self-referencing** rule is how `orderd` reaches the others — no CIDRs to maintain. |
| **ECR** ×4 | `aws_ecr_repository` | Private registry. `force_delete`, so `destroy` is never blocked. |
| **ECS cluster** | `aws_ecs_cluster` | The namespace the services live in. |
| **Capacity providers** | `aws_ecs_cluster_capacity_providers` | `FARGATE` + `FARGATE_SPOT`. |
| **Cloud Map namespace** | `aws_service_discovery_private_dns_namespace` | Creates `ecom.local` as a **Route 53 private hosted zone**. |
| **Cloud Map service** ×4 | `aws_service_discovery_service` | A records appear and vanish as tasks come and go. |
| **Log group** | `aws_cloudwatch_log_group` | `/ecs/<name>`, 1-day retention. |
| **Execution role** | `aws_iam_role` | The **agent** uses it: pull image, read secret, create log streams. |
| **Task role** | `aws_iam_role` | **Your process's** credentials. Deliberately **empty**. |
| **Secret** | `aws_secretsmanager_secret` + version | The shared `JWT_SECRET`. |
| **Task definition** ×4 | `aws_ecs_task_definition` | The recipe. |
| **ECS service** ×4 | `aws_ecs_service` | Keeps N tasks alive. |

**22 resource types. No NAT Gateway, no ALB, no RDS** — each one deliberate.

---

## The two IAM roles, because people conflate them

| Role | Who uses it | When |
|---|---|---|
| **Execution role** | the **ECS agent** | **Before** your code runs: pull the image, resolve secrets, create log streams |
| **Task role** | **your process** | At runtime. The AWS SDK picks it up by itself. |

Get these backwards and your task fails to start with an error pointing at the
wrong role. It is a genuinely confusing hour.

Here the **task role is empty on purpose** — these services call no AWS API at
runtime. Nothing granted "just in case".

```sh
docker inspect <task> | grep AWS_CONTAINER_CREDENTIALS
# AWS_CONTAINER_CREDENTIALS_FULL_URI=http://.../v2/credentials/46e1d1eb-...
```

> **You never created a key, so there is none to leak.**

---

## Cloud Map is just DNS

`orderd` knows no IP addresses. It dials a **name**:

```
userd.ecom.local:50051
```

ECS registers each task's IP with Cloud Map; Cloud Map maintains the A records
in a Route 53 private hosted zone. Tasks come and go; the name does not.

```hcl
routing_policy = "MULTIVALUE"          # return EVERY healthy task
dns_records { type = "A"  ttl = 10 }   # low TTL, so scale-out is seen fast
health_check_custom_config { failure_threshold = 1 }
```

---

## A story about that last block

I had a deprecation warning on `failure_threshold`. So I tidied it up — left
the block empty. Then deployed.

**What I saw:**

```
rpc error: code = Unavailable desc = ... "no children to pick from"
```

**What I checked, all of which looked fine:**

- 4 tasks `RUNNING` ✅
- 4 Cloud Map services, all present ✅
- VPC DNS hostnames enabled ✅
- Security groups correct ✅

**The cause:** an *empty* block makes the provider send **no health config at
all**. Cloud Map then never accepts ECS's health reports → every instance stays
`UNHEALTHY` → **unhealthy instances are excluded from DNS answers**.

Four healthy tasks. Zero addresses returned.

> The comment in that file now says, in capitals, **do not clean this up.**

---

## [BEAT]

# One command.
# It is the whole talk.

---

## One pipeline. Laptop and production.

```sh
diff terraform/envs/local/provider.tf terraform/envs/aws/provider.tf
```

```hcl
# envs/local/provider.tf                 # envs/aws/provider.tf
provider "aws" {                         provider "aws" {
  access_key = "test"                      region = var.aws_region
  secret_key = "test"                    }
  region     = "us-east-1"
  skip_credentials_validation = true
  ...
  endpoints {                            # (nothing else)
    ecs = "http://localhost:4566"
    ecr = "http://localhost:4566"
    ...
  }
}
```

Everything describing the deployment lives in `terraform/deployment/`. Not a
copy. Not a simplified local version. **Both environments load the same
files.**

> That is the argument for emulating locally instead of keeping a second set of
> "local" manifests: **there is no second set to drift.** Change a task
> definition and you change it once.

---

## Where the laptop version is honest

**Ministack** (MIT) emulates ECS by launching **real Docker containers**, plus
ECR, Cloud Map, Secrets Manager and SSM. LocalStack's free Community edition
ended 2026-03-23, and ECS was never in it.

**Real locally:** the Terraform, the task definitions, `awsvpc` networking,
secret injection, health checks, graceful shutdown, metrics, traces.

**Where I substitute — and I will say so on stage:**

| Gap | Stand-in |
|---|---|
| Cloud Map stores registrations but serves **no DNS** | `make dns` adds Docker network aliases. Legitimate, because **service discovery is only DNS underneath.** |
| `awsvpc` tasks have **no host port** | `make forward` runs a relay. On AWS this is SSM port forwarding. |
| `healthStatus` / `launchType` not echoed back | Real ECS reports `HEALTHY` / `FARGATE`. |

> Naming the emulator's limits yourself is more credible than being caught by
> them.

---

# Part 7 · Running it

## Demo 1 — Is this really ECS, or just docker compose?

```sh
make ps
```

**The task describes itself** — nothing in our code sets this:

```
ECS_CONTAINER_METADATA_URI_V4=http://.../v4/Iw-s54...
  Cluster : arn:aws:ecs:us-east-1:...:cluster/ecom-local
  Family  : userd rev 1
```

**And credentials arrive with no key existing anywhere:**

```
AWS_CONTAINER_CREDENTIALS_FULL_URI=http://.../v2/credentials/46e1d1eb-...
```

---

## Demo 2 — The code, running

```sh
make demo        # over gRPC
make dev-rest    # the same flow over REST
```

```
search  -> Wireless Noise Cancelling Headphones, Noise Cancelling Earbuds Ultra
login   -> token acquired
order   -> ORDER_STATUS_CONFIRMED  total=36997.0 INR  (2 lines priced by productsd)
replay  -> same order returned (replay=True)
reject  -> ORDER_STATUS_REJECTED | REJECTION_REASON_OUT_OF_STOCK
no token-> Unauthenticated, as expected
```

Then the internal RPC, two ways:

```
REST  -> 404        gRPC  -> works
```

And the same flow from **generated TypeScript**: `make ts-demo`

---

## Demo 3 — The same Terraform, against real AWS

```sh
cd terraform/envs/aws
terraform plan      # same modules. no endpoints block.
terraform apply     # ~2 minutes
make ps-aws
```

What changed between laptop and AWS: **one provider block.**

What is real now that was substituted before:

- **Cloud Map does the DNS for real** — no alias shim
- `healthStatus: HEALTHY`, `launchType: FARGATE`
- Tasks spread across **two availability zones**

```sh
terraform destroy   # before leaving the venue
```

---

# Now, the promise from slide 2

## The setup

`userd` is scaled to **3 tasks**.

| Check | Result |
|---|---|
| Tasks running | **3 of 3** ✅ |
| Cloud Map instances healthy | **3 of 3** ✅ |
| DNS answer for `userd.ecom.local` | **3 A records** ✅ |
| Client configured with a load balancer | not needed — gRPC does this itself |

I send **120 requests** through `orderd`, and every one of them makes `orderd`
call `userd`.

---

## [ASK] Where does the traffic go?

> ### 120 requests. Three healthy tasks.
>
> ### How many land on each one?

Shout out the split.

---

## 120 / 0 / 0

```
task 10e77597   120 calls     <- all of them
task 90c7be3b     0
task 02e3010a     0
```

Measured in this repo.

No error. No log line. No failed health check. Every dashboard green.

**Everybody's first conclusion is that service discovery is broken.**

It is not. Discovery did its job perfectly — it handed back three addresses.

> ## The client never asked to balance.

---

## Why: `pick_first`

gRPC's default load-balancing policy is `pick_first`:

1. Resolve the name → get three addresses
2. Connect to **one** of them
3. Send **every** request down that one connection, forever

**And that default is reasonable!** With HTTP/2 one connection multiplexes many
requests, so opening more looks wasteful. It optimises for a world where the
other end is a **single load balancer**.

On ECS the other end is **three tasks with three IPs**.

> The default is not a bug. It is a correct answer to a different question.

---

## The fix is two halves. Either alone does nothing.

```go
// CLIENT — ask for balancing, AND use a resolver that returns every address.
grpc.NewClient("dns:///userd.ecom.local:50051",
    grpc.WithDefaultServiceConfig(
        `{"loadBalancingConfig":[{"round_robin":{}}]}`))
```

**`dns:///` is not decoration.** A bare `host:port` uses the *passthrough*
resolver, which hands the name straight to the dialer and yields exactly
**one** address — so `round_robin` has nothing to balance over.

> That is the version that **looks** fixed and is not.

```go
// SERVER — recycle connections so clients re-resolve after a scale-out.
grpc.KeepaliveParams(keepalive.ServerParameters{
    MaxConnectionAge:      30 * time.Second,
    MaxConnectionAgeGrace: 5 * time.Second,
})
```

Without the server half, a client connected **before** you scaled out never
learns the new tasks exist. Which is exactly when you scale: during the sale.

With both: **40 / 40 / 40.**

---

## What to take away

1. **Draw boundaries where the copies, deploys or owners differ.** Otherwise it
   is one service.
2. **Adopt gRPC for the contract.** The performance is a bonus you probably
   will not measure.
3. **Expose REST only where you do not control the client** — 9 of our 10 RPCs,
   not all of them.
4. **A gateway is a choice, not a requirement.** ConnectRPC gives you one port
   and no gateway at all.
5. **Fargate does not mean nobody patches.** AWS patches the platform *and
   retires your tasks to do it.*
6. **One Terraform stack, two provider blocks.** There is no second set of
   manifests to drift.
7. **`pick_first` is the default.** Three tasks and one gets everything is the
   *normal* outcome.

---

## The one I would tattoo on the back of my hand

# Green dashboards are not the same thing as working.

Four tasks running, four Cloud Map services, DNS enabled — and zero addresses
returned.

Three healthy tasks, correct DNS — and one of them doing everything.

**Both looked perfectly fine.** Deploy it once before you trust it.

---

## Clone it and run it

### `github.com/lakhansamani/grpc-ecs-demo`

```sh
make test          # no Docker, no AWS needed
make local-up      # real ECS tasks on your laptop
make ps            # prove it is really ECS
make demo          # the whole flow
```

- `docs/ARCHITECTURE.md` — every AWS component: what, why, how
- `docs/DEMO_GUIDE.md` — every command, and how to inspect each component
- `SPEC.md` — every decision, marked verified or assumed

**Thank you. Questions?**
