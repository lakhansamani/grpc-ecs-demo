# AWS permissions needed for the demo

**You have:** full ECS, ECR, EC2.
**Verdict: not enough.** Four more services are required, and one of them has a
non-obvious dependency that will fail the apply halfway through.

Derived from the actual resources in `terraform/`, not from memory:

```
aws_vpc, aws_subnet, aws_internet_gateway, aws_route_table,
aws_route_table_association, aws_security_group,
aws_vpc_security_group_{ingress,egress}_rule          -> EC2        ✅ you have this
aws_ecr_repository                                     -> ECR        ✅ you have this
aws_ecs_cluster, aws_ecs_cluster_capacity_providers,
aws_ecs_task_definition, aws_ecs_service               -> ECS        ✅ you have this
aws_iam_role, aws_iam_role_policy,
aws_iam_role_policy_attachment                         -> IAM        ❌ MISSING
aws_cloudwatch_log_group                               -> Logs       ❌ MISSING
aws_secretsmanager_secret{,_version}                   -> Secrets    ❌ MISSING
aws_service_discovery_private_dns_namespace,
aws_service_discovery_service                          -> Cloud Map  ❌ MISSING (+ Route 53)
```

## The two that will definitely stop you

**1. IAM — `iam:PassRole` is not optional.** A Fargate task *must* have an
execution role so the ECS agent can pull from ECR, write logs and resolve
secrets. Even if the role already exists, Terraform cannot attach it to a task
definition without `iam:PassRole`. There is no way around this one.

**2. Cloud Map needs Route 53 too.** `servicediscovery:CreatePrivateDnsNamespace`
creates a hosted zone behind the scenes, so it also requires
`route53:CreateHostedZone`, `route53:GetHostedZone` and
`route53:ListHostedZonesByName` ([AWS Cloud Map API permissions
reference](https://docs.aws.amazon.com/cloud-map/latest/dg/cloud-map-api-permissions-ref.html)).
Miss these and the apply dies *after* creating the VPC and cluster, which is a
miserable thing to debug on the day.

## Policy to request

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "IamForEcsTaskRoles",
      "Effect": "Allow",
      "Action": [
        "iam:CreateRole", "iam:DeleteRole", "iam:GetRole", "iam:TagRole",
        "iam:AttachRolePolicy", "iam:DetachRolePolicy",
        "iam:ListAttachedRolePolicies",
        "iam:PutRolePolicy", "iam:DeleteRolePolicy", "iam:GetRolePolicy",
        "iam:ListRolePolicies", "iam:ListInstanceProfilesForRole"
      ],
      "Resource": "arn:aws:iam::*:role/ecom-aws-*"
    },
    {
      "Sid": "PassRoleToEcsTasksOnly",
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": "arn:aws:iam::*:role/ecom-aws-*",
      "Condition": { "StringEquals": { "iam:PassedToService": "ecs-tasks.amazonaws.com" } }
    },
    {
      "Sid": "Logs",
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogGroup", "logs:DeleteLogGroup", "logs:DescribeLogGroups",
        "logs:PutRetentionPolicy", "logs:TagResource", "logs:ListTagsForResource",
        "logs:CreateLogStream", "logs:PutLogEvents", "logs:GetLogEvents",
        "logs:FilterLogEvents", "logs:DescribeLogStreams"
      ],
      "Resource": "*"
    },
    {
      "Sid": "SecretsForSharedJwtKey",
      "Effect": "Allow",
      "Action": [
        "secretsmanager:CreateSecret", "secretsmanager:DeleteSecret",
        "secretsmanager:DescribeSecret", "secretsmanager:GetSecretValue",
        "secretsmanager:PutSecretValue", "secretsmanager:UpdateSecret",
        "secretsmanager:TagResource", "secretsmanager:ListSecrets",
        "secretsmanager:GetResourcePolicy"
      ],
      "Resource": "*"
    },
    {
      "Sid": "CloudMapServiceDiscovery",
      "Effect": "Allow",
      "Action": [
        "servicediscovery:CreatePrivateDnsNamespace",
        "servicediscovery:DeleteNamespace", "servicediscovery:GetNamespace",
        "servicediscovery:ListNamespaces", "servicediscovery:CreateService",
        "servicediscovery:DeleteService", "servicediscovery:GetService",
        "servicediscovery:ListServices", "servicediscovery:GetOperation",
        "servicediscovery:TagResource", "servicediscovery:UntagResource",
        "servicediscovery:ListTagsForResource",
        "servicediscovery:RegisterInstance", "servicediscovery:DeregisterInstance",
        "servicediscovery:ListInstances", "servicediscovery:DiscoverInstances"
      ],
      "Resource": "*"
    },
    {
      "Sid": "Route53BackingCloudMap",
      "Effect": "Allow",
      "Action": [
        "route53:CreateHostedZone", "route53:DeleteHostedZone",
        "route53:GetHostedZone", "route53:ListHostedZones",
        "route53:ListHostedZonesByName", "route53:GetChange",
        "route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets"
      ],
      "Resource": "*"
    },
    {
      "Sid": "WhoAmI",
      "Effect": "Allow",
      "Action": ["sts:GetCallerIdentity"],
      "Resource": "*"
    }
  ]
}
```

IAM is scoped to `ecom-aws-*` so nobody has to grant blanket role creation.
The rest use `*` because Terraform needs List/Describe calls that do not accept
a narrower resource.

**Not needed, so do not ask for it:** Bedrock (there is no LLM in this demo at
all), RDS (SQLite lives on the task — SPEC.md 6), ELB
(the ALB segment is above the cut line), S3 (Terraform state is local).

## Demo-minimum vs. nice-to-have — and the fallbacks are real code

Everything below is implemented and tested, not advice. Apply the minimum
profile with:

```sh
terraform -chdir=terraform/envs/aws apply -var-file=minimal.tfvars \
  -var identity_image=... -var payment_image=...
