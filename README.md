# Fleetwatch

Self-hosted monitoring and control for the apps on your own VPSs. One dashboard for PM2 and systemd apps across all your servers: live logs, metrics, health checks, incidents with alerts, and safe one-click actions.

- **One central server** (`ops-server`, a single Go binary with SQLite and the dashboard built in).
- **A small agent per VPS** (`ops-agent`, a systemd service) that connects out over WSS. No inbound ports on your VPSs.
- **Finds your apps by itself:** PM2 processes, systemd services, static sites served by Caddy, project folders, ports, git info and domains.
- **Logs:** live tail plus 7 days of full-text search.
- **Metrics:** CPU, RAM, disk and network per server and per app.
- **Health checks and incidents:** HTTP health checks, and incidents opened automatically for downtime, crash loops, failing checks, high resource use and repeated errors. They resolve themselves when the problem clears.
- **Alerts** as push notifications to your phone or desktop (installable app), or through ntfy, Telegram, Discord or a custom webhook (signed JSON).
- **Domains:** SSL certificate (public and on the server) and domain registration expiry, with alerts 14 days ahead.
- **API tokens and Prometheus:** read-only tokens for scripts and Grafana, `/metrics` for Prometheus.
- **Disk rescue and forecasts:** see what fills a disk and clean the safe parts (journal, old logs, package and Docker caches) with one click; get warned days before a disk is full or when an app's memory keeps growing.
- **Security:** twice-daily checks for SSH password login, waiting security updates, failed login bursts, ports open to the internet, readable .env files and sites leaking .git or .env, each with a fix; SSH logins with their country, and alerts for new countries and keys.
- **Team and on-call:** operators and viewers with their own sign-in, optionally limited to some servers; a weekly on-call rotation with overrides that pages the person on call (push and email) and escalates when nobody acknowledges.
- **Maintenance mode:** pause alerts for a server or app while you deploy; a summary is sent if something is still broken afterwards.
- **Public status pages:** pages your users can check when something is down: one for everything, one per app or customer, or both (`/status`, `/status/<name>`, or their own domains), each with your logo, colours, texts, chosen apps and servers, 30-90 days of uptime history and response times, notices with update timelines (also opened automatically for outages and maintenance), email and webhook subscribers, password or team-only access, a status badge, monthly uptime reports and custom CSS/HTML.
- **Safe actions:** start, stop, restart and reload, plus commands you define per app. There is no browser shell.
- **Security:** argon2id passwords, TOTP 2FA, an audit log and encrypted secrets.

## Quick start

**1. Install the central server** on a small Linux VPS (Ubuntu or Debian). Point a domain at it first:

```bash
curl -fsSL https://raw.githubusercontent.com/nourddinak/fleetwatch/main/scripts/install-server.sh | sudo bash -s -- --domain ops.example.com
```

The installer:

1. downloads the latest release and checks its SHA-256;
2. sets up a systemd service;
3. installs Caddy for HTTPS, putting the site in its own file (`/etc/caddy/fleetwatch.caddy`, from [`deploy/`](deploy/fleetwatch.caddy)) and adding only an `import` line to your Caddyfile;
4. prints a one-time setup code.

**2. Open `https://ops.example.com`.** Enter the setup code and create your admin account in the setup wizard.

**3. Add your VPSs.** Go to Servers → Add server. Copy the command it shows and run it on each VPS:

```bash
curl -fsSL https://ops.example.com/install.sh | sudo bash -s -- <one-time-token>
```

Your apps show up under Discoveries a few seconds later.

### Upgrade

- **Server:** when a new release is out, the dashboard shows a banner. Click **Update now**: the server installs the checksum-verified release, restarts, and the page reloads by itself. Installs from before v0.9.3 need the install command re-run once to enable this. Re-running it also upgrades by hand, and data and settings are kept.
- **Agents:** Settings → Agents → **Update agents**, or **Update agent** on one server's Agent tab.

### Uninstall

- **Agent**, on a VPS. This is also shown under Server → Settings:

  ```bash
  curl -fsSL https://ops.example.com/uninstall.sh | sudo bash
  ```

- **Central server.** Add `--purge` to also delete its data:

  ```bash
  curl -fsSL https://raw.githubusercontent.com/nourddinak/fleetwatch/main/scripts/install-server.sh | sudo bash -s -- --uninstall
  ```

## Documentation

- [Install](docs/install.md): installer options, manual install, Caddy, environment variables, agent configuration.
- [Operations](docs/operations.md): backups, upgrades, the security model, troubleshooting.

## Building from source

You need Go 1.26+ and Node.js 20+.

```powershell
./scripts/build.ps1 -Version 1.0.0            # pwsh works on Linux and macOS too
```

The script builds:

1. the UI (`web/dist`);
2. the Linux agents (amd64 and arm64), which are embedded into the server;
3. `ops-server` for this machine and for Linux amd64 and arm64, into `bin/`.

`-Release` also writes `bin/SHA256SUMS`. Pushing a `v*` tag builds a GitHub release with GitHub Actions.

For development:

```powershell
cd web; npm ci; npm run build; cd ..
go test ./...
cd web; npm run dev          # UI on Vite, proxies /api and /ws to an ops-server on :8090
```

| Path | What |
|---|---|
| `cmd/server`, `cmd/agent` | entry points |
| `internal/server/…` | API, auth, fleet hub, apps, logs, metrics, commands, incidents, notifications |
| `internal/agent/…` | discovery, process managers, log tailing, commands, health checks, storage, self-update |
| `internal/proto` | the agent ↔ server protocol (JSON envelopes over WebSocket) |
| `web/` | React dashboard, embedded into `ops-server` |
| `scripts/` | build script and the server installer |
| `deploy/` | the Caddy site file the installer uses, embedded into `ops-server` |

## Contributing

Issues and pull requests are welcome: fork the repository, make your change on a branch and open a pull request. Every change is reviewed before it is merged; see [CONTRIBUTING.md](CONTRIBUTING.md).

**Security issues:** report them privately through GitHub's "Report a vulnerability" button on this repository, not in public issues.

## License

[PolyForm Internal Use License 1.0.0](LICENSE)
