# Commands & walkthrough

Everything that used to be slides and is better read than projected: the guided
code tour, every `make` target, how to switch AWS profiles, and the live-AWS
path start to finish.

For hand-testing the APIs see [`../INSTRUCTIONS.md`](../INSTRUCTIONS.md).
For what each AWS component is, see [`ARCHITECTURE.md`](ARCHITECTURE.md).

---

## The tour, in the order it makes sense

Seven walkthroughs. Each one answers the same three questions.

| | What we look at | Why it is next |
|---|---|---|
| **A** | The contract — `.proto` | Everything else is generated from it |
| **B** | The generated code | So you believe nobody hand-wrote the gateway |
| **C** | The Go implementation | Where the two hops and the money rules live |
| **D** | The Terraform | How it gets to Fargate |
| **E** | Bring it up locally | Real ECS tasks, on a laptop |
| **F** | Poke it by hand | `grpcurl`, `curl`, Postman |
| **G** | The same thing on real AWS | One provider block different |

Every command here is also in **`INSTRUCTIONS.md`**, so nobody has to
photograph a slide.



## A · The contract

**What:** three `.proto` files. The only interface code written by hand.

**Why:** if the contract is the source of truth, then the server, the client,
the REST routes and the docs cannot disagree with each other — they are all
built from this.

**How:**

```sh
ls proto/*/v1/*.proto
$EDITOR proto/order/v1/order.proto
```

Three things to point at, in this order:

1. `rpc CreateOrder(CreateOrderRequest) returns (CreateOrderResponse)` — a
   function, not a URL
2. `message RequestedItem { product_id, quantity }` — **no price field**
3. `enum RejectionReason` — out of stock is a *value*, not an error

Then the one in `product.proto` that is deliberately **not** public:

```sh
grep -B4 "rpc CheckAvailability" proto/product/v1/product.proto
```



## B · The generated code

**What:** five outputs from one source.

**Why:** "generated" is easy to claim and easy to doubt. Showing the generated
gateway is what makes the claim land — and it is the file nobody has to review.

**How:**

```sh
cat buf.gen.yaml                 # five plugins, one source
make proto                       # regenerate everything

tree gen -L 3
wc -l gen/go/order/v1/*.go       # scroll it, do NOT read it
head -40 gen/openapi/api.swagger.json
ls clients/node/src/gen          # the SAME protos, in TypeScript
```

Then prove the contract is enforced, not advisory:

```sh
make proto-breaking              # rename a proto field -> CI fails
```



## C · The Go implementation

**What:** the order path, and the two bits of platform code that matter on ECS.

**Why:** this is where the business rules and the ECS-specific lessons live. The
rest of the Go is ordinary.

**How — in this order:**

```sh
# 1. the two hops, and the total computed from CATALOGUE prices
$EDITOR internal/order/service.go          # CreateOrder

# 2. idempotency: a pre-check AND a unique-index fallback, because they race
grep -n "IdempotencyKey\|ErrDuplicatedKey" internal/order/service.go

# 3. THE load-balancing fix (round_robin + the dns:/// prefix)
$EDITOR internal/platform/grpcclient/grpcclient.go

# 4. graceful shutdown, in the right order: NOT_SERVING, drain, stop
$EDITOR internal/platform/grpcserver/server.go

# 5. search on two engines: FTS5 and postgres tsvector
$EDITOR internal/product/store.go          # BuildSearchIndex, searchPostgres
```



## D · The Terraform

**What:** six modules, one deployment, two environments.

**Why:** it is the only part that differs between a laptop and production — and
it differs by one file.

**How:**

```sh
tree terraform -L 3

$EDITOR terraform/deployment/main.tf            # the four module "…" blocks
$EDITOR terraform/modules/ecs-service/main.tf   # task definition + service
$EDITOR terraform/modules/network/main.tf       # the self-referencing SG rule
$EDITOR terraform/modules/iam/main.tf           # execution role vs task role
$EDITOR terraform/modules/rds/main.tf           # the cost arithmetic, up top

# THE SLIDE:
diff terraform/envs/local/provider.tf terraform/envs/aws/provider.tf
```

Then the guard that stops you doing the wrong thing:

```sh
make show-guard
```



