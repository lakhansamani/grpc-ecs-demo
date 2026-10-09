---
marp: true
theme: default
paginate: true
title: Running Go gRPC Services on ECS
---

<!--
Renders as slides with Marp:   npx @marp-team/marp-cli PRESENTATION.md -o slides.html
                               npx @marp-team/marp-cli PRESENTATION.md -o slides.pdf
Reads fine as a plain document on GitHub too.

Speaker notes are the HTML comments under each slide. Marp shows them in
presenter view; GitHub hides them.

21 slides, 7 acts. Acts 1-3 need no prior gRPC knowledge. Act 6 is the part
experienced people came for.
-->

# Running Go gRPC services on ECS

### From `localhost:50051` to production, with one set of Terraform modules

**Lakhan Samani** · AWS Community Day, Vadodara

`github.com/lakhansamani/grpc-ecs-demo`

<!--
Opening line: everything here was run, not sketched. Every number came out of a
terminal this week, and where something is not implemented I will say so.

Promise three takeaways: why gRPC cannot run on Lambda, how to diagnose the most
common gRPC-on-ECS bug, and how to run the same Terraform locally and on AWS.
-->

---

## How this is built

| | |
|---|---|
| **Acts 1–3** · new to gRPC | Why split services at all · what gRPC is · gRPC vs REST · Lambda vs ECS vs Kubernetes |
| **Acts 4–5** · shipped code before | The contract · three services · running real ECS on a laptop · Terraform |
| **Act 6** · done this before | **Three tasks and one gets all the traffic** · deploys that drop requests · bugs only a deploy finds |

Same system throughout. Three live demos.

<!--
Give people a map and permission to be at whatever level they are at.

If you have never written a gRPC service, acts one to three are the whole talk
for you and you will leave able to argue the choice in a design review.

If you have shipped on ECS already, act six is the reason to stay. It is the bug
that costs people a day.
-->

---

# Act 1 · The problem

## A sale starts. Traffic goes up 7×.

But **not evenly**. People browse far more than they buy.

| Browsing — searching, scrolling, comparing, abandoning carts | Ordering |
|---|---|
| huge, spiky, **read-only** | small, and it **writes** |

**One application cannot answer this.** To survive the browsing you scale everything — including the part that writes orders, which is the one thing you least want more copies of.

> **Scale the reads. Not the writes.**

<!--
Everyone has lived this from both sides.

Say the important part slowly: traffic does not multiply evenly. People search,
compare, open a product page, abandon a cart. Only a fraction becomes an order.

Two very different workloads on one request path. If that is one application,
surviving the browse load means scaling the order writer too - and copies of a
writer is how you get inconsistency.

No numbers on purpose. I am not Flipkart and I am not borrowing their metrics.
The shape is the argument.
-->

---

## Why not just one application?

Honestly, **for most teams one application is right.** Starting with microservices before you have the problem they solve is a reliable way to get the costs and none of the benefits.

**Good reasons to split**

- **Independent release cadence** — one team ships daily, another weekly. In one codebase the slow team gates the fast one.
- **Independent scaling** — browse load spikes 7×; order volume does not.
- **Different blast radius** — a bad search deploy must not stop people checking out.

**The bill you pay immediately:** a function call becomes a network call that can be slow, fail, or arrive twice. You now need service discovery, retries with idempotency, tracing, versioned contracts, and deploys that don't drop requests.

**Acts 4–6 of this talk are that bill.**

> Split along **team boundaries**, not along nouns. "User" and "Order" being different words is not a reason.

<!--
I want the newer engineers to leave able to push back on this, because they will
be in a meeting about it within a year.

Lead with the unpopular answer. Then be concrete about what actually forces a
split. Independent scaling is our reason here, and it comes straight from the
previous slide.

The right-hand side is the honest price list, and every item on it is a slide
later in this talk.

Land the rule of thumb: split along team boundaries, not nouns. The classic
mistake is seeing that "user" and "order" are different words.
-->

---

# Act 2 · Why gRPC

## Call a function that lives on another machine, as if it were local

The official docs: *"a client application can directly call a method on a server application on a different machine as if it were a local object."*

**Asking REST "who is this user?"**

```go
url := base + "/users/" + id
req, _ := http.NewRequest("GET", url, nil)
req.Header.Set("Authorization", tok)
res, err := client.Do(req)
if res.StatusCode == 404 { /* ... */ }
if res.StatusCode == 401 { /* ... */ }
var u User
json.NewDecoder(res.Body).Decode(&u)
```

