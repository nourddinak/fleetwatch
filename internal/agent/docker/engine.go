package docker

import (
	"context"
	"time"

	"fleetwatch/internal/agent/sysexec"
)

// Installing and removing Docker itself, with Docker's own install script
// (get.docker.com: Ubuntu, Debian, Fedora, CentOS/RHEL…) and the system's package tool.

const installScript = `set -e
if command -v curl >/dev/null 2>&1; then curl -fsSL https://get.docker.com -o /tmp/fleetwatch-get-docker.sh
else wget -qO /tmp/fleetwatch-get-docker.sh https://get.docker.com; fi
sh /tmp/fleetwatch-get-docker.sh
rm -f /tmp/fleetwatch-get-docker.sh
systemctl enable --now docker
docker version --format 'Docker {{.Server.Version}} is installed and running.'`

// uninstallScript removes Docker's packages, whichever were installed; containers,
// images and volumes stay in /var/lib/docker unless purgeData follows.
const uninstallScript = `systemctl stop docker docker.socket containerd 2>/dev/null || true
if command -v apt-get >/dev/null 2>&1; then
  pk=$(dpkg-query -W -f='${Package} ${Status}\n' 'docker*' 'containerd*' 2>/dev/null | awk '/install ok installed/{print $1}')
  if [ -n "$pk" ]; then apt-get purge -y $pk; apt-get autoremove -y; fi
elif command -v dnf >/dev/null 2>&1; then dnf remove -y 'docker*' 'containerd*'
elif command -v yum >/dev/null 2>&1; then yum remove -y 'docker*' 'containerd*'
else echo "no apt, dnf or yum: remove Docker by hand" >&2; exit 1; fi
echo "Docker's packages are removed."`

const purgeData = `
rm -rf /var/lib/docker /var/lib/containerd /etc/docker
echo "All containers, images and volumes are deleted."`

func engine(ctx context.Context, action string) (string, error) {
	script := ""
	switch action {
	case "install":
		if Available() {
			return "Docker is already installed.", nil
		}
		script = installScript
	case "uninstall":
		script = uninstallScript
	case "purge":
		script = uninstallScript + purgeData
	default:
		return "", errUnknown
	}
	out, err := sysexec.Run(ctx, sysexec.Opts{Timeout: 20 * time.Minute, MaxOut: 1 << 20,
		Env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "DEBIAN_FRONTEND=noninteractive", "LANG=C.UTF-8"}},
		"/bin/sh", "-c", script)
	return string(out), err
}
