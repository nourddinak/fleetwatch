// node --test (Node 20+): the Worker's logic with a fake message and a fake dashboard.
import { test } from "node:test";
import assert from "node:assert/strict";
import { handle, edgeRoute } from "../src/worker.js";

const env = {
  FLEETWATCH_URL: "https://dash.example/api/emails/inbound",
  INBOUND_SECRET: "s3cret",
  ROUTES: JSON.stringify({ v: 1, d: { "nashop.store": ["me@gmail.com"] }, a: { "noreply@nashop.store": [] } }),
};

function fakeMessage(to, body = "From: a@b.c\r\n\r\nhi") {
  const m = { to, from: "Jane@Example.com", forwarded: [], rejected: null, failForward: false };
  m.raw = new Response(body).body;
  m.forward = async (dest) => {
    if (m.failForward) throw new Error("not verified");
    m.forwarded.push(dest);
  };
  m.setReject = (r) => (m.rejected = r);
  return m;
}

const answer = (status, json) => async () => new Response(JSON.stringify(json), { status });

test("stores, then forwards where the dashboard says", async () => {
  let seen;
  const fetchFn = async (url, init) => {
    seen = init;
    return new Response(JSON.stringify({ stored: true, forward: ["a@gmail.com", "b@gmail.com"] }));
  };
  const m = fakeMessage("Contact@Nashop.store");
  const r = await handle(m, env, fetchFn);
  assert.deepEqual(m.forwarded, ["a@gmail.com", "b@gmail.com"]);
  assert.equal(r.stored, true);
  assert.equal(seen.headers["X-Fw-Rcpt"], "contact@nashop.store");
  assert.match(seen.headers["X-Fw-Signature"], /^[0-9a-f]{64}$/);
  assert.equal(new Uint8Array(seen.body).length, "From: a@b.c\r\n\r\nhi".length);
});

test("unknown address is refused, not forwarded", async () => {
  const m = fakeMessage("nobody@nashop.store");
  await handle(m, env, answer(200, { stored: false, reject: "No such address.", forward: [] }));
  assert.equal(m.rejected, "No such address.");
  assert.deepEqual(m.forwarded, []);
});

test("dashboard down: Gmail copy from the edge routes", async () => {
  let calls = 0;
  const down = async () => {
    calls++;
    throw new Error("connection refused");
  };
  const m = fakeMessage("contact@nashop.store");
  await handle(m, env, down);
  assert.equal(calls, 2, "tries twice");
  assert.deepEqual(m.forwarded, ["me@gmail.com"]);
  assert.equal(m.rejected, null);
});

test("a refusal (4xx) is not retried; the Gmail copy still goes", async () => {
  let calls = 0;
  const m = fakeMessage("contact@nashop.store");
  await handle(m, env, async () => (calls++, new Response("{}", { status: 401 })));
  assert.equal(calls, 1);
  assert.deepEqual(m.forwarded, ["me@gmail.com"]);
});

test("dashboard down and nowhere to copy: refused with try-later, never dropped", async () => {
  const m = fakeMessage("noreply@nashop.store");
  await handle(m, env, async () => new Response("", { status: 503 }));
  assert.match(m.rejected, /try again later/);
});

test("dashboard down and Gmail forward fails: refused", async () => {
  const m = fakeMessage("contact@nashop.store");
  m.failForward = true;
  await handle(m, env, async () => new Response("", { status: 503 }));
  assert.match(m.rejected, /try again later/);
});

test("stored but Gmail forward fails: the message is kept, not refused", async () => {
  const m = fakeMessage("contact@nashop.store");
  m.failForward = true;
  await handle(m, env, answer(200, { stored: true, forward: ["a@gmail.com"] }));
  assert.equal(m.rejected, null);
});

test("large message is passed through whole", async () => {
  const big = "From: a@b.c\r\n\r\n" + "x".repeat(5 * 1024 * 1024);
  let size = 0;
  const m = fakeMessage("contact@nashop.store", big);
  await handle(m, env, async (u, init) => ((size = init.body.byteLength), new Response(JSON.stringify({ stored: true, forward: [] }))));
  assert.equal(size, big.length);
});

test("edge routes: address, plus addressing, domain default, bad JSON", () => {
  assert.deepEqual(edgeRoute(env.ROUTES, "noreply@nashop.store"), []);
  assert.deepEqual(edgeRoute(env.ROUTES, "noreply+x@nashop.store"), []);
  assert.deepEqual(edgeRoute(env.ROUTES, "sales@nashop.store"), ["me@gmail.com"]);
  assert.deepEqual(edgeRoute(env.ROUTES, "x@unknown.io"), []);
  assert.deepEqual(edgeRoute("{not json", "x@nashop.store"), []);
});