**Asking gRPC the same thing**

```go
res, err := users.VerifyToken(ctx, req)
```

> **REST is about resources and verbs. gRPC is about functions and their inputs and outputs.**

<!--
Lead with the definition, not with trivia.

Read the headline, then the official wording - it is the clearest version there
is. Remote procedure call. The idea is decades old; gRPC is a good modern
implementation.

Then let the code do the work. Build a URL, choose a verb, handle 404 and 401
differently, decode JSON and hope the field names still match. Versus one line
with a typed request, typed response and typed error.

The bold sentence is the one to say twice. Everything else - protobuf, HTTP/2,
codegen - follows from that difference.

(gRPC is a recursive acronym for "gRPC Remote Procedure Calls", and the g has
never officially meant Google. Keep that for the Q&A, not the slide.)
-->

---

## You write the contract. A compiler writes the code.

```protobuf
// proto/user/v1/user.proto — you write this, once
service UserService {
  rpc VerifyToken(VerifyTokenRequest) returns (VerifyTokenResponse);
}

message User {
  string id    = 1;
  string name  = 2;
  string email = 3;
}
```

**One command generates both sides:**

- **For the server** — an interface to fill in. Miss a method and it will not compile.
- **For the caller** — a working client, in Go, Java, Python or Node, from the same file.

The numbers are **field tags**, not order. The tag travels on the wire instead of the field name — which is why protobuf is small, why renaming a field is safe, and why **reusing a number is not**.

**The third piece: HTTP/2.** One connection carrying many calls at once — like one home internet cable carrying a video call, a download and a chat simultaneously. *Remember that: in Act 6 it causes the hardest bug in the talk.*

<!--
This makes the previous slide concrete. Someone who has never seen a proto file
now sees one, and it is nine lines.

A service is a list of methods. A method has one input and one output. A message
is a list of fields.

Spend a moment on the numbers because everyone asks. Those are field tags, not
ordering. The tag is what goes on the wire instead of the name, so payloads are
small, renaming is free, and reusing a number is dangerous - an old client reads
the new field as the old one.

Plant the HTTP/2 flag deliberately. The multiplexing is where the speed comes
from, and in act six it is exactly why three tasks end up with one doing all the
work.
-->

---

## gRPC or REST? Usually both — know which, where

| | gRPC | REST + JSON |
|---|---|---|
| Contract | a `.proto`, compiler-enforced both sides | a document, a wiki page, or hope |
| Clients | generated, every language | hand-written, drifting quietly |
| On the wire | binary protobuf — compact | text JSON — larger, readable |
| Errors | typed codes you can branch on | HTTP codes plus whatever the body says |
| **Browser** | **not natively — needs a gateway** | **works everywhere** |
| Debugging | grpcurl, Postman, reflection — not curl | curl, the browser, any tool ever made |
| Caching / CDN | bypasses it entirely | the whole HTTP ecosystem helps you |

**Pick gRPC when:** service-to-service inside your own network · two teams must not drift · real latency budget · polyglot clients · large or numeric payloads · you need push/watch.

**Do NOT pick gRPC when:** a **browser** or third party is the caller · it is a **public API** (Stripe, GitHub, Twilio are all REST) · five endpoints and one consumer · curl-ability matters more than throughput · you want CDN caching · **nobody on the team has run codegen in CI before.**

<!--
People will photograph this to win an argument at work, so be fair to REST.

Do not read every row. Land three: contract, because that is the real reason;
browser, because it is the hard constraint; and caching, because people forget
that choosing gRPC opts you out of the entire HTTP caching layer.

Give the "do not pick" list equal weight. If a browser or an outside developer is
your caller, gRPC is wrong and enthusiasm does not fix it. And that last item is
a real cost you pay in week two.

For us the deciding fact is one sentence: these services talk to each other
inside a VPC, and a browser never touches them directly.
-->

---

## Most people adopt gRPC for the contract — not for streaming

I pulled the real `.proto` files from 14 well-known projects and counted:

| Project | RPCs | Streaming |
|---|---|---|
| Temporal | 121 | **none** |
| Milvus | 154 | 2 |
| TiKV | 73 | 9 |
| Kubernetes CRI | 43 | 7 |
| Qdrant | 30 | **none** |
| containerd | 17 | **none** |
| **Envoy xDS** | **2** | **2 — all of them** |
| **OpenTelemetry OTLP** | **1** | **none** |

**Dropbox, in their own engineering blog:**

