# Manual testing with grpcurl and curl

Copy-paste commands for poking every API by hand — locally, and on real AWS
**with no load balancer and no domain, just IP addresses.**

- [1. No ALB, no domain — does that work?](#1-no-alb-no-domain--does-that-work)
- [2. Setup](#2-setup)
- [3. gRPC by hand](#3-grpc-by-hand)
- [4. REST by hand](#4-rest-by-hand)
- [5. Against real AWS, by IP](#5-against-real-aws-by-ip)
- [6. Postman](#6-postman)
- [7. Seeded data and deliberate failures](#7-seeded-data-and-deliberate-failures)

---

## 1. No ALB, no domain — does that work?

**Yes. Both gRPC and REST run over plain IP, and that is what this deployment
does.** There is no load balancer, no Route 53 record and no TLS certificate
anywhere in it.

> ### This is a DEMO shortcut, not a recommendation
>
> It exists to keep the moving parts down on a conference stage. The production
> shape is in [§1b](#1b-the-ideal-path) — read that before copying anything
> here into a real account. **The one thing never to copy: public subnets with
> public task IPs.**

| | Needs a domain? | Needs a cert? | Works on a bare IP? |
|---|---|---|---|
| **gRPC over h2c** (plaintext HTTP/2) | No | No | **Yes** |
| **REST/JSON** through `gatewayd` | No | No | **Yes** |
| gRPC **through an ALB** | Yes | **Yes** — ALB gRPC target groups require an HTTPS listener | n/a |

That last row is the whole reason there is no ALB here. From the AWS docs on
gRPC target groups: *"The only supported listener protocol is HTTPS."* HTTPS
means an ACM certificate, which means a domain — three moving parts to show a
lesson that a plain IP already shows.

**What you give up by skipping it**, stated plainly:

- **Addresses are not stable.** With `awsvpc`, every task has its own ENI and
  its own public IP, and that IP changes whenever the task is replaced — a
  deploy, a scale event, or AWS retiring the Fargate platform version
  underneath it. Re-run `make aws-ip` instead of writing an address down.
- **No TLS**, so no browser calling it from an HTTPS page (mixed content).
  `curl`, `grpcurl` and Postman are fine.
- **No per-request balancing for outside traffic** — you hit one task's IP, so
  you reach that one task. The *internal* hops still balance across tasks via
  Cloud Map plus the client-side fix.

None of that affects the demo.

---

## 1b. The ideal path

| | This demo | Production |
|---|---|---|
| Public entry | each task's public IP, SG locked to one `/32` | **Route 53 + ACM + ALB (HTTPS)** in front of `gatewayd` only |
| Subnets | public, `assign_public_ip = true` | **private**, plus VPC endpoints for ECR, S3, logs, Secrets Manager |
| NAT Gateway | none | not needed, because of those endpoints |
| Service-to-service | Cloud Map DNS + client-side `round_robin` | the same, **or** ECS Service Connect |
| TLS | none | ALB terminates; in-VPC plaintext, or mTLS via a mesh |
| Database | SQLite on the task, or one `db.t4g.micro` | RDS **Multi-AZ**, backups on, deletion protection **on** |
| Operator access | public IP + an SG rule | **SSM port forwarding** — no public IP, no inbound rule |

### How load balancing works with multiple replicas

The thing people get wrong: **an ALB fixes the edge and does nothing for
service-to-service.**

```
                 Route 53   aws-demo.example.com
                      |
                ALB (HTTPS, ACM)              <-- LAYER 1
                balances PER REQUEST
                target_type = ip
         +------------+------------+
         v            v            v
   gatewayd-1    gatewayd-2    gatewayd-3     3 replicas

  each gatewayd is itself a gRPC CLIENT:      <-- LAYER 2
         |            |            |
         +------------+------------+
                      |
           userd.ecom.local  (Cloud Map -> 3 A records)
         +------------+------------+
         v            v            v
     userd-1      userd-2      userd-3        3 replicas
```

**Layer 1 — the edge.** The ALB spreads requests across `gatewayd` replicas.
Set `load_balancing.algorithm.type = least_outstanding_requests`, **not**
`round_robin`: with HTTP/2 a single connection carries many requests, so
counting connections says nothing about which task is actually busy.

**Layer 2 — inside the VPC.** Each `gatewayd` replica opens its own gRPC
connections to `userd`, and the ALB is not in that path. So every client has to
balance for itself:

| | Cloud Map + client `round_robin` | ECS Service Connect |
|---|---|---|
| Mechanism | `dns:///` resolves every task IP; the client spreads RPCs | an **Envoy sidecar** in each task resolves Cloud Map and balances |
| Granularity | per RPC | per request, plus retries and outlier detection |
| Client code needed | the two settings in `internal/platform/grpcclient` | **none** |
| Cost | free | CPU and memory for a sidecar in every task |
| Works off AWS | yes | no |

**This repo uses the first.** It works anywhere, and it makes the default
failure visible — which is the lesson. Service Connect is the better default
for an AWS-only system: set `appProtocol: grpc` (already set in the task
definition) and it balances per request with no client configuration.

> **3 replicas × 3 replicas is nine paths, and a load balancer you can see
> covers only the first hop.** Add an ALB and stop there, and the inside of
> your system is still pinned to one task.

> **Security note:** public task IPs are only reachable because the security
> group allows your `/32`. `operator_ingress_cidrs` is **empty by default** —
> set it to your own address and nothing else. Check the address you actually
> egress from with `curl ifconfig.me`, from wherever you will be presenting.

---

## 2. Setup

```sh
brew install grpcurl jq        # or: go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
```

**Locally**, `awsvpc` tasks have no host port (faithful to Fargate), so publish
them first:

```sh
make forward
```

That gives you `localhost:50051` (userd), `:50052` (orderd), `:50053`
(productsd) and `:8080` (gatewayd REST).

**Load the addresses into your shell rather than typing them.** One command,
and every example below works as written:

```sh
eval "$(make -s local-env)"     # local
eval "$(make -s aws-env)"       # real AWS - reads the live task IPs
```

Either way you get:

```sh
U=...:50051     # userd        BASE=http://...:8080   # gatewayd REST
O=...:50052     # orderd       REST_BASE=$BASE        # used by scripts/rest-smoke.sh
P=...:50053     # productsd
```

> **Re-run the `aws-env` one after any deploy or scale event.** `awsvpc` gives
> each task its own ENI, so the IPs move when a task is replaced.

> **No `.proto` files needed.** Every service registers gRPC **server
> reflection**, so `grpcurl` and Postman discover the API from the server
> itself.

---

## 3. gRPC by hand

### Discover the API

```sh
grpcurl -plaintext $U list
grpcurl -plaintext $P list
grpcurl -plaintext $O list

# every method on one service, with full request/response types
grpcurl -plaintext $P describe product.v1.ProductService

# the shape of one message
grpcurl -plaintext $O describe order.v1.CreateOrderRequest
```

### Health checks

Standard `grpc.health.v1` — the same endpoint the ECS container health check
calls.

**How it is wired, in four layers.** Each one does something different:

| Layer | Where | If it reports unhealthy |
|---|---|---|
| **1 · your code** | `internal/platform/grpcserver/server.go` registers `grpc.health.v1.Health` and only sets `SERVING` once everything is wired up | callers and all layers below find out |
| **2 · the container** | the task definition's `healthCheck` runs `/healthcheck` | **ECS replaces the task** |
| **3 · service discovery** | `health_check_custom_config` makes Cloud Map accept ECS's verdict | the task is **dropped from DNS** |
| **4 · load balancer** | ALB target group, production only | the target stops getting requests |

**Why a custom binary?** The runtime image is `distroless/static` — no shell,
no `curl`, no `grpc-health-probe`. Rather than grow the base image, a ~80-line
Go probe is baked in:

```hcl
# gRPC services
command = ["CMD", "/healthcheck", "-addr", "localhost:50051", "-service", "userd"]
# gatewayd, which speaks HTTP
command = ["CMD", "/healthcheck", "-http", "http://localhost:8080/healthz"]
```

It dials `passthrough:///` on purpose: the probe must hit **its own**
container, so DNS resolution and load balancing would be actively wrong there.

**It is also the shutdown path.** On `SIGTERM` the status flips to
`NOT_SERVING` **before** draining, so ECS and any load balancer stop sending
new work while in-flight RPCs finish. And `stopTimeout` (30s) must exceed the
app's `SHUTDOWN_TIMEOUT` (15s), or ECS SIGKILLs mid-drain.

Try all three:

```sh
grpcurl -plaintext -d '{"service":"userd"}'     $U grpc.health.v1.Health/Check
grpcurl -plaintext -d '{"service":"productsd"}' $P grpc.health.v1.Health/Check
grpcurl -plaintext -d '{"service":"orderd"}'    $O grpc.health.v1.Health/Check
# -> {"status": "SERVING"}
```

### Browse — the read path, no auth

```sh
# full-text search (FTS5 on sqlite, tsvector + ts_rank on postgres)
grpcurl -plaintext -d '{"query":"cancelling"}' \
  $P product.v1.ProductService/SearchProducts

# prefix matching: "head" finds "headphones"
grpcurl -plaintext -d '{"query":"head"}' \
  $P product.v1.ProductService/SearchProducts | jq -r '.products[].title'

# list, with a category filter and paging
grpcurl -plaintext -d '{"category":"footwear","pageSize":5}' \
  $P product.v1.ProductService/ListProducts | jq -r '.products[] | "\(.id)  \(.title)"'

# one product
grpcurl -plaintext -d '{"id":"p-1005"}' \
  $P product.v1.ProductService/GetProduct | jq '.product | {title, stock, priceMinor}'
```

### Register and log in

```sh
# register a NEW user (note: on sqlite at >1 task this lands on ONE task only)
grpcurl -plaintext -d '{"name":"Test User","email":"test@example.com","password":"hunter2hunter2"}' \
  $U user.v1.UserService/Register

# log in as a SEEDED user - always use these once userd has more than one task
TOKEN=$(grpcurl -plaintext -d '{"email":"demo@example.com","password":"demo-password"}' \
  $U user.v1.UserService/Login | jq -r .token)
echo "${TOKEN:0:40}..."

# resolve the caller from the token (this is the hop orderd makes internally)
grpcurl -plaintext -H "authorization: Bearer $TOKEN" -d '{}' \
  $U user.v1.UserService/VerifyToken | jq '.user'
```

### The internal-only RPC

`CheckAvailability` has no `google.api.http` annotation, so it works over gRPC
and is a 404 over REST. This is how `orderd` prices a cart:

```sh
grpcurl -plaintext -d '{"items":[{"product_id":"p-1001","quantity":2},{"product_id":"p-1003","quantity":1}]}' \
  $P product.v1.ProductService/CheckAvailability | jq
```

### Place an order

Note what is **not** in the request: a price. The client sends ids and
quantities only.

```sh
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"manual-1"}' \
  $O order.v1.OrderService/CreateOrder | jq '.order | {id, status, totalMinor, lines}'
```

**Send the same key again** — you get the same order back, not a second one:

```sh
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"manual-1"}' \
  $O order.v1.OrderService/CreateOrder | jq '{replay: .idempotentReplay, id: .order.id}'
# -> { "replay": true, ... }  and the SAME id
```

### Read orders back

```sh
grpcurl -plaintext -H "authorization: Bearer $TOKEN" -d '{"pageSize":5}' \
  $O order.v1.OrderService/ListOrders | jq -r '.orders[] | "\(.id)  \(.status)  \(.totalMinor)"'

ORDER_ID=$(grpcurl -plaintext -H "authorization: Bearer $TOKEN" -d '{"pageSize":1}' \
  $O order.v1.OrderService/ListOrders | jq -r '.orders[0].id')
grpcurl -plaintext -H "authorization: Bearer $TOKEN" -d "{\"id\":\"$ORDER_ID\"}" \
  $O order.v1.OrderService/GetOrder | jq '.order.status'
```

### Things that must fail

```sh
# no token -> Unauthenticated, rejected via the hop to userd
grpcurl -plaintext -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"x"}' \
  $O order.v1.OrderService/CreateOrder

# bad token -> Unauthenticated
grpcurl -plaintext -H "authorization: Bearer nonsense" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"y"}' \
  $O order.v1.OrderService/CreateOrder

# empty cart -> InvalidArgument
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"items":[],"idempotency_key":"z"}' $O order.v1.OrderService/CreateOrder

# missing idempotency key -> InvalidArgument
grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}]}' \
  $O order.v1.OrderService/CreateOrder

# wrong password -> Unauthenticated
grpcurl -plaintext -d '{"email":"demo@example.com","password":"wrong"}' \
  $U user.v1.UserService/Login
```

### Rejections, which are NOT errors

Out of stock comes back as `OK` with a `REJECTED` status and a reason enum:

```sh
for id in p-1003 p-1005 p-9999; do
  printf '%-8s ' "$id"
  grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
    -d "{\"items\":[{\"product_id\":\"$id\",\"quantity\":3}],\"idempotency_key\":\"r-$id-$RANDOM\"}" \
    $O order.v1.OrderService/CreateOrder \
    | jq -r '.order | "\(.status)  \(.rejectionReason // "-")"'
done
```

```
p-1003   ORDER_STATUS_REJECTED  REJECTION_REASON_OUT_OF_STOCK
p-1005   ORDER_STATUS_REJECTED  REJECTION_REASON_INSUFFICIENT_STOCK
p-9999   ORDER_STATUS_REJECTED  REJECTION_REASON_PRODUCT_NOT_FOUND
```

---

## 4. REST by hand

Same services, through the generated gateway. Nothing here was hand-written.

```sh
curl -s "$BASE/healthz"

curl -s "$BASE/v1/products?pageSize=3" | jq -r '.products[].title'
curl -s "$BASE/v1/products/p-1005" | jq '.product | {title, stock}'
curl -s "$BASE/v1/products:search?query=cancelling" | jq -r '.products[].title'

TOKEN=$(curl -s -X POST "$BASE/v1/sessions" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@example.com","password":"demo-password"}' | jq -r .token)

curl -s "$BASE/v1/users/me" -H "Authorization: Bearer $TOKEN" | jq '.user'

curl -s -X POST "$BASE/v1/orders" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"items":[{"productId":"p-1001","quantity":1}],"idempotencyKey":"rest-1"}' \
  | jq '.order | {status, totalMinor}'

curl -s "$BASE/v1/orders" -H "Authorization: Bearer $TOKEN" | jq -r '.orders[].id'
```

**Note the JSON field names**: the proto says `product_id` and
`idempotency_key`, and the gateway accepts `productId` / `idempotencyKey`.
That is protobuf's JSON mapping, not something anyone configured.

### The internal RPC is NOT reachable

```sh
curl -s -o /dev/null -w 'REST -> %{http_code}\n' \
  -X POST "$BASE/v1/products:checkAvailability" -d '{}'
# -> REST -> 404
```

### Everything at once

```sh
make api-coverage     # all 10 RPCs + all 9 REST routes + the negative check
```

---

## 5. Against real AWS, by IP

No load balancer, no domain. Get the addresses:

```sh
make aws-ip
```

```
SERVICE     PORT  PUBLIC IP        TRY THIS
userd       50051 54.x.x.x         grpcurl -plaintext 54.x.x.x:50051 list
productsd   50053 3.x.x.x          grpcurl -plaintext 3.x.x.x:50053 list
orderd      50052 44.x.x.x         grpcurl -plaintext 44.x.x.x:50052 list
gatewayd    8080  18.x.x.x         curl http://18.x.x.x:8080/healthz
```

**Do not copy those by hand.** Load them instead — it reads the live task IPs
and sets the same variable names used above:

```sh
eval "$(make -s aws-env)"
```

```
export U=35.172.110.136:50051
export O=44.213.133.245:50052
export P=98.80.192.198:50053
export BASE=http://34.201.31.110:8080
export REST_BASE=http://34.201.31.110:8080
```

Now **every command in §3 and §4 works unchanged**:

```sh
grpcurl -plaintext $P list
curl -s "$BASE/v1/products:search?query=cancelling" | jq -r '.products[].title'
bash scripts/rest-smoke.sh          # picks up REST_BASE on its own
```

**If it hangs**, it is almost always the security group:

```sh
curl ifconfig.me          # your egress IPv4 right now
terraform -chdir=terraform/envs/aws apply \
  -var 'operator_ingress_cidrs=["YOUR.IP.HERE/32"]'
```

**Re-run `make aws-ip` after any deploy or scale event** — the IP moves with the
task.

### Closed-security-group alternative: SSM port forwarding

If you would rather not open any inbound rule at all, ECS Exec gives you a
tunnel instead. Needs `enable_execute_command = true` and the
[session-manager-plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html):

```sh
CLUSTER=ecom-aws
TASK=$(aws ecs list-tasks --cluster $CLUSTER --service-name orderd --query 'taskArns[0]' --output text)
RUNTIME=$(aws ecs describe-tasks --cluster $CLUSTER --tasks "$TASK" \
  --query 'tasks[0].containers[0].runtimeId' --output text)

aws ssm start-session \
  --target "ecs:${CLUSTER}_${TASK##*/}_${RUNTIME}" \
  --document-name AWS-StartPortForwardingSession \
  --parameters '{"localPortNumber":["50052"],"portNumber":["50052"]}'

# then, in another terminal, localhost works again:
grpcurl -plaintext localhost:50052 list
```

This is strictly better for a locked-down account: no public IP, no inbound
rule. It is **not** testable against the local emulator, which does not
emulate `ssmmessages` — that is what `make forward` stands in for.

---

## 6. Postman

Postman has had a gRPC client since 2022, and it needs no `.proto` file here.

1. **New → gRPC Request**
2. URL: `localhost:50053` (or the public IP from `make aws-ip`)
3. Tick **"Using server reflection"**
4. Pick a method, e.g. `product.v1.ProductService/SearchProducts`
5. Message: `{"query":"cancelling"}` → **Invoke**

For `orderd`, add **Metadata**: key `authorization`, value `Bearer <token>`.

For REST, import the generated spec — you get all nine routes with schemas:

```
gen/openapi/api.swagger.json
```

---

## 7. Seeded data and deliberate failures

| | |
|---|---|
| **Users** | `demo@example.com`, `asha@example.com`, `ravi@example.com` — all `demo-password` |
| **Products** | `p-1001` … `p-1012` |

Three products exist to make failures reproducible without editing data:

| Product | State | Ask for | You get |
|---|---|---|---|
| `p-1003` (JBL speaker) | stock **0** | any quantity | `OUT_OF_STOCK` |
| `p-1005` (Ultraboost) | stock **1** | 3 | `INSUFFICIENT_STOCK` |
| `p-9999` | does not exist | any | `PRODUCT_NOT_FOUND` |

> **Always authenticate as a seeded user once `userd` runs more than one task
> on the SQLite setup.** A user created by `Register` exists on exactly one
> task, so two of three tasks have never heard of them — and it fails looking
> exactly like a load-balancing bug. With `use_rds = true` the services share a
> database and this stops mattering.
