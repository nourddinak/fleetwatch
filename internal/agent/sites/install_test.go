//go:build linux

package sites

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake firewall commands: iptables already allows 443 but not 80; ufw and firewalld are
// not running.
func TestOpenWebPortsIptables(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := func(name, body string) {
		os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho \""+name+" $*\" >> "+calls+"\n"+body+"\n"), 0o755)
	}
	fake("ufw", `echo "Status: inactive"`)
	fake("iptables", `case "$*" in "-C INPUT -p tcp --dport 80 -j ACCEPT") exit 1;; esac; exit 0`)
	fake("netfilter-persistent", "")
	t.Setenv("PATH", dir)
	var log bytes.Buffer
	OpenWebPorts(context.Background(), &log)
	b, _ := os.ReadFile(calls)
	got := string(b)
	if !strings.Contains(got, "iptables -I INPUT 1 -p tcp --dport 80 -j ACCEPT") || strings.Contains(got, "-I INPUT 1 -p tcp --dport 443") ||
		!strings.Contains(got, "netfilter-persistent save") || strings.Contains(got, "ufw allow") {
		t.Fatalf("calls:\n%s\nlog: %s", got, log.String())
	}
}