> "We settled on gRPC **primarily because it allowed us to bring forward our existing protobufs.** For our use cases, multiplexing HTTP/2 transport and bi-directional streaming were also attractive."

Their framework carries *"hundreds of services, written in different languages, which exchange millions of requests per second."*

**Your first gRPC service will be almost all unary. That is correct.**

<!--
This corrects what most gRPC talks imply, and now it has a primary source rather
than just my counting.

Streaming is the minority nearly everywhere. Temporal: 121 RPCs, zero streaming.
And OpenTelemetry, which everybody calls streaming telemetry, is a single unary
Export call.

Two outliers prove the rule: Envoy's xDS is two RPCs and both are bidirectional,
because a control plane pushing config genuinely is a stream.

Read the Dropbox quote aloud. They run hundreds of services at millions of
requests per second and they say they chose gRPC primarily for the protobuf
contract. Streaming is listed second.

The protos are committed in docs/evidence, so anyone who disbelieves the numbers
can re-run the count.
-->

---

## When do you need a REST gateway?

**You need one when:** a browser or mobile app calls you directly · a partner integrates · your frontend wants an **OpenAPI spec** · someone needs to debug with curl.

**You do not when:** only your own services call you · a BFF already fronts everything and speaks gRPC inward.

Adding one you don't need buys you an extra hop, an extra deployment, and two places for a bug to hide.

**Generate it from the same proto:**

```protobuf
rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse) {
  option (google.api.http) = { post: "/v1/orders" body: "*" };
}
```

Two more buf plugins produce a reverse proxy **and** an OpenAPI spec. Nine REST routes, same source files:

```
POST /v1/users      POST /v1/sessions       GET /v1/users/me
GET  /v1/products   GET  /v1/products/{id}  GET /v1/products:search
POST /v1/orders     GET  /v1/orders         GET /v1/orders/{id}
```

**And the one that isn't there.** `ProductService.CheckAvailability` carries no annotation:

```
POST /v1/products:checkAvailability   ->  HTTP 404
same RPC over gRPC                    ->  AVAILABILITY_INSUFFICIENT_STOCK
```

A client **cannot** ask what the real price is over REST and then submit a different one. Enforced by four *missing* lines of proto, not by a check someone has to remember.

<!--
This is the most common objection in the room, so get ahead of it. A REST gateway
is not a retreat from gRPC - it is an edge concern.

Then do the demo. Show the nine routes, then curl the one that is not there and
get a 404, then call the same RPC over grpcurl and watch it answer.

That contrast is the slide. Same proto, same generation step, and the only
difference is an annotation that CheckAvailability does not have. There is a test
asserting that 404, so exposing it later fails CI.

If asked: grpc-gateway if you want real REST paths and an OpenAPI spec; Connect
if you would rather not run a proxy and can live with RPC-shaped URLs.
-->

---

# Act 3 · Where does it run?

## Four options. One is ruled out by physics, not preference.

| Option | Serves gRPC? | What you operate | Verdict |
|---|---|---|---|
| **Lambda** | **No.** Runs no listening process | nothing | **ruled out** |
| **EC2** | yes | AMIs, patching, ASGs, your own deploy tooling | you'd be building a platform |
| **EKS** | yes — genuinely excellent at it | control plane, node groups, upgrades, CNI, ingress, Helm, RBAC | right answer **later** |
| **ECS Fargate** | **yes** | a task definition and a service | **chosen, for now** |

From the AWS ELB docs, on gRPC target groups:

> "The only supported target types are `instance` and `ip`. … **You can't use Lambda functions as targets.**"

**Why ECS today:** three services need three task definitions · nothing to patch · a task role *is* an IAM role, no IRSA to learn first · a small team can hold it in their head.

**Move to Kubernetes when:** dozens of services and several teams · you need a real mesh (mTLS, traffic shifting, policy) · you want operators, Argo, KEDA · the same manifests must run on-prem — a real requirement in regulated Indian BFSI.

**And moving later is not a rewrite.** The container, the proto, the health check, the graceful shutdown and the load-balancing fix all come with you. Only the YAML changes.

<!--
Give all four a fair hearing - somebody here runs EKS happily and they are not
wrong.

Lambda first, and be precise: this is not me preferring containers. Lambda runs
no listening process, API Gateway strips the framing gRPC needs, and the ALB docs
say gRPC target groups cannot use Lambda. Read that quote. It is a hard stop.

Say clearly that Kubernetes is excellent at this. The cost is honest: a control
plane, node groups, an upgrade every few months, CNI, ingress, Helm, RBAC.

