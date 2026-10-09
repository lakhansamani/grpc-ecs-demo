# Reaching the services from Postman (or grpcurl) on AWS

Short answer to "do we need Cloud Map for Postman?" — **no.** They solve
different problems, and conflating them is easy:

```
  Postman on your laptop ──────────────> paymentd        EXTERNAL access
                                            │            (public IP, SSM, or ALB)
                                            │
                                            ↓
                                        identityd        INTERNAL discovery
                                                          (Cloud Map)
```

**Cloud Map is for the inside hop**, `paymentd` → `identityd`. Postman never
touches it. So you can demo from Postman with no `servicediscovery` or
`route53` permission at all.

## Postman does speak gRPC

Postman has had a gRPC client since 2022. Both services register the **server
reflection** service, so Postman can introspect the methods without being given
a `.proto` file. Point it at `host:50052`, pick `payment.v1.PaymentService`, and
put the token in metadata as `authorization: Bearer <jwt>`.

## Three ways in, cheapest first

### 1. Public IP + a security-group rule for your IP — already implemented

```hcl
# terraform/envs/aws/terraform.tfvars
operator_ingress_cidrs = ["203.0.113.4/32"]   # curl ifconfig.me
```

Tasks already run in public subnets with `assign_public_ip = true` (there is no
NAT Gateway, by design). Get the address with:

```sh
aws ecs describe-tasks --cluster ecom-aws --tasks <arn> \
  --query 'tasks[0].attachments[0].details[?name==`networkInterfaceId`].value' --output text
aws ec2 describe-network-interfaces --network-interface-ids <eni> \
  --query 'NetworkInterfaces[0].Association.PublicIp' --output text
```

Needs no extra permission beyond what you have. **Caveats:** plaintext gRPC over
the public internet, the IP changes whenever the task is replaced, and venue
wifi may hand you a different egress IP than the one you allow-listed. Check
`curl ifconfig.me` from the venue, and keep a `/0` rule ready to paste if the
room's NAT surprises you.

### 2. SSM port forwarding — no public IP, no inbound rule

```sh
# needs enable_execute_command = true (the default in envs/aws)
TASK=$(aws ecs list-tasks --cluster ecom-aws --service-name paymentd \
  --query 'taskArns[0]' --output text)
RUNTIME=$(aws ecs describe-tasks --cluster ecom-aws --tasks "$TASK" \
  --query 'tasks[0].containers[0].runtimeId' --output text)

aws ssm start-session \
  --target "ecs:ecom-aws_${TASK##*/}_${RUNTIME}" \
  --document-name AWS-StartPortForwardingSession \
  --parameters '{"localPortNumber":["50052"],"portNumber":["50052"]}'
```

Then Postman talks to `localhost:50052`. This is the **better demo**: the task
stays private, nothing is exposed, and "my laptop is tunnelled into the VPC" is
a nice aside. Costs two extra permissions — `ssmmessages:*` on the task role
(wired into the `iam` module when `enable_execute_command = true`) and
`ssm:StartSession` for you.

### 3. ALB with a gRPC target group — above the cut line

Needs ELB permissions, an **HTTPS listener**, an ACM certificate and therefore a
domain. Correct for production, too much setup for this talk.

## So what do you actually lose without Cloud Map?

Not Postman access. You lose two things:

| Lost | Why it matters |
|---|---|
| Stable internal addressing | `paymentd` would need `identityd`'s private IP passed in, which means a two-stage apply and breaks the moment a task is replaced |
| **The load-balancing segment** | it needs 3 `identityd` tasks behind ONE name so the client resolves multiple A records. Port forwarding cannot substitute: the client that must load-balance is `paymentd`, *inside* the VPC |

### The fallback if Cloud Map is refused

Put both containers in a **single task definition**, so `paymentd` reaches
`identityd` on `localhost:50051`. What survives and what does not:

- ✅ still two processes, two binaries, real gRPC over the loopback, the auth
  delegation and trust boundary intact
- ✅ Postman demo, idempotency, rules, explanations, graceful shutdown, health
  checks, the Fargate task shape, `make ps`
- ❌ service discovery
- ❌ the load-balancing demo

If that happens, demo discovery and load balancing **locally** — where you need
the Docker-alias shim anyway, because Ministack cannot create Cloud Map services
through Terraform — and let AWS prove the deploy is real. State the split
plainly; a known boundary reads as competence, a discovered one does not.

**Worth asking for anyway:** `servicediscovery` plus `route53` is a small,
uncontroversial grant, and it buys the single best segment in the talk.
