# Presenter deck

Companion to [`PRESENTATION.md`](PRESENTATION.md). One section per slide: what
to say, what to type, and what to do when it breaks.

## Pick your runtime first

The full deck is **37 slides ≈ 55 minutes**. It does not fit in 40. Choose a
path before you walk in and mark your copy.

| Slot | Path | Drop these slides |
|---|---|---|
| **55 min** | Everything | — |
| **40 min** | Core | 10, 14, 23, 31 (fold one line of each into its neighbour) and cut `make dev-rest` from Demo 2 |
| **30 min** | Spine only | Also drop 12, 16, 20, 25, 28, and Demo 3 (describe it instead) |

**Never drop:** 6 (use-case diagram), 7 (the boundary test), 22 (architecture),
27 (every AWS component), 30 (one pipeline), 32 (Demo 1), 35 (`pick_first`).

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

## Slide 2 · Where we are going

**0:45 · 1:45**

Give people a map and permission to be at whatever level they're at.

> "The first three parts need no gRPC knowledge. Part six — the Terraform — is
> the heart of this talk, and it's where I'd most want you awake."

Don't dwell. It's a map, not content.

---

## Slide 3 · It is sale season

**1:15 · 3:00**

**This is the hook. Slow down.**

> "It's sale season. Big Billion Days, Great Indian Festival — whichever one is
> running right now. Honestly, how many of you have a tab open?
>
> Midnight. The banner goes live. And everybody opens the app at the same
> moment."

Ask and **actually wait for hands.** Five seconds, and it buys you the room.

---

## Slide 4 · What everyone is actually doing

**1:30 · 4:30**

Walk the table. The last row is the point.

> "Search. Scroll. Compare. Open a product page. Add to cart and then go look at
> one more thing. And then — far less often — somebody actually buys."

Make it personal, because it's unarguable:

> "Think about your own last sale. How many things did you open? How many did
> you buy? That ratio is the whole architecture."

**Say that you are not quoting anyone's numbers:**

> "I'm not putting Flipkart's traffic graphs on a slide — I don't have them, and
> I'm not Flipkart. I don't need them. The *shape* is the argument."

---

## Slide 5 · Suppose the store is one application

**1:30 · 6:00**

Be generous to the monolith. People here ship monoliths, and some are right to.

> "One deployment. Traffic multiplies, you scale up, and **it works.** This
> isn't a story about somebody being stupid."

Then the three costs. The second lands hardest with senior people:

> "You're scaling checkout in order to survive search. A slow catalogue query
> and a checkout bug share one deploy, one rollback, one on-call page. And you
> can't tune them separately, because they're the same process."

---

## Slide 6 · The overall picture

**1:30 · 7:30 · NEVER CUT**

Switch to the GitHub tab (or your screenshot). **Trace it with your finger.**

> "Shopper, three things they do. Two are reads — search and browse, and log in.
> One is a write — place an order.
>
> Teal is the read path. Orange is the write path. And see this bit: when you
> place an order, `orderd` turns around and asks the other two — who is this,
> and what does this cost.
>
> And down here is me, with Terraform — **one pipeline that points at my laptop
> or at production.** That's part six."

Land it:

> "Two paths through one system. **They do not grow at the same rate.**"

---

## Slide 7 · So: three services

**2:00 · 9:30 · NEVER CUT**

This answers "why not a monolith" *and* "why not twelve services" at once.

> "Reads here, reads here, writes there."

**Then give them the test, slowly** — it's the thing they can use on Monday:

> "How do you know that's the right boundary? Here's a test you can use at work.
> Ask: would these ever need a different number of copies, a different deploy
> schedule, or a different on-call owner?
>
> If it's no to all three — **it's one service.** Put it back."

Then pay the cost out loud, so nobody thinks you're selling microservices:

> "Every split costs you a network hop, a new failure mode, and another thing to
> deploy. Three is what *this* use case pays for. If yours pays for two, build
> two."

---

## Slide 8 · But now they have to talk

**0:45 · 10:15**

Short bridge. Don't linger.

> "One order needs two questions answered by other services. Inside one app
> those were function calls. Now they cross a network. So — how should services
> call each other?"