Then the reframe: the question is not serverless versus containers, it is how much
orchestration three services actually need. Both ECS and EKS run this correctly;
one asks you to operate Kubernetes first.

Close on the migration point - it removes the fear of a wrong decision.
"For now" is a legitimate engineering answer. "Forever" rarely is.
-->

---

# Act 4 · Building it

## Three services, one REST door

```
browser ──REST──> gatewayd ──gRPC──> userd      (who is this)        3 tasks
                     │     ──gRPC──> productsd  (list/get/search)    3 tasks
                     │     ──gRPC──> orderd ───────────────────────── 1 task
                     │                  │
                     │                  ├──gRPC──> userd      who is asking?
                     │                  └──gRPC──> productsd  does it exist,
                     │                                        is there stock,
                     │                                        what does it cost?
```

**Every order makes two more hops.** So one order is a three-span trace, and two places where load balancing can go wrong.

**`orderd` holds no signing key and no prices.** It asks `userd` who you are and `productsd` what things cost. Both are real trust boundaries.

`userd` is called by `gatewayd` **and** `orderd` — which makes it the busiest service, and the one we scale.

<!--
Walk it left to right. A browser talks to gatewayd over JSON. gatewayd talks to
everything else over gRPC. That boundary is the answer to "my frontend needs
REST".

Then the important part: every single order makes two more calls. Who is making
this request, because orderd holds no signing key. And what does this actually
cost.

That second question is the one to dwell on - the create-order request has no
price field at all. The client does not get a say in what it pays.

Note the footer: userd is called by the gateway and by orderd, so it is the
busiest thing here. That is why it is the service we scale later, not an
arbitrary pick.
-->

---

## Four decisions visible in the proto

```protobuf
message RequestedItem {
  string product_id = 1;
  int32  quantity   = 2;   // ← no price field. At all.
}

message CreateOrderRequest {
  repeated RequestedItem items = 1;
  string idempotency_key       = 2;
}
```

- **No price field.** The client cannot influence what it pays. `orderd` asks `productsd` and uses *that* number. A test asserts the total is computed from catalogue prices, so this stays true.
- **`idempotency_key` is required.** A retry must not place a second order. Any order API without one is broken.
- **Enums, not strings** for status and rejection reason — a client cannot invent one, and the contract documents itself.
- **A rejection returns `OK`** with a `REJECTED` order. Out-of-stock is a business outcome, not a transport error. Make it an error and every client ends up string-matching messages.

<!--
Four decisions I would defend in review.

The missing price field is the one to lead with. If the client sends a price, you
have to decide whether to trust it, and someone eventually will. Removing the
field removes the question.

idempotency_key is required, not optional. The client will retry - mobile
networks guarantee it - and without a key you charge twice.

The last one people argue about. A rejection comes back OK with a REJECTED
status. If you make out-of-stock a gRPC error, clients cannot tell it apart from
the database being down, and they resort to matching error strings.
-->

---

## "Can I scale this?" Storage decides. Not traffic.

| Shape | Services | What it means | Tasks |
|---|---|---|---|
| **Baked into the image** | `userd`, `productsd` | the SQLite file — and the search index — are built at *image build time*, so every task ships a byte-identical copy and all answer reads the same | **3** |
| **Empty and writable** | `orderd` | starts empty, written at runtime on the task's own filesystem. Three tasks = **three different databases** | **1** |
| **Nothing at all** | `gatewayd` | no database, no `/data`, not even a DB driver in the image | **3** |

```
$ terraform plan -var order_desired_count=3

Error: Invalid value for variable
  orderd keeps its database on the task filesystem, so N tasks would mean
  N divergent databases. Scale userd or productsd instead.
```

**The rule is enforced in Terraform**, not written in a wiki nobody reads.

*SQLite here is a demo shortcut — it takes the AWS apply from ~10 minutes to ~2. In production `orderd` uses RDS, and the moment the state leaves the task the constraint disappears.*

<!--
This slide makes the scaling argument concrete and I would not cut it.

Three services, three different answers to "can I add more copies", and the
answer is decided entirely by where the data lives.

gatewayd stores nothing - not even a /data directory. I had to fix that, because
it was inheriting one from a shared Dockerfile. Worth mentioning: cruft in a
production image is a real finding, not a nit.

Then run the terraform plan and let people read the refusal. A validation rule
means somebody who tries to scale the writer gets told why. Better than a
comment.

Be straight about SQLite: a shortcut to keep the apply fast. RDS is what you
would run.
-->

---

# Act 5 · Running it

