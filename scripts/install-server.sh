#!/usr/bin/env bash
# Fleetwatch central server installer for Ubuntu/Debian with systemd.
#
# Install or upgrade (re-run the same command to upgrade; data and settings are kept):
#   curl -fsSL https://raw.githubusercontent.com/nourddinak/fleetwatch/main/scripts/install-server.sh | sudo bash -s -- --domain ops.example.com
#
# Options:
#   --domain NAME    public domain; installs Caddy (if missing) and serves HTTPS on it
#   --no-caddy       do not install or configure Caddy (bring your own reverse proxy)
#   --version vX.Y.Z install that release instead of the latest
#   --uninstall      remove the server; keeps the database unless --purge is given
#   --purge          with --uninstall: also delete the database, secret key and config
#
# FLEETWATCH_DOWNLOAD_URL overrides where binaries come from (mirrors, testing).
set -euo pipefail

REPO="nourddinak/fleetwatch"
DOMAIN="" VERSION="latest" CADDY=1 UNINSTALL=0 PURGE=0
DATA=/var/lib/ops-server ENVFILE=/etc/ops-server.env UNIT=/etc/systemd/system/ops-server.service
BIN=/usr/local/bin/ops-server CADDYFILE=/etc/caddy/Caddyfile

die() { echo "error: $*" >&2; exit 1; }
say() { echo "==> $*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --no-caddy) CADDY=0; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --purge) PURGE=1; shift ;;
    -h | --help) sed -n '2,15p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run as root (pipe into 'sudo bash')"
command -v systemctl >/dev/null || die "systemd is required"

remove_caddy_block() {
  [ -f "$CADDYFILE" ] && grep -q '^# >>> fleetwatch' "$CADDYFILE" || return 0
  sed -i '/^# >>> fleetwatch/,/^# <<< fleetwatch/d' "$CADDYFILE"
  systemctl reload caddy 2>/dev/null || true
}

if [ "$UNINSTALL" -eq 1 ]; then
  say "Stopping ops-server"
  systemctl disable --now ops-server >/dev/null 2>&1 || true
  rm -f "$UNIT" "$BIN"
  systemctl daemon-reload
  remove_caddy_block
  if [ "$PURGE" -eq 1 ]; then
    rm -rf "$DATA" "$ENVFILE"
    id ops >/dev/null 2>&1 && userdel ops
    say "Removed ops-server, its data, config and the ops user."
  else
    say "Removed ops-server. Data kept in $DATA and $ENVFILE (delete with --uninstall --purge)."
  fi
  echo "Agents on your VPSs are separate: remove each with  curl -fsSL https://<your-domain>/uninstall.sh | sudo bash"
  exit 0
fi

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac
[ "$CADDY" -eq 0 ] || [ -n "$DOMAIN" ] || die "pass --domain ops.example.com (or --no-caddy to skip HTTPS setup)"
case "$DOMAIN" in *[!A-Za-z0-9.-]*) die "invalid domain: $DOMAIN" ;; esac
command -v curl >/dev/null || die "curl is required"
command -v sha256sum >/dev/null || die "sha256sum is required"

if [ -n "${FLEETWATCH_DOWNLOAD_URL:-}" ]; then
  BASE="$FLEETWATCH_DOWNLOAD_URL"
elif [ "$VERSION" = latest ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "Downloading ops-server-linux-$ARCH ($VERSION)"
curl -fsSL "$BASE/ops-server-linux-$ARCH" -o "$TMP/ops-server-linux-$ARCH"
curl -fsSL "$BASE/SHA256SUMS" -o "$TMP/SHA256SUMS"
(cd "$TMP" && grep " ops-server-linux-$ARCH\$" SHA256SUMS | sha256sum --check --quiet -) || die "checksum mismatch, download corrupted"

UPGRADE=0
[ -f "$UNIT" ] && UPGRADE=1
install -m 0755 "$TMP/ops-server-linux-$ARCH" "$BIN"
id ops >/dev/null 2>&1 || useradd --system --home "$DATA" --shell /usr/sbin/nologin ops
install -d -o ops -g ops -m 0700 "$DATA"

if [ ! -f "$ENVFILE" ]; then
  URL="http://127.0.0.1:8090"
  [ -n "$DOMAIN" ] && URL="https://$DOMAIN"
  umask 077
  cat > "$ENVFILE" <<EOF
# Fleetwatch server settings. Restart after editing: systemctl restart ops-server
OPS_DATA_DIR=$DATA
OPS_LISTEN=127.0.0.1:8090
OPS_PUBLIC_URL=$URL
EOF
  umask 022
fi

cat > "$UNIT" <<'EOF'
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
EOF
systemctl daemon-reload
systemctl enable ops-server >/dev/null 2>&1
STARTED="$(date '+%Y-%m-%d %H:%M:%S')"
systemctl restart ops-server

if [ "$CADDY" -eq 1 ]; then
  if ! command -v caddy >/dev/null; then
    command -v apt-get >/dev/null || die "install Caddy yourself (https://caddyserver.com/docs/install), then re-run"
    say "Installing Caddy from its official repository"
    apt-get update -qq
    apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gnupg >/dev/null
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq
    apt-get install -y -qq caddy >/dev/null
  fi
  # Replace Caddy's placeholder site; otherwise add (or refresh) our block only.
  if [ -f "$CADDYFILE" ] && grep -q '/usr/share/caddy' "$CADDYFILE" && ! grep -q '^# >>> fleetwatch' "$CADDYFILE"; then
    : > "$CADDYFILE"
  fi
  remove_caddy_block
  cat >> "$CADDYFILE" <<EOF
# >>> fleetwatch (managed by install-server.sh)
$DOMAIN {
	encode zstd gzip
	header Strict-Transport-Security "max-age=31536000"
	reverse_proxy 127.0.0.1:8090
}
# <<< fleetwatch
EOF
  caddy validate --config "$CADDYFILE" --adapter caddyfile >/dev/null 2>&1 || die "Caddy rejected $CADDYFILE; fix it and run: systemctl reload caddy"
  systemctl enable caddy >/dev/null 2>&1
  systemctl reload caddy 2>/dev/null || systemctl restart caddy
fi

URL="$(grep '^OPS_PUBLIC_URL=' "$ENVFILE" | cut -d= -f2-)"
if [ "$UPGRADE" -eq 1 ]; then
  say "Upgraded to $("$BIN" version). Open $URL"
  exit 0
fi

say "Waiting for the first start"
CODE=""
for _ in $(seq 1 30); do
  CODE="$(journalctl -u ops-server --since "$STARTED" --no-pager -o cat 2>/dev/null | grep -o 'setup_code=[A-Z2-7-]*' | tail -1 | cut -d= -f2)" || true
  [ -n "$CODE" ] && break
  sleep 1
done
echo
echo "Fleetwatch $("$BIN" version) is running."
echo "  1. Open $URL"
if [ -n "$CODE" ]; then
  echo "  2. Enter the setup code: $CODE"
else
  echo "  2. Find the setup code with: journalctl -u ops-server | grep 'setup code'"
fi
echo "  3. Create your admin account, then add servers from the dashboard."
if [ -n "$DOMAIN" ]; then
  echo "Make sure $DOMAIN points to this machine and ports 80 and 443 are open, so Caddy can get a certificate."
fi
