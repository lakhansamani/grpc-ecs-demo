#!/usr/bin/env bash
# REST surface check, through gatewayd. Plain curl - no gRPC tooling at all.
set -euo pipefail
BASE="${REST_BASE:-http://localhost:8080}"
j() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

echo "== health =="
curl -sf "$BASE/healthz" | sed 's/^/   /'

echo "== GET /v1/products:search?query=cancelling =="
curl -sf "$BASE/v1/products:search?query=cancelling" \
  | j '", ".join(p["title"] for p in d.get("products",[]))' | sed 's/^/   /'

echo "== GET /v1/products/p-1005 =="
curl -sf "$BASE/v1/products/p-1005" | j 'd["product"]["title"]+" | stock "+str(d["product"]["stock"])' | sed 's/^/   /'

echo "== POST /v1/sessions =="
TOKEN=$(curl -sf -X POST "$BASE/v1/sessions" -H 'Content-Type: application/json' \
  -d '{"email":"demo@example.com","password":"demo-password"}' | j 'd["token"]')
echo "   token acquired"

echo "== GET /v1/users/me  (Authorization header -> gRPC metadata) =="
curl -sf "$BASE/v1/users/me" -H "Authorization: Bearer $TOKEN" | j 'd["user"]["email"]' | sed 's/^/   /'

echo "== POST /v1/orders =="
curl -sf -X POST "$BASE/v1/orders" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"productId\":\"p-1001\",\"quantity\":1}],\"idempotencyKey\":\"rest-$(date +%s)\"}" \
  | j 'd["order"]["status"]+"  total="+str(int(d["order"]["totalMinor"])/100)' | sed 's/^/   /'

echo "== the point: CheckAvailability has NO http annotation =="
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/products:checkAvailability" -d '{}')
if [ "$code" != "404" ]; then
  echo "   FAIL: expected 404, got $code - an internal RPC is exposed over REST"; exit 1
fi
echo "   POST /v1/products:checkAvailability -> 404, unreachable from the internet"
echo "   (the same RPC over gRPC works - that is how orderd gets real prices)"

echo
echo "REST SMOKE TEST PASSED"