## You do not need the emulator to develop

| Loop | Command | Restart | Catches |
|---|---|---|---|
| **1. `go run`** | `make dev-userd` + `dev-productsd` + `dev-orderd` | ~2s | all your business logic, rules, auth, the contract |
| **2. docker build** | `make images` | ~30s | CGO creeping in, file ownership, architecture |
| **3. emulator** | `make local-up && make tf-local-apply` | ~60s | task definitions, env wiring, IAM, secret resolution |
| **4. real AWS** | `terraform -chdir=terraform/envs/aws apply` | ~2min | everything the emulator does not model |

**Use the cheapest loop that can catch your bug.** Most of the week is loop 1 — three terminals, SQLite files in `./data`, and the real gRPC hops between the services.

### The local emulator had a plot twist

LocalStack retired its free Community edition in **March 2026** — and ECS was never in the free image anyway. I listed `localstack/services/` at tags v1.4.0, v2.3.2, v3.8.1 and v4.0.0: **no `ecs`, no `elbv2`, no Cloud Map at any of them.**

So this uses **Ministack** (MIT, free). Its ECS is 3,563 lines that really launch containers, and it also emulates ECR, Cloud Map, Secrets Manager and Bedrock.

<!--
Somebody always asks whether you need the emulator to develop. No - and reaching
for the heaviest tool first is the most common way to make yourself miserable.

Loop one is where I spent most of the week. Two seconds per change. Every
business-logic bug is findable there.

Loop two catches a different class: things about the image. Loop three catches
infrastructure. Loop four catches what the emulator lies about - and two of my
real bugs lived there.

Then the LocalStack story. Make clear I checked rather than assumed: I listed the
open-source service directory at four tags going back years. ECS was never
there. So "use an older image" does not work either.

Ministack: I read its ECS implementation before trusting it. Three and a half
thousand lines that drive the Docker daemon, with a real rollout state machine.
Not an API-shaped stub.
-->

---

## One module set. Two targets. One differing file.

```hcl
# terraform/envs/local/provider.tf
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true

  endpoints {
    ecs = "http://localhost:4566"
    ecr = "http://localhost:4566"
    iam = "http://localhost:4566"
    # ... 8 more
  }
}
```

```hcl
# terraform/envs/aws/provider.tf
provider "aws" {
  region = "ap-south-1"
}
```

**That is the whole difference.** `terraform/stack/` — network, ECR, cluster, four services, secrets, IAM — is shared verbatim and has no idea which environment it is in.

The emulator exists so the **feedback loop is seconds**, not a five-minute round trip to a real account.

<!--
This is the slide the whole talk is built around, so slow down.

Put the two files side by side and let the room read them. Local has fake
credentials, some skips, and an endpoints block pointing at a port on this
laptop. AWS has a region.

Everything above the provider - the VPC, the cluster, four task definitions, the
IAM roles, the secret - is one shared stack directory. It does not know which
environment it is running in, because the provider decides where the API calls
land.

The payoff is the loop. I can destroy and re-apply the whole thing in under a
minute, offline, as often as I like. You cannot iterate on infrastructure if
every attempt is five minutes and a few cents.

Pedantic honesty: two flags do differ, and the Cloud Map one has a reason I will
mention. They are variables, like the image tag - not different code.
-->

---

## 🔴 Demo 1 — "How is that different from docker compose?"

```
make tf-local-apply     # terraform apply -> four ECS tasks
make ps                 # seven escalating proofs
```

1. **Control plane** reconciles desired vs running
2. Tasks carry a **task-definition revision** — deploys are immutable revisions, not restarts
3. `networkMode: awsvpc`, `FARGATE`, `ARM64`, an execution role
4. The task **self-describes** via `ECS_CONTAINER_METADATA_URI_V4` — *nothing in my code sets that*
5. `AWS_CONTAINER_CREDENTIALS_FULL_URI` — **this is how task roles deliver credentials.** No key anywhere.
6. `secrets[].valueFrom` is an **ARN**, so the secret never entered git or the image
7. Log in as each seeded user — the baked database is identical on every task

**Honest about the emulator:** `healthStatus` comes back `UNKNOWN` and `LaunchType` as `None`, even though the task definition requests `FARGATE`. Real ECS reports both. Say it before someone reads the table.

<!--
Run make ps and talk over it.

Step 4 is the moment. The task asks the platform who it is and gets a real
cluster ARN, task ARN, family and revision. Nothing in my code sets that
variable - the ECS agent injects it. A compose service has no such thing.

