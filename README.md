# Fleetwatch

Self-hosted monitoring and control for the apps on your own VPSs. One dashboard for PM2 and systemd apps across all your servers: live logs, metrics, health checks, incidents with alerts, and safe one-click actions.

- **One central server** (`ops-server`, a single Go binary with SQLite and the dashboard built in).
- **A small agent per VPS** (`ops-agent`, a systemd service) that connects out over WSS. No inbound ports on your VPSs.
- **Finds your apps by itself:** PM2 processes, systemd services, project folders, ports, git info, Caddy domains.
- **Logs:** live tail plus 7 days of full-text search.
- **Metrics:** CPU, RAM, disk and network per server and per app.
- **Health checks and incidents:** HTTP health checks, and incidents opened automatically for downtime, crash loops, failing checks, high resource use and repeated errors. They resolve themselves when the problem clears.
- **Alerts** through ntfy, Telegram or Discord.
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
3. installs Caddy for HTTPS;
4. prints a one-time setup code.

**2. Open `https://ops.example.com`.** Enter the setup code and create your admin account in the setup wizard.

**3. Add your VPSs.** Go to Servers → Add server. Copy the command it shows and run it on each VPS:

```bash
curl -fsSL https://ops.example.com/install.sh | sudo bash -s -- <one-time-token>
```

Your apps show up under Discoveries a few seconds later.

### Upgrade

- **Server:** re-run the install command. Data and settings are kept.
- **Agents:** each server's Agent tab then offers **Update agent**.

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

## Contributing

Issues and pull requests are welcome. Please run `go vet ./...`, `go test ./...` and `npx tsc --noEmit` (in `web/`) before opening a PR.

**Security issues:** report them privately through GitHub's "Report a vulnerability" button on this repository, not in public issues.

## License

[PolyForm Internal Use License 1.0.0](LICENSE)
