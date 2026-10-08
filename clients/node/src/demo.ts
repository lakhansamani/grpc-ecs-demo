/**
 * TypeScript client for identityd + paymentd, generated from the SAME .proto
 * files that produced the Go server stubs. Nothing here was hand-written from
 * documentation: the types, field names and method signatures all come out of
 * `buf generate`.
 *
 * This is the gRPC argument that actually holds up in practice (SPEC.md 14.3):
 * one contract, N languages, no drift. Rename a field in the proto and this
 * file stops compiling.
 *
 *   npm run demo
 *   IDENTITY_URL=... PAYMENT_URL=... npm run demo
 */
import { createClient, Code, ConnectError } from "@connectrpc/connect";
import { createGrpcTransport } from "@connectrpc/connect-node";

import { IdentityService } from "./gen/identity/v1/identity_pb.js";
import {
  PaymentService,
  Decision,
  DeclineReason,
} from "./gen/payment/v1/payment_pb.js";

const identityUrl = process.env.IDENTITY_URL ?? "http://localhost:50151";
const paymentUrl = process.env.PAYMENT_URL ?? "http://localhost:50152";

// Real gRPC over HTTP/2 - the same protocol the Go client speaks, not a REST shim.
//
// FOR THE TALK: this works because Node can open an HTTP/2 connection and send
// trailers. A BROWSER cannot, which is why gRPC-Web and the Connect protocol
// exist. Swap createGrpcTransport for createConnectTransport and these exact
// generated types work in a browser instead.
const identity = createClient(IdentityService, createGrpcTransport({ baseUrl: identityUrl }));
const payments = createClient(PaymentService, createGrpcTransport({ baseUrl: paymentUrl }));

/** Minor units -> readable. amountMinor is a bigint: int64 does not fit safely in a JS number. */
function rupees(minor: bigint): string {
  const sign = minor < 0n ? "-" : "";
  const abs = minor < 0n ? -minor : minor;
  return `${sign}₹${abs / 100n}.${String(abs % 100n).padStart(2, "0")}`;
}

function codeOf(err: unknown): string {
  return err instanceof ConnectError ? Code[err.code] : String(err);
}

async function main(): Promise<void> {
  const email = `ts-${Date.now()}@example.com`;
  const password = "supersecret";

  console.log("--- identityd ---");
  const registered = await identity.register({ name: "TypeScript Client", email, password });
  console.log("registered   ->", registered.userId);

  const session = await identity.login({ email, password });
  console.log("logged in    -> expires", new Date(Number(session.expiresAt)).toISOString());

  // The token rides in gRPC metadata, exactly as paymentd forwards it onward.
  const auth = { headers: { authorization: `Bearer ${session.token}` } };
  const me = await identity.verifyToken({}, auth);
  console.log("verified     ->", me.user?.email);

  console.log("--- paymentd (each call makes a second gRPC hop to identityd) ---");

  const ok = await payments.authorize(
    {
      amountMinor: 120_000n,
      currency: "INR",
      merchantId: "M-GROCER",
      merchantCategory: "5411",
      idempotencyKey: `ts-${Date.now()}-a`,
    },
    auth,
  );
  const okTxn = ok.transaction!;
  console.log("approved     ->", rupees(okTxn.amountMinor), Decision[okTxn.decision]);

  // Same key twice must not authorize twice.
  const replayKey = `ts-${Date.now()}-replay`;
  const first = await payments.authorize(
    { amountMinor: 50_000n, currency: "INR", merchantId: "M-CAFE", merchantCategory: "5812", idempotencyKey: replayKey },
    auth,
  );
  const second = await payments.authorize(
    { amountMinor: 50_000n, currency: "INR", merchantId: "M-CAFE", merchantCategory: "5812", idempotencyKey: replayKey },
    auth,
  );
  console.log(
    "idempotent   ->",
    first.transaction!.id === second.transaction!.id ? "same transaction" : "BUG: different ids",
    `(replay=${second.idempotentReplay})`,
  );

  // Over the per-transaction limit. A decline is a business outcome, so it
  // arrives as OK with a DECLINED decision - not as a thrown error.
  const declined = await payments.authorize(
    {
      amountMinor: 4_500_000n,
      currency: "INR",
      merchantId: "M-TV",
      merchantCategory: "5732",
      idempotencyKey: `ts-${Date.now()}-b`,
    },
    auth,
  );
  const badTxn = declined.transaction!;
  console.log(
    "declined     ->",
    rupees(badTxn.amountMinor),
    Decision[badTxn.decision],
    DeclineReason[badTxn.declineReason],
  );

  const why = await payments.explainDecision({ transactionId: badTxn.id }, auth);
  console.log(`explanation  -> "${why.explanation}" [${why.provider}]`);

  const list = await payments.listTransactions({ pageSize: 5 }, auth);
  console.log("history      ->", list.transactions.length, "transactions");

  console.log("--- typed errors cross the language boundary ---");
  try {
    await identity.register({ name: "Duplicate", email, password });
    console.error("BUG: duplicate registration should have failed");
    process.exitCode = 1;
  } catch (err) {
    console.log("duplicate    ->", codeOf(err));
  }

  try {
    await payments.authorize(
      { amountMinor: 1n, currency: "INR", merchantId: "M-X", merchantCategory: "5411", idempotencyKey: "nope" },
      { headers: { authorization: "Bearer nonsense" } },
    );
    console.error("BUG: a bogus token should have failed");
    process.exitCode = 1;
  } catch (err) {
    console.log("bad token    ->", codeOf(err));
  }

  try {
    await payments.authorize({ amountMinor: 0n, currency: "INR", idempotencyKey: "zero" } as never, auth);
    console.error("BUG: a zero amount should have failed");
    process.exitCode = 1;
  } catch (err) {
    console.log("zero amount  ->", codeOf(err));
  }
}

main().catch((err: unknown) => {
  console.error("client failed:", err);
  process.exit(1);
});