Step 5 is the one I would build the segment around. Those two environment
variables are how a task role actually works: the AWS SDK reads them itself and
gets temporary credentials. That is the entire "no API keys on ECS" story,
visible in a docker inspect.

Then admit the gaps in the same breath. Naming the emulator's limits yourself is
more credible than being caught by them.
-->

---

## 🔴 Demo 2 — Browse, then buy

```
make demo        # gRPC, through the ECS tasks
make dev-rest    # the same thing over plain curl
```

```
== browse: search the catalogue (no auth; the read path that scales) ==
   2 matches: Wireless Noise Cancelling Headphones, Noise Cancelling Earbuds Ultra
== order placed: two hops, and the client sent NO prices ==
   ORDER_STATUS_CONFIRMED  total=36997.0 INR  (2 lines priced by productsd)
== idempotent replay ==            same order returned (replay=True)
== rejected: out of stock ==       REJECTION_REASON_OUT_OF_STOCK
== rejected: one left, asked 3 ==  REJECTION_REASON_INSUFFICIENT_STOCK
== no token ==                     Unauthenticated, via the userd hop
```

- **Search is real** — SQLite FTS5 with relevance ranking and prefix matching, pure Go, static binary. No Elasticsearch.
- **Idempotency has two layers** — a pre-check *and* a fallback to the stored row on unique-index violation. The pre-check alone loses the race when two retries arrive together.
- **`grpcurl` needs no `.proto`** — reflection means it discovers everything from the running task. So does Postman.

<!--
Run make demo, then do one call by hand in Postman so people see a human-facing
client. Both services register reflection, so Postman introspects rather than
importing a proto file.

Two things to call out while it runs.

Idempotency: there is a pre-check and a catch on the unique-index violation that
returns the stored row. The pre-check alone is a race - two retries arriving
together both pass it. That is the bug that charges a customer twice.

And the catalogue is the only source of prices. The request had no price field,
so those line items were priced by productsd on the second hop.
-->

---

# Act 6 · What bites in production

## Three tasks. One of them gets everything.

```
                                 ┌──> userd task 1   ← 100% of traffic, saturated
  orderd ──1 connection──────────┤
         (round_robin configured)├ ─ ─  userd task 2   idle
                                 └ ─ ─  userd task 3   idle

                          Cloud Map returns all three. DNS is fine.
```

**The cause:** grpc-go defaults to **`pick_first`**. It resolves the target, connects to **one** address, and multiplexes every RPC over that single HTTP/2 connection.

**Why it fools everyone:** nothing is broken. DNS resolves correctly, all three tasks are healthy, the dashboard is green — and your p99 is awful.

*This hits unary traffic hardest. Nothing here involves streaming.*

<!--
This is the slide I would keep if I had to throw away all the others.

The setup everyone walks into: you scale to three tasks, Cloud Map returns three
A records, every task is healthy, and one task is on fire while two sit idle.
People spend days on DNS, on Cloud Map, on health checks. None of it is broken.

The cause is one line of default behaviour. grpc-go uses pick_first: resolve,
connect to one address, send everything there. HTTP/2 multiplexes, so one
connection happily carries thousands of concurrent RPCs - which is exactly why
you never notice the connection is the bottleneck.

Say the last line clearly: this is connection-level, so it hits unary traffic
hardest. People assume load-balancing problems are a streaming concern.

Pause here before the fix. Let the room sit in the problem.
-->

---

## The fix is two halves. One alone does nothing.

```go
// ① client — balance per RPC, and resolve every A record
grpc.NewClient("dns:///userd.ecom.local:50051",
    grpc.WithDefaultServiceConfig(
        `{"loadBalancingConfig":[{"round_robin":{}}]}`))

// ② server — recycle connections so clients re-resolve after a scale-out
grpc.KeepaliveParams(keepalive.ServerParameters{
    MaxConnectionAge:      30 * time.Second,
    MaxConnectionAgeGrace: 5 * time.Second,
})
```

**The trap inside the fix:** a bare `host:port` uses the **passthrough** resolver, which yields exactly **one** address. `round_robin` then has nothing to balance over — you have "fixed" it with no effect. The `dns:///` prefix is not decoration, and there is a unit test pinning it.

**Why the server half is needed:** without `MaxConnectionAge`, a client that connected *before* you scaled out never re-resolves, so it never learns the new tasks exist — whatever policy it uses.