## E · Bring it up locally

**What:** real ECS tasks, real task definitions, on your laptop.

**Why:** the point is not that it is a simulator. The point is that it is the
**same Terraform** — so what you learn here transfers.

**How:**

```sh
make test              # 8 packages, no Docker, no AWS
make local-up          # emulator :4566, jaeger :16686, prometheus :9090
make images            # four ARM64 distroless images
make tf-local-apply    # terraform apply -> ECS tasks, then wait-ready
make ps                # seven proofs that this is ECS
make forward           # publish ports (awsvpc tasks have no host port)
```

> `tf-local-apply` ends with `make wait-ready`, which blocks until `orderd` can
> actually reach `userd`. Without it the first command after a deploy can fail
> while DNS is still settling.



## E2 · The same AWS CLI, pointed at your laptop

**What:** `aws ecs`, `aws ecr`, `aws servicediscovery`, `aws logs` — unchanged.

**Why:** this is the strongest argument for emulating rather than mocking.
**The commands you practise are the commands you will run in production.**

**How** — set this once, and every command below is what you would run on real
AWS minus the last line:

```sh
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:4566
```

```sh
aws ecs describe-services --cluster ecom-local \
  --services userd productsd orderd gatewayd \
  --query 'services[].{Service:serviceName,Desired:desiredCount,Running:runningCount}' --output table

aws ecs describe-task-definition --task-definition userd \
  --query 'taskDefinition.{Family:family,Rev:revision,Net:networkMode,Arch:runtimePlatform.cpuArchitecture}'

aws ecr describe-repositories --query 'repositories[].repositoryName'
aws servicediscovery list-services --query 'Services[].Name'
aws secretsmanager list-secrets --query 'SecretList[].Name'
aws iam list-roles --query 'Roles[?starts_with(RoleName,`ecom-`)].RoleName'
aws logs tail /ecs/ecom-local --since 5m
```

> There is **no web console** for the emulator. Inspection is the CLI plus
> `docker ps` — and that is the honest trade, because the CLI is the part that
> transfers.



## F1 · Poke it by hand — gRPC

**What:** `grpcurl`, with no `.proto` file.

**Why:** every service registers **server reflection**, so the API documents
itself. This is also how you debug a service you did not write.

**How:**

```sh
U=localhost:50051; O=localhost:50052; P=localhost:50053

grpcurl -plaintext $P list                                   # discover
grpcurl -plaintext $P describe product.v1.ProductService     # full signatures
grpcurl -plaintext -d '{"service":"userd"}' $U grpc.health.v1.Health/Check

grpcurl -plaintext -d '{"query":"cancelling"}' $P product.v1.ProductService/SearchProducts
grpcurl -plaintext -d '{"query":"head"}'       $P product.v1.ProductService/SearchProducts

TOKEN=$(grpcurl -plaintext -d '{"email":"demo@example.com","password":"demo-password"}' \
  $U user.v1.UserService/Login | jq -r .token)

grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"live-1"}' \
  $O order.v1.OrderService/CreateOrder | jq '.order | {status, totalMinor}'
```

Run the last one **twice** — same key, same order, `idempotentReplay: true`.

**And the one that is not public:**

```sh
grpcurl -plaintext -d '{"items":[{"product_id":"p-1001","quantity":2}]}' \
  $P product.v1.ProductService/CheckAvailability     # works
```



## F2 · Poke it by hand — REST

**What:** the same services over HTTP/JSON, through the generated gateway.

**Why:** it is what a browser would actually call — and it proves the REST
surface is a side effect of the contract, not a second implementation.

**How:**

```sh
BASE=http://localhost:8080

curl -s "$BASE/healthz"
curl -s "$BASE/v1/products:search?query=cancelling" | jq -r '.products[].title'
curl -s "$BASE/v1/products/p-1005" | jq '.product | {title, stock}'

TOKEN=$(curl -s -X POST "$BASE/v1/sessions" -H 'Content-Type: application/json' \
  -d '{"email":"demo@example.com","password":"demo-password"}' | jq -r .token)

curl -s "$BASE/v1/users/me" -H "Authorization: Bearer $TOKEN" | jq '.user'

curl -s -X POST "$BASE/v1/orders" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"items":[{"productId":"p-1001","quantity":1}],"idempotencyKey":"rest-live-1"}' \
  | jq '.order.status'
```

