#!/usr/bin/env bash
# Print how many VerifyToken calls each userd TASK has served.
#
# Reads each task's own /metrics endpoint directly, so the load-balancing demo
# needs no Prometheus and no browser - the answer lands in the terminal.
#
# Task containers are distroless (no curl inside), and awsvpc tasks have no
# host port, so a throwaway curl container on the task network does the asking.
set -uo pipefail

NETWORK="${METRICS_NETWORK:-ecom-infra}"
METRIC="${METRIC:-grpc_server_started_total}"
SERVICE="${LB_SERVICE:-userd}"
PORT="${LB_METRICS_PORT:-9091}"

names=$(docker ps --filter "name=ministack-ecs-.*-${SERVICE}$" --format '{{.Names}}' | sort)
if [ -z "$names" ]; then
  echo "no running $SERVICE tasks. run: make tf-local-apply" >&2
  exit 1
fi

printf '  %-12s %s\n' "TASK" "VerifyToken calls served"
printf '  %-12s %s\n' "----" "------------------------"
total=0
for n in $names; do
  task=$(echo "$n" | sed -E 's/^ministack-ecs-([^-]+)-.*/\1/')
  v=$(docker run --rm --network "$NETWORK" curlimages/curl:latest \
        -s --max-time 5 "http://${n}:${PORT}/metrics" 2>/dev/null \
      | awk -v m="$METRIC" '$0 ~ "^"m"{" && /VerifyToken/ {s+=$NF} END {printf "%d", s+0}')
  v=${v:-0}
  total=$((total + v))
  printf '  %-12s %s\n' "$task" "$v"
done
printf '  %-12s %s\n' "" "----"
printf '  %-12s %s\n' "TOTAL" "$total"
