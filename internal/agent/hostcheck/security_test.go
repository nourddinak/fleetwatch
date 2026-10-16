package hostcheck

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseSyslog(t *testing.T) {
	since := time.Now().Add(-48 * time.Hour)
	iso := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	l, ok := parseSyslog(iso+" web1 sshd[123]: Accepted publickey for deploy from 203.0.113.9 port 51515 ssh2: ED25519 SHA256:abcDEF", since)
	if !ok || !strings.HasPrefix(l.msg, "Accepted publickey") {
		t.Fatalf("iso: %v %+v", ok, l)
	}
	classic := time.Now().Add(-time.Hour).Format("Jan _2 15:04:05")
	if _, ok := parseSyslog(classic+" web1 sshd-session[9]: Invalid user admin from 198.51.100.7 port 22", since); !ok {
		t.Fatal("classic syslog line not read")
	}
	if _, ok := parseSyslog(iso+" web1 cron[1]: something", since); ok {
		t.Fatal("non-SSH line read")
	}
	if _, ok := parseSyslog(time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339)+" web1 sshd[1]: Accepted password for a from 1.2.3.4 port 1 ssh2", since); ok {
		t.Fatal("line older than since read")
	}
}

func TestLoginAndFailureLines(t *testing.T) {
	m := acceptedRe.FindStringSubmatch("Accepted publickey for root from 2001:db8::1 port 4242 ssh2: RSA SHA256:xyz")
	if m == nil || m[1] != "publickey" || m[2] != "root" || m[3] != "2001:db8::1" || m[4]+" "+m[5] != "RSA SHA256:xyz" {
		t.Fatalf("accepted: %q", m)
	}
	if m := acceptedRe.FindStringSubmatch("Accepted password for bob from 10.0.0.5 port 22 ssh2"); m == nil || m[1] != "password" || m[5] != "" {
		t.Fatalf("password login: %q", m)
	}
	for line, ip := range map[string]string{
		"Failed password for invalid user oracle from 192.0.2.1 port 3 ssh2": "192.0.2.1",
		"Failed password for root from 192.0.2.2 port 3 ssh2":                "192.0.2.2",
		"Invalid user test from 192.0.2.3 port 5":                            "192.0.2.3",
	} {
		if m := failedRe.FindStringSubmatch(line); m == nil || m[3] != ip {
			t.Errorf("%s: %q", line, m)
		}
	}
}

func TestEnvFindings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions")
	}
	dir := t.TempDir()
	open := filepath.Join(dir, ".env")
	os.WriteFile(open, []byte("KEY=1"), 0o644)
	os.Chmod(open, 0o644)
	closed := filepath.Join(dir, ".env.production")
	os.WriteFile(closed, []byte("KEY=1"), 0o600)
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("KEY="), 0o644)
	fs := envFindings([]string{dir, "relative/path"})
	if len(fs) != 1 || !strings.Contains(fs[0].Title, open) || !strings.Contains(fs[0].Fix, "chmod 600 "+open) {
		t.Fatalf("findings: %+v", fs)
	}
}