**The payoff — the same RPC, two answers:**

```sh
curl -s -o /dev/null -w 'REST -> %{http_code}\n' \
  -X POST "$BASE/v1/products:checkAvailability" -d '{}'       # -> 404
```



## F3 · The whole flow, and Postman

**What:** the scripted version, then a human-facing client.

**Why:** the scripts are what you trust; Postman is what your frontend team
will actually use.

**How:**

```sh
make demo              # gRPC: browse, login, order, replay, 3 rejections
make dev-rest          # the identical flow over REST
make ts-demo           # the identical flow from generated TypeScript
make api-coverage      # all 10 RPCs + all 9 REST routes + the 404 check -> 20/20
```

**Postman, no `.proto` import:**

1. New → **gRPC Request** → `localhost:50053`
2. Tick **"Using server reflection"**
3. Pick `product.v1.ProductService/SearchProducts`, body `{"query":"cancelling"}`
4. For `orderd`, add metadata `authorization: Bearer <token>`

For REST, import `gen/openapi/api.swagger.json` — all nine routes with schemas.



## G1 · The same Terraform, on real AWS

**What:** one `terraform apply`, a different provider block.

**Why:** this is the claim the whole talk rests on. Run it, do not assert it.

**How** (pre-provisioned the day before — never a cold apply on venue wifi):

```sh
cd terraform/envs/aws
terraform plan          # read it: ~1 VPC, 4 task definitions, 4 services
terraform apply         # ~2 min without RDS, ~7-10 min with it

cd ../../..
make ps-aws             # the same seven proofs, now against real ECS
```

What is real now that was substituted locally:

- **Cloud Map does the DNS for real** — no Docker-alias shim
- `healthStatus: HEALTHY`, `launchType: FARGATE`
- Tasks across **two availability zones**

```sh
terraform destroy       # before leaving the venue. Actually run it.
```



## G2 · Reaching it with no load balancer and no domain

**What:** `grpcurl` and `curl` straight at a task's public IP.

**Why:** because you do not need an ALB, a domain or a certificate to run gRPC
or HTTP. An ALB gRPC target group **requires** an HTTPS listener, which needs a
cert, which needs a domain — three moving parts for a lesson a bare IP already
teaches.

**How:**

```sh
make aws-ip
```

```
SERVICE     PORT  PUBLIC IP    TRY THIS
userd       50051 54.x.x.x     grpcurl -plaintext 54.x.x.x:50051 list
gatewayd    8080  18.x.x.x     curl http://18.x.x.x:8080/healthz
```

**Do not retype those.** Load them, and every command from F1 and F2 works
unchanged:

```sh
eval "$(make -s aws-env)"      # sets U, O, P, BASE, REST_BASE from the live tasks

grpcurl -plaintext $P list
bash scripts/rest-smoke.sh
```

**The honest cost of skipping the ALB:** `awsvpc` gives every task its own ENI
and its own public IP, and that IP changes whenever the task is replaced. So
you re-run `make aws-ip` instead of writing it down. And the only reason those
IPs answer at all is that the security group allows your `/32` —
`operator_ingress_cidrs` is empty by default.



## G3 · The AWS console tour

**What:** the same four facts, in the UI, for people who live there.

**Why:** half the room will go back to the console on Monday. Show them where
the things you have been naming actually are.

**How** — in this order, four tabs:

| Console page | Point at |
|---|---|
| **ECS → Clusters → `ecom-aws` → Services** | Desired vs Running. Then **Deployments** — ECS reconciling, not you |
| **ECS → Task definitions → `userd`** | **Revisions.** Then JSON: `awsvpc`, `FARGATE`, `ARM64`, and `secrets` holding an **ARN** |
| **ECS → the task → Networking** | Its own ENI and private IP. **This is `awsvpc`.** |
| **Cloud Map → Namespaces → `ecom.local` → `userd`** | **Instances** — one per task, and the **health status** that decides whether DNS answers |

Two more worth 20 seconds each:

- **Secrets Manager → `ecom-aws-jwt-secret`** — "Retrieve secret value" is a
  deliberate click. The task never needed it.
