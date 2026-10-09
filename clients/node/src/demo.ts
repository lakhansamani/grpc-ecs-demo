/**
 * TypeScript client for userd + productsd + orderd, generated from the SAME
 * .proto files that produced the Go server stubs. Nothing here was hand-written
 * from documentation: the types, field names and method signatures all come out
 * of `buf generate`.
 *
 * This is the gRPC argument that actually holds up in practice (SPEC.md 13.5):
 * one contract, N languages, no drift. Rename a field in the proto and this
 * file stops compiling.
 *
 *   make forward && npm run demo
 *   USER_URL=... PRODUCT_URL=... ORDER_URL=... npm run demo
 */
import { createClient, Code, ConnectError } from "@connectrpc/connect";
import { createGrpcTransport } from "@connectrpc/connect-node";

import { UserService } from "./gen/user/v1/user_pb.js";
import { ProductService } from "./gen/product/v1/product_pb.js";
import { OrderService, OrderStatus, RejectionReason } from "./gen/order/v1/order_pb.js";

const userUrl = process.env.USER_URL ?? "http://localhost:50051";
const orderUrl = process.env.ORDER_URL ?? "http://localhost:50052";
const productUrl = process.env.PRODUCT_URL ?? "http://localhost:50053";

// Real gRPC over HTTP/2 - the same protocol the Go client speaks, not a REST shim.
//
// FOR THE TALK: this works because Node can open an HTTP/2 connection and send
// trailers. A BROWSER cannot, which is why gRPC-Web, the Connect protocol and
// gatewayd exist. Swap createGrpcTransport for createConnectTransport and these
// exact generated types work in a browser instead.
const users = createClient(UserService, createGrpcTransport({ baseUrl: userUrl }));
const products = createClient(ProductService, createGrpcTransport({ baseUrl: productUrl }));
const orders = createClient(OrderService, createGrpcTransport({ baseUrl: orderUrl }));

/** Minor units -> readable. priceMinor is a bigint: int64 does not fit safely in a JS number. */
function rupees(minor: bigint): string {
  const sign = minor < 0n ? "-" : "";
  const abs = minor < 0n ? -minor : minor;
  return `${sign}₹${abs / 100n}.${String(abs % 100n).padStart(2, "0")}`;
}

function codeOf(err: unknown): string {
  return err instanceof ConnectError ? Code[err.code] : String(err);
}

async function main(): Promise<void> {
  console.log("--- productsd: the read path, no auth required ---");

  const found = await products.searchProducts({ query: "cancelling" });
  console.log("search       ->", found.products.map((p) => p.title).join(", ") || "(none)");

  const listed = await products.listProducts({ category: "footwear", pageSize: 5 });
  console.log("list         ->", listed.products.length, "in footwear");

  const one = await products.getProduct({ id: "p-1001" });
  console.log("get          ->", one.product?.title, rupees(one.product?.priceMinor ?? 0n));

  console.log("--- userd: log in as a SEEDED user ---");
  console.log("             (a Register'd user exists on only one task - SPEC.md 5.4)");

  const session = await users.login({ email: "demo@example.com", password: "demo-password" });
  console.log("logged in    -> expires", new Date(Number(session.expiresAt)).toISOString());

  // The token rides in gRPC metadata, exactly as orderd forwards it onward.
  const auth = { headers: { authorization: `Bearer ${session.token}` } };
  const me = await users.verifyToken({}, auth);
  console.log("verified     ->", me.user?.email);

  console.log("--- orderd: each call makes TWO more gRPC hops ---");
  console.log("             (userd.VerifyToken, then productsd.CheckAvailability)");

  // Note what is NOT in this request: a price. The client sends ids and
  // quantities; orderd asks productsd what things cost.
  const placed = await orders.createOrder(
    {
      items: [{ productId: "p-1001", quantity: 1 }],
      idempotencyKey: `ts-${Date.now()}-a`,
    },
    auth,
  );
  const order = placed.order!;
  console.log(
    "ordered      ->",
    OrderStatus[order.status],
    rupees(order.totalMinor),
    `(${order.lines.length} line priced by productsd)`,
  );

  // Same key twice must not place two orders.
  const replayKey = `ts-${Date.now()}-replay`;
  const first = await orders.createOrder(
    { items: [{ productId: "p-1002", quantity: 1 }], idempotencyKey: replayKey },
    auth,
  );
  const second = await orders.createOrder(
    { items: [{ productId: "p-1002", quantity: 1 }], idempotencyKey: replayKey },
    auth,
  );
  console.log(
    "idempotent   ->",
    first.order!.id === second.order!.id ? "same order" : "BUG: different ids",
    `(replay=${second.idempotentReplay})`,
  );

  // p-1003 is seeded with zero stock. A rejection is a business outcome, so it
  // arrives as OK with a REJECTED status - not as a thrown error. Clients
  // branch on an enum, never on a message string.
  const rejected = await orders.createOrder(
    { items: [{ productId: "p-1003", quantity: 1 }], idempotencyKey: `ts-${Date.now()}-b` },
    auth,
  );
  console.log(
    "rejected     ->",
    OrderStatus[rejected.order!.status],
    RejectionReason[rejected.order!.rejectionReason],
  );

  const history = await orders.listOrders({ pageSize: 5 }, auth);
  console.log("history      ->", history.orders.length, "orders");

  console.log("--- typed errors cross the language boundary ---");

  try {
    await users.login({ email: "demo@example.com", password: "wrong-password" });
    console.error("BUG: a wrong password should have failed");
    process.exitCode = 1;
  } catch (err) {
    console.log("bad password ->", codeOf(err));
  }

  try {
    await orders.createOrder(
      { items: [{ productId: "p-1001", quantity: 1 }], idempotencyKey: "nope" },
      { headers: { authorization: "Bearer nonsense" } },
    );
    console.error("BUG: a bogus token should have failed");
    process.exitCode = 1;
  } catch (err) {
    console.log("bad token    ->", codeOf(err));
  }

  try {
    await orders.createOrder({ items: [], idempotencyKey: `ts-${Date.now()}-empty` }, auth);
    console.error("BUG: an empty cart should have failed");
    process.exitCode = 1;
  } catch (err) {
    console.log("empty cart   ->", codeOf(err));
  }
}

main().catch((err: unknown) => {
  console.error("client failed:", err);
  process.exit(1);
});
