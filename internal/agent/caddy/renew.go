package caddy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fleetwatch/internal/proto"
)

// Renew makes Caddy get a fresh certificate for a host now (agent 0.9.132+): the stored
// one is set aside and Caddy restarted, so it obtains a new one (Let's Encrypt or
// ZeroSSL, as configured). Without a newer certificate within two minutes, the old one
// is put back (the site keeps working on it) and Caddy's own errors are reported.
func Renew(ctx context.Context, req proto.CertRenew) proto.CertRenewed {
	res := proto.CertRenewed{RunID: req.RunID, Host: strings.ToLower(req.Host)}
	if !PublicName(res.Host) {
		res.Error = "not a public domain name"
		return res
	}
	dirs := certDirs(res.Host)
	if len(dirs) == 0 {
		res.Error = "Caddy on this server has no stored certificate for " + res.Host + " (is the site served by Caddy here?)"
		return res
	}
	before := checkLocal(ctx, localTLS, res.Host)
	stamp := ".fleetwatch-renew-" + time.Now().UTC().Format("20060102T150405")
	for _, d := range dirs {
		if err := os.Rename(d, d+stamp); err != nil {
			restore(dirs, stamp)
			res.Error = "could not set the old certificate aside: " + err.Error()
			return res
		}
	}
	if out, err := restartCaddy(); err != nil {
		restore(dirs, stamp)
		restartCaddy()
		res.Error, res.Log = "could not restart Caddy", string(out)
		return res
	}
	deadline := time.Now().Add(renewWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			deadline = time.Now()
		case <-time.After(renewPoll):
		}
		if now := checkLocal(ctx, localTLS, res.Host); now.NotAfter > before.NotAfter {
			for _, d := range dirs {
				os.RemoveAll(d + stamp)
			}
			res.OK, res.NotAfter, res.Issuer = true, now.NotAfter, now.Issuer
			return res
		}
	}
	// No new certificate: the old one back, and why from Caddy's log.
	for _, d := range dirs {
		os.RemoveAll(d) // a half-made new one
	}
	restore(dirs, stamp)
	restartCaddy()
	res.Log = caddyLog(res.Host)
	res.Error = "Caddy did not get a new certificate in time; the old one is back in place"
	if l := lastError(res.Log); l != "" {
		res.Error += ": " + l
	}
	return res
}

// Paths and commands; tests replace them.
var (
	localTLS     = "127.0.0.1:443"
	renewWait    = 2 * time.Minute
	renewPoll    = 5 * time.Second
	storageRoots = []string{"/var/lib/caddy/.local/share/caddy", "/root/.local/share/caddy", "/home/*/.local/share/caddy"}
	restartCaddy = func() ([]byte, error) { return exec.Command("systemctl", "restart", "caddy").CombinedOutput() }
	caddyLog     = func(host string) string {
		out, _ := exec.Command("journalctl", "-u", "caddy", "--since", "-5 min", "--no-pager", "-o", "cat").Output()
		var keep []string
		for _, l := range strings.Split(string(out), "\n") {
			if strings.Contains(l, host) && (strings.Contains(l, "error") || strings.Contains(l, "obtain") || strings.Contains(l, "challenge")) {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep[max(0, len(keep)-15):], "\n")
	}
)

// certDirs are the host's certificate folders in Caddy's storage (one per issuer),
// and its wildcard's.
func certDirs(host string) []string {
	names := []string{host}
	if _, parent, ok := strings.Cut(host, "."); ok {
		names = append(names, "wildcard_."+parent)
	}
	var out []string
	for _, root := range storageRoots {
		for _, n := range names {
			matches, _ := filepath.Glob(filepath.Join(root, "certificates", "*", n))
			for _, m := range matches {
				if st, err := os.Stat(m); err == nil && st.IsDir() {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func restore(dirs []string, stamp string) {
	for _, d := range dirs {
		if _, err := os.Stat(d + stamp); err == nil {
			os.RemoveAll(d)
			os.Rename(d+stamp, d)
		}
	}
}

// lastError picks the most telling line of Caddy's log: its error message.
func lastError(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if _, msg, ok := strings.Cut(lines[i], `"error":"`); ok {
			msg, _, _ = strings.Cut(msg, `"`)
			if len(msg) > 300 {
				msg = msg[:300] + "…"
			}
			return msg
		}
	}
	return ""
}
