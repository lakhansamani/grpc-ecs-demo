#!/usr/bin/env bash
# Prove, on screen, that this is really running on ECS - not just docker.
#
# Four levels of proof, weakest to strongest:
#   1. the ECS control plane knows about it   (aws ecs ...)
#   2. the task definition shape is Fargate   (awsvpc, ARM64, secrets)
#   3. the task can SELF-describe via ECS_CONTAINER_METADATA_URI_V4
#   4. credentials arrive the way task roles deliver them, with no keys anywhere
set -uo pipefail

CLUSTER="${CLUSTER:-payments-local}"
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-us-east-1}"
AWS=(aws)
[ -n "${AWS_ENDPOINT_URL:-}" ] && AWS=(aws --endpoint-url "$AWS_ENDPOINT_URL")

rule() { printf '\n\033[1m%s\033[0m\n' "$*"; }

rule "1. The ECS control plane: services and task counts"
"${AWS[@]}" ecs describe-services --cluster "$CLUSTER" \
  --services identityd paymentd \
  --query 'services[].{Service:serviceName,Desired:desiredCount,Running:runningCount,Pending:pendingCount,LaunchType:capacityProviderStrategy[0].capacityProvider,TaskDef:taskDefinition}' \
  --output table 2>/dev/null

rule "2. Tasks, with the task-definition revision actually running"
"${AWS[@]}" ecs list-tasks --cluster "$CLUSTER" --query 'taskArns' --output text 2>/dev/null \
  | tr '\t' '\n' | while read -r arn; do
      [ -z "$arn" ] && continue
      "${AWS[@]}" ecs describe-tasks --cluster "$CLUSTER" --tasks "$arn" \
        --query 'tasks[0].{Group:group,Status:lastStatus,Health:healthStatus,Def:taskDefinitionArn,CPU:cpu,Mem:memory}' \
        --output text 2>/dev/null
    done

rule "3. The task definition is Fargate-shaped (not a docker-compose service)"
for fam in identityd paymentd; do
  "${AWS[@]}" ecs describe-task-definition --task-definition "$fam" \
    --query 'taskDefinition.{Family:family,NetworkMode:networkMode,Compat:requiresCompatibilities[0],Arch:runtimePlatform.cpuArchitecture,ExecRole:executionRoleArn}' \
    --output text 2>/dev/null
done

rule "4. The task SELF-describes through ECS_CONTAINER_METADATA_URI_V4"
echo "   (every real ECS task gets this; nothing in our code sets it)"
CID=$(docker ps --filter "name=ministack-ecs-.*-identityd$" --format '{{.Names}}' | head -1)
if [ -n "$CID" ]; then
  URI=$(docker inspect "$CID" --format '{{range .Config.Env}}{{println .}}{{end}}' \
        | grep ECS_CONTAINER_METADATA_URI_V4 | cut -d= -f2-)
  docker run --rm --network ecom-local curlimages/curl:latest -s "$URI/task" 2>/dev/null \
    | python3 -c 'import sys,json;d=json.load(sys.stdin);print("   Cluster  :",d["Cluster"]);print("   TaskARN  :",d["TaskARN"]);print("   Family   :",d["Family"],"rev",d["Revision"]);print("   AZ       :",d.get("AvailabilityZone"));print("   Status   :",d["KnownStatus"])' 2>/dev/null \
    || echo "   metadata endpoint unavailable"
fi

rule "5. Credentials: delivered the task-role way, no keys in the image"
echo "   The AWS SDK reads these automatically. There is no access key anywhere."
if [ -n "$CID" ]; then
  docker inspect "$CID" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | grep -E "^AWS_CONTAINER_CREDENTIALS|^ECS_CONTAINER_METADATA" \
    | sed 's/^/   /' | cut -c1-96
fi

rule "6. The JWT secret came from Secrets Manager, not the image"
"${AWS[@]}" ecs describe-task-definition --task-definition identityd \
  --query 'taskDefinition.containerDefinitions[0].secrets' --output text 2>/dev/null | sed 's/^/   /'
echo "   ^ the task definition stores an ARN. The value never touches git or the image."

rule "7. The users living inside the ECS tasks"
echo "   No ListUsers RPC by design, so prove it by authenticating as each"
echo "   SEEDED user. These were baked into the image at build time, so EVERY"
echo "   identityd task answers for them identically - which is what makes the"
echo "   service scalable despite using an embedded database."
for email in demo@example.com asha@example.com ravi@example.com; do
  out=$(docker run --rm --network ecom-dns fullstorydev/grpcurl:latest -plaintext \
        -d "{\"email\":\"$email\",\"password\":\"demo-password\"}" \
        identityd.ecom.local:50051 identity.v1.IdentityService/Login 2>&1)
  tok=$(echo "$out" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])' 2>/dev/null)
  if [ -z "$tok" ]; then
    printf '   %-22s LOGIN FAILED\n' "$email"
    continue
  fi
  who=$(docker run --rm --network ecom-dns fullstorydev/grpcurl:latest -plaintext \
        -H "authorization: Bearer $tok" -d '{}' \
        identityd.ecom.local:50051 identity.v1.IdentityService/VerifyToken 2>/dev/null \
        | python3 -c 'import sys,json;u=json.load(sys.stdin)["user"];print(u["id"][:8]+"  "+u["name"])' 2>/dev/null)
  printf '   %-22s %s\n' "$email" "$who"
done

rule "Emulator fidelity: three fields the local run does NOT populate"
cat <<'NOTE'
   Health     : UNKNOWN  - Ministack does not report container healthStatus.
                           Real ECS shows HEALTHY once the healthCheck passes.
   LaunchType : None/EC2 - the task definition requests FARGATE and the service
                           uses the FARGATE capacity provider; the emulator just
                           does not echo it back. Real ECS reports FARGATE.
   Say this out loud rather than hoping nobody reads the table. It is the honest
   boundary of the emulator, and it is exactly why you also deploy to AWS.
NOTE
