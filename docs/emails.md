# Emails

The Emails page gives you mailboxes on your own domains (`contact@yourshop.com`, `support@…`, `noreply@…`). You can receive, read, search, organise, reply, forward and send mail there, and a copy of each message can go to Gmail.

## How mail flows

```
sender ──► Cloudflare Email Routing (one catch-all rule per domain)
              │
              ▼
        Email Worker  ──(signed POST, raw message)──►  Fleetwatch  ──► encrypted .eml + SQLite
              │                                            │
              │◄────── "copy to these Gmail addresses" ────┘
              ▼
        message.forward() to Gmail

you ──► Composer ──► outbox (undo / schedule / retries) ──► Integrations → Email sender (Resend or SMTP)
```

- **Receiving.** Each domain has one catch-all rule that sends every address to the Worker. Addresses exist only in Fleetwatch, so adding one never touches Cloudflare. The Worker posts the raw message to `/api/emails/inbound`. The request is signed with HMAC-SHA256 over a timestamp, the recipient, the sender and the size. The Worker then copies the message to the Gmail addresses Fleetwatch returns.
- **Dashboard down.** If the dashboard does not answer, the Worker copies the message to Gmail anyway, using the forwarding list Fleetwatch last deployed with it (the `ROUTES` secret). If there is nowhere to copy it, the Worker refuses the message with "try again later", so the sending server retries or bounces it. Nothing is dropped silently. Messages that arrive during the outage are in Gmail, not in Fleetwatch: the Free plan has no queue that could hold them for the dashboard.
- **Sending.** Mail goes out through the senders in **Integrations → Email**. A Resend sender for your domain lets any address on that domain send. Any other SMTP provider also works. Each domain picks a sender, or uses the one whose From address is on the domain, or the default.
- **The outbox.** A sent message waits through the undo delay (10 seconds by default), or until its scheduled time. Temporary errors are retried with backoff, up to 8 attempts. A permanent refusal (5xx) fails the message. If the connection breaks while the provider is taking the message, the message is marked **unknown** and is never sent again by itself. "Accepted" means the provider took it. It does not mean it reached the inbox.

## Storage and security

- Raw messages are kept exactly as received, as files under `$OPS_DATA_DIR/mail`. They are encrypted with AES-256-GCM using the dashboard's secret key (`$OPS_DATA_DIR/secret.key` or `OPS_SECRET_KEY`). SQLite holds the metadata, the threads and the full-text search index (FTS5). The index stores message text in plain form.
- HTML mail is cleaned against an allowlist of tags and shown in a sandboxed iframe with no scripts and a strict CSP. Remote images are held back until you click **Load images**.
- Attachments are always downloaded, never opened in the dashboard. Program and script types (`.exe`, `.js`, `.scr`…) are flagged. There is **no virus scanning yet**: every attachment shows "not scanned".
- Spam: the score uses Cloudflare's SPF, DKIM and DMARC results (missing results count far less than failed ones), spoofing of your own domains, mismatched or bare-IP links and phishing wording. Messages above the domain's threshold go to Spam and are not copied to Gmail. Nothing is ever deleted automatically. **Not spam** moves the message back and trusts the sender for that inbox.
- Permissions: mail is the owner's alone. The owner can give **operators** read, read + send, or manage access to a whole domain or to single addresses (Emails → Settings → Team access). The server checks access on every request. Viewers and clients never see mail. New-mail alerts are sent through your notification channels, but they name only the address and the sender, because the notification history is visible to the whole team.
- Retention: Trash and Spam can be emptied after N days (off by default). Nothing else is ever deleted automatically. A domain or address that still has mail cannot be removed.

## Setting it up

You need:

- your domains on Cloudflare (any plan, the Free plan included);
- the dashboard reachable at a public **https** address (set `OPS_PUBLIC_URL`, or open the dashboard at that address when connecting);
- a sender in **Integrations → Email** to send from. Resend's free tier sends 100 emails a day, so heavy sending needs a paid plan with Resend or another provider.