**Also worth knowing:** ECS **Service Connect** does per-request balancing via a sidecar, with `appProtocol: grpc` on the port mapping. On an **ALB**, use `least_outstanding_requests`, not round-robin — with multiplexed HTTP/2, a connection count tells you nothing about task load.

<!--
Both halves. This is where people half-fix it and conclude gRPC is broken.

The client half asks for round_robin. Fine. But the trap underneath is the
resolver: a bare host:port makes grpc-go use passthrough, which gives you one
address. round_robin over one address is round_robin over nothing. You will have
added the service config, seen no change, and moved on. There is a unit test
pinning the dns:/// prefix precisely because it is so easy to lose in a refactor.

The server half is the one almost nobody mentions. A client running for an hour,
then you scale from one task to three - it already has its connection and has no
reason to re-resolve. MaxConnectionAge forces a fresh lookup.
-->

---

## Deploys nobody notices, and health checks that mean something

**Graceful shutdown, in this order.** ECS sends SIGTERM, waits `stopTimeout`, then SIGKILLs.

1. **Fail health first** — flip to `NOT_SERVING` so load balancers stop sending new work *before* you stop accepting it. This is what makes a deploy invisible.
2. **Drain with a deadline** — `GracefulStop` raced against a timeout. In-flight RPCs finish.
3. **Then force it** — out of time? `Stop()`. Deliberate beats SIGKILLed mid-write.

> Your drain deadline **must be below** the task's `stopTimeout`. Here: 15s inside 30s. Backwards, and the drain is cut off anyway.

**Health checks live at three layers:** `grpc.health.v1` in-process · the container `healthCheck` · the ALB target group.

Distroless has **no shell, no curl, no grpc-health-probe** — so the probe is a 20-line Go binary baked into the image, reusing the gRPC client already compiled in. It dials `passthrough:///`, because it always checks its *own* container and DNS balancing would be actively wrong there.

*ALB gotcha: a gRPC target group requires an **HTTPS listener** → ACM → a certificate → a domain. Find that out early.*

<!--
Step one is the one people miss. If you stop accepting connections before you
fail your health check, the load balancer is still sending you work you now
refuse. Flip the status first, let the LB notice, then drain.

The ordering number is the practical trap: the drain deadline has to be less than
stopTimeout. Fifteen inside thirty. Backwards and ECS kills you mid-drain, so all
the careful code achieves nothing.

There is a test for this: it starts an RPC, cancels the context underneath it, and
asserts the call still completes. Without GracefulStop it fails with "transport is
closing" - and that error message is exactly what your users see during a deploy.

On health: the usual reaction to distroless is to reach for a bigger base image.
Twenty lines of Go using the client already in the binary is cheaper.
-->

---

## 🔴 Demo 3 — Scale out. Break it. Fix it.

```
make scale N=3          # userd 1 -> 3 tasks
make demo-load
```

1. Three tasks healthy. Watch per-task metrics: **one** is doing all the work.
2. Add the two-line fix. Redeploy. Traffic spreads across all three.
3. Kill a task mid-load. **Zero** failed RPCs — graceful shutdown working.
4. Then kill **`orderd`**. The orders are gone. State on the task, as promised.

> **Use a seeded user.** A user created by `Register` exists on exactly **one** `userd` task. At three tasks, two have never heard of them — it fails intermittently and looks precisely like the load-balancing bug you just fixed. I nearly shipped that into this demo.

**Scale `userd` or `productsd`. Never `orderd` — Terraform will refuse.**

<!--
Rehearse this one most: most moving parts, biggest payoff.

Order matters. Show the broken state first with per-task metrics on screen, so
the room sees one task working and two idle. Then apply the fix and show traffic
spread. Then kill a task under load and show zero errors.

The fourth beat is the closer: kill orderd and the orders vanish. That lands the
storage-shapes slide, and it is the thing people remember on the train home.

The box is a mistake I nearly made, so tell it as a mistake. Register a fresh
user, scale out, and two of three tasks do not know them - failing intermittently
in a way identical to the bug you just fixed. Always authenticate as a seeded
user past one task. It is a rule in the runbook now because I would forget it
under stage pressure.
-->

---

## Four bugs that only a real deploy found

| Bug | Why local testing missed it |
|---|---|
| **OTel schema mismatch crash-looped every task** | `resource.Merge` rejects a resource whose schema URL differs from the SDK's. **Invisible locally** — with no OTLP endpoint the tracer short-circuits to a no-op and never builds a resource. |
| **`docker ps` ORs multiple `--filter name=`** | So my DNS helper aliased *orderd's* container as `userd` — a silent misroute that would have broken the demo in a baffling way |
| **`count` can't depend on a resource attribute** | The Cloud Map namespace doesn't exist at plan time, so Terraform refuses |
| **`grpc.NewClient` is lazy** | `orderd`'s *first* RPC paid for resolution and handshake and failed fast. **That's the first request after every deploy.** Fixed with a `Warm` step before serving. |

