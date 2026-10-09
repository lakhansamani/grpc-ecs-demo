#!/usr/bin/env bash
# Hits EVERY rpc in the protos and EVERY generated REST route, then reports
# anything it did not reach. Run against the dev loop (all four services up).
set -uo pipefail

USER_ADDR="${USER_ADDR:-127.0.0.1:50051}"
PRODUCT_ADDR="${PRODUCT_ADDR:-127.0.0.1:50053}"
ORDER_ADDR="${ORDER_ADDR:-127.0.0.1:50052}"
REST="${REST_BASE:-http://localhost:8080}"

pass=0; fail=0
check() { # name, then the command
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then printf '  ok   %s\n' "$name"; pass=$((pass+1))
  else printf '  FAIL %s\n' "$name"; fail=$((fail+1)); fi
}
j() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

STAMP=$(date +%s)
TOKEN=$(grpcurl -plaintext -d '{"email":"demo@example.com","password":"demo-password"}' \
  "$USER_ADDR" user.v1.UserService/Login 2>/dev/null | j 'd["token"]')
if [ -z "${TOKEN:-}" ]; then echo "could not log in - are all four services running?"; exit 1; fi
AUTH="authorization: Bearer $TOKEN"

echo "gRPC — user.v1.UserService"
check "Register"    grpcurl -plaintext -d "{\"name\":\"Cov\",\"email\":\"cov-$STAMP@example.com\",\"password\":\"supersecret\"}" "$USER_ADDR" user.v1.UserService/Register
check "Login"       grpcurl -plaintext -d '{"email":"demo@example.com","password":"demo-password"}' "$USER_ADDR" user.v1.UserService/Login
check "VerifyToken" grpcurl -plaintext -H "$AUTH" -d '{}' "$USER_ADDR" user.v1.UserService/VerifyToken

echo "gRPC — product.v1.ProductService"
check "ListProducts"      grpcurl -plaintext -d '{"page_size":5}' "$PRODUCT_ADDR" product.v1.ProductService/ListProducts
check "GetProduct"        grpcurl -plaintext -d '{"id":"p-1001"}' "$PRODUCT_ADDR" product.v1.ProductService/GetProduct
check "SearchProducts"    grpcurl -plaintext -d '{"query":"shoes"}' "$PRODUCT_ADDR" product.v1.ProductService/SearchProducts
check "CheckAvailability" grpcurl -plaintext -d '{"items":[{"product_id":"p-1001","quantity":1}]}' "$PRODUCT_ADDR" product.v1.ProductService/CheckAvailability

echo "gRPC — order.v1.OrderService"
ORDER_ID=$(grpcurl -plaintext -H "$AUTH" \
  -d "{\"items\":[{\"product_id\":\"p-1001\",\"quantity\":1}],\"idempotency_key\":\"cov-$STAMP\"}" \
  "$ORDER_ADDR" order.v1.OrderService/CreateOrder 2>/dev/null | j 'd["order"]["id"]')
check "CreateOrder" test -n "$ORDER_ID"
check "GetOrder"    grpcurl -plaintext -H "$AUTH" -d "{\"id\":\"$ORDER_ID\"}" "$ORDER_ADDR" order.v1.OrderService/GetOrder
check "ListOrders"  grpcurl -plaintext -H "$AUTH" -d '{"page_size":5}' "$ORDER_ADDR" order.v1.OrderService/ListOrders

echo "REST — through gatewayd"
check "POST /v1/users"          curl -sf -X POST "$REST/v1/users" -H 'Content-Type: application/json' -d "{\"name\":\"Cov\",\"email\":\"rcov-$STAMP@example.com\",\"password\":\"supersecret\"}"
check "POST /v1/sessions"       curl -sf -X POST "$REST/v1/sessions" -H 'Content-Type: application/json' -d '{"email":"demo@example.com","password":"demo-password"}'
check "GET  /v1/users/me"       curl -sf "$REST/v1/users/me" -H "Authorization: Bearer $TOKEN"
check "GET  /v1/products"       curl -sf "$REST/v1/products?pageSize=5"
check "GET  /v1/products/{id}"  curl -sf "$REST/v1/products/p-1001"
check "GET  /v1/products:search" curl -sf "$REST/v1/products:search?query=shoes"
check "POST /v1/orders"         curl -sf -X POST "$REST/v1/orders" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"items\":[{\"productId\":\"p-1001\",\"quantity\":1}],\"idempotencyKey\":\"rcov-$STAMP\"}"
check "GET  /v1/orders"         curl -sf "$REST/v1/orders?pageSize=5" -H "Authorization: Bearer $TOKEN"
check "GET  /v1/orders/{id}"    curl -sf "$REST/v1/orders/$ORDER_ID" -H "Authorization: Bearer $TOKEN"

echo "negative checks"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$REST/v1/products:checkAvailability" -d '{}')
if [ "$code" = "404" ]; then printf '  ok   CheckAvailability NOT exposed over REST (404)\n'; pass=$((pass+1))
else printf '  FAIL internal RPC reachable over REST (got %s)\n' "$code"; fail=$((fail+1)); fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
