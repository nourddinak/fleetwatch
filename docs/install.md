# Install

You need:

- one host for the central server (any small Linux VPS works);
- a domain name pointing at it, for example `ops.example.com`;
- [Caddy](https://caddyserver.com/docs/install) on the same host, for TLS. The installer sets it up for you.

The server and the agents run on Ubuntu 22.04+ or Debian 12+ with systemd, on amd64 or arm64.

## 1. Central server

### Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/nourddinak/fleetwatch/main/scripts/install-server.sh | sudo bash -s -- --domain ops.example.com
```

The installer:

1. downloads the latest release from GitHub and checks it against `SHA256SUMS`;
2. installs `/usr/local/bin/ops-server`;
3. creates the `ops` system user, `/var/lib/ops-server` and `/etc/ops-server.env`;
4. sets up the systemd unit shown below, plus `ops-server-update.path` and `ops-server-update.service` for one-click updates from the dashboard;
5. installs Caddy from its official apt repository if it is missing;
6. writes the Fleetwatch site to its own file, `/etc/caddy/fleetwatch.caddy` (from [`deploy/fleetwatch.caddy`](../deploy/fleetwatch.caddy)), and adds a single `import /etc/caddy/fleetwatch.caddy` line to `/etc/caddy/Caddyfile`. Your other sites in the Caddyfile are left alone. If the Caddyfile only holds Caddy's placeholder site, the placeholder is replaced.

At the end it prints a one-time **setup code**. Open your domain, enter the code, and create the admin account in the setup wizard. If the code was not printed, find it with `journalctl -u ops-server | grep 'setup code'`. The code is printed again on every restart until the admin exists.

| Option | Effect |
|---|---|
| `--domain NAME` | Public domain for HTTPS through Caddy. |
| `--no-caddy` | Skip Caddy and use your own reverse proxy. It must run on the same host (see below). |
| `--version vX.Y.Z` | Install a specific release instead of the latest. |
| `--uninstall` | Stop and remove the server, `/etc/caddy/fleetwatch.caddy` and its import line. The database and `/etc/ops-server.env` are kept. |
| `--uninstall --purge` | Also delete the database, secret key, config and the `ops` user. |

To upgrade, re-run the install command. Data and settings are kept.

### Manual install

Copy the binary for your architecture (`bin/ops-server-linux-amd64` or `-arm64`) to the server:

```bash
sudo install -m 0755 ops-server-linux-amd64 /usr/local/bin/ops-server
sudo useradd --system --home /var/lib/ops-server --shell /usr/sbin/nologin ops
sudo install -d -o ops -g ops -m 0700 /var/lib/ops-server
```

Write `/etc/ops-server.env` (mode 0600, owned by root):

```bash
OPS_DATA_DIR=/var/lib/ops-server
OPS_LISTEN=127.0.0.1:8090
OPS_PUBLIC_URL=https://ops.example.com
```

Create `/etc/systemd/system/ops-server.service`:

```ini
[Unit]
Description=Fleetwatch central server
After=network-online.target
Wants=network-online.target

[Service]
User=ops
Group=ops
EnvironmentFile=/etc/ops-server.env
ExecStart=/usr/local/bin/ops-server serve
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/ops-server

[Install]
WantedBy=multi-user.target
```

Then start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ops-server
journalctl -u ops-server -f
```

On first start, the log shows a one-time setup code. Open the dashboard and the setup wizard asks for it.

For automated deployments, you can create the admin from the environment instead:

1. Run `ops-server hash-password`. It asks for the password twice and prints an argon2id hash.
2. Add `ADMIN_USERNAME=admin` and `ADMIN_PASSWORD_HASH='<hash>'` to the env file. Use single quotes, because the hash contains `$`.

These two variables are read on first start only. To change the password later, use the dashboard (Settings → Account) or `ops-server reset-admin` (see [Operations](operations.md#locked-out)).

### Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `OPS_LISTEN` | `127.0.0.1:8090` | Listen address. Keep it on loopback and let Caddy face the internet. |
| `OPS_DATA_DIR` | `./data` | Holds `ops.db` (SQLite, WAL mode) and `secret.key`. |
| `OPS_SECRET_KEY` | — | Optional 32-byte key, base64 (`openssl rand -base64 32`). It encrypts the TOTP secret and notification secrets. If it is not set, the server generates `OPS_DATA_DIR/secret.key` (mode 0600) on first start. |
| `OPS_PUBLIC_URL` | taken from the request | The URL that agents and notification links use. Set it. |
| `OPS_UPDATE_SOURCE` | `https://github.com/nourddinak/fleetwatch/releases` | Where one-click updates come from. Point it at your fork's releases, or set it to `off` to disable update checks. |
| `ADMIN_USERNAME` | — | Optional. Admin username, read on first start only. Without it, the setup wizard creates the admin. |
| `ADMIN_PASSWORD_HASH` | — | Optional. Output of `ops-server hash-password`, read on first start only. |

**Keep the secret key.** If you lose it, 2FA and the saved notification credentials can no longer be decrypted. You would then need `reset-admin` and would have to re-enter the provider secrets.

## 2. Caddy

The installer handles this. To do it by hand, write the site file, which is the same file as [`deploy/fleetwatch.caddy`](../deploy/fleetwatch.caddy) in this repo:

```bash
sudo sh -c 'ops-server caddy-config ops.example.com > /etc/caddy/fleetwatch.caddy'
```

That writes:

```caddyfile
ops.example.com {
	encode zstd gzip
	header Strict-Transport-Security "max-age=31536000"
	reverse_proxy 127.0.0.1:8090
}
```

Add one line to `/etc/caddy/Caddyfile`:

```caddyfile
import /etc/caddy/fleetwatch.caddy
```

Then reload Caddy with `sudo systemctl reload caddy`.

- **Proxied traffic:** Caddy obtains the certificate and proxies both the dashboard and the agent WebSocket (`/agent/ws`); nothing else is needed.
- **Why Caddy must be on the same host:** `ops-server` only trusts `X-Forwarded-For` and `X-Forwarded-Proto` from a loopback peer. That is how it learns the client IP (for lockout and the audit log) and knows the connection is HTTPS (for Secure cookies).
- **Restricting access by IP:** do it in Caddy with a `remote_ip` matcher.
- **Cloudflare in front (orange cloud):** this works without changes. When a request reaches the server from one of [Cloudflare's networks](https://www.cloudflare.com/ips/), the real address is taken from Cloudflare's `CF-Connecting-IP` header. That address is what the dashboard shows for each server and uses for login lockout and the audit log. The header is ignored on requests that do not come from Cloudflare, so it cannot be faked by connecting to the server directly. Use SSL mode "Full (strict)" so Cloudflare talks to Caddy over HTTPS.

Open `https://ops.example.com` and sign in. Enable 2FA under Settings → 2FA.

## 3. Pair a VPS

1. In the dashboard, go to Servers → Add server. This creates a one-time token that is valid for one hour.
2. Run the command it shows on the VPS:

   ```bash
   curl -fsSL https://ops.example.com/install.sh | sudo bash -s -- <token>
   ```

The installer:

1. downloads the agent from your central server and checks its SHA-256;
2. installs it to `/usr/local/bin/ops-agent`;
3. pairs with the server, saving a permanent credential to `/etc/ops-agent/config.json` (mode 0600);
4. starts `ops-agent.service`.

The server appears as connected within a few seconds. Discovered PM2 and systemd apps show up under Discoveries.

### Remove an agent

Run this on the VPS (it is also shown under Server → Settings in the dashboard):

```bash
curl -fsSL https://ops.example.com/uninstall.sh | sudo bash
```

It stops `ops-agent` and removes its binary, systemd unit, `/etc/ops-agent` and `/var/lib/ops-agent`. Your apps, PM2, services and logs are not touched. Then delete the server in the dashboard to remove its history.

### Agent configuration

`/etc/ops-agent/config.json`:

| Key | Meaning |
|---|---|
| `serverUrl` | Central server URL. It must be `https`; plain `http` is accepted only for localhost. |
| `serverId`, `credential` | Written during pairing. The server stores only a hash of the credential. |
| `allowCustomCommands` | `true` by default. Set it to `false` to make this VPS refuse admin-defined commands, whatever the central server asks; start/stop/restart/reload still work. Restart the agent after changing it. Re-pairing keeps your value. |

Other paths:

- Agent state (log offsets, journald cursor): `/var/lib/ops-agent/state.json`.
- Agent logs: `journalctl -u ops-agent`.

The agent runs as root. It needs that to read every user's PM2, journald and `/proc`, and to manage units. Custom commands run as the app's own user, inside the app's directory.

### PM2 notes

- **How PM2 is found:** the agent looks for PM2 in `/root/.pm2` and `/home/*/.pm2`, and runs `pm2` as the owner of each home. It also looks for the `pm2` binary in common nvm and npm-global locations.
- **Checking what is found:** run `sudo ops-agent inventory`.