Not one would have been caught by unit tests or `terraform validate`.

> **The emulator buys a feedback loop, not confidence. You still deploy before you present.**

<!--
I debated including this and then decided it is the most honest thing in the deck.

The OTel one is the best lesson. A semconv version mismatch meant resource.Merge
refused, and both tasks crash-looped on startup. Completely invisible locally,
because with no OTLP endpoint my code returns a no-op tracer and never builds a
resource at all. The bug was hiding behind a perfectly reasonable optimisation.

The docker filter one is almost funny, and it would have produced the most
confusing possible failure right in the middle of the load-balancing demo.

The lazy-client one is the most generally useful: grpc.NewClient does not connect.
Your first request after a deploy pays for the handshake, and if the upstream is
not up yet it fails. A one-line warm-up before you start serving fixes it.

So: the emulator buys speed. It does not buy confidence. I deployed before
standing up here, which is why I am describing these rather than discovering them
live.
-->

---

# Act 7 · What to take away

1. **You cannot serve gRPC from Lambda.** ALB gRPC target groups accept only `instance` and `ip`. That is the whole why-ECS argument, and it needs no streaming.
2. **grpc-go defaults to `pick_first`.** Three tasks, one connection, 100% of traffic. Fix it on **both** sides — `round_robin` with `dns:///` on the client, `MaxConnectionAge` on the server.
3. **gRPC is for a typed contract, not for streaming.** Measured across 14 projects, and Dropbox says so themselves. Your first service will be 90% unary and that is correct.
4. **Storage decides what can scale**, not traffic. Baked → scale freely. Writable → stay at one. Nothing → scale easiest.
5. **One module set, two providers.** If "local" and "production" are different Terraform code, you are testing something you will never deploy.
6. **Swap at the boundary, never with an `if`.** Database driver, LLM provider, REST-vs-gRPC — one interface, one env var, **no `if local` branch anywhere in the codebase**.
7. **The emulator buys a feedback loop, not confidence.** Deploy before you present.

<!--
I would be happy if people took two: number one and number two.

Number one is the argument that gets gRPC on ECS approved in a design review.

Number two is the bug that will cost somebody in this room a day.

Number six is the one I became most opinionated about while building this. Several
places in the codebase choose between a local and a cloud implementation - the
database driver, the LLM, the Bedrock endpoint. None of them has a conditional.
They each have an interface and an environment variable. That discipline is what
makes "test locally, deploy to cloud" true rather than aspirational.
-->

---

# Clone it and break it yourself

## `github.com/lakhansamani/grpc-ecs-demo`

```sh
make dev-seed        # loop 1: no docker, no emulator
make dev-userd       # terminal 1
make dev-productsd   # terminal 2
make dev-orderd      # terminal 3
make dev-smoke

make local-up && make tf-local-apply   # loop 3: real ECS tasks
make ps                                # prove it is ECS
make demo                              # the business flow
make api-coverage                      # all 10 RPCs + all 9 REST routes
```

| | |
|---|---|
| `SPEC.md` | every decision, each claim marked verified or not |
| `docs/RUNBOOK.md` | step by step, and what the emulator cannot do |
| `docs/AWS_PERMISSIONS.md` | the exact IAM policy, and what you can skip |
| `docs/evidence/` | the 14 projects' protos — re-run the count yourself |

**Thank you.** Questions?

<!--
Close on the repo, not a thank-you slide nobody reads.

Point out that the evidence folder is deliberate: the fourteen protos are
committed, so anyone who thinks my streaming numbers are wrong can re-count. That
is the difference between a claim and a measurement.

Likely questions, short answers:

Why not EKS? For three services you are paying a control plane and an upgrade
cycle for scheduling you are not using.

Why not Service Connect? It does per-request balancing for you and it is the right
answer on AWS - but it is not emulated locally, and I wanted the client-side fix
visible because that is what you will actually debug.

Why SQLite in a demo about ECS? Because it removed ten minutes from every apply.
The closing beat shows exactly why you would not ship it.

Is Ministack production-ready? Wrong question - it is a development tool. The
question is whether it is faithful enough to catch your mistakes, and for task
shape and secrets injection it was.
-->
