/**
 * TypeScript client for identityd, generated from the SAME .proto file that
 * produced the Go server stubs. Nothing here was hand-written from docs: the
 * types, the field names and the method signatures all come out of
 * `buf generate`.
 *
 * This is the argument for gRPC that actually holds up in practice (see
 * SPEC.md 14.3): one contract, N languages, no drift. Rename a field in the
 * proto and this file stops compiling.
 *
 *   npm run demo            # against a local identityd
 *   IDENTITY_URL=... npm run demo
 */
import { createClient } from "@connectrpc/connect";
import { createGrpcTransport } from "@connectrpc/connect-node";

import { IdentityService } from "./gen/identity/v1/identity_pb.js";

const baseUrl = process.env.IDENTITY_URL ?? "http://localhost:50151";

// createGrpcTransport speaks real gRPC over HTTP/2 - the same protocol the Go
// client uses, not a REST shim.
//
// NOTE for the talk: this works because Node can open an HTTP/2 connection
// and set trailers. A BROWSER cannot, which is why gRPC-Web and the Connect
// protocol exist. Swap this for createConnectTransport and the same generated
// types work in a browser.
const transport = createGrpcTransport({ baseUrl });
const identity = createClient(IdentityService, transport);

function unique(prefix: string): string {
  return `${prefix}-${Date.now()}@example.com`;
}

async function main(): Promise<void> {
  const email = unique("ts-client");
  const password = "supersecret";

  // 1. Register. `userId` is camelCased by the generator from `user_id`.
  const registered = await identity.register({
    name: "TypeScript Client",
    email,
    password,
  });
  console.log("registered  ->", registered.userId);

  // 2. Login. expiresAt is a bigint, because int64 does not fit safely in a
  //    JS number. The generator gets this right so you cannot silently
  //    truncate a timestamp.
  const session = await identity.login({ email, password });
  console.log("logged in   -> token", session.token.slice(0, 24) + "...");
  console.log("expires at  ->", new Date(Number(session.expiresAt)).toISOString());

  // 3. VerifyToken. The token goes in gRPC metadata, not the body - matching
  //    what paymentd forwards on every Authorize call.
  const me = await identity.verifyToken(
    {},
    { headers: { authorization: `Bearer ${session.token}` } },
  );
  console.log("verified    ->", me.user?.email, `(${me.user?.name})`);

  // 4. Typed errors. gRPC status codes survive the language boundary, so a
  //    client can branch on them instead of string-matching a message.
  try {
    await identity.register({ name: "Duplicate", email, password });
    console.error("BUG: duplicate registration should have failed");
    process.exitCode = 1;
  } catch (err) {
    const code = (err as { code?: unknown }).code;
    console.log("duplicate   -> rejected with code:", String(code));
  }

  try {
    await identity.verifyToken({}, { headers: { authorization: "Bearer nonsense" } });
    console.error("BUG: a bogus token should have failed");
    process.exitCode = 1;
  } catch (err) {
    const code = (err as { code?: unknown }).code;
    console.log("bad token   -> rejected with code:", String(code));
  }
}

main().catch((err: unknown) => {
  console.error("client failed:", err);
  process.exit(1);
});