---

## Slide 9 · What gRPC is

**1:30 · 11:45**

**Lead with the definition. Never with the joke.**

> "gRPC lets you call a function that lives on another machine, as if it were
> local. You write the function down — name, inputs, outputs — in a file, and a
> compiler turns that file into real code for both sides."

*Then* the acronym, as a throwaway:

> "RPC is Remote Procedure Call. The `g`… officially stands for 'gRPC'. The
> acronym contains itself — they reassign it every release as a joke."

🔶 **40-min path:** fold slide 10's code contrast in here and skip slide 10.

---

## Slide 10 · The mental shift

**1:30 · 13:15 · 🔶 first to cut**

Read both code blocks aloud. The contrast does the work.

> "With REST you think about resources and verbs. What's the right noun? POST or
> PUT? Which status code? With gRPC you think about functions. What does it
> take, what does it give back.
>
> That's the whole shift. HTTP/2, binary encoding, codegen — all machinery
> serving that one idea."

---

## Slide 11 · You write the contract, a compiler writes the code

**1:30 · 14:45**

> "This proto file is the only hand-written interface code in the project. One
> command turns it into five things."

The line that matters most to someone junior:

> "Forget to implement an RPC and the Go code **will not compile**. That's not a
> runtime 501 you find in production — it's a build failure on your laptop."

And the honest reason teams adopt it:

> "Nobody should be writing a client from a wiki page that was last accurate
> four months ago."

---

## Slide 12 · Let me be honest about performance

**1:30 · 16:15 · 🔶 cut at 30 min**

**This slide buys you credibility for the whole talk.** Someone here has read
"gRPC is 7x faster" and is ready to either repeat it or challenge you.

> "You'll read that gRPC is faster. Here's the accurate version. The advantages
> are real and structural: binary Protobuf instead of JSON, one multiplexed
> HTTP/2 connection instead of one per call, and headers that aren't re-sent in
> full every time.
>
> **But** — for most internal services, the network and your database dominate.
> Encoding is rarely your bottleneck. And I haven't benchmarked this repo, so
> I'm not going to put a speed-up number on a slide and have you quote me."

Land it:

> "Performance is a genuine benefit. It's just not usually *why* teams switch —
> and it's not what I can prove to you today."

---

## Slide 13 · What I *can* prove: streaming is not the point

**2:00 · 18:15**

Own the method — your strongest slide for an expert audience.

> "People say gRPC is for streaming. I wanted to know if that's true, so I
> counted. These are the real proto files from 14 projects you've heard of. 611
> RPCs. 50 of them stream — about eight percent.
>
> Temporal: 121 RPCs, **zero** streaming. Qdrant: 30, zero. containerd: 17,
> zero."

Then invite the check:

> "The protos are in the repo under `docs/evidence`. The counts reproduce with
> one `grep`. Please go check me — I'd rather you did."

Second conclusion:

> "And not one of these 14 exposes gRPC to the public internet. So when someone
> asks 'should our mobile app speak gRPC?' — the industry's answer is mostly no."

---

## Slide 14 · When gRPC, and when REST

**1:00 · 19:15 · 🔶 second to cut**

> "Not a competition. A question of *where*. Inside your network, gRPC. Facing a
> browser or a third party, REST. You'll usually need both."

🔶 If cutting, say that one sentence over slide 15 instead.

---

## Slide 15 · A browser cannot speak gRPC

**1:30 · 20:45**

Be precise, because this is a factual claim people will test.

> "This isn't a configuration problem. A browser can't open a raw HTTP/2
> connection and control trailers the way gRPC needs. That's just true.
>
> So something has to translate. There are three real options, and I want to
> show you all three — because most talks show one and call it the answer."

Read the Connect quote off the slide. It sets up the next slide.

---

## Slide 16 · So do we actually need `gatewayd`?

**2:00 · 22:45 · 🔶 cut at 30 min**

**Answer the question in the first four words.** This is a design review, not a
sales pitch.