- **CloudWatch → Log groups → `/ecs/ecom-aws`** — one stream per task, and this
  is the only place a crash-looping task explains itself.

> **Do not** demo the console as the primary tool. It is lovely for *looking*
> and useless for *reproducing* — which is the whole reason Part 6 exists.



## G4 · What changes if you add a load balancer

Everything so far used **bare public IPs** — a demo shortcut. The production
version differs in four places:

| | Demo | Production |
|---|---|---|
| Getting in | task public IPs, locked to your `/32` | **ALB + a domain** in front of `gatewayd` |
| Subnets | public | **private**, plus VPC endpoints |
| Database | SQLite on the task | RDS **Multi-AZ**, backups on |
| Your access | public IP | **SSM port forwarding** |

**Why no ALB here:** an ALB gRPC target group needs an HTTPS listener → a
certificate → a domain. Three things to set up to teach what a bare IP already
teaches.

> **Never copy one thing from this repo into production:** public subnets with
> public task IPs.



## G5 · An ALB only covers the first hop

This is the bit people assume wrong.

```
      internet
         |
        ALB            <-- balances across gatewayd replicas
      /  |  \
   gw-1 gw-2 gw-3

   each gw is itself a gRPC client
      /  |  \
   usr-1 usr-2 usr-3   <-- the ALB is NOT in this path
```

| Hop | Who balances it |
|---|---|
| internet → `gatewayd` | **the ALB**, per request |
| `gatewayd` → `userd` | **the client itself** — Cloud Map + `round_robin` |

So an ALB solves the outside. Inside, every client still has to balance for
itself — either the two settings we are about to look at, or **ECS Service
Connect**, which puts a proxy in each task and does it for you.

> Add an ALB and stop there, and the inside of your system is still pinned to
> one task.




---

# Command reference

## Showing these slides

Marp turns this file into slides. Nothing to install — `npx` fetches it.

```sh
# live preview in a browser, reloads as you edit  <- use this on the day
npx @marp-team/marp-cli@latest -w -s .

# or a single self-contained HTML file
npx @marp-team/marp-cli@latest PRESENTATION.md -o slides.html --allow-local-files

# PDF, as a backup on a USB stick
npx @marp-team/marp-cli@latest PRESENTATION.md --pdf --allow-local-files

# PowerPoint, if the venue insists
npx @marp-team/marp-cli@latest PRESENTATION.md --pptx --allow-local-files
```

**`--allow-local-files` is required** for PDF and PPTX, because the
architecture diagram is a local SVG. Without it the slide renders blank.

The theme is `default` — Marp's light one — with a small `style:` block in the
front matter to size the tables and code down. The other built-ins are `gaia`
and `uncover`:

```sh
npx @marp-team/marp-cli@latest PRESENTATION.md --theme gaia -o slides.html
```

> Install it properly if you would rather not wait for `npx` on venue wifi:
> `npm i -g @marp-team/marp-cli`, then just `marp -w -s .`
>
> There is also a **Marp for VS Code** extension, which previews in the editor.



## Every `make` target, grouped by what you are doing

**Loop 1 — no Docker, no AWS. Fastest. Five terminals.**

```sh
make test              # 8 Go packages, fully offline
make dev-seed          # create ./data/{user,product}.db
make dev-userd         # :50051    |  make dev-productsd  # :50053
make dev-orderd        # :50052    |  make dev-gatewayd   # :8080 REST
make dev-smoke         # the gRPC flow
make dev-rest          # the same flow over REST
make dev-clean         # delete ./data
```

**Loop 2 — build the images.**

```sh
make images            # 4 ARM64 distroless images (one Dockerfile per storage shape)
```

**Loop 3 — real ECS tasks on your laptop.**

```sh
make local-up          # ministack :4566, jaeger :16686, prometheus :9090
make tf-local-apply    # terraform apply -> ECS tasks, then dns + wait-ready
make dns               # re-alias Cloud Map names (run after any deploy)
make wait-ready        # block until orderd can reach its upstreams
make ps                # 7 proofs that this is really ECS
make forward           # publish ports (awsvpc tasks have no host port)
make forward-stop      # remove the relays
make local-down        # stop the emulator stack
make tf-local-destroy  # terraform destroy
```

