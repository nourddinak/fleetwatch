# Operations

The examples assume the layout from [Install](install.md): the data directory is `/var/lib/ops-server` and the server runs as the `ops` user.

## Backups

Everything lives in two files: `ops.db` and the secret key. You can take a backup while the server is running:

```bash
sudo -u ops OPS_DATA_DIR=/var/lib/ops-server ops-server backup /var/lib/ops-server/backup-$(date +%F).db
```

This writes a consistent copy with SQLite's `VACUUM INTO`, mode 0600. It refuses to overwrite an existing file. Copy that file off the host, together with `secret.key`, or keep your `OPS_SECRET_KEY` somewhere safe.

Never copy `ops.db` alone while the server runs. In WAL mode, recent writes live in `ops.db-wal`.

To restore:

```bash
sudo systemctl stop ops-server
sudo -u ops cp backup-2026-10-15.db /var/lib/ops-server/ops.db
sudo -u ops rm -f /var/lib/ops-server/ops.db-wal /var/lib/ops-server/ops.db-shm
sudo systemctl start ops-server
```

Agents keep their credentials, so they reconnect on their own. Logs are buffered on each agent while the server is down; the buffer holds up to 50,000 lines or 32 MB per VPS.

**What is kept, and for how long:**

| Data | Retention |
|---|---|
| Logs | 7 days (Settings → Logs: 1–30 days) |
| Metrics | 1-minute data for 48 h, 5-minute data for 8 days, hourly data for 90 days (Settings → Metrics) |
| Incidents | 90 days after they resolve |
| Notifications history | 30 days |

## Upgrades

**Central server, one click:** the server checks GitHub for a newer release every 15 minutes, and again whenever someone opens the dashboard if the last check is more than 2 minutes old. Settings → System → **Check for updates** checks immediately.

A check is a single request to the release page's `latest` link, not to GitHub's rate-limited API. When it finds a new release, every open dashboard shows the banner right away over its live connection, with no reload needed. Only finished releases (pushed `v*` tags that GitHub Actions has built) are offered, never plain commits to `main`.

**Update now** then:

1. writes the wanted version to `update-request` in the data directory;
2. `ops-server-update.path` sees the file and starts `ops-server-update.service` as root;
3. that service downloads the release, checks it against the release's `SHA256SUMS`, swaps `/usr/local/bin/ops-server` and restarts the server;
4. the page reloads once the new version answers.

If anything fails, nothing is replaced and the error shows under Settings → System.

**Agents too, in the same click:** the update dialog has "Also update the agents", ticked by default.
- **How it works:** the dashboard leaves a note before restarting. When the new version starts, it asks every agent that connects on an older version to install the matching agent, once each. Agents normally reconnect within seconds, and offline agents are covered for the next hour.
- **Progress:** a line at the top of the dashboard shows it ("Updating agents: 2 of 3 done").
- **After the hour:** agents that come back later can be updated from Settings → Agents → **Update agents**. The helper's log is in `journalctl -u ops-server-update`.

- **Why the root helper:** the dashboard runs as the unprivileged `ops` user and cannot replace its own binary. Through the helper it can only ask for an official release that is newer than the running one and passes the checksum.
- **Older installs:** installs from before v0.9.3 lack the two helper units; re-run the install command once.
- **Turning it off:** set `OPS_UPDATE_SOURCE=off` to stop the checks.