> "Do we need it? **No.** It's a choice, and here's the honest trade-off.
>
> I picked the gateway-process option because it makes the lesson *visible* —
> `gatewayd` is a separate ECS service with its own task definition, so you
> watch a stateless service deploy next to stateful ones. That's useful for a
> talk about ECS.
>
> But if I were starting a product today? Option three is very attractive. One
> port, no extra hop, nothing extra to operate, and the browser talks to your
> service directly. The TypeScript client in this repo already uses Connect."

Then when it *does* earn its place, so you're not dismissing your own
architecture:

> "A gateway process still earns its keep when you want one public door to audit
> and rate-limit, or when another team owns the services and they must stay
> plain gRPC."

> **Expect the follow-up:** *"so why not rewrite it with Connect?"* Honest
> answer: the services use standard `grpc-go`, which is what most teams have,
> and the ECS lessons are identical either way. Switching the server library
> would change nothing in parts 4–7.

---

## Slide 17 · Not every RPC needs REST

**1:30 · 24:15**

The second question, answered directly.

> "Do we need REST for all the APIs? **No.** Nine of our ten RPCs have a REST
> route. One doesn't, on purpose."

> "`CheckAvailability` is how `orderd` prices a cart. Nothing outside should be
> able to call it. So it has no HTTP annotation — it works over gRPC, and it's a
> 404 over REST."

Give them the rule to take home:

> "The rule: **a REST route exists for a client you don't control.** Expose
> exactly those. Four lines of annotation are the entire difference between an
> internal and a public API."

---

## Slide 18 · Four options, one ruled out

**2:00 · 26:15**

Give all four a fair hearing — somebody here runs EKS happily and is right to.

> "Lambda first, and let me be precise, because this isn't me preferring
> containers. gRPC needs a process that **stays listening**, holding an HTTP/2
> connection. Lambda has none."

**Read the quote off the slide, verbatim.** It's a hard stop, not an opinion.

> "EC2 works — but now you're building a deployment platform. EKS: Kubernetes is
> genuinely excellent at this, and the cost is honest — control plane, nodes,
> upgrades, CNI, ingress, RBAC."

---

## Slide 19 · Why ECS for now

**1:30 · 27:45**

The reframe that makes this land:

> "The question isn't serverless versus containers. It's: how much orchestration
> do four services actually need? Both ECS and EKS run this correctly. One of
> them asks you to operate Kubernetes first."

Then remove the fear of a wrong decision:

> "And moving later is **not a rewrite**. The container, the contract, the health
> check, the graceful shutdown — all of it comes with you. Only the YAML
> changes.
>
> 'For now' is a legitimate engineering answer. 'Forever' rarely is."

---

## Slide 20 · What Fargate actually gives you

**2:00 · 29:45 · 🔶 cut at 30 min**

**Do not just say "serverless containers".** This is where an experienced person
decides whether you actually know Fargate.

> "Fargate means you don't manage EC2 instances. AWS's words: you no longer have
> to provision, configure or scale clusters of virtual machines.
>
> What AWS owns is the **platform version** — and AWS defines that as a
> combination of the kernel and the container runtime versions."

Then the part people miss — **read it off the slide:**

> "If a security issue is found, AWS creates a patched revision **and retires
> tasks running on the vulnerable revision.**
>
> So AWS will stop your task to patch underneath you. A task never upgrades in
> place; a new task gets the new revision."

And the correction that matters:

> "But you still own everything *inside* your image. Your base image, your
> packages, your CVEs. **Serverless does not mean nobody patches** — it means AWS
> patches their half and you still patch yours."

Then connect it forward:

> "Which is why graceful shutdown isn't optional here. Your task **will** be
> replaced, on somebody else's schedule."

---

## Slide 21 · Three words

**0:45 · 30:30**

Don't skip, even for an advanced room. 45 seconds, and it stops people silently
falling behind.

> "Task: one container with its own IP — one copy of your service. Task
> definition: the recipe, and it's versioned — editing it makes revision 2.
> Service: the thing that keeps N tasks alive and replaces the dead ones."

---

## Slide 22 · Three services, one contract

**1:30 · 32:00 · NEVER CUT**

GitHub tab again. Trace it.