**Exercise it.**

```sh
make demo              # gRPC: browse, login, order, replay, 3 rejections
make dev-rest          # the identical flow over REST
make ts-demo           # the identical flow from generated TypeScript
make api-coverage      # 10 RPCs + 9 REST routes + the 404 check -> 20/20
make demo-load         # sustained load through orderd (needs `make forward`)
make scale N=3         # scale userd; SCALE_SVC=productsd to pick another
make show-guard        # terraform refusing to scale the writer
```

**Codegen.**

```sh
make proto             # buf lint + generate (Go, gateway, OpenAPI, TypeScript)
make proto-breaking    # fail if a change would break existing clients
```

**Present the slides.**

```sh
make slides          # http://localhost:8030, live reload   <- use this on the day
make slides-html     # one self-contained slides.html
make slides-pdf      # PDF, as a backup on a USB stick
```

Port **8030**, not Marp's default 8080, because `gatewayd` already listens on
8080. `--allow-local-files` is already in the HTML/PDF targets and is
**required** — the diagrams are local SVGs, and without it those slides render
blank.

**Load the addresses into your shell** instead of copying IPs:

```sh
eval "$(make -s local-env)"       # U, O, P, BASE, REST_BASE -> localhost
eval "$(make -s aws-env)"         # ...from the live AWS tasks
eval "$(make -s local-profile)"   # aws CLI -> localhost:4566
eval "$(make -s unset-profile)"   # clear it BEFORE touching real AWS
```

> `make` runs each recipe line in its own subshell, so a target cannot
> `export` into your shell. That is why these print and you `eval` them.

**The load-balancing demo, in the terminal.**

```sh
make demo-lb-before   # gRPC's DEFAULT: one task takes everything
make demo-lb-after    # the fix: evenly spread
make demo-lb          # both, back to back
make lb-report        # just the per-task counters
make wire-size        # protobuf vs JSON, measured
make seed-check       # which driver, which databases, and did seeding run
```

**AWS — two commands.**

```sh
make tf-aws-apply      # init -> ECR repos -> build+push -> apply -> wait -> print IPs
make ps-aws            # the same 7 proofs, against real ECS
make aws-ip            # every task's PUBLIC IP:port, ready for grpcurl/curl
eval "$(make -s aws-env)"    # load U/O/P/BASE/REST_BASE from the live tasks
eval "$(make -s local-env)"  # the same variables, pointed at localhost
make tf-aws-destroy    # drain -> destroy -> VERIFY nothing is still billing
```

`tf-aws-apply` prints the account and your egress IP first, and refuses to run
without `terraform/envs/aws/terraform.tfvars`. `tf-aws-destroy` drains every
service to 0 and waits 75s before destroying, because ECS has to deregister
from Cloud Map or `DeleteService` returns `ResourceInUse`.



## Switching AWS accounts and profiles

**This is the step people get wrong on stage** — they demo into the wrong
account, or into a company account. Check before you apply, every time.

```sh
# 1. WHICH ACCOUNT AM I IN? Read the number out loud before applying.
aws sts get-caller-identity
```

**Named profiles** live in `~/.aws/config`:

```ini
[profile demo]
region = us-east-1

[profile work]
sso_start_url = https://example.awsapps.com/start
sso_region     = us-east-1
sso_account_id = 111122223333
sso_role_name  = PowerUserAccess
region         = us-east-1
```

```sh
export AWS_PROFILE=demo          # for this shell
aws sts get-caller-identity      # confirm it took effect

aws ecs list-clusters --profile demo    # or per-command
```

**Three ways to get credentials**, cheapest-risk first:

```sh
# a) IAM Identity Center / SSO — short-lived, nothing stored. PREFERRED.
aws sso login --profile demo

# b) `aws login` — browser flow, temporary credentials, no access keys
aws login --profile demo

# c) long-lived access keys — last resort, and rotate them after the talk
aws configure --profile demo
```

```sh
# Region, without editing anything:
export AWS_REGION=us-east-1
# or per command:
terraform apply -var aws_region=us-east-1
```

