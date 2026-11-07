// Fleetwatch Email Worker. Cloudflare Email Routing (one catch-all rule per domain)
// hands every message here. The Worker:
//   1. posts the raw message to the dashboard, signed (it stores it and answers where
//      Gmail copies go, or that the address does not exist);
//   2. copies it to those Gmail addresses (message.forward);
//   3. when the dashboard cannot be reached, still copies it to Gmail using ROUTES, the
//      forwarding the dashboard pushed here last, so nothing is lost while it is down.
//      If there is nowhere to copy it either, the message is refused with a "try later"
//      reason rather than dropped in silence.
//
// Bindings: FLEETWATCH_URL (plain text: https://dashboard/api/emails/inbound),
// INBOUND_SECRET (secret), ROUTES (secret JSON: {"d":{domain:[to…]},"a":{address:[to…]}}).

const enc = new TextEncoder();

async function sign(secret, text) {
  const key = await crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const mac = await crypto.subtle.sign("HMAC", key, enc.encode(text));
  return [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// edgeRoute is where copies go without the dashboard: the address's own list, else its
// domain's (plus addressing: me+x@ → me@).
export function edgeRoute(routesJSON, rcpt) {
  let routes;
  try {
    routes = JSON.parse(routesJSON || "{}");
  } catch {
    return [];
  }
  const a = routes.a || {};
  const d = routes.d || {};
  const at = rcpt.lastIndexOf("@");
  const local = rcpt.slice(0, at);
  const domain = rcpt.slice(at + 1);
  if (a[rcpt]) return a[rcpt];
  const plus = local.indexOf("+");
  if (plus > 0 && a[local.slice(0, plus) + "@" + domain]) return a[local.slice(0, plus) + "@" + domain];
  return d[domain] || [];
}

// post hands the message to the dashboard: its JSON answer, or null when it is not
// reachable (or answers with an error).
export async function post(env, rcpt, from, raw, fetchFn = fetch) {
  const ts = Math.floor(Date.now() / 1000).toString();
  // The signature covers the envelope and size, not the bytes: hashing 25 MiB would
  // not fit the Free plan's CPU time. TLS protects the body on the way.
  const sig = await sign(env.INBOUND_SECRET, `${ts}\n${rcpt}\n${from}\n${raw.byteLength}`);
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const res = await fetchFn(env.FLEETWATCH_URL, {
        method: "POST",
        headers: { "Content-Type": "message/rfc822", "X-Fw-Timestamp": ts, "X-Fw-Rcpt": rcpt, "X-Fw-From": from, "X-Fw-Signature": sig },
        body: raw,
      });
      if (res.ok) return await res.json();
      if (res.status < 500) return null; // refused (signature, size): retrying will not help
    } catch {
      /* network: try once more */
    }
  }
  return null;
}

export async function handle(message, env, fetchFn = fetch) {
  const rcpt = String(message.to).toLowerCase();
  const from = String(message.from || "").toLowerCase();
  const raw = await new Response(message.raw).arrayBuffer();
  const answer = await post(env, rcpt, from, raw, fetchFn);
  if (answer && answer.reject) {
    message.setReject(answer.reject);
    return { stored: false, forwarded: [], rejected: answer.reject };
  }
  const targets = answer ? answer.forward || [] : edgeRoute(env.ROUTES, rcpt);
  if (!answer && targets.length === 0) {
    message.setReject("Mailbox temporarily unavailable, please try again later.");
    return { stored: false, forwarded: [], rejected: "unavailable" };
  }
  const forwarded = [];
  for (const to of targets) {
    try {
      await message.forward(to);
      forwarded.push(to);
    } catch (e) {
      console.log(`forward to ${to} failed: ${e && e.message}`); // never the content
    }
  }
  if (!answer && forwarded.length === 0) {
    message.setReject("Mailbox temporarily unavailable, please try again later.");
  }
  return { stored: !!(answer && answer.stored), forwarded };
}

export default {
  async email(message, env) {
    await handle(message, env);
  },
};