> "Browser comes in over REST to `gatewayd`. `gatewayd` speaks gRPC to all three.
> And the thick arrows are the interesting ones — `orderd` calling `userd` and
> `productsd` on every single order.
>
> Nobody in here knows anybody's IP address. They dial a name, `ecom.local`, and
> Cloud Map resolves it. That's slide 29."

> "And one proto set generated the Go servers, the Go clients, `gatewayd`, the
> OpenAPI spec and the TypeScript client."

---

## Slide 23 · One order, end to end

**1:00 · 33:00 · 🔶 third to cut**

Walk the arrows top to bottom. The line to emphasise:

> "Total computed from **catalogue** prices. Not from anything the client sent."

🔶 If cutting, say that one sentence on slide 24 instead.

---

## Slide 24 · Two contract decisions

**1:30 · 34:30**

> "The order request carries **no price**. The client sends what, and how many."

Make it concrete and slightly alarming:

> "Think about what the alternative means during a sale. If the client sends the
> price, then a client can ask 'is this ₹2,000 sale price real?' — and then
> submit ₹200."

Then the second:

> "And out of stock isn't an error. It's an answer. So it comes back as OK, with
> a status and a reason enum — not a 500. Status codes stay for unauthenticated,
> invalid argument, unavailable."

The line that makes it stick:

> "If your client does `strings.Contains(err.Error(), \"stock\")`, then somebody
> rephrasing a message breaks production. An enum can't be rephrased."

---

## Slide 25 · One honest note on copies

**1:15 · 35:45 · 🔶 cut at 30 min**

**This corrects a thing I used to say wrong, so say it carefully.**

> "`userd` and `productsd` run three copies because their database is baked into
> the image — every copy is identical, so any copy can answer any read.
>
> `orderd` runs one. And I want to be precise about why, because I used to say
> this badly: **not** 'because it writes'. Writers scale fine. It's because it
> writes **to a file inside the task**. Three copies would be three different
> databases.
>
> Give it RDS and `orderd` scales like the others."

Then own the shortcut:

> "SQLite on the task is a demo shortcut. It keeps my AWS deploy at about two
> minutes instead of ten, which on a conference stage is the biggest risk
> reduction available. Terraform refuses to scale it, so the shortcut can't bite
> me by accident."

```sh
make show-guard
```

---

## Slide 26 · Six modules, four services, two environments

**1:30 · 37:15**

**Part 6 starts. Tell them it's the important bit.**

> "This is the part I'd most like you to take away, so let me orient you first.
> Six modules. The `stack` directory is the whole deployment, and it's shared
> verbatim. And two environment directories that differ in exactly one file."

> "`ecs-service` is instantiated four times — one per service. A single
> `protocol` variable switches the port mapping and which health-check mode the
> baked-in probe uses, so the gRPC services and the HTTP gateway come out of the
> same module."

---

## Slide 27 · Every AWS component this creates

**2:30 · 39:45 · NEVER CUT**

Do **not** read all sixteen rows. Pick five and let them read the rest.

> "Twenty-two resource types. I won't read them all — but let me pull out the
> ones that bite people."

1. **VPC** — *"`enable_dns_hostnames` is required for Cloud Map. Miss it and
   service discovery silently resolves nothing."*
2. **Security group** — *"one self-referencing rule. That's how `orderd` reaches
   the other two. No CIDR lists to maintain as tasks come and go."*
3. **Cloud Map namespace** — *"this creates a Route 53 private hosted zone. Cloud
   Map is the registry; Route 53 is the DNS underneath it."*
4. **The two IAM roles** — *"next slide, because people conflate them."*
5. **What's missing** — *"no NAT Gateway, no ALB, no RDS. Each one deliberate."*

> **If asked about NAT:** a NAT Gateway is the most common way a demo account
> quietly bills you. Public subnets with `assign_public_ip` instead. **For
> production you'd do the opposite** — private subnets plus VPC endpoints for
> ECR, S3, logs and Secrets Manager. Say that, so nobody copies public subnets
> into prod.

---

## Slide 28 · The two IAM roles

**1:30 · 41:15 · 🔶 cut at 30 min**

