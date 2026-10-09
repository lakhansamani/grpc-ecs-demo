#!/usr/bin/env bash
# Block until orderd can actually reach its upstreams.
#
# WHY THIS EXISTS: terraform replaces the task containers on every apply, and
# the Cloud Map stand-in (`make dns`) can only attach the alias network AFTER
# those containers exist. So a freshly deployed orderd starts, fails to resolve
# userd.ecom.local, and its gRPC client caches that failure with a backoff. It
# self-heals - but not before a demo has already failed in front of a room.
#
# The probe has to exercise the REAL hop. Checking that userd.ecom.local
# resolves from a new container is not enough: the name resolves while orderd
# is still sitting in resolver backoff. So: log in at userd, then call an
# ORDERD rpc that authenticates, which forces orderd -> userd.
set -uo pipefail

USER_ADDR="${USER_ADDR:-userd.ecom.local:50051}"
ORDER_ADDR="${ORDER_ADDR:-orderd.ecom.local:50052}"
NETWORK="${DEMO_NETWORK:-ecom-dns}"
EMAIL="${DEMO_EMAIL:-demo@example.com}"
PASSWORD="${DEMO_PASSWORD:-demo-password}"
ATTEMPTS="${WAIT_ATTEMPTS:-30}"

if [ "$NETWORK" = "host" ]; then
  g() { grpcurl -plaintext "$@"; }
else
  g() { docker run --rm --network "$NETWORK" fullstorydev/grpcurl:latest -plaintext "$@"; }
fi

printf 'waiting for orderd to reach its upstreams'
for _ in $(seq 1 "$ATTEMPTS"); do
  tok=$(g -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
        "$USER_ADDR" user.v1.UserService/Login 2>/dev/null \
        | python3 -c 'import sys,json
raw=sys.stdin.read().strip()
print(json.loads(raw)["token"] if raw else "")' 2>/dev/null)

  if [ -n "$tok" ] && g -H "authorization: Bearer $tok" -d '{}' \
       "$ORDER_ADDR" order.v1.OrderService/ListOrders >/dev/null 2>&1; then
    printf ' ready\n'
    exit 0
  fi
  printf '.'
  sleep 2
done

printf '\n'
echo "orderd still cannot reach its upstreams." >&2
echo "try: make dns   (and for the localhost loop, make forward)" >&2
exit 1
