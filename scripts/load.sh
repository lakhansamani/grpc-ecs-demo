#!/usr/bin/env bash
# Sustained load for the load-balancing segment.
#
# It drives orderd.ListOrders, and ListOrders authenticates - so EVERY request
# makes orderd call userd.VerifyToken. That is the hop being balanced, and
# userd's per-task metrics are what you watch in Prometheus:
#
#   sum by (task) (grpc_server_started_total{service="userd"})
#
# Deliberately NOT hitting userd directly: the socat forwarder opens a new
# connection per request, so it would spread traffic by accident and the demo
# would "work" whatever the client is configured to do.
#
# Authenticates as a SEEDED user. A user created with Register exists on
# exactly one userd task, so at N>1 two thirds of requests would fail in a way
# that looks exactly like the bug you are about to fix.
set -euo pipefail

USER_ADDR="${USER_ADDR:-localhost:50051}"
ORDER_ADDR="${ORDER_ADDR:-localhost:50052}"
EMAIL="${EMAIL:-demo@example.com}"
PASSWORD="${PASSWORD:-demo-password}"
N="${N:-300}"
CONCURRENCY="${CONCURRENCY:-8}"

command -v grpcurl >/dev/null || {
  echo "grpcurl not found. brew install grpcurl" >&2
  exit 1
}

# `make forward` recreates the socat relays on every deploy, and they take a
# second or two to accept connections. Without this the script used to fail at
# the first call right after an apply - which on stage looks like the demo
# itself broke.
echo "logging in as $EMAIL via $USER_ADDR"
TOKEN=""
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  TOKEN=$(grpcurl -plaintext -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
    "$USER_ADDR" user.v1.UserService/Login 2>/dev/null \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])' 2>/dev/null) || true
  [ -n "$TOKEN" ] && break
  echo "  $USER_ADDR not answering yet (attempt $attempt) - is \`make forward\` up?"
  sleep 2
done
[ -n "$TOKEN" ] || { echo "could not log in via $USER_ADDR. Run \`make forward\` first." >&2; exit 1; }

echo "driving $N ListOrders through $ORDER_ADDR at concurrency $CONCURRENCY"
echo "each one makes orderd call userd.VerifyToken"
echo

export TOKEN ORDER_ADDR
one() {
  grpcurl -plaintext -H "authorization: Bearer $TOKEN" -d '{}' \
    "$ORDER_ADDR" order.v1.OrderService/ListOrders >/dev/null 2>&1 \
    && printf . || printf '\033[31mX\033[0m'
}
export -f one

seq "$N" | xargs -P "$CONCURRENCY" -I{} bash -c one
echo
echo
echo "Now look at Prometheus: http://localhost:9090/graph?g0.expr=sum+by+(task)+(grpc_server_started_total)"
echo "A dot is a success, a red X is a failure. Zero X's is the point when you"
echo "kill a task mid-load: graceful shutdown drops nothing."