> **Before `terraform apply` in `envs/aws`:**
>
> 1. `aws sts get-caller-identity` — is this the demo account?
> 2. `curl ifconfig.me` — is that IPv4 address in `operator_ingress_cidrs`?
> 3. `terraform plan` — read it. ~1 VPC, 4 task definitions, 4 services.
>
> **Check the Fargate vCPU quota in your region first.** A region with quota 0
> reports only *"your account is currently blocked"*, which looks like a
> billing problem and is not.



## The live AWS path, start to finish

```sh
export AWS_PROFILE=demo
aws sts get-caller-identity                      # confirm the account

# 1. create the ECR repositories before pushing to them
cd terraform/envs/aws
terraform init
terraform apply -target=module.deployment.module.ecr \
  -var user_image=placeholder -var product_image=placeholder \
  -var order_image=placeholder -var gateway_image=placeholder

# 2. build + push, ARM64
cd ../../..
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
REGION=${AWS_REGION:-us-east-1}
ECR="$ACCOUNT.dkr.ecr.$REGION.amazonaws.com"
aws ecr get-login-password --region "$REGION" \
  | docker login --username AWS --password-stdin "$ECR"
# (see docs/DEPLOY_AWS.md for the four build commands)

# 3. the real apply
cd terraform/envs/aws
cat > terraform.tfvars <<EOF
user_image    = "$ECR/userd:0.1.0"
product_image = "$ECR/productsd:0.1.0"
order_image   = "$ECR/orderd:0.1.0"
gateway_image = "$ECR/gatewayd:0.1.0"
operator_ingress_cidrs = ["YOUR.IPV4/32"]
EOF
terraform plan -out=tf.plan
terraform apply tf.plan                          # ~2 min, or ~7-10 with RDS

# 4. verify
cd ../../..
make ps-aws
make aws-ip
REST_BASE="http://<gatewayd-ip>:8080" bash scripts/rest-smoke.sh

# 5. optional: a real database, separate DB per service (~6c for 3 hours)
terraform -chdir=terraform/envs/aws apply -var use_rds=true

# 6. BEFORE YOU LEAVE THE VENUE
for s in userd productsd orderd gatewayd; do
  aws ecs update-service --cluster ecom-aws --service $s --desired-count 0
done
sleep 75                                         # let Cloud Map deregister
terraform -chdir=terraform/envs/aws destroy
```



## Local AWS CLI — same commands, one extra line

```sh
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:4566     # <- the only difference
```

```sh
aws ecs list-clusters
aws ecs describe-services --cluster ecom-local --services userd productsd orderd gatewayd \
  --query 'services[].{Service:serviceName,Desired:desiredCount,Running:runningCount}' --output table
aws ecs describe-task-definition --task-definition userd
aws ecr describe-repositories --query 'repositories[].repositoryName'
aws servicediscovery list-services --query 'Services[].Name'
aws secretsmanager list-secrets --query 'SecretList[].Name'
aws iam list-roles --query 'Roles[?starts_with(RoleName,`ecom-`)].RoleName'
aws logs tail /ecs/ecom-local --since 5m
```

**Unset `AWS_ENDPOINT_URL` before touching real AWS**, or every command keeps
going to your laptop and you will think nothing deployed:

```sh
unset AWS_ENDPOINT_URL AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
```



## Where everything lives

| What | Path |
|---|---|
| The contract | `proto/{user,product,order}/v1/*.proto` |
| Codegen config | `buf.gen.yaml`, `buf.gen.ts.yaml` |
| Order logic, both hops | `internal/order/service.go` |
| **The load-balancing fix** | `internal/platform/grpcclient/grpcclient.go` |
| Health + graceful shutdown | `internal/platform/grpcserver/server.go` |
| The distroless health probe | `cmd/healthcheck/main.go` |
| Search on two engines | `internal/product/store.go` |
| Per-service database creation | `internal/platform/store/ensuredb.go` |
| The whole deployment | `terraform/deployment/main.tf` |
| Task definition + service | `terraform/modules/ecs-service/main.tf` |
| RDS, with the cost sums | `terraform/modules/rds/main.tf` |
| **The only differing files** | `terraform/envs/{local,aws}/provider.tf` |
| Manual test commands | `INSTRUCTIONS.md` |
