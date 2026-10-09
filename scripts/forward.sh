#!/usr/bin/env bash
# Publish local ports that reach the ECS tasks, so Postman / grpcurl / BloomRPC
# on the host can call them.
#
# WHY THIS IS NEEDED: tasks use awsvpc networking, which is faithful to Fargate
# and means they have NO host port binding. From the Mac there is no route to
# the task. This is the same problem SSM port forwarding solves on real AWS -
# and it is NOT testable locally, because Ministack does not emulate
# ssmmessages. So locally we stand in a socat relay on the task network.
#
#   local : make forward   -> socat relay containers
#   AWS   : aws ssm start-session --document-name AWS-StartPortForwardingSession
#
# Same outcome, different mechanism. Say which one you are using on stage.
set -uo pipefail

NETWORK="${TASK_NETWORK:-ecom-dns}"
declare -a MAP=("identityd:50051" "paymentd:50052")

for entry in "${MAP[@]}"; do
  svc="${entry%%:*}"
  port="${entry##*:}"
  name="forward-$svc"

  docker rm -f "$name" >/dev/null 2>&1 || true

  if ! docker ps --filter "name=ministack-ecs-.*-$svc\$" -q | grep -q .; then
    echo "skip $svc: no running task"
    continue
  fi

  docker run -d --name "$name" --network "$NETWORK" \
    -p "127.0.0.1:$port:$port" \
    alpine/socat:latest \
    "TCP-LISTEN:$port,fork,reuseaddr" "TCP:$svc.ecom.local:$port" >/dev/null

  printf 'forwarding  localhost:%-6s -> %s.ecom.local:%s\n' "$port" "$svc" "$port"
done

echo
echo "Postman / grpcurl can now use localhost:50051 and localhost:50052."
echo "Stop with: make forward-stop"
