#!/usr/bin/env bash
# End-to-end smoke test against whatever is listening at the given addresses.
# Runs grpcurl inside a container because awsvpc tasks have no host port.
set -euo pipefail

IDENTITY="${IDENTITY_ADDR:-identityd.ecom.local:50051}"
PAYMENT="${PAYMENT_ADDR:-paymentd.ecom.local:50052}"
NETWORK="${DEMO_NETWORK:-ecom-dns}"
EMAIL="${DEMO_EMAIL:-demo@example.com}"
PASSWORD="${DEMO_PASSWORD:-demo-password}"

g() { docker run --rm --network "$NETWORK" fullstorydev/grpcurl:latest -plaintext "$@"; }
jq_get() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

echo "== login as a SEEDED user =="
echo "   (a freshly registered user would exist on only one identityd task)"
TOKEN=$(g -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
  "$IDENTITY" identity.v1.IdentityService/Login | jq_get 'd["token"]')
echo "   token acquired"

AUTH="authorization: Bearer $TOKEN"
STAMP=$(date +%s)

echo "== approved: Rs 1,200 at a grocer =="
g -H "$AUTH" -d "{\"amount_minor\":120000,\"currency\":\"INR\",\"merchant_id\":\"M-GROCER\",\"merchant_category\":\"5411\",\"idempotency_key\":\"s-$STAMP-a\"}" \
  "$PAYMENT" payment.v1.PaymentService/Authorize \
  | jq_get '"   "+d["transaction"]["decision"]+"  amount_minor="+d["transaction"]["amountMinor"]'

echo "== idempotent replay: same key, no second authorization =="
FIRST=$(g -H "$AUTH" -d "{\"amount_minor\":50000,\"currency\":\"INR\",\"merchant_id\":\"M-CAFE\",\"merchant_category\":\"5812\",\"idempotency_key\":\"s-$STAMP-r\"}" \
  "$PAYMENT" payment.v1.PaymentService/Authorize | jq_get 'd["transaction"]["id"]')
SECOND=$(g -H "$AUTH" -d "{\"amount_minor\":50000,\"currency\":\"INR\",\"merchant_id\":\"M-CAFE\",\"merchant_category\":\"5812\",\"idempotency_key\":\"s-$STAMP-r\"}" \
  "$PAYMENT" payment.v1.PaymentService/Authorize | jq_get 'd["transaction"]["id"]+" "+str(d.get("idempotentReplay"))')
[ "$FIRST" = "${SECOND%% *}" ] && echo "   same transaction returned (replay=${SECOND##* })" \
  || { echo "   FAIL: ids differ"; exit 1; }

echo "== declined: over the Rs 25,000 per-transaction limit =="
DECLINED=$(g -H "$AUTH" -d "{\"amount_minor\":4500000,\"currency\":\"INR\",\"merchant_id\":\"M-TV\",\"merchant_category\":\"5732\",\"idempotency_key\":\"s-$STAMP-b\"}" \
  "$PAYMENT" payment.v1.PaymentService/Authorize)
TXN_ID=$(echo "$DECLINED" | jq_get 'd["transaction"]["id"]')
echo "$DECLINED" | jq_get '"   "+d["transaction"]["decision"]+"  "+d["transaction"]["declineReason"]'

echo "== explain that decline =="
g -H "$AUTH" -d "{\"transaction_id\":\"$TXN_ID\"}" \
  "$PAYMENT" payment.v1.PaymentService/ExplainDecision \
  | jq_get '"   \""+d["explanation"]+"\"  ["+d["provider"]+"]"'

echo "== declined: blocked merchant category (gambling) =="
g -H "$AUTH" -d "{\"amount_minor\":50000,\"currency\":\"INR\",\"merchant_id\":\"M-BET\",\"merchant_category\":\"7995\",\"idempotency_key\":\"s-$STAMP-c\"}" \
  "$PAYMENT" payment.v1.PaymentService/Authorize \
  | jq_get '"   "+d["transaction"]["declineReason"]'

echo "== no token: rejected by the identityd hop =="
if g -d '{"amount_minor":100,"idempotency_key":"nope"}' "$PAYMENT" payment.v1.PaymentService/Authorize >/dev/null 2>&1; then
  echo "   FAIL: unauthenticated call succeeded"; exit 1
fi
echo "   Unauthenticated, as expected"

echo
echo "SMOKE TEST PASSED"
