#!/usr/bin/env bash
# Wait until the ECS services on real AWS have all their tasks RUNNING.
set -uo pipefail
CLUSTER="${CLUSTER:-ecom-aws}"
WANT="${WANT:-4}"
for _ in $(seq 1 40); do
  n=$(aws ecs list-tasks --cluster "$CLUSTER" --desired-status RUNNING \
        --query 'length(taskArns)' --output text 2>/dev/null || echo 0)
  if [ "$n" = "$WANT" ]; then printf ' %s/%s running\n' "$n" "$WANT"; exit 0; fi
  printf '.'
  sleep 15
done
printf '\nonly %s of %s tasks are running. Check: aws logs tail /ecs/%s --since 10m\n' "${n:-0}" "$WANT" "$CLUSTER" >&2
exit 1