```

### MUST request (three things)

| Permission | Why there is no way around it |
|---|---|
| `iam:PassRole` on the execution role, conditioned on `ecs-tasks.amazonaws.com` | Fargate cannot start a task without an execution role. Blocking, full stop. |
| `logs:CreateLogGroup` (+ Describe/Delete/PutRetentionPolicy) | the `awslogs` driver needs a group. The alternative is `create_log_group = false` plus `awslogs-create-group`, but then the **execution role** needs `logs:CreateLogGroup`, and the AWS managed policy does **not** include it — so somebody needs this either way. |
| `servicediscovery:*` **+ `route53:*`** | only if you want the service-discovery and load-balancing segments on AWS. This is the talk's best content, so I would fight for it. |

### CAN SKIP — just mention it on a slide

| Skip | How | Set |
|---|---|---|
| `iam:CreateRole` | reuse `ecsTaskExecutionRole`, which most accounts already have | `existing_execution_role_arn = "arn:..."` |
| **Secrets Manager** | `JWT_SECRET` as a plain env value, still one shared value across all tasks, so scaling still works | `use_secrets_manager = false` |
| Fargate Spot | plain `FARGATE` launch type | `use_spot = false` (the default) |
| Bedrock, RDS, ELB, S3 | already not used by the demo | — |

**Verified on the emulator with the minimum profile:** 0 secrets created, the
reused role attached to the task definition, `JWT_SECRET` arriving as an env
var, and the full smoke test passing.

**What to say when you skip Secrets Manager** — show the task definition on a
slide and say it out loud:

> "In production this is `secrets: [{name: JWT_SECRET, valueFrom: <arn>}]`, and
> the ECS agent resolves it with the execution role before my code starts, so
> the value never touches the image or git. Here it is a plain environment
> variable, because this account does not grant Secrets Manager — and that is
> one line of Terraform apart."

That is a stronger moment than a working demo of it, because you are showing
you know the difference.

## If a permission is refused, here is what the demo loses

Ordered by how much it hurts.

| Blocked | Workaround | Cost to the talk |
|---|---|---|
| `iam:CreateRole` | Reuse an existing role (most accounts already have `ecsTaskExecutionRole`). I can add an `execution_role_arn` variable in ~10 minutes. **Still needs `iam:PassRole`.** | None — the role is plumbing, not content |
| `logs:CreateLogGroup` | Drop the Terraform log group and set `awslogs-create-group=true` in the task definition so ECS creates it | None |
| Secrets Manager | Pass `JWT_SECRET` as a plain `environment` value instead of `secrets` | **Loses the Secrets Manager segment**, which is the concrete "no keys in the image" story |
| Cloud Map + Route 53 | Put all four containers in **one** task definition so `orderd` reaches `userd` on `localhost` | **Severe.** Kills service discovery *and* the load-balancing demo — the best content in the talk |
| `iam:PassRole` | none | **Blocking. Fargate cannot run.** |

## What to send your admin

> I need to deploy an ECS Fargate demo. I already have ECS, ECR and EC2. I also
> need IAM (scoped to roles named `ecom-aws-*`, including `PassRole`
> conditioned on `ecs-tasks.amazonaws.com`), CloudWatch Logs, Secrets Manager,
> and Cloud Map plus the Route 53 permissions it depends on. Policy attached.
> No Bedrock, RDS, ELB or S3 needed.

## Check it yourself before the day

```sh
aws sts get-caller-identity
aws iam list-roles --max-items 1                     >/dev/null && echo "iam ok"
aws logs describe-log-groups --limit 1               >/dev/null && echo "logs ok"
aws secretsmanager list-secrets --max-results 1      >/dev/null && echo "secrets ok"
aws servicediscovery list-namespaces --max-results 1 >/dev/null && echo "cloudmap ok"
aws route53 list-hosted-zones --max-items 1          >/dev/null && echo "route53 ok"
```

Read-only calls succeeding does not prove you can *create*, so the real check is
`terraform -chdir=terraform/envs/aws plan` followed by an actual apply into a
throwaway region. **Do that before the talk, not on the day** — a partial apply
leaves a half-built VPC you then have to unpick.
