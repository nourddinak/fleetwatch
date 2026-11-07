# Fleetwatch Email Worker

This Worker receives your domains' mail from Cloudflare Email Routing. It posts each message to Fleetwatch, signed, and copies it to Gmail. When the dashboard is down it copies the message to Gmail by itself, and if there is nowhere to copy it, it refuses the message with "try again later". See [docs/emails.md](../../docs/emails.md) for the whole picture.

Normally you never deploy this by hand: **Emails → Settings → Cloudflare → Connect** uploads `src/worker.js` with its bindings, and redeploys it whenever forwarding changes.

## Deploying it by hand

You need Node.js 20+ and Wrangler (`npm i -g wrangler`, then `wrangler login`).

1. `cp wrangler.toml.example wrangler.toml` and set `FLEETWATCH_URL` to `https://<your dashboard>/api/emails/inbound`.
2. Emails → Settings → Manual setup → **Show the Worker's secret**, then run:
   ```bash
   wrangler secret put INBOUND_SECRET
   ```
3. Set where Gmail copies go while the dashboard is down:
   ```bash
   wrangler secret put ROUTES
   ```
   Paste, for example: `{"v":1,"d":{"example.com":["you@gmail.com"]},"a":{"noreply@example.com":[]}}`. The dashboard tells the Worker the same thing on every message while it is up, so this list only matters during downtime.
4. Deploy:
   ```bash
   wrangler deploy
   ```
5. In Cloudflare → your domain → Email → Email Routing:
   - turn routing on (it adds the MX records);
   - set the **catch-all** rule to *Send to a Worker* → `fleetwatch-email`;
   - under Destination addresses, add and verify each Gmail address.

## Tests

```bash
npm test
```

The tests use Node's built-in test runner and need no install. They cover storing and forwarding, unknown recipients, the dashboard being down or refusing, Gmail failures, large messages, and the edge routes.

## Notes

- The signature covers the timestamp, recipient, sender and size, not the bytes. Hashing up to 25 MiB would not fit the Free plan's CPU limit, and TLS already protects the body in transit. Fleetwatch rejects signatures older than five minutes.
- The Worker never logs message content, only "forward to X failed".
