#!/usr/bin/env bash
# The load-balancing demo, entirely in the terminal.
#
#   bash scripts/lb-demo.sh pick_first    # gRPC's DEFAULT: one task takes all
#   bash scripts/lb-demo.sh round_robin   # the fix: evenly spread
#
# It redeploys orderd with the chosen client behaviour, re-aliases DNS, waits
# for the upstreams, snapshots per-task counters, drives load, and prints the
# delta per task. No Prometheus, no browser.
set -uo pipefail

MODE="${1:-}"
case "$MODE" in
  pick_first|round_robin) ;;
  *) echo "usage: $0 pick_first|round_robin" >&2; exit 1 ;;
esac

N="${N:-120}"
TF="terraform -chdir=terraform/envs/local"

echo "==> redeploying orderd with lb_policy=$MODE"
if [ "$MODE" = "round_robin" ]; then
  $TF apply -auto-approve >/dev/null       # round_robin is the default
else
  $TF apply -auto-approve -var lb_policy=pick_first >/dev/null
fi

make --no-print-directory dns >/dev/null
make --no-print-directory wait-ready
make --no-print-directory forward >/dev/null

echo "==> before"
before=$(bash scripts/lb-report.sh)
echo "$before"

echo "==> driving $N authenticated calls through orderd"
N="$N" bash scripts/load.sh >/dev/null 2>&1

echo "==> after"
after=$(bash scripts/lb-report.sh)
echo "$after"

echo
echo "==> delta per task  (lb_policy=$MODE)"
join -j1 \
  <(echo "$before" | awk 'NF==2 && $1!="TASK" && $1!="----" && $1!="TOTAL" {print $1, $2}' | sort) \
  <(echo "$after"  | awk 'NF==2 && $1!="TASK" && $1!="----" && $1!="TOTAL" {print $1, $2}' | sort) \
  2>/dev/null | awk '{printf "  %-12s +%d\n", $1, $3-$2}'

if [ "$MODE" = "pick_first" ]; then
  echo
  echo "  ^ gRPC's DEFAULT. One task served everything, and the others were"
  echo "    healthy and in DNS the whole time. Nothing warned you."
  echo "    Now run:  make demo-lb-after"
else
  echo
  echo "  ^ round_robin + the dns:/// prefix. Same image, same task definition,"
  echo "    same DNS. Two client settings."
fi
