# Presenter deck

Companion to [`PRESENTATION.md`](PRESENTATION.md) — **48 slides, about 45
minutes** with the three demos.

**The questions in here are deliberately NOT on the slides.** Asking them is
what turns a lecture into a conversation, and a question printed on a slide
answers itself. They are marked **ASK**.

```sh
make slides        # http://localhost:8030, live reload
```

| Slot | Cut |
|---|---|
| **45 min** | nothing |
| **35 min** | 10, 12, 23, 35, 39, 45 |
| **25 min** | also 13, 16, 18, 30, 33 · and describe Demo 3 instead of running it |

**Never cut:** 6 and 7 (the use case and the boundary test), 19 (architecture),
21 (no price), 29 (every AWS component), 34 (the `diff`), 36 (Demo 1),
40–44 (the payoff), 47 (the close).

**Before you walk in:** [§0 Pre-flight](#0--pre-flight) · [§Panic buttons](#panic-buttons)

---

## 0 · Pre-flight

The night before, and again 20 minutes before.

```sh
cd ~/projects/grpc-ecs-demo
docker compose down -v
docker ps -aq --filter "name=ministack-ecs-" | xargs -r docker rm -f
docker ps -aq --filter "name=forward-"       | xargs -r docker rm -f
docker network rm ecom-dns ecom-infra 2>/dev/null
rm -rf data

make test && make local-up && make images && make tf-local-apply && make forward
make ps && make demo && make api-coverage      # must be 20/20
```

Then, in separate terminals:

```sh
make slides                       # :8030, leave it running
eval "$(make -s local-env)"       # U, O, P, BASE for hand-run commands
```

**Terminal:** 18pt+, notifications off. **Pre-deploy AWS the day before** and
demo a read-only `make ps-aws` — never a cold apply on venue wifi.

---

## 1 · Title

**1:00** — "Three Go services from `localhost` to Fargate, and the Terraform
that does it is the same on my laptop and in a real AWS account, one provider
block apart. Everything here was run, not sketched."

## 2 · What you will leave with

**1:00 · 2:00** — Read the three. Don't oversell the third; you cover it
properly at the end.

## 3 · It is sale season

**1:00 · 3:00** — **ASK:** *"Sale season — how many of you have a tab open
right now?"* Wait for hands. Five seconds, and it buys you the room.

## 4 · What everyone is actually doing

**1:30 · 4:30** — **ASK, and wait for answers:** *"Your last sale — how many
products did you open? And how many did you buy?"* The gap between those two
numbers is the architecture.

Then: *"I'm not putting someone else's traffic graph on a slide. Your own
browsing is better evidence, because you trust it."*

## 5 · Suppose the store is one application

**1:30 · 6:00** — Be generous to the monolith. "It works. This isn't a story
about anyone being stupid." Then the cost: you scale checkout to survive
search; one deploy, one rollback, one page.

## 6 · Two paths through one system  ·  NEVER CUT

**1:30 · 7:30** — Trace the diagram. Green is reads, red is writes. Then the
dashed arrows: placing an order makes `orderd` ask the other two.

> "Reads and writes do not grow at the same rate."

## 7 · So: three services  ·  NEVER CUT

**2:00 · 9:30** — Give them the test slowly; it's the thing they can use on
Monday. Then pay the cost out loud: every split is a network hop, a failure
mode and another deploy. "If yours pays for two, build two."

## 8 · But now they have to talk

**0:45 · 10:15** — Short bridge. Don't linger.

## 9 · What gRPC is

**1:30 · 11:45** — **Lead with the definition, never the joke.** Then the
acronym as a throwaway.

## 10 · The mental shift  ·  🔶 first to cut

**1:15 · 13:00** — Read both blocks. The contrast does the work.

## 11 · You write the contract

**1:30 · 14:30** — "Forget to implement an RPC and it will not compile. That's
a build failure on your laptop, not a 501 in production."

## 12 · Honest about performance  ·  🔶 cut at 25

**1:15 · 15:45** — Buys credibility. Someone has read "gRPC is 7× faster" and
is ready to repeat or challenge it. "I haven't benchmarked this, so I'm not
putting a number up and having you quote me."

## 13 · How much smaller? Measure it.  ·  🔶 cut at 25

**1:30 · 17:15** — **ASK first:** *"Protobuf versus JSON for the same order —
what ratio would you guess?"* Then run it live:

```sh
make wire-size
```

## 14 · A browser cannot speak gRPC

**1:30 · 18:45** — Be precise: it can't open a raw HTTP/2 connection and
control trailers. Three options, and most talks show one and call it the
answer.

## 15 · So do we need `gatewayd`?

**1:30 · 20:15** — **Answer in one word, then justify.** "No. It's a choice. I
picked it because it makes the lesson visible — a separate ECS service you
watch deploy next to stateful ones."

## 16 · Not every RPC needs REST  ·  🔶 cut at 25

**1:15 · 21:30** — "Nine of ten. The proto file *is* the access-control
decision, and you can read it in a code review."

## 17 · Four options, one ruled out

**1:45 · 23:15** — Give all four a fair hearing. **Read the AWS quote word for
word** — it's a hard stop, not an opinion.

## 18 · Why ECS for now  ·  🔶 cut at 25

**1:15 · 24:30** — "Not serverless versus containers. How much orchestration do
four services need?" Then remove the fear: moving later is not a rewrite.

## 19 · The architecture  ·  NEVER CUT

**2:00 · 26:30** — Walk the diagram: the VPC, two AZs, the services, Cloud Map,
RDS. Land on: **nobody here knows anybody's IP address.**

## 20 · One order, end to end

**1:30 · 28:00** — Follow the arrows. Emphasise: the total is computed from
**catalogue** prices.

## 21 · The request has no price in it  ·  NEVER CUT

**1:15 · 29:15** — Make it concrete: "If the client sends the price, a client
can ask 'is this ₹2,000 sale price real?' and then submit ₹200."

## 22 · You tap "Buy"

**1:15 · 30:30** — **ASK:** *"Midnight, patchy 4G, the spinner spins, your
phone retries. Did you just buy one phone, or two?"* Wait. Someone says "two",
someone says "depends" — both useful.

## 23 · Three details that make it work  ·  🔶 cut at 35

**1:15 · 31:45** — The third is the one people miss: a unique index, not an
`if`. Two retries race; one loses and returns the stored order.

## 24 · "Out of stock" is not an error

**1:00 · 32:45** — "An enum cannot be reworded. A log message can."

## 25 · What decides whether a service can scale

**1:15 · 34:00** — **Say this carefully; I used to get it wrong.** Not "because
it writes" — writers scale fine. Because it writes to a file *inside the task*.

## 26 · So give it a database

**1:00 · 35:00** — One variable. Then `make show-guard`: Terraform refuses
`orderd`×3 on SQLite and allows it on RDS. **The guard lifts itself.**

## 27 · One instance, a database per service

**1:15 · 36:15** — "Terraform cannot create those databases — `CREATE DATABASE`
is SQL and the provider only speaks the AWS API. So each service makes its own
at boot." `make seed-check` proves it.

## 28 · Six modules  ·  Part 6 starts

**1:15 · 37:30** — "This is the part I'd most like you to take away."

## 29 · Every AWS component  ·  NEVER CUT

**2:00 · 39:30** — **Do not read the table.** Pick three: `enable_dns_hostnames`
is required for Cloud Map; the self-referencing SG rule; no NAT Gateway and no
ALB, both deliberate.

> **If asked about NAT:** the commonest way a demo account bills you. In
> production: private subnets plus VPC endpoints. Don't copy public subnets.

## 30 · The two IAM roles  ·  🔶 cut at 25

**1:15 · 40:45** — "Get these backwards and your task fails to start with an
error pointing at the wrong role. Ours is empty on purpose."

## 31 · Cloud Map is just DNS

**1:15 · 42:00** — Point at the three settings, then go straight to the story.

## 32 · A story about that last block

**1:45 · 43:45** — **Tell it as a mystery: symptoms first, cause last.** Four
tasks running, four services, DNS enabled, all green — and `code 14`.

## 33 · The same error, a second cause  ·  🔶 cut at 25

**1:00 · 44:45** — Found on real AWS. One error string, two unrelated causes,
and in both everything else looks fine.

## 34 · One pipeline  ·  NEVER CUT — the thesis

**1:45 · 46:30** — **Run the diff live.** "That diff is the talk. There is no
second set of manifests to drift."

## 35 · Where the laptop version is honest  ·  🔶 cut at 35

**1:15 · 47:45** — Volunteer the gaps before anyone finds them. Earns you the
right to have shown a local demo at all.

## 36 · DEMO 1 — is this really ECS?  ·  NEVER CUT

**2:30 · 50:15**

```sh
make ps
```

Scroll back to step 4 and point. Then step 5: *"There is no access key
anywhere — not in the image, not in git, not in a `.env` file."*

Then admit the gaps: `healthStatus` UNKNOWN, `launchType` empty on the
emulator.

## 37 · DEMO 2 — the code, running

**2:30 · 52:45**

```sh
make demo
curl -s -o /dev/null -w 'REST -> %{http_code}\n' \
  -X POST http://localhost:8080/v1/products:checkAvailability -d '{}'
```

Narrate while it runs. The 404 is the slide-16 payoff — always keep it.

## 38 · DEMO 3 — real AWS  ·  🔶 describe it at 25

**2:00 · 54:45** — Pre-deployed. `make ps-aws`, then `eval "$(make -s aws-env)"`.
"Now `healthStatus` says HEALTHY and `launchType` says FARGATE."

## 39 · No load balancer, just IPs  ·  🔶 cut at 35

**1:00 · 55:45** — `make aws-ip`. The cost: IPs change when a task is replaced.

## 40 · The setup  ·  NEVER CUT

**1:00 · 56:45** — Walk the checklist, ticking each. Everything is correct.

## 41 · 120 / 0 / 0  ·  NEVER CUT

**1:30 · 58:15** — **ASK BEFORE ADVANCING:** *"120 requests, three healthy
tasks. How many land on each?"* Most rooms say forty/forty/forty. Take a couple
of answers, **then** reveal.

Let the number sit. Then: *"Everybody's first conclusion is that service
discovery is broken. It isn't."* Slowly: **"The client never asked to
balance."**

## 42 · Why: `pick_first`

**1:15 · 59:30** — Don't make the gRPC authors sound foolish. "It's a correct
answer to a different question."

## 43 · The fix is two halves  ·  NEVER CUT

**1:30 · 61:00** — Spend the time on the prefix. "You can set `round_robin`,
feel good, and it still has nothing to balance over. That's the version that
looks fixed and isn't."

## 44 · Run it both ways yourself  ·  NEVER CUT

**1:30 · 62:30**

```sh
make demo-lb-before
make demo-lb-after
```

+120/+0/+0, then +40/+40/+40. "Same image, same task definition, same DNS."

## 45 · An ALB only covers the first hop  ·  🔶 cut at 35

**1:00 · 63:30** — "Add an ALB and stop there, and the inside of your system is
still pinned to one task."

## 46 · What to take away

**1:00 · 64:30** — Read them. If only three: the boundary test, one stack with
two provider blocks, and `pick_first`.

## 47 · Green dashboards  ·  NEVER CUT

**0:45 · 65:15** — The closing line. Deliver it and **stop talking.**

## 48 · Clone it

**0:45 · 66:00** — "`SPEC.md` marks every claim verified or assumed, including
the ones I got wrong first." **Thank you.**

---

## Panic buttons

| It broke | Do this |
|---|---|
| **A demo hangs** | `Ctrl-C`, say *"this is why I recorded it"*, play the clip. Do not debug on stage. |
| `connection refused` on `localhost:5005x` | `make forward` — `awsvpc` tasks have no host port |
| `code 14 "no children to pick from"` | `make dns`, then `make wait-ready` |
| Login fails after scaling | you used a registered user, not a seeded one. `demo@example.com` / `demo-password` |
| Slides won't load | port 8030 — `make slides`. 8080 is `gatewayd`. |
| A diagram is blank in the PDF | you exported without `--allow-local-files`. `make slides-pdf` has it. |
| Docker wedged | `make local-down && make local-up && make tf-local-apply` — ~90s. Talk through slide 34. |
| Venue wifi gone | everything except Demo 3 is local. Say so — it's a selling point. |

### Questions you will get

| | Short answer |
|---|---|
| *Why not an ALB?* | A gRPC target group needs HTTPS → a cert → a domain. Above the cut line. |
| *Why not Kubernetes?* | It would work. Four services need four task definitions. Slide 18 says when to switch. |
| *Why not ConnectRPC and drop the gateway?* | Good option, and slide 14 says so. The services use standard `grpc-go` because that's what most teams have. |
| *Is SQLite serious?* | No — and `use_rds=true` is one variable. Slides 25–27. |
| *Does Service Connect fix the balancing?* | Yes — an Envoy sidecar per task, per request, no client code. I show the client fix because it works anywhere. |
| *Why public subnets?* | Cost, for a demo. **In production: private subnets plus VPC endpoints.** Don't copy it. |
| *How much did it cost?* | Four Fargate tasks plus a `db.t4g.micro` is cents per hour. The thing that bills is a NAT Gateway, and there is none. |