1. **Connect Cloudflare through the dashboard's sign-in.** Open Emails → Settings → Cloudflare and follow the steps it shows. Nothing needs to be pasted:
   - **Sign in with Cloudflare** must be set up once in Integrations → Cloudflare. A sign-in app made there from now on already includes the email permissions.
   - A sign-in app made before email existed needs three permissions added in Cloudflare → Manage account → OAuth clients → Fleetwatch → edit scopes: `email-routing-rule.write`, `email-routing-address.write` and `workers-scripts.write`. Then press **I added them: sign in with Cloudflare**.
   - Sign in with Cloudflare. You come back to the Emails settings, and email turns on by itself: Fleetwatch deploys the Worker (`fleetwatch-email`) with its secret and routes.

   If something is missing, the page shows Cloudflare's own error message. If the sign-in expires later, the page offers **Sign in again**.
2. Alternatively, use an API token instead (under **Or use an API token instead**), created with these permissions: Account · Workers Scripts · Edit; Account · Email Routing Addresses · Edit; Zone · Email Routing Rules · Edit; Zone · DNS · Edit; Zone · Zone · Read; Zone · Zone Settings · Edit.
3. **Emails → Domains & addresses → Add domain:** pick the domain.
4. **Set up in Cloudflare** (on the domain). This:
   - merges `include:_spf.mx.cloudflare.net` into your SPF record (it never adds a second SPF record);
   - turns on Email Routing, which adds the MX records;
   - points the catch-all rule at the Worker;
   - adds `v=DMARC1; p=none` if you have no DMARC record.

   Then click **Check** to see the records as the rest of the internet sees them. DNS can take a few minutes to update.
5. **Create addresses.** Each address can have its own inbox, or be an alias that delivers into another address's inbox. On each you can turn receiving and sending on or off, set Gmail copies (like the domain, its own list, or none), alerts and a signature. Turn on **catch-all** in the domain's settings to accept mail for addresses you have not created.
6. **Gmail copies.** Add the Gmail addresses in the domain's or the address's settings. Cloudflare emails each new destination a confirmation link, and it forwards nothing there until the link is clicked. Emails → Settings shows which destinations are verified.
7. **Sending.** In Integrations → Email, add a Resend sender for the domain (Fleetwatch can create its DNS records) or any SMTP sender. Then pick it in the domain's settings. The domain's DKIM check reports Resend's verification.

### Deploying the Worker by hand (optional)

Instead of steps 1–2, see [`cloudflare/email-worker/README.md`](../cloudflare/email-worker/README.md). Emails → Settings → Manual setup shows the secret the Worker needs.

## Testing

- **Inbound:** send a message from another account to `contact@yourdomain`. It should appear in the Inbox within seconds, with a copy in Gmail.
- **Outbound:** reply from the dashboard. The Sent folder shows the outbox state (scheduled → accepted, or failed with the provider's error).
- **Dashboard downtime:** stop the server (`systemctl stop ops-server`) and send a message. It should arrive in Gmail but not in Fleetwatch. If the address has no Gmail copy, the sender gets a "try again later" bounce. Start the server again: new mail is filed as usual.
- **Recovery:** Emails → Settings → Recovery lists messages that arrived but could not be filed (kept on disk, with **Try again**). It also lists outgoing mail that failed or whose fate is unknown.

## Backups and export

- Add the database (`$OPS_DATA_DIR/ops.db`, as SQLite) and the folder `$OPS_DATA_DIR/mail` to the **same** backup plan of the dashboard's server, so both are taken together. Restoring both brings back the messages, threads, labels and files together.
- Keep a copy of `secret.key` (or `OPS_SECRET_KEY`) somewhere else. Without it the mail files cannot be read.
- Export: **Download .eml** on any message, or MBOX of everything (Settings → Storage) or of one inbox or folder (`/api/emails/export?mailbox=<address id>&folder=inbox`). MBOX opens in Thunderbird and Apple Mail, and Gmail can import it with tools that read MBOX.

## Limits

These come from Cloudflare and your provider, not from Fleetwatch:

- 25 MiB per message, including the MIME encoding: attachments grow by about a third. Larger messages are refused (bounced), never dropped.
- Cloudflare: 200 verified destination addresses per account, and 200 routing rules per zone. Fleetwatch uses one rule per domain.
- The Worker's forwarding list (`ROUTES`) must fit in 5 KB. If you have too many per-address exceptions, only the domain defaults apply while the dashboard is down, and the Settings page tells you so.
- Workers Free: 100,000 requests a day. Every received message is one request.
- No virus scanning, no Cloudflare Queues/R2 staging (not available on the Free plan), and no read receipts or delivery tracking beyond "accepted by the provider".
