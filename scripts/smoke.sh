#!/usr/bin/env bash
# End-to-end smoke test across all three services.
#
# Runs grpcurl in a container by default, because awsvpc ECS tasks have no host
# port. Set DEMO_NETWORK=host (or run with localhost addresses) for the
# no-emulator dev loop.
set -euo pipefail

USER_ADDR="${USER_ADDR:-userd.ecom.local:50051}"
PRODUCT_ADDR="${PRODUCT_ADDR:-productsd.ecom.local:50053}"
ORDER_ADDR="${ORDER_ADDR:-orderd.ecom.local:50052}"
NETWORK="${DEMO_NETWORK:-ecom-dns}"
EMAIL="${DEMO_EMAIL:-demo@example.com}"
PASSWORD="${DEMO_PASSWORD:-demo-password}"

if [ "$NETWORK" = "host" ]; then
  g() { grpcurl -plaintext "$@"; }
else
  g() { docker run --rm --network "$NETWORK" fullstorydev/grpcurl:latest -plaintext "$@"; }
fi
# Fail with a sentence, not a JSONDecodeError traceback. An empty stdin here
# almost always means grpcurl could not reach the service, and a stack trace
# from json.load buries that on a projector.
j() {
  python3 -c "
import sys, json
raw = sys.stdin.read().strip()
if not raw:
    sys.stderr.write('   !! no response from the service (see the grpcurl error above)\n')
    sys.exit(1)
try:
    d = json.loads(raw)
except json.JSONDecodeError:
    sys.stderr.write('   !! unexpected non-JSON response:\n' + raw[:400] + '\n')
    sys.exit(1)
print($1)"
}

# Shared with `make wait-ready`. Terraform replaces task containers on every
# apply, so a fresh orderd can still be in DNS backoff when the demo starts.
USER_ADDR="$USER_ADDR" ORDER_ADDR="$ORDER_ADDR" DEMO_NETWORK="$NETWORK" \
  DEMO_EMAIL="$EMAIL" DEMO_PASSWORD="$PASSWORD" \
  bash "$(dirname "$0")/wait-ready.sh"

STAMP=$(date +%s)

echo "== browse: search the catalogue (no auth; this is the read path that scales) =="
g -d '{"query":"cancelling"}' "$PRODUCT_ADDR" product.v1.ProductService/SearchProducts \
  | j 'str(d.get("totalMatches",0))+" matches: "+", ".join(p["title"] for p in d.get("products",[]))' | sed 's/^/   /'

echo "== browse: list one category =="
g -d '{"category":"footwear"}' "$PRODUCT_ADDR" product.v1.ProductService/ListProducts \
  | j '", ".join(p["title"] for p in d.get("products",[]))' | sed 's/^/   /'

echo "== log in as a SEEDED user =="
echo "   (a freshly registered user exists on only one userd task)"
TOKEN=$(g -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" "$USER_ADDR" user.v1.UserService/Login | j 'd["token"]')
AUTH="authorization: Bearer $TOKEN"
echo "   token acquired"

echo "== order placed: two hops, and the client sent NO prices =="
g -H "$AUTH" -d "{\"items\":[{\"product_id\":\"p-1001\",\"quantity\":1},{\"product_id\":\"p-1010\",\"quantity\":2}],\"idempotency_key\":\"s-$STAMP-a\"}" \
  "$ORDER_ADDR" order.v1.OrderService/CreateOrder \
  | j 'd["order"]["status"]+"  total="+str(int(d["order"]["totalMinor"])/100)+" "+d["order"]["currency"]+"  ("+str(len(d["order"]["lines"]))+" lines priced by productsd)"' | sed 's/^/   /'

echo "== idempotent replay: same key, no second order =="
FIRST=$(g -H "$AUTH" -d "{\"items\":[{\"product_id\":\"p-1008\",\"quantity\":1}],\"idempotency_key\":\"s-$STAMP-r\"}" \
  "$ORDER_ADDR" order.v1.OrderService/CreateOrder | j 'd["order"]["id"]')
SECOND=$(g -H "$AUTH" -d "{\"items\":[{\"product_id\":\"p-1008\",\"quantity\":1}],\"idempotency_key\":\"s-$STAMP-r\"}" \
  "$ORDER_ADDR" order.v1.OrderService/CreateOrder | j 'd["order"]["id"]+" "+str(d.get("idempotentReplay"))')
[ "$FIRST" = "${SECOND%% *}" ] && echo "   same order returned (replay=${SECOND##* })" \
  || { echo "   FAIL: ids differ"; exit 1; }

echo "== rejected: out of stock =="
g -H "$AUTH" -d "{\"items\":[{\"product_id\":\"p-1003\",\"quantity\":1}],\"idempotency_key\":\"s-$STAMP-b\"}" \
  "$ORDER_ADDR" order.v1.OrderService/CreateOrder \
  | j 'd["order"]["status"]+" | "+d["order"]["rejectionReason"]' | sed 's/^/   /'

echo "== rejected: only one left, asked for three =="
g -H "$AUTH" -d "{\"items\":[{\"product_id\":\"p-1005\",\"quantity\":3}],\"idempotency_key\":\"s-$STAMP-c\"}" \
  "$ORDER_ADDR" order.v1.OrderService/CreateOrder \
  | j 'd["order"]["status"]+" | "+d["order"]["rejectionReason"]' | sed 's/^/   /'

echo "== no token: rejected via the userd hop =="
if g -d '{"items":[{"product_id":"p-1001","quantity":1}],"idempotency_key":"nope"}' \
     "$ORDER_ADDR" order.v1.OrderService/CreateOrder >/dev/null 2>&1; then
  echo "   FAIL: unauthenticated call succeeded"; exit 1
fi
echo "   Unauthenticated, as expected"

echo
echo "SMOKE TEST PASSED"
