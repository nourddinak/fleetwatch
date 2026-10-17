package sites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

var installing sync.Mutex

// The official package repositories (https://caddyserver.com/docs/install).
const (
	aptCaddy = `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gnupg
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
apt-get update -qq
apt-get install -y -qq caddy`
	dnfCaddy = `set -e
dnf install -y 'dnf-command(copr)'
dnf copr enable -y @caddy/caddy
dnf install -y caddy`
)

// InstallCaddy installs Caddy when this server has none, so sites can be published.
// It leaves alone a Caddy that is already set up, and refuses when another web server
// holds port 80 or 443 (Caddy could not start next to it).
func InstallCaddy(ctx context.Context, log io.Writer) error {
	installing.Lock()
	defer installing.Unlock()
	if _, err := os.Stat(Caddyfile); err == nil {
		return nil
	}
	if _, err := exec.LookPath("caddy"); err == nil {
		return fmt.Errorf("Caddy is installed but %s is missing; create it (it can be empty) and run Caddy as the caddy service", Caddyfile)
	}
	for _, port := range []string{":80", ":443"} {
		l, err := net.Listen("tcp", port)
		if err != nil {
			return fmt.Errorf("Caddy was not installed: port %s is already used by another web server (nginx or Apache?), and Fleetwatch sites need Caddy there", port[1:])
		}
		l.Close()
	}
	var script string
	switch {
	case has("apt-get"):
		script = aptCaddy
	case has("dnf"):
		script = dnfCaddy
	default:
		return errors.New("Caddy is not installed and this system has neither apt nor dnf: install it yourself (https://caddyserver.com/docs/install)")
	}
	fmt.Fprintln(log, "Installing Caddy from its official repository")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "bash", "-c", script).CombinedOutput(); err != nil {
		return fmt.Errorf("could not install Caddy: %s", lastLine(out, err))
	}
	// The package's placeholder site would hold port 80 for nothing: sites import theirs.
	if b, err := os.ReadFile(Caddyfile); err != nil || bytes.Contains(b, []byte("/usr/share/caddy")) {
		os.WriteFile(Caddyfile, []byte("# Sites are kept in separate files and imported here.\n"), 0o644)
	}
	if out, err := exec.CommandContext(ctx, "systemctl", "enable", "--now", "caddy").CombinedOutput(); err != nil {
		return fmt.Errorf("Caddy was installed but did not start: %s", lastLine(out, err))
	}
	reload()
	fmt.Fprintln(log, "Caddy is installed")
	return nil
}

func has(cmd string) bool { _, err := exec.LookPath(cmd); return err == nil }
