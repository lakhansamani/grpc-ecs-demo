# Presenter deck

Companion to [`PRESENTATION.md`](PRESENTATION.md). One section per slide: what
to say, what to type, and what to do when it breaks.

## Pick your runtime first

The deck is now **69 slides**: 64 of talk plus a 5-slide **command-reference
appendix** you never present — it is there so you can flip to it mid-demo
instead of fumbling, and so people can read it after.

Of the 64, slides 40–52 are the **guided walkthrough** (Part 7): what to open,
in what order, with the commands. Those are reference-dense on purpose. **You
will not show all of them.** Pick a lane:

| Lane | Show from Part 7 | Roughly |
|---|---|---|
| **Code-first audience** | A, B, C, then E, F1, F3 | 20 min |
| **Infra-first audience** | D, E, E2, G1, G3 | 20 min |
| **Mixed / default** | A, C, D, E, F1, F2, G1, G2 | 25 min |

Everything outside Part 7 is the narrative and runs ~35 minutes. So:
**narrative + one Part-7 lane ≈ 55-60 minutes.** For a 40-minute slot, use the
cut list below and show only E, F1 and G1.

Six slides are deliberately fast: `[ASK]` slides are audience questions (20–30
seconds, but **do not advance until someone answers**) and `[BEAT]` slides are
single lines you let land (10 seconds).

| Slot | Path | Drop |
|---|---|---|
| **55 min** | Everything | — |
| **40 min** | Core | 3, 13, 25, 31, 39 · and cut `make dev-rest` from Demo 2 |
| **30 min** | Spine | Also 15, 19, 24, 34, 36 · and Demo 3 (describe it) |

**Never drop:** the promise, the use-case diagram, the boundary test, the
streaming ASK + reveal, "no price in the request", the every-AWS-component
table, the one-pipeline `diff`, walkthrough E, and the whole `pick_first`
payoff (setup → ASK → 120/0/0 → the fix) plus the closing line.

**The 20-minute spine:** the promise → use-case diagram → three services →
no price in the request → every AWS component → the `diff` → walkthrough E →
the promise revisited → ASK → 120/0/0 → the fix → green dashboards.

> Slide numbers shifted when the walkthroughs and the health-check slides went
> in. The per-slide sections below are keyed by **title**, so match on the
> title, not the number.

Timings below are for the **55-minute** run.

