#!/usr/bin/env bash
# Fleetwatch agent uninstaller, served by ops-server.
# Usage: curl -fsSL https://ops.example.com/uninstall.sh | sudo bash
# Removes ops-agent, its service, config, credential and state. Your apps, PM2,
# systemd services and logs are not touched.
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "error: run as root (pipe into 'sudo bash')" >&2; exit 1; }

if systemctl list-unit-files ops-agent.service >/dev/null 2>&1; then
  systemctl disable --now ops-agent >/dev/null 2>&1 || true
fi
rm -f /etc/systemd/system/ops-agent.service
systemctl daemon-reload
rm -f /usr/local/bin/ops-agent
rm -rf /etc/ops-agent /var/lib/ops-agent

echo "ops-agent is uninstalled."
echo "In the dashboard, delete this server (Server -> Settings -> Delete server) to remove its history."