**Central server, by hand:** take a backup first. Then re-run the [install command](install.md#quick-install), or replace `/usr/local/bin/ops-server` yourself and run `sudo systemctl restart ops-server`. Database migrations run automatically on start.

**Agents:** each `ops-server` build carries the matching agent binaries.

- After you upgrade the server, every server whose agent is older shows **Update agent** on its Agent tab. Settings → Agents → **Update agents** updates them all at once.
- Clicking it sends the expected SHA-256 over the authenticated agent connection. The agent then:
  1. downloads the new binary from your central server;
  2. checks the SHA-256;
  3. swaps the file atomically;
  4. restarts through systemd.
- If the download or the check fails, nothing is replaced and the error appears in the server's events.
- You can also update by running a new install command on the VPS. That re-pairs the agent.

## Security model

- **Network:** only Caddy listens publicly. Agents connect out over WSS, so no inbound port is needed on monitored VPSs.
- **Admin login:**
  - Passwords use argon2id. Sessions are random 32-byte cookies; only their SHA-256 is stored.
  - Cookies are HttpOnly, Secure and SameSite=Strict, with idle and absolute expiry.
  - Every state-changing request needs a matching `Origin`.
  - After 5 failed logins (configurable to 3–20), an IP is locked out for 15 minutes.
  - Optional TOTP 2FA with recovery codes.
- **Secrets:** the TOTP secret and notification credentials are encrypted with AES-256-GCM. The API never returns them.
- **Agent credentials:**
  - Pairing tokens are single-use and expire after one hour.
  - The permanent agent credential is stored hashed. Revoking it in the dashboard disconnects the agent immediately.
  - Everything an agent sends (logs, health, command output) is accepted only for that agent's own server and apps.
- **Commands:**
  - The browser can only ask to run a command by its ID. Command text comes from definitions that need your password to create or change.
  - Built-in actions go through PM2 or systemd.
  - Custom commands run as the app's owner, confined to the app directory, with a timeout.
  - Set `allowCustomCommands: false` on a VPS to refuse them there entirely.
  - There is no browser shell and no file manager.
- **Audit log:** logins, 2FA changes, pairing and revoking, command runs and settings changes are recorded under Settings → Security.
- **Main risk:** whoever controls the central server can restart apps on every VPS and run the custom commands you defined. Protect it with 2FA and a strong password. Consider a Caddy IP allowlist.

## Troubleshooting

### Locked out

**Too many failed attempts:** wait 15 minutes, or sign in from another IP.

**Lost the password or the 2FA device:**

```bash
sudo -u ops OPS_DATA_DIR=/var/lib/ops-server ops-server reset-admin
```

This sets a new password, turns 2FA off and signs out every session. If you use `OPS_SECRET_KEY`, pass it as well.

**TOTP codes are always rejected:** check the clock on the server and on the phone (`timedatectl`).

### An agent shows as disconnected

On the VPS, check the agent's log:

```bash
systemctl status ops-agent
journalctl -u ops-agent -n 50
```

Look for these messages:

- **`server rejected the agent credential`:** the agent was revoked or re-paired elsewhere. Create a new install command in the dashboard and run it.
- **TLS or DNS errors:** check that `serverUrl` in `/etc/ops-agent/config.json` resolves from the VPS and that Caddy is up.
- **Plain `http` is refused** for anything but localhost.

The dashboard opens an "Agent Offline" incident once an agent has been away for longer than the grace period (60 s by default, under Settings → Incident rules).

### An app is not discovered

- Run `sudo ops-agent inventory` on the VPS. It prints what the agent sees, plus any probe errors (for example, `pm2 not runnable`).
- **systemd services** count as apps only when:
  - their unit file is in `/etc/systemd/system`; and
  - they have a working directory, a non-root user, or a binary outside the system paths.
- **Project folders** are found one level below the discovery roots (Settings → System; default `/var/www`).
- If a discovery was ignored, it stays ignored until its process name or path changes.
- **Static sites** are recognized when Caddy serves their folder (`root` + `file_server`, also through `import`ed files). Their status comes from Caddy's admin API (`localhost:2019`, on by default) or, if that is turned off, from a health check. Turn on a health check for `https://your-domain/` to get alerts when a static site goes down.

### Logs are missing

- **PM2:** the agent tails the out and error files that PM2 reports. Check the paths with `pm2 describe <name>`.
- **systemd:** logs come from journald (`journalctl -u <unit>`).
- **Log collection is per app:** check the app's Settings tab.
- **Dropped lines:** lines longer than the maximum line size (16 KB by default) are cut. During long outages, the agent drops its oldest buffered lines and records a marker.

### Domains and certificates

The **Domains** page lists every site the dashboard knows: Caddy's sites on each server, domains set on apps, and the dashboard's own domain. For each it shows:

| Column | Where it comes from | How often |
|---|---|---|
| **Certificate** | what visitors get: the dashboard connects like a browser. Behind Cloudflare, this is Cloudflare's certificate. | every 6 h |
| **On the server** | what your server's own web server (Caddy) presents, checked by the agent on that server. Behind a CDN, this one can expire unnoticed and break "Full (strict)" mode. | every 6 h |
| **Registration** | the registry's own RDAP service (the official successor to WHOIS), found through IANA's directory. | daily |

- **Alerts:** an alert opens 14 days before a certificate or a domain expires, and becomes critical at 3 days, or immediately if the certificate is expired, invalid or for another name. It resolves itself once renewed. Change the lead times in Settings → Incident rules.
- **What is skipped:**
  - names under shared domains from free DNS providers (for example `*.dpdns.org`) have no registration of your own to check;
  - some registries offer no RDAP, and the page says so;
  - a site that cannot be reached is shown as such but does not open an incident.
- **What leaves the dashboard:** registration lookups send the domain name to its registry. The certificate check connects to the site like any visitor.

### API tokens and Prometheus

Settings → **API tokens** creates read-only tokens for scripts, Grafana or Prometheus. Creating one needs your password. The token (`fwt_…`) is shown once; only a hash is stored. Each token shows when and from where it was last used, and revoking one stops it immediately. Creating and revoking tokens are recorded in the audit log.

- **What a token can do:** read every `GET /api/...` endpoint the dashboard uses.
- **What it cannot do:** change anything, run commands, or read sessions, the audit log, the token list or the live WebSocket.
- **Usage:** send it as `Authorization: Bearer fwt_…`:

```bash
curl -H "Authorization: Bearer fwt_…" https://ops.example.com/api/servers
```

`GET /metrics` serves the Prometheus text format:

```yaml
scrape_configs:
  - job_name: fleetwatch
    scheme: https
    metrics_path: /metrics
    authorization:
      credentials: fwt_…
    static_configs:
      - targets: ["ops.example.com"]
```

| Metric | Labels | Meaning |
|---|---|---|
| `fleetwatch_server_up` | server, name | 1 while the agent is connected |
| `fleetwatch_server_cpu_percent`, `_memory_percent`, `_disk_percent`, `_load1`, `_network_receive_mbps`, `_network_transmit_mbps`, `_uptime_seconds` | server, name | latest sample |
| `fleetwatch_app_up` | app, name, server | 1 while the app is online |
| `fleetwatch_app_cpu_percent`, `fleetwatch_app_memory_bytes`, `fleetwatch_app_restarts_total` | app, name, server | process tree usage, restarts |
| `fleetwatch_app_health_up` | app, name, server | 1 while the health check passes (only with a health check) |
| `fleetwatch_incidents_open` | severity | open incidents |
| `fleetwatch_certificate_expiry_timestamp_seconds` | domain, where (`public`/`origin`) | certificate expiry |
| `fleetwatch_domain_expiry_timestamp_seconds` | domain | registration expiry |
| `fleetwatch_info` | version | always 1 |

### Maintenance mode

Before a deploy or a reboot, open the server or app and choose **Maintenance…**. You can pause alerts for 30 minutes, 1, 4 or 24 hours, or until you end it.

- **During maintenance:** incidents are still recorded, so the history stays complete, but no alerts are sent. Maintenance on a server covers all of its apps. A **Maintenance** badge shows in the lists.
- **When it ends**, on time or with **End now**, one summary alert is sent if anything is still broken, for example "Maintenance on api ended: 2 problem(s) still open". With a webhook, its `event` is `maintenance.ended`.
- **After that**, alerts work normally again.

### Push notifications (phone and desktop)

The dashboard can send alerts straight to your devices, with no ntfy, Telegram or Discord needed.

1. Open the dashboard on the device, on its `https://` domain. Push needs HTTPS.
2. Settings → Notifications → **Push notifications** → **Turn on for this device**, and allow notifications.
3. **Send test**.

On each platform:

- **Android and desktop (Chrome, Edge, Firefox):** works in the browser. Installing the dashboard as an app (browser menu → **Install**) gives it its own window and icon.
- **iPhone and iPad (iOS 16.4+):** works only in the installed app. Tap Share → **Add to Home Screen**, open Fleetwatch from the home screen, then turn push on there.

How it behaves:

- **Rules:** push follows the same alert rules, cooldown and maintenance windows as the other channels.
- **Critical alerts** stay on screen until dismissed. A newer alert for the same incident replaces the older one.
- **Tapping** an alert opens the Incidents page.
- **Devices:** every device is listed with its last delivery. Remove one there, or with **Turn off on this device**. Devices that uninstall the app or revoke permission are removed automatically.
- **Signing keys:** the dashboard creates its push signing key (VAPID) on first use and stores the private part encrypted. Messages are encrypted end to end to each device; the browser's push service (Google, Mozilla, Apple) only relays them.

### Custom webhooks

Settings → Notifications → **Custom webhook** sends every alert as a JSON `POST` to your URL, for n8n, Zapier, Slack workflows, Home Assistant or your own code. The URL and the signing secret are stored encrypted and never shown again.

The body looks like this:

```json
{
  "event": "incident.opened",
  "type": "app_offline",
  "typeLabel": "App Offline",
  "incidentId": "inc_8f2c1a",
  "serverId": "srv_466816c1b51c",
  "serverName": "VPS-1",
  "appId": "app_5149c42f7cd5",
  "appName": "api",
  "severity": "critical",
  "title": "api is offline",
  "message": "App Offline · CRITICAL · api on VPS-1",
  "link": "https://ops.example.com/#/incidents",
  "ts": 1792090000000
}
```

- **`event`** is `incident.opened`, `incident.resolved` or `test`. It is also sent in the `X-Fleetwatch-Event` header.
- **`severity`** is `critical`, `warning` or `info`, and `ok` for resolutions.
- **Fields that don't apply are left out**, for example `appId` on server incidents.
- **Signing secret:** if you set one, each request carries `X-Fleetwatch-Signature: sha256=<hex>`. That value is the HMAC-SHA256 of the raw request body, keyed by the secret. Check it before trusting the request. In Node.js:

```js
const crypto = require("crypto");
const expected = "sha256=" + crypto.createHmac("sha256", process.env.SECRET).update(rawBody).digest("hex");
const got = Buffer.from(req.headers["x-fleetwatch-signature"] ?? "");
const want = Buffer.from(expected);
const ok = got.length === want.length && crypto.timingSafeEqual(got, want); // timingSafeEqual throws on unequal lengths
```

The receiver should answer with a 2xx status; anything else counts as a failed delivery and is retried once.

### Notifications don't arrive

- Use **Send test** under Settings → Notifications. The exact provider error appears there and in the Notifications history.
- **Cooldown:** each incident sends at most one "opened" and one "resolved" message per 10 minutes.
- **Muted incidents:** they don't notify.
- **Overrides:** a server's notification override (Server → Settings) or an app's Notifications switch can turn alerts off.
- **Telegram:** the bot must have received a message from you (or be in the group) before it can write to the chat.

### Disk use

Settings → Logs and Settings → Metrics show the estimated storage. SQLite reuses freed pages, so the file does not shrink after pruning. Run `ops-server backup`, then restore the copy, to compact it.