> "Execution role: the **ECS agent** uses it, **before your code runs** — pull
> the image, resolve the secret, create log streams. Task role: **your
> process's** credentials, at runtime.
>
> Get these backwards and your task fails to start with an error pointing at the
> wrong role. It's a genuinely confusing hour."

Then the good bit:

> "Our task role is **empty**. On purpose. These services call no AWS API at
> runtime, so nothing is granted 'just in case'."

Run it live if the terminal is up:

```sh
docker inspect $(docker ps --filter "name=ministack-ecs-.*-userd$" -q) \
  --format '{{range .Config.Env}}{{println .}}{{end}}' | grep AWS_CONTAINER
```

> "That variable is the whole 'no API keys on ECS' story. The SDK reads it and
> gets temporary credentials. **You never created a key, so there's none to
> leak.**"

---

## Slide 29 · Cloud Map is just DNS

**2:00 · 43:15**

> "`orderd` knows no IP addresses. It dials a name. ECS registers each task's IP
> with Cloud Map, Cloud Map maintains the A records in a Route 53 private zone,
> and tasks come and go while the name stays put."

Point at the three settings, then tell the story — **the best war story in the
talk:**

> "That last block, `health_check_custom_config`. It looks like dead weight. I
> had a deprecation warning on that field, so I tidied it up — emptied the block.
>
> Which makes the provider send **no** health config at all. Cloud Map then never
> accepts ECS's health reports, every instance stays UNHEALTHY, and unhealthy
> instances are excluded from DNS answers. My client failed with `code 14: no
> children to pick from`.
>
> Meanwhile: four tasks running, four Cloud Map services, VPC DNS enabled.
> Everything looked healthy. The comment in that file now says, in capitals, do
> not clean this up."

---

## Slide 30 · One pipeline. Laptop and production.

**2:00 · 45:15 · NEVER CUT — this is the thesis**

Run the diff live. It is more convincing than the slide.

```sh
diff terraform/envs/local/provider.tf terraform/envs/aws/provider.tf
```

> "That diff is the talk. Local has fake credentials, a few skip flags, and an
> endpoints block that sends every AWS API call to a local emulator. AWS has a
> region. That's it.
>
> Everything that *describes the deployment* — the VPC, the task definitions, the
> services, the IAM, Cloud Map — lives in `stack/` and is shared **verbatim**.
> Not a copy. Not a simplified local version. The same files."

Land the reason it matters:

> "Which is the real argument for emulating locally instead of keeping a second
> set of 'local' manifests: **there is no second set to drift.** When I change a
> task definition, I change it once."

---

## Slide 31 · How the laptop part works

**1:30 · 46:45 · 🔶 fourth to cut**

**Volunteer the gaps before anyone finds them.** This is where you earn the
right to have shown a local demo at all.

> "Ministack emulates ECS by launching real Docker containers. LocalStack's free
> Community edition ended in March 2026, and ECS was never in it anyway.
>
> What's real locally: the Terraform, the task definitions, `awsvpc` networking,
> secret injection, health checks, graceful shutdown, metrics, traces.
>
> Three places I substitute. Cloud Map stores the registrations but serves no
> DNS, so `make dns` adds Docker network aliases — and that substitution is
> legitimate, because **service discovery is only DNS underneath.** `awsvpc`
> tasks have no host port, so I run a relay; on AWS that's SSM port forwarding.
> And the emulator doesn't echo back `healthStatus` or `launchType`."

> "Naming the limits yourself is more credible than being caught by them. It's
> also exactly why this deploys to real AWS too."

🔶 If cutting, say the last sentence during Demo 1.

---

## Slide 32 · 🔴 DEMO 1 — is this really ECS?

**3:00 · 49:45 · NEVER CUT**

Answers the question the skeptic in row three is already forming.

```sh
make ps
```

Let it scroll, then scroll **back up** to step 4 and point.

> "This variable — `ECS_CONTAINER_METADATA_URI_V4`. Nothing in my code sets it.
> The platform injected it. The task can describe itself: cluster, task ARN,
> family and revision, availability zone."

(The revision number is whatever your applies have reached — mine was `rev 3`.
The point is that a revision *exists*, not its value.)

Then step 5, the real prize:

> "And this one. `AWS_CONTAINER_CREDENTIALS_FULL_URI`. The AWS SDK reads it on
> its own and gets temporary credentials. **There is no access key anywhere** —
> not in the image, not in git, not in a `.env` file."

Then volunteer the emulator's limits (or, if you cut slide 31, here):

> "Two honest gaps: `healthStatus` says UNKNOWN and `launchType` comes back
> empty, even though the task definition asks for FARGATE. The emulator doesn't
> echo those back. Real ECS reports both."

---

## Slide 33 · 🔴 DEMO 2 — the code, running

**3:00 · 52:45**

```sh
make demo
```

Narrate while it runs. Do not read the output silently.

> "Search, no auth — the read path that scales. Log in as a **seeded** user.
> Order placed: two network hops, and notice the client sent no prices. Same
> idempotency key again: same order, not a second one. Then out of stock. Then
> no token."

🔶 **40-min path: stop here.** Otherwise:

```sh
make dev-rest
```

> "Identical flow over plain HTTP and JSON. Same services, same contract,
> generated gateway."

Then the slide-17 payoff — **always keep this:**

```sh
curl -s -o /dev/null -w 'REST -> %{http_code}\n' \
  -X POST http://localhost:8080/v1/products:checkAvailability -d '{}'
```

> "404. Four missing lines of annotation."

If time allows, `make ts-demo`:

> "Same proto. I added one plugin. I wrote no types."

---

## Slide 34 · 🔴 DEMO 3 — the same Terraform, on real AWS

**2:30 · 55:15 · 🔶 cut at 30 min (describe it instead)**

**Pre-deployed the day before.** Do a read-only demo.

```sh
make ps-aws
```

> "Same modules. One provider block different. And now `healthStatus` says
> HEALTHY, `launchType` says FARGATE, and the tasks are spread across two
> availability zones."

Show Cloud Map doing real DNS:

```sh
aws servicediscovery list-services --query 'Services[].Name'
```

> "No alias shim here. Cloud Map is doing the DNS for real."

Then say the number:

> "That apply takes about two minutes. It's two minutes because there's no RDS
> and no NAT Gateway in it."

**And the discipline line:**

> "And `terraform destroy` before I leave the venue. Which I will actually run."

---

## Slide 35 · One thing to know before you ship gRPC on ECS

**2:00 · 57:15 · NEVER CUT**

**Not a demo — knowledge.** This is the slide people will thank you for.

> "`pick_first` is gRPC's default load-balancing policy. It resolves the name,
> connects to **one** address, and sends everything down that one connection.
>
> So you scale to three tasks, all three healthy, DNS returning all three
> addresses — and one task takes a hundred percent of the traffic. I measured
> it: 120 of 120 requests on one task.
>
> And nothing warns you. No error, no log line, no failed health check."

Pause, then:

> "Everybody's first conclusion is that service discovery is broken. It isn't.
> Discovery handed back three addresses. **The client never asked to balance.**"

Then the fix, and spend your time on the prefix:

> "Two settings, and either alone does nothing. `dns:///` is not decoration —
> with a bare host:port, gRPC uses the *passthrough* resolver, which gives you
> exactly one address. So you can set `round_robin`, feel good about it, and it
> still has nothing to balance over. That's the version that looks fixed and
> isn't.
>
> Plus `MaxConnectionAge` on the server, so clients re-resolve after a
> scale-out. With both: forty, forty, forty."

> **If someone asks to see it:** `-var lb_policy=pick_first` redeploys `orderd`
> with the bug, then `make scale N=3 && make demo-load`. Offer it as a hallway
> conversation rather than burning five minutes on stage.

---

## Slide 36 · What to take away

**1:00 · 58:15**

Read them. Don't elaborate — the room is full.

If you only have time for three: **the boundary test**, **one Terraform stack
with two provider blocks**, and **`pick_first`**.

---

## Slide 37 · Clone it

**0:45 · 59:00**

> "It's all open. `make test` needs no Docker and no AWS. `make local-up` gives
> you real ECS tasks on your laptop.
>
> And `SPEC.md` marks every claim as verified or assumed — including the ones I
> got wrong first."

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