**Before you walk in:** [§0 Pre-flight](#0--pre-flight) · [§Panic buttons](#panic-buttons).

---

## 0 · Pre-flight

Run this **the night before**, and again 20 minutes before you present.

```sh
cd ~/projects/grpc-ecs-demo

# clean slate
docker compose down -v
docker ps -aq --filter "name=ministack-ecs-" | xargs -r docker rm -f
docker ps -aq --filter "name=forward-"       | xargs -r docker rm -f
docker network rm ecom-dns ecom-infra 2>/dev/null
rm -rf data

# bring it up
make test              # expect 8 packages ok
make local-up          # emulator :4566, jaeger :16686, prometheus :9090
make images            # four ARM64 images
make tf-local-apply    # ECS tasks; ends with `make wait-ready`
make forward           # publish ports for Postman / grpcurl

# prove the demos work BEFORE the room is watching
make ps
make demo
make dev-rest
make api-coverage      # expect 20/20
```

**Render the two diagrams.** Marp does not render Mermaid. Either present
slides 6 and 22 from the GitHub tab, or screenshot them and paste the images in.
Decide now, not on stage.

Tabs to leave open:

| Tab | URL | Used in |
|---|---|---|
| GitHub repo (for the diagrams) | `github.com/lakhansamani/grpc-ecs-demo` | Slides 6, 22, 37 |
| Jaeger | `http://localhost:16686` | Demo 2, optional |

**Terminal:** font 18pt+, notifications off, two tabs.

**If you are doing Demo 3 live on AWS, deploy it the day before** and demo a
read-only `make ps-aws`. Never run a cold `apply` on venue wifi.

---

## Slide 1 · Title

**1:00**

> "I'm going to take three Go services from `localhost` to AWS Fargate. And the
> Terraform that does it is the same whether it runs on my laptop or in a real
> AWS account — one provider block apart.
>
> One promise: **everything here was run, not sketched.** Every number came out
> of a terminal in this repo. Where something is a shortcut, I'll say so."

---

## Slide 2 · Before we start, a promise

**1:15 · 2:15 · NEVER CUT**

**This is the hook for the entire talk.** You are opening a loop you close on
slide 45. Deliver it slowly and do not explain it yet.

> "Before anything else, let me tell you where we're going.
>
> Later I'll show you a system where three servers are running. All three
> healthy. DNS is correct — it hands back all three addresses. And one of them
> is doing a hundred percent of the work.
>
> No error. No log line. No failed health check. Nothing in your dashboards."

Pause. Then make it personal — this is what buys their attention:

> "I lost most of a day to this. By the end of this talk you'll recognise it in
> about ten seconds. Everything in between is how we get there."

---

## Slide 3 · Where we are going

**0:45 · 3:00 · 🔶 first to cut**

> "First three parts need no gRPC knowledge. Part six — the Terraform — is the
> heart of this talk."

A map, not content. Don't dwell.

---

## Slide 4 · It is sale season

**1:15 · 4:15**

> "It's sale season. Big Billion Days, Great Indian Festival — whichever one is
> running right now. Honestly, how many of you have a tab open?
>
> Midnight. The banner goes live. And everybody opens the app at the same
> moment."

Ask and **actually wait for hands.** Five seconds; it buys you the room.

---

## Slide 5 · [ASK] Think about your own last sale

**0:45 · 5:00**

**Do not advance until you get answers.** Ask it as two separate questions and
let the gap between the numbers do the work.

> "Quick question, and be honest. Your last sale — how many products did you
> *open*?" *(wait, take a couple of shouted numbers)*
>
> "And how many did you actually *buy*?" *(wait — the answers are small)*

> "Hold that ratio. We're about to build the architecture it implies."

---

## Slide 6 · That ratio, as a table

**1:15 · 6:15**

Walk the rows; the last one is the point.

> "Search, scroll, compare, add to cart and wander off. And then, far less
> often, somebody buys."

**Say why there are no third-party numbers here:**

> "I'm not putting somebody else's traffic graph on a slide. I don't have
> Flipkart's numbers, and I don't need them — your own browsing is better
> evidence, because you actually trust it."

---

## Slide 7 · Suppose the store is one application

**1:30 · 7:45**

Be generous to the monolith. People here ship monoliths, and some should.

> "One deployment. Traffic multiplies, you scale up, and **it works.** This
> isn't a story about anybody being stupid."

Then the three costs. The middle one lands hardest with senior people:

> "You're scaling checkout in order to survive search. A slow catalogue query
> and a checkout bug share one deploy, one rollback, one on-call page. And you
> can't tune them separately — same process."

---

## Slide 8 · [BEAT] Two workloads. One deploy button.

**0:10 · 7:55**

Say it. Stop. Let it sit for two seconds. Advance.

---

## Slide 9 · The overall picture

**1:30 · 9:25 · NEVER CUT**

GitHub tab (or your screenshot). **Trace it with your finger.**

> "Shopper, three things they do. Two are reads — browse, and log in. One is a
> write — place an order. Teal reads, orange writes.
>
> And see this bit: placing an order makes `orderd` turn around and ask the
> other two — who is this, and what does this cost.
>
> Down here is me, with Terraform — one pipeline pointing at my laptop *or* at
> production. That's part six."

---

## Slide 10 · So: three services

**2:00 · 11:25 · NEVER CUT**

Answers "why not a monolith" *and* "why not twelve services" at once.

**Give them the test slowly** — it's the thing they can use on Monday:

> "How do you know that's the right boundary? A test you can use at work. Ask:
> would these ever need a different number of copies, a different deploy
> schedule, or a different on-call owner?
>
> **No to all three — it's one service.** Put it back."

Then pay the cost out loud, so nobody thinks you're selling microservices:

> "Every split costs you a network hop, a failure mode, and another thing to
> deploy. Three is what *this* use case pays for. If yours pays for two, build
> two."

---

## Slide 11 · But now they have to talk

**0:45 · 12:10**

> "One order needs two questions answered by other services. Inside one app
> those were function calls. Now they cross a network. So — how should services
> call each other?"

---

## Slide 12 · What gRPC is

**1:30 · 13:40**

**Lead with the definition. Never the joke.**

> "gRPC lets you call a function that lives on another machine, as if it were
> local. You write the function down — name, inputs, outputs — in a file, and a
> compiler turns that into real code for both sides."

*Then* the acronym, as a throwaway:

> "The `g`… officially stands for 'gRPC'. The acronym contains itself. They
> reassign it every release as a joke."

---

## Slide 13 · The mental shift

**1:30 · 15:10 · 🔶 second to cut**

Read both blocks aloud. The contrast does the work.

> "REST makes you think about resources and verbs — what's the right noun, POST
> or PUT, which status code. gRPC makes you think about functions — what does
> it take, what does it give back."

🔶 If cutting, say that one sentence on slide 12.

---

## Slide 14 · You write the contract, a compiler writes the code

**1:30 · 16:40**

> "This proto file is the only hand-written interface code in the project. One
> command turns it into five things."

The line that matters most to someone junior:

> "Forget to implement an RPC and the Go code **will not compile**. That's not a
> runtime 501 you find in production — it's a build failure on your laptop."

---

## Slide 15 · Let me be honest about performance

**1:30 · 18:10 · 🔶 cut at 30 min**

**This slide buys credibility for the whole talk.** Someone here has read "gRPC
is 7x faster" and is ready to repeat it or challenge you.

> "The advantages are real and structural: binary Protobuf instead of JSON, one
> multiplexed HTTP/2 connection instead of one per call, headers not re-sent
> every time.
>
> **But** — for most internal services the network and your database dominate.
> And I haven't benchmarked this repo, so I'm not putting a speed-up number on a
> slide and having you quote me."

> "Performance is a genuine benefit. It's just not usually *why* teams switch."

---

## Slide 16 · [ASK] So people say gRPC is for streaming

**0:45 · 18:55 · NEVER CUT**

**The best participation beat in the talk. Do not rush it, and do not answer
your own question.**

> "People will tell you gRPC is for streaming. I wanted to know if that's true,
> so I counted every RPC in the real proto files of 14 projects you've heard of.
> Temporal, etcd, containerd, Kubernetes CRI, Milvus, Qdrant, TiKV, Dapr,
> CockroachDB, Vitess, Thanos, Bazel, Envoy, OpenTelemetry.
>
> Six hundred and eleven RPCs. **What percentage use streaming?**"

Take three or four shouted guesses. People usually say 30–60%. **Let the wrong
answers happen** — that is the whole point.

---

## Slide 17 · About 8%

**1:45 · 20:40 · NEVER CUT**

> "Eight percent. Fifty out of six hundred and eleven.
>
> Temporal: 121 RPCs, **zero** streaming. Qdrant: 30, zero. containerd: 17,
> zero."

Then invite the check — strongest thing you can say:

> "The protos are in the repo under `docs/evidence`. The counts reproduce with
> one `grep`. **Please go check me.** I'd rather you did."

Second conclusion:

> "And not one of these 14 exposes gRPC to the public internet. So when someone
> asks 'should our mobile app speak gRPC?' — the industry's answer is mostly no."

---

## Slide 18 · A browser cannot speak gRPC

**1:30 · 22:10**

Be precise; this is a factual claim people will test.

> "Not a configuration problem. A browser can't open a raw HTTP/2 connection and
> control trailers the way gRPC needs.
>
> So something has to translate. Three real options — and I want to show you all
> three, because most talks show one and call it the answer."

Read the Connect quote off the slide. It sets up the next slide.

---

## Slide 19 · So do we actually need `gatewayd`?

**2:00 · 24:10 · 🔶 cut at 30 min**

**The slide answers in one word. Say it, then justify.**

> "**No.** It's a choice.
>
> I picked the gateway-process option because it makes the lesson *visible* —
> `gatewayd` is a separate ECS service with its own task definition, so you watch
> a stateless service deploy next to stateful ones. Useful for a talk about ECS.
>
> But if I were starting a product today? Option three is very attractive. One
> port, no extra hop, nothing extra to operate. The TypeScript client in this
> repo already uses Connect."

Then when it *does* earn its keep, so you're not dismissing your own design:

> "A gateway still earns its place when you want one public door to audit and
> rate-limit, or another team owns the services and they must stay plain gRPC."

> **Follow-up to expect:** *"so why not rewrite it with Connect?"* — the services
> use standard `grpc-go`, which is what most teams have, and every ECS lesson is
> identical either way.

---

## Slide 20 · And do we need REST for every API?

**1:30 · 25:40**

> "**Also no.** Nine of our ten RPCs have a REST route. One doesn't, on purpose.
>
> `CheckAvailability` is how `orderd` prices a cart. Nothing outside should call
> it. So it has no HTTP annotation — works over gRPC, 404 over REST. **The proto
> is the access-control decision**, and you can read it in a code review."

The nuance is worth the extra twenty seconds:

> "And `VerifyToken` is *both* — it's `GET /v1/users/me` for the browser, and the
> internal hop `orderd` makes on every order. Same RPC, two callers. The
> annotation decides who can *reach* it, not who it's *for*."

Give them the rule:

> "A REST route exists for a client you **don't control**. Expose exactly those."

---

## Slide 21 · Four options, one ruled out

**2:00 · 27:40**

Give all four a fair hearing — somebody here runs EKS happily.

> "Lambda first, and let me be precise, because this isn't me preferring
> containers. gRPC needs a process that **stays listening**, holding an HTTP/2
> connection. Lambda has none."

**Read the quote off the slide, word for word.** It's a hard stop, not an
opinion.

---

## Slide 22 · Why ECS for now

**1:30 · 29:10**

> "The question isn't serverless versus containers. It's: how much orchestration
> do four services actually need? Both ECS and EKS run this correctly. One asks
> you to operate Kubernetes first."

Then remove the fear of a wrong decision:

> "Moving later is **not a rewrite**. The container, the contract, the health
> check, the graceful shutdown — all of it comes with you. Only the YAML changes.
>
> 'For now' is a legitimate engineering answer. 'Forever' rarely is."

---

## Slide 23 · [ASK] A question about Fargate

**0:30 · 29:40**

> "Your container is running on Fargate. There's a kernel underneath it. **Who
> patches that kernel?**"

Most rooms say "AWS" quickly. Then the one they haven't thought about:

> "Right. And **what happens to your running task when they do?**"

Let the silence sit. Then advance.

---

## Slide 24 · What Fargate actually gives you

**2:00 · 31:40 · 🔶 cut at 30 min**

**This is where an experienced person decides whether you actually know
Fargate.** Don't just say "serverless containers".

> "AWS owns the **platform version** — and AWS defines that as a combination of
> the kernel and the container runtime versions. So yes, AWS patches it."

Then read the quote off the slide:

> "If a security issue is found, AWS creates a patched revision **and retires
> tasks running on the vulnerable revision.** A task never upgrades in place — a
> *new* task gets the new revision."

And the correction that matters:

> "But you still own everything *inside* your image. Your base image, your
> packages, your CVEs. **Serverless does not mean nobody patches** — AWS patches
> their half, kills your task to do it, and you still patch yours."

Then plant the callback:

> "Which is why graceful shutdown isn't optional. Remember that — it comes back."

---

## Slide 25 · Three words

**0:45 · 32:25 · 🔶 third to cut**

Don't skip for a mixed room. 45 seconds, and it stops people silently falling
behind.

> "Task: one container with its own IP. Task definition: the recipe, and it's
> versioned — editing it makes revision 2. Service: keeps N tasks alive and
> replaces the dead ones."

---

## Slide 26 · Three services, one contract

**1:30 · 33:55**

GitHub tab. Trace it.

> "Browser in over REST to `gatewayd`. `gatewayd` speaks gRPC to all three. The
> thick arrows are the interesting ones — `orderd` calling the other two on every
> order."

The line to land:

> "**Nobody in this diagram knows anybody's IP address.** They dial names. That's
> slide 35."

---

## Slide 27 · The request has no price in it

**1:15 · 35:10 · NEVER CUT**

> "The client sends *what* and *how many*. `orderd` asks `productsd` what it
> costs."

Make it concrete and slightly alarming — the sale framing does the work:

> "Think about the alternative during a sale. If the client sends the price, then
> a client can ask 'is this ₹2,000 sale price real?' — and then submit ₹200."

---

## Slide 28 · [ASK] You tap "Buy". The spinner spins.

**0:45 · 35:55**

A scenario everyone has lived. **Ask it and wait.**

> "Midnight, sale traffic, patchy 4G. Your phone sends the order and the response
> never comes back. So your phone retries — reasonably, it has no idea whether
> the server got it.
>
> **Did you just buy one phone, or two?**"

Hands or shouts. Someone will say "two", someone "depends". Both are useful.

---

## Slide 29 · That is what an idempotency key is for

**1:45 · 37:40**

> "The client makes up a unique string per *intent to buy* and sends it with the
> order. And the server's deal is: send me the same key twice, you get the same
> order back. I will not create a second one."

Then the three details, because this is where people implement it wrong:

> "**Required** — no key, `InvalidArgument`. A retry-unsafe order API is a bug,
> not a missing feature.
>
> **Namespaced per user**, so two shoppers can't collide on `cart-1`.
>
> And **a unique index backs it**, not just an `if`. Because two retries race —
> one loses, catches the duplicate-key error, and returns the stored order."

Land it:

> "The check alone isn't enough. **The database constraint is what makes it
> true.**"

---

## Slide 30 · And "out of stock" is not an error

**1:15 · 38:55**

> "Out of stock isn't a transport failure. It's an answer. So it comes back as
> OK, with a status and a reason enum — not a 500."

The line that makes it stick:

> "If your client does `strings.Contains(err.Error(), "stock")`, then somebody
> rephrasing a message breaks production. **An enum can't be rephrased.**"

---

## Slide 31 · One honest note on copies

**1:15 · 40:10 · 🔶 fourth to cut**

**This corrects something I used to say wrong. Say it as a correction** — it
reads as honesty, not as hedging.

> "`userd` and `productsd` run three copies because their database is baked into
> the image. Every copy identical, so any copy answers any read.
>
> `orderd` runs one — and I want to be precise, because I used to say this
> badly. **Not** 'because it writes'. Writers scale fine. It's because it writes
> **to a file inside the task**. Three copies would be three different databases.
>
> Give it a managed database and `orderd` scales like the others."

```sh
make show-guard
```

> "Terraform refuses to scale it, so the shortcut can't bite me by accident."

---

## Slide 32 · Six modules, four services, two environments

**1:30 · 41:40**

**Part 6 starts. Tell them it's the important bit.**

> "This is the part I'd most like you to take away. Six modules. The
> `deployment` directory is the whole thing, and both environments load it
> unchanged. And two environment directories that differ in exactly one file."

> "`ecs-service` is instantiated four times. One `protocol` variable switches the
> port mapping and the health-check mode — so three gRPC services and an HTTP
> gateway come out of the same module."

---

## Slide 33 · Every AWS component this creates

**2:30 · 44:10 · NEVER CUT**

**Do not read sixteen rows.** Pick five, let them read the rest.

> "Twenty-two resource types. Let me pull out the ones that bite people."

1. **VPC** — *"`enable_dns_hostnames` is required for Cloud Map. Miss it and
   discovery silently resolves nothing."*
2. **Security group** — *"one self-referencing rule. That's how `orderd` reaches
   the other two. No CIDR lists to maintain as tasks come and go."*
3. **Cloud Map namespace** — *"creates a Route 53 private hosted zone. Cloud Map
   is the registry; Route 53 is the DNS underneath."*
4. **The two IAM roles** — *"next slide, because people conflate them."*
5. **What's missing** — *"no NAT Gateway, no ALB, no RDS. Each deliberate."*

> **If asked about NAT:** the most common way a demo account quietly bills you.
> Public subnets with `assign_public_ip` instead. **In production you'd do the
> opposite** — private subnets plus VPC endpoints for ECR, S3, logs and Secrets
> Manager. Say that, so nobody copies public subnets into prod.

---

## Slide 34 · The two IAM roles

**1:30 · 45:40 · 🔶 cut at 30 min**

> "Execution role: the **ECS agent** uses it, **before your code runs** — pull the
> image, resolve the secret, create log streams. Task role: **your process's**
> credentials, at runtime.
>
> Get these backwards and your task fails to start with an error pointing at the
> wrong role. It's a genuinely confusing hour."

Then the good bit:

> "Ours is **empty**. On purpose. These services call no AWS API at runtime."

Run it live if the terminal is up:

```sh
docker inspect $(docker ps --filter "name=ministack-ecs-.*-userd$" -q) \
  --format '{{range .Config.Env}}{{println .}}{{end}}' | grep AWS_CONTAINER
```

> "**You never created a key, so there's none to leak.**"

---

## Slide 35 · Cloud Map is just DNS

**1:30 · 47:10**

> "`orderd` knows no IP addresses. It dials a name. ECS registers each task's IP
> with Cloud Map, Cloud Map keeps the A records in a Route 53 private zone, and
> tasks come and go while the name stays put."

Point at the three settings, then advance — the next slide is the story.

---

## Slide 36 · A story about that last block

**2:00 · 49:10 · 🔶 cut at 30 min**

**Tell it as a mystery: symptoms first, cause last.** It is the best war story
in the deck and the structure is what makes it land.

> "I had a deprecation warning on `failure_threshold`. So I tidied it up — left
> the block empty. Deployed. And my client started failing with `code 14: no
> children to pick from`.
>
> So I checked. Four tasks running. Four Cloud Map services, all present. VPC DNS
> hostnames enabled. Security groups fine. Everything green."

Pause, then the cause:

> "An *empty* block makes the provider send **no health config at all**. Cloud Map
> then never accepts ECS's health reports, every instance stays UNHEALTHY — and
> unhealthy instances are excluded from DNS answers.
>
> Four healthy tasks. Zero addresses returned. The comment in that file now says,
> in capitals, do not clean this up."

---

## Slide 37 · [BEAT] One command. It is the whole talk.

**0:10 · 49:20**

Say it, then switch to the terminal. Do not explain it on this slide.

---

## Slide 38 · One pipeline. Laptop and production.

**2:00 · 51:20 · NEVER CUT — this is the thesis**

Run the diff **live**. It is more convincing than the slide.

```sh
diff terraform/envs/local/provider.tf terraform/envs/aws/provider.tf
```

> "That diff is the talk. Local has fake credentials, a few skip flags, and an
> endpoints block sending every AWS API call to a local emulator. AWS has a
> region. That's it.
>
> Everything describing the deployment — VPC, task definitions, services, IAM,
> Cloud Map — lives in `deployment/`, and both environments load the same files.
> Not a copy. Not a simplified local version."

Land the reason it matters:

> "Which is the real argument for emulating locally rather than keeping a second
> set of 'local' manifests: **there is no second set to drift.** Change a task
> definition and you change it once."

---

## Slide 39 · Where the laptop version is honest

**1:30 · 52:50 · 🔶 fifth to cut**

**Volunteer the gaps before anyone finds them.** This earns you the right to
have shown a local demo at all.

> "Ministack emulates ECS by launching real Docker containers. LocalStack's free
> Community edition ended March 2026, and ECS was never in it anyway.
>
> Three places I substitute. Cloud Map stores registrations but serves no DNS, so
> `make dns` adds Docker network aliases — legitimate, because **service discovery
> is only DNS underneath.** `awsvpc` tasks have no host port, so I run a relay; on
> AWS that's SSM port forwarding. And the emulator doesn't echo back
> `healthStatus` or `launchType`."

🔶 If cutting, say the last sentence during Demo 1.

---

## Slide 40 · 🔴 DEMO 1 — is this really ECS?

**3:00 · 55:50 · NEVER CUT**

```sh
make ps
```

Let it scroll, then scroll **back up** to step 4 and point.

> "This variable — `ECS_CONTAINER_METADATA_URI_V4`. Nothing in my code sets it.
> The platform injected it. The task describes itself: cluster, task ARN, family
> and revision, availability zone."

(Revision number is whatever your applies reached — mine was `rev 3`. The point
is that a revision *exists*.)

Then step 5, the real prize:

> "And this one. `AWS_CONTAINER_CREDENTIALS_FULL_URI`. The SDK reads it and gets
> temporary credentials. **There is no access key anywhere** — not in the image,
> not in git, not in a `.env` file."

---

## Slide 41 · 🔴 DEMO 2 — the code, running

**3:00 · 58:50**

```sh
make demo
```

Narrate while it runs. Do not read output silently.

> "Search, no auth. Log in as a **seeded** user. Order placed: two network hops,
> and the client sent no prices. Same idempotency key again — same order, not a
> second one. Then out of stock. Then no token."

🔶 **40-min path: stop here.** Otherwise `make dev-rest`, then always keep this:

```sh
curl -s -o /dev/null -w 'REST -> %{http_code}\n' \
  -X POST http://localhost:8080/v1/products:checkAvailability -d '{}'
```

> "404. Four missing lines of annotation."

---

## Slide 42 · 🔴 DEMO 3 — the same Terraform, on real AWS

**2:30 · 61:20 · 🔶 cut at 30 min (describe it)**

**Pre-deployed the day before.** Read-only.

```sh
make ps-aws
aws servicediscovery list-services --query 'Services[].Name'
```

> "Same modules. One provider block different. Now `healthStatus` says HEALTHY,
> `launchType` says FARGATE, tasks across two availability zones. And no alias
> shim — Cloud Map is doing the DNS for real."

> "And `terraform destroy` before I leave the venue. Which I will actually run."

---

## Slide 43 · Now, the promise from slide 2

**1:00 · 62:20 · NEVER CUT**

**Close the loop you opened. Say so explicitly** — the callback is the payoff.

> "Right. Remember the promise from the second slide?"

Walk the checklist, ticking each one:

> "`userd` scaled to three tasks. Three of three running. Three of three healthy
> in Cloud Map. DNS returns three A records. Everything correct.
>
> And I'm going to send 120 requests through `orderd`, each one making a call to
> `userd`."

---

## Slide 44 · [ASK] Where does the traffic go?

**0:45 · 63:05 · NEVER CUT**

**Do not answer. Make them commit to a number out loud.**

> "120 requests. Three healthy tasks. **How many land on each one?**"

Most rooms say "forty, forty, forty". Take a couple of answers, then advance.

---

## Slide 45 · 120 / 0 / 0

**1:30 · 64:35 · NEVER CUT**

Let the number sit before you explain anything.

> "A hundred and twenty. Zero. Zero.
>
> No error. No log line. No failed health check. Every dashboard green."

Pause. Then:

> "And everybody's first conclusion is that service discovery is broken. It
> isn't. Discovery did its job perfectly — it handed back three addresses."

Slowly:

> "**The client never asked to balance.**"

---

## Slide 46 · Why: `pick_first`

**1:30 · 66:05**

Explain it without making the gRPC authors sound foolish — this matters for
credibility.

> "`pick_first` is the default. Resolve the name, connect to one address, send
> everything down that connection.
>
> And that's a *reasonable* default! With HTTP/2 one connection multiplexes many
> requests, so opening more looks wasteful. It's optimised for the other end
> being a single load balancer.
>
> On ECS the other end is three tasks with three IPs. **The default isn't a bug —
> it's a correct answer to a different question.**"

---

## Slide 47 · The fix is two halves

**2:00 · 68:05 · NEVER CUT**

Spend your time on the prefix. This is where people half-fix it.

> "Two settings, and either alone does nothing. `dns:///` is not decoration —
> with a bare host:port, gRPC uses the *passthrough* resolver, which gives you
> exactly one address. So you can set `round_robin`, feel good about it, and it
> still has nothing to balance over. **That's the version that looks fixed and
> isn't.**"

Then the server half, with the callback to slide 24:

> "And `MaxConnectionAge` on the server, so clients re-resolve after a scale-out.
> Without it, a client connected *before* you scaled never learns the new tasks
> exist. Which is exactly when you scale — during the sale.
>
> With both: forty, forty, forty."

> **If someone asks to see it live:** `-var lb_policy=pick_first` redeploys
> `orderd` with the bug, then `make scale N=3 && make demo-load`. Offer it as a
> hallway conversation rather than burning five minutes.

---

## Slide 48 · What to take away

**1:00 · 69:05**

Read them. Don't elaborate.

If you only have time for three: **the boundary test**, **one Terraform stack
with two provider blocks**, and **`pick_first`**.

---

## Slide 49 · Green dashboards are not working

**0:45 · 69:50 · NEVER CUT**

**The closing line. Deliver it and stop talking.**

> "Two stories in this talk. Four tasks running, four Cloud Map services, DNS
> enabled — and zero addresses returned. Three healthy tasks, correct DNS — and
> one of them doing everything.
>
> Both looked perfectly fine."

Pause.

> "**Green dashboards are not the same thing as working.** Deploy it once before
> you trust it."

---

## Slide 50 · Clone it

**0:45 · 70:35**

> "All open. `make test` needs no Docker and no AWS. And `SPEC.md` marks every
> claim as verified or assumed — including the ones I got wrong first."

**Thank you.** Then questions.

---

## Panic buttons

| It broke | Do this |
|---|---|
| **A demo hangs** | `Ctrl-C`. Say *"this is why I recorded it"* and play the clip. Do not debug on stage. |
| `connection refused` on `localhost:5005x` | `make forward`. `awsvpc` tasks have no host port — faithful to Fargate, not a bug. |
| `code 14 "no children to pick from"` | `make dns`. Terraform replaced the task containers and the aliases went with them. |
| `Unavailable ... user service unavailable` after an apply | `make wait-ready`. `tf-local-apply` already runs it, so you should not see this. |
| Login fails and you have scaled `userd` | You used a registered user, not a seeded one. `demo@example.com` / `demo-password`. |
| Mermaid shows as raw text | You're in Marp. Switch to the GitHub tab for slides 6 and 22. |
| Docker is wedged | `make local-down && make local-up && make tf-local-apply`. ~90s. Talk through slide 30 while it runs. |
| Venue wifi is gone | Everything except Demo 3 is **fully local**. Say so — it's a selling point. |

### Questions you will get

| Question | Short answer |
|---|---|
| *Why not an ALB?* | A gRPC target group needs an HTTPS listener → ACM cert → a domain. Above the cut line. `gatewayd` is the right thing to put one in front of. |
| *Why not Kubernetes?* | It would work. Four services need four task definitions — that's the whole requirement. Slide 19 says when to switch. |
| *Why not ConnectRPC and drop the gateway?* | Genuinely a good option, and slide 16 says so. The services use standard `grpc-go` because that's what most teams have, and every ECS lesson is identical either way. |
| *Is SQLite serious?* | No, and slide 25 says so. Use RDS. It halves my stage risk, and `DB_DRIVER=postgres` is the switch. |
| *Does Service Connect fix the load balancing?* | Yes — its proxy balances per request, not per connection, which is why `appProtocol: grpc` is in the task definition. I show the client-side fix because it works everywhere, including off AWS. |
| *Why public subnets?* | Cost, for a demo — no NAT Gateway. **In production: private subnets plus VPC endpoints.** Don't copy this bit. |
| *What about mTLS between services?* | Not here. Traffic is in-VPC and authenticated at the packet level. Needing mTLS everywhere is one of the reasons to move to a mesh. |
| *Does this work with LocalStack?* | Community edition ended March 2026, and ECS was never in the free image. This uses Ministack (MIT). |
| *How much did it cost?* | Four small Fargate tasks for a talk is cents. The thing that bills you is a NAT Gateway, and this VPC has none. Check current Fargate pricing — I'm not quoting rates from memory. |
