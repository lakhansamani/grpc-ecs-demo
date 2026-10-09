# Presenter deck

Companion to [`PRESENTATION.md`](PRESENTATION.md). One section per slide: what to
say, what to click, and what to do when something breaks.

**Budget: 40 minutes + 5 for questions.** Timings are cumulative. If you are
behind at a 🔶 marker, use the cut listed there.

**Before you walk in:** read [§0 Pre-flight](#0--pre-flight) and
[§Panic buttons](#panic-buttons).

---

## 0 · Pre-flight

Run this **the night before**, and again 20 minutes before you present.

```sh
cd ~/projects/grpc-ecs-demo

# 1. clean slate
docker compose down -v
docker ps -aq --filter "name=ministack-ecs-" | xargs -r docker rm -f
docker ps -aq --filter "name=forward-"       | xargs -r docker rm -f
docker network rm ecom-dns ecom-infra 2>/dev/null
rm -rf data

# 2. bring it up
make test              # expect 8 packages ok
make local-up          # ministack :4566, jaeger :16686, prometheus :9090
make images            # four ARM64 images, ~30s
make tf-local-apply    # ECS tasks; auto-runs `make dns`
make forward           # publish ports so Postman/grpcurl reach the tasks

# 3. prove all three demos work BEFORE the room is watching
make ps
make demo
make dev-rest
make api-coverage      # expect 20/20
```

Then open these tabs and **leave them open**:

| Tab | URL | Used in |
|---|---|---|
| Prometheus | `http://localhost:9090/graph` | Demo 3 |
| Jaeger | `http://localhost:16686` | Demo 2 (optional) |
| GitHub repo | `github.com/lakhansamani/grpc-ecs-demo` | Last slide |

Paste this into the Prometheus query box now, so you are not typing it live:

```promql
sum by (task) (grpc_server_started_total{service="userd", grpc_method="VerifyToken"})
```

**Terminal setup:** font at 18pt+, two tabs — one for commands, one already
sitting in Prometheus. Turn off notifications.

> **Leave `userd` at 1 task after pre-flight.** Demo 3 scales it to 3 live, and
> that reveal is the best moment in the talk.

---

## Slide 1 · Title

**~1 min · 0:01**

> "I'm going to take three Go services from `localhost` to AWS Fargate, and the
> Terraform that does it is the same whether it's running on my laptop or in a
> real AWS account.
>
> One promise: **everything in this talk was run, not sketched.** Every number
> came out of a terminal in this repo. Where something isn't implemented, I'll
> tell you."

Three things they'll leave with: why gRPC can't run on Lambda, how to diagnose
the most common gRPC-on-ECS bug, and how to run one set of Terraform in both
places.

---

## Slide 2 · Who this is for

**~1 min · 0:02**

Give people permission to be at whatever level they're at.

> "If you've never written a gRPC service, the first half is the whole talk for
> you — you'll leave able to argue the choice in a design review.
>
> If you already run ECS, the last third is why you should stay. It's the bug
> that costs people a full day."

Don't dwell. This slide is a map, not content.

---

## Slide 3 · It is sale season

**~1 min · 0:03**

**This is the hook. Slow down and let it land.** No slides full of numbers yet —
just a scene everyone in the room has lived.

> "It's sale season. Big Billion Days, Great Indian Festival — whichever one is
> running right now. Honestly, how many of you have a tab open?
>
> Midnight. The banner goes live. And everybody opens the app at the same
> moment."

Ask the room the question and **actually wait** for hands. It costs five seconds
and buys you their attention for the next forty minutes.

> **Why this example:** it needs no setup. The payments example I built first
> needed authorization-vs-capture-vs-settlement explained before the ECS lesson
> could even start. A sale needs one sentence.

---

## Slide 4 · What everyone is actually doing

**~2 min · 0:05**

Walk the table slowly. The last row is the point.

> "Search. Scroll. Compare. Open a product page. Add to cart and then go look at
> one more thing.
>
> And then — far less often — somebody actually buys."

Then make it personal, because it's unarguable:

> "Think about your own last sale. How many things did you open? And how many did
> you actually buy? That ratio is the whole architecture."

**Say explicitly that you are not quoting anyone's numbers:**

> "I'm not going to put Flipkart's traffic graphs on a slide — I don't have them,
> and I'm not Flipkart. I don't need them. The *shape* is the argument, and you
> already know the shape from your own behaviour."

---

## Slide 5 · Suppose the store is one application

**~2 min · 0:07**

Be generous to the monolith. The room has shipped monoliths, and some are right.

> "One deployment. Search, product pages, login, cart, checkout. Traffic
> multiplies, so you scale up. More copies.
>
> **And it works.** I want to be clear — this isn't a story about somebody being
> stupid. It's a story about what you paid for it."

Then the cost:

> "To survive all that *searching*, you also made more copies of the part that
> **writes orders**. Which is the one part you least want copies of — because
> copies of a writer is how two people buy the last phone."

Land the quote and pause:

> **"Scale the reads. Not the writes."**

---

## Slide 6 · So: three services

**~1 min · 0:08**

> "Reads here, reads here, writes there. Search gets hammered at midnight — add
> `productsd` copies. `orderd` doesn't move."

**Pre-empt the eye-roll.** Somebody in this room has been burned by premature
microservices:

> "And look — this isn't 'microservices because microservices'. There are three
> services because there are three different scaling needs. If your system has
> two, build two."

---

## Slide 7 · But now they have to talk

**~1 min · 0:09**

The bridge into Part 2. Keep it short.

> "One order needs two questions answered by other services: who is this, and
> what does this cost. Inside one app those were function calls. Now they cross
> a network.
>
> So — how should services call each other?"

---

## Slide 8 · What gRPC is

**~2 min · 0:11**

**Lead with the definition. Do not lead with the joke.**

> "gRPC lets you call a function that lives on another machine, as if it were
> local. You write the function down — name, inputs, outputs — in a file, and a
> compiler turns that file into real code for both sides."

*Then* the acronym, as a footnote:

> "RPC is Remote Procedure Call. The `g`… officially stands for 'gRPC'. The
> acronym contains itself. It's never officially meant Google — they reassign it
> every release as a joke."

🔶 **If behind:** skip the acronym entirely.

---

## Slide 9 · The actual difference from REST

**~2 min · 0:13**

Read both code blocks aloud. The contrast does the work.

> "With REST you think about resources and verbs. What's the right noun? Is this
> a POST or a PUT? Which status code?
>
> With gRPC you think about functions. What does it take? What does it give
> back?
>
> That's the whole mental shift. HTTP/2, binary encoding, code generation — all
> machinery serving that one idea."

---

## Slide 10 · You write the contract, a compiler writes the code

**~2 min · 0:15**

> "This proto file is the only hand-written interface code in the project. One
> command turns it into five things."

The line that matters most to a fresher:

> "Forget to implement an RPC and the Go code **will not compile**. That's not a
> runtime 501 you find in production — it's a build failure on your laptop."

And the honest reason teams adopt it:

> "Nobody should write a client from a wiki page that was last accurate four
> months ago."

---

## Slide 11 · Why teams actually adopt gRPC

**~2 min · 0:17**

This slide is your credibility. Own the method.

> "People will tell you gRPC is for streaming and for speed. I wanted to know if
> that's true, so I counted. These are the real `.proto` files from 14 projects
> you've heard of. 611 RPCs. 50 of them stream — about 8%.
>
> Temporal: 121 RPCs, **zero** streaming. Qdrant: 30, zero. containerd: 17,
> zero."

Then invite the check — it's the strongest thing you can say:

> "The protos are in the repo under `docs/evidence`. The counts reproduce with
> one `grep`. Please go check me."

Second conclusion, say it plainly:

> "And not one of these 14 exposes gRPC to the public internet. So when someone
> asks 'should our mobile app speak gRPC?' — the industry's answer is mostly no."

---

## Slide 12 · When gRPC, when REST

**~1 min · 0:18**

> "This isn't a competition. It's a question of *where*. Inside your network,
> gRPC. Facing a browser or a third party, REST. You'll usually need both."

Set up the next slide:

> "And a browser genuinely cannot speak gRPC — it can't open a raw HTTP/2
> connection and control trailers the way the protocol needs. That's not AWS's
> fault or Google's. It's just true."

---

## Slide 13 · So we need a REST door

**~1 min · 0:19**

> "`gatewayd` translates REST to gRPC, and it's **generated from the same
> protos**. I didn't write it. Add four lines to an RPC and it gets a REST
> route."

Plant the seed for slide 18:

> "Leave those four lines out and it has **no** REST route at all. Hold that
> thought — we're going to use it on purpose."

---

## Slide 14 · Four options, one ruled out

**~3 min · 0:22**

Give all four a fair hearing. Somebody here runs EKS happily and they're right.

> "Lambda first, and let me be precise, because this isn't me preferring
> containers. gRPC needs a process that **stays listening**, holding an HTTP/2
> connection. Lambda has none."

**Read the quote off the slide, verbatim.** It's a hard stop, not an opinion:

> "AWS's own load balancer docs, on gRPC target groups: *'The only supported
> target types are instance and ip.'* And: *'You can't use Lambda functions as
> targets.'*"

> "EC2 works — but now you're building a deployment platform. EKS: Kubernetes is
> genuinely excellent at this, and the cost is honest — a control plane, nodes,
> upgrades every few months, CNI, ingress, RBAC."

---

## Slide 15 · Why ECS for now

**~2 min · 0:24**

The reframe that makes this easy to accept:

> "The question isn't serverless versus containers. It's: how much orchestration
> do four services actually need? Both ECS and EKS run this correctly. One of
> them asks you to operate Kubernetes first."

Then remove the fear of a wrong decision — this matters to juniors in the room
who are told Kubernetes is the only real answer:

> "And moving later is **not a rewrite**. The container, the contract, the health
> check, the graceful shutdown, the load-balancing fix — all of that comes with
> you unchanged. Only the YAML around them changes.
>
> 'For now' is a legitimate engineering answer. 'Forever' rarely is."

---

## Slide 16 · Three words

**~1 min · 0:25**

Do not skip this, even for an advanced room. It costs 45 seconds and stops
people silently falling behind.

> "Task: one running container with its own IP — one copy of your service. Task
> definition: the recipe. And it's versioned — editing it makes revision 2,
> revision 1 never changes. Fargate: 'run this, don't make me manage a server.'"

---

## Slide 17 · 🔴 DEMO 1 — is this really ECS?

**~3 min · 0:28**

This answers the question the skeptic in row three is already forming.

```sh
make ps
```

Let it scroll. Then scroll **back up** to step 4 and point:

> "This variable — `ECS_CONTAINER_METADATA_URI_V4`. Nothing in my code sets it.
> The platform injected it. The task can describe itself: its cluster, its task
> ARN, its family and revision, its availability zone."

(The revision number is whatever your applies have reached — the slide shows
`rev 1`, mine was `rev 3` after a few redeploys. The point is that a revision
*exists*, not its value.)

Then step 5, which is the real prize:

> "And this one. `AWS_CONTAINER_CREDENTIALS_FULL_URI`. The AWS SDK reads that on
> its own and gets temporary credentials. **There is no access key anywhere** —
> not in the image, not in git, not in a `.env` file. That's the whole 'no API
> keys on ECS' story in one `docker inspect`."

**Then volunteer the emulator's limits before anyone catches you:**

> "Two honest gaps. `healthStatus` says UNKNOWN and `launchType` comes back
> empty, even though the task definition asks for FARGATE. The emulator just
> doesn't echo those back. Real ECS reports both. I'd rather tell you that than
> have you find it."

That admission buys you more trust than the demo does.

---

## Slide 18 · Two contract choices

**~2 min · 0:30**

> "The order request carries **no price**. The client sends what, and how many.
> `orderd` asks `productsd` what it costs."

Make it concrete and a little scary:

> "Think about what the alternative means during a sale. If the client sends the
> price, a client can ask 'is this ₹2,000 sale price real?' and then submit
> ₹200."

Then the second:

> "And `CheckAvailability` has no HTTP annotation. On purpose. It works over
> gRPC and it's a 404 over REST. Four lines of annotation are the entire
> difference between an internal and a public API."

---

## Slide 19 · Out of stock is not an error

**~1 min · 0:31**

> "Out of stock isn't a transport failure. It's an answer. So it comes back as
> OK, with a status and a reason enum — not as a 500."

The line that makes it stick:

> "If your client does `strings.Contains(err.Error(), "stock")`, then somebody
> rephrasing a log message breaks production. An enum can't be rephrased."

---

## Slide 20 · 🔴 DEMO 2 — browse, then buy

**~3 min · 0:34**

```sh
make demo
```

Narrate while it runs — do not read the output silently:

> "Search, no auth — that's the read path that scales. Log in as a **seeded**
> user, and I'll come back to why that word matters. Order placed: two network
> hops, and notice the client sent no prices. Same idempotency key again: same
> order, not a second one. Then out of stock, then only-one-left, then no token."

Then REST:

```sh
make dev-rest
```

> "Identical flow over plain HTTP and JSON. Same services, same contract,
> generated gateway."

Then the 404 — this is the slide-18 payoff:

```sh
curl -s -o /dev/null -w 'REST -> %{http_code}\n' \
  -X POST http://localhost:8080/v1/products:checkAvailability -d '{}'
```

> "404. Four missing lines."

🔶 **If behind:** skip `make dev-rest`, keep the 404.

---

## Slide 21 · Storage decides

**~2 min · 0:36**

> "Here's the question people get wrong. 'Can I run three copies of this?' has
> nothing to do with traffic. It's about where the data lives."

Walk the four rows, then:

> "`orderd` writes. Three copies would be three different databases. A Fargate
> task's filesystem dies with the task — and I'm not hiding that, it's the
> lesson."

---

## Slide 22 · The code refuses to let you get it wrong

**~2 min · 0:38**

```sh
make show-guard
```

> "Terraform refuses to scale the writer, and tells you why. The rule is
> enforced, not remembered in somebody's head."

Then be straight about the shortcut, because an experienced person is already
thinking it:

> "And yes — SQLite on the task is a conference-demo shortcut. Use a managed
> database. Dropping RDS took my AWS deploy from about ten minutes to about two,
> which on a conference stage is the single biggest risk reduction available.
> `DB_DRIVER=postgres` is the whole application-side switch."

The EFS trap, since someone always asks:

> "And don't reach for EFS to fix it. SQLite's own docs: file locking is buggy on
> many network filesystems, which leads to corruption, and *there is nothing
> SQLite can do to prevent it*. That's a correctness problem, not a cost one."

---

## Slide 23 · Three tasks, one gets everything

**~2 min · 0:40**

**This is the headline. Slow right down.**

> "You scale `userd` to three. All three healthy. DNS returns all three
> addresses. And one task serves one hundred percent of the traffic.
>
> I measured it in this repo. 120 requests. 120 landed on one task. The other
> two got nothing."

Pause. Then:

> "Everybody's first conclusion is that service discovery is broken. It isn't.
> Discovery did its job perfectly — it handed back three addresses.
>
> **The client never asked to balance.**"

---

## Slide 24 · Why: `pick_first`

**~2 min · 0:42**

Explain it without making the gRPC authors sound foolish:

> "gRPC's default policy is `pick_first`. Resolve the name, connect to one
> address, send everything down that connection forever.
>
> And that's a *reasonable* default! With HTTP/2 one connection multiplexes many
> requests, so opening more looks wasteful. It's optimised for the other end
> being a single load balancer.
>
> On ECS the other end is three tasks with three IPs. So the default is wrong
> here — and nothing warns you. No error. No log line. No failed health check."

---

## Slide 25 · The fix is two halves

**~3 min · 0:45**

> "Two settings, and **either one alone does nothing.**"

Spend your time on the prefix, because this is where people half-fix it:

> "`dns:///` is not decoration. With a bare `host:port`, gRPC uses the
> *passthrough* resolver — it hands the name straight to the dialer and gives
> you exactly **one** address. So you can set `round_robin`, feel good about it,
> and it still has nothing to balance over. That's the version that looks fixed
> and isn't."

Then the server half:

> "And on the server, recycle connections so clients re-resolve. Without it, a
> client that connected *before* you scaled out never learns the new tasks
> exist. Which is precisely when you scale — during the sale."

---

## Slide 26 · 🔴 DEMO 3 — break it, then fix it

**~5 min · 0:50 🔶 This is the demo to protect. Cut others, never this one.**

Prometheus tab should already be open with the query from §0.

**Step 1 — ship the bug on purpose.**

```sh
terraform -chdir=terraform/envs/local apply -auto-approve -var lb_policy=pick_first
make dns && make scale N=3 && make forward
```

> "Three tasks. All healthy — you can see desired three, running three."

```sh
make demo-load
```

Switch to Prometheus. **One line moving.**

> "Three healthy tasks. One line. And if I check service discovery, all three
> addresses are registered. Nothing is broken — the client just never asked."

**Step 2 — the fix.**

```sh
terraform -chdir=terraform/envs/local apply -auto-approve
make dns && make forward && make demo-load
```

Three lines climbing together. Say the measured numbers:

> "Forty, forty, forty out of a hundred and twenty. That's the two settings.
> Nothing else changed — same image, same task definition, same DNS."

(Repeat runs land at 39–41 per task; the split is even, not exact.)

**Step 3 — kill a task mid-load.**

```sh
make demo-load &
docker rm -f $(docker ps --filter "name=ministack-ecs-.*-userd$" -q | head -1)
```

> "Zero failed requests. That's graceful shutdown and `stopTimeout` agreeing."

Verified in this repo: **300 requests driven, a `userd` task killed mid-flight,
0 failures.**

> **Why a seeded user matters** (callback to Demo 2): a user created by
> `Register` lives on exactly **one** task, because `userd`'s database is baked
> into the image. At three tasks, two of them have never heard of them — and it
> fails looking *exactly* like the bug you just fixed. The load script always
> logs in as a seeded user.

---

## Slide 27 · Shutdown order

**~2 min · 0:52**

> "Health to NOT_SERVING **first**, so you stop being given new work. *Then*
> drain. Then a hard stop as a backstop. Getting that order backwards means you
> keep accepting requests while shutting down."

The number that catches people:

> "And `stopTimeout` has to be **longer** than your drain. If ECS waits 10
> seconds and you drain for 15, it kills you mid-drain and every line of
> graceful-shutdown code you wrote did nothing."

---

## Slide 28 · Four bugs only a real deploy found

**~3 min · 0:55**

This is the most valuable slide for experienced people. Don't rush it.

> "The OpenTelemetry one is my favourite, because it's *perfectly* invisible
> locally. With no tracing endpoint configured, the tracer short-circuits — so
> locally it never builds the object that crashes. Deploy it, set an endpoint,
> every task crash-loops."

The Cloud Map one — tell it as a story:

> "This one cost me the most. I had a deprecation warning on a field, so I
> tidied it up: emptied the block. Which makes the provider send no health
> config at all. Cloud Map then never accepts ECS's health reports, every
> instance stays UNHEALTHY, unhealthy instances are excluded from DNS — and my
> client failed with `code 14: no children to pick from`.
>
> Meanwhile: four tasks running, four Cloud Map services, VPC DNS enabled.
> Everything looked healthy. The comment in that file now says, in capitals, do
> not clean this up."

Land the lesson:

> "Unit tests wouldn't have found any of these. `terraform validate` wouldn't
> have. **Deploy it once before you trust it.**"

---

## Slide 29 · One module set, two environments

**~2 min · 0:57**

```sh
diff terraform/envs/local/provider.tf terraform/envs/aws/provider.tf
```

> "That diff is the talk. Local has fake credentials and an endpoints block
> pointing at the emulator. AWS has a region. Everything that *describes the
> deployment* is shared verbatim — not a copy, not a simplified local version.
> The same files.
>
> Which is the real argument for emulating locally: there is no second set of
> manifests to drift."

If you deployed to real AWS the day before, show it here — **a delta only**:

```sh
make ps-aws
```

---

## Slide 30 · Takeaways

**~2 min · 0:59**

Read them. Don't elaborate; they're a summary, and the room is full.

If you only have time for three: **boundaries where scaling differs**,
**`pick_first`**, and **deploy it once before you trust it**.

---

## Slide 31 · Clone it

**~1 min · 1:00**

> "It's all open. `make test` needs no Docker and no AWS. `make local-up` gives
> you real ECS tasks on your laptop. And `make scale N=3` lets you break it
> yourself.
>
> `SPEC.md` marks every claim as verified or assumed — including the ones I got
> wrong first."

**Thank you.** Then take questions.

---

## Panic buttons

| It broke | Do this |
|---|---|
| **A demo hangs** | `Ctrl-C`. Say *"this is why I recorded it"* and play the backup clip. Do not debug on stage. |
| `connection refused` on `localhost:5005x` | `make forward`. `awsvpc` tasks have no host port — that's faithful to Fargate, not a bug. |
| `code 14 "no children to pick from"` | `make dns`. Terraform replaced the task containers and the aliases went with them. |
| `Unavailable ... user service unavailable` right after an apply | `make wait-ready`. `tf-local-apply` already runs it, so you should never see this — it probes the real `orderd → userd` hop and blocks until it works. |
| Prometheus graph is empty | `curl -s localhost:9090/api/v1/targets \| jq '.data.activeTargets[].health'` — expect four `up`. If not, `docker compose restart prometheus`. |
| Login fails after `make scale N=3` | You used a registered user, not a seeded one. Use `demo@example.com` / `demo-password`. |
| Docker is wedged | `make local-down && make local-up && make tf-local-apply`. ~90 seconds. Talk through slide 29 while it runs. |
| Venue wifi is gone | Everything except `make ps-aws` is **fully local**. Say so — it's a selling point, not an excuse. |

### Cut-lines, in the order to use them

1. Slide 8 acronym joke
2. `make dev-rest` in Demo 2 (keep the 404)
3. Slide 27 (shutdown order) — fold one sentence into Demo 3
4. Slide 12 (gRPC vs REST table) — the room usually gets it from slide 9
5. Jaeger in Demo 2

**Never cut:** Demo 1 step 5 (no access keys), slides 23–26 (the bug and the
fix), slide 28 (the four bugs).

### Questions you will get

| Question | Short answer |
|---|---|
| *Why not an ALB?* | A gRPC target group needs an HTTPS listener → an ACM cert → a domain. Above the cut line for 40 minutes. `gatewayd` is the right thing to put one in front of. |
| *Why not Kubernetes?* | It would work. Four services need four task definitions — that's the whole orchestration requirement. Slide 15 says when to switch. |
| *Is SQLite serious?* | No, and the slide says so. Use RDS. It's a shortcut that halves my stage risk, and `DB_DRIVER=postgres` is the switch. |
| *Does Service Connect fix the load balancing?* | Yes — its proxy balances per request rather than per connection, which is why `appProtocol: grpc` is set in the task definition. I show the client-side fix because it works everywhere, including outside AWS. |
| *What about mTLS between services?* | Not here. Traffic is inside the VPC and authenticated at the packet level. If you need mTLS everywhere, that's one of the reasons to move to a mesh. |
| *Does this work with LocalStack?* | Its Community edition ended in March 2026, and ECS, ECR and Cloud Map were never in the free image. This uses Ministack (MIT). |
| *How much did this cost?* | Four small Fargate tasks for a talk is cents. The thing that actually bills you is a NAT Gateway, and this VPC deliberately has none. Check current Fargate pricing — I'm not quoting rates from memory. |
