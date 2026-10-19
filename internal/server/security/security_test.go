package security

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/db"
)

type sent struct {
	mu   sync.Mutex
	list []string
}

func (s *sent) add(t string) { s.mu.Lock(); s.list = append(s.list, t); s.mu.Unlock() }
func (s *sent) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.list...)
}

func newTest(t *testing.T) (*Service, *sent) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s := New(d)
	alerts := &sent{}
	s.ServerIDs = func() []string { return []string{"s1"} }
	s.Name = func(string) string { return "web1" }
	s.Notify = func(title, body, sev string) { alerts.add(title) }
	s.Country = func(_ context.Context, ip string) string {
		if strings.HasPrefix(ip, "203.") {
			return "NL"
		}
		return "DE"
	}
	s.Ports = func(string) ([]apps.PortRow, error) {
		return []apps.PortRow{
			{Proto: "TCP", Bind: "0.0.0.0", Port: 22, Process: "sshd"},
			{Proto: "TCP", Bind: "::", Port: 443, Process: "caddy"},
			{Proto: "TCP", Bind: "0.0.0.0", Port: 3000, Process: "node", AppName: "api"},
			{Proto: "TCP", Bind: "127.0.0.1", Port: 5432, Process: "postgres"},
			{Proto: "UDP", Bind: "0.0.0.0", Port: 68, Process: "dhclient"},
		}, nil
	}
	s.Domains = func() []apps.DomainRef {
		return []apps.DomainRef{{Host: "leaky.example.com", ServerID: "s1"}, {Host: "fine.example.com", ServerID: "s1"}}
	}
	s.AppPaths = func(string) []string { return nil }
	s.IsGitHub = func(_ context.Context, ip string) bool { return strings.HasPrefix(ip, "20.") }
	s.Fetch = func(_ context.Context, url string) (int, []byte) {
		switch url {
		case "https://leaky.example.com/.git/HEAD":
			return 200, []byte("ref: refs/heads/main\n")
		case "https://fine.example.com/.env": // an app that answers every path with its page
			return 200, []byte("<!doctype html><html>app</html>")
		}
		return 404, nil
	}
	return s, alerts
}

func TestFindings(t *testing.T) {
	s, alerts := newTest(t)
	rep, _ := json.Marshal(proto.SecurityReport{At: 1, Findings: []proto.Finding{{ID: "ssh_password", Severity: "warning", Title: "SSH accepts passwords"}}})
	s.OnReport("s1", rep)
	s.webChecks(context.Background())
	got := map[string]proto.Finding{}
	for _, f := range s.findings("s1") {
		got[f.ID] = f
	}
	if len(got) != 3 || got["web_git:leaky.example.com"].Severity != "critical" || !strings.Contains(got["port:3000"].Title, "api (node)") {
		t.Fatalf("findings: %+v", got)
	}
	if s.findings("s1")[0].Severity != "critical" {
		t.Fatal("critical findings come first")
	}
	if len(alerts.all()) != 2 { // the agent finding and the leaked .git (ports are listed, not pushed)
		t.Fatalf("alerts: %v", alerts.all())
	}
	if err := s.SetExpected("s1", 3000, true); err != nil {
		t.Fatal(err)
	}
	for _, f := range s.findings("s1") {
		if f.ID == "port:3000" {
			t.Fatal("an expected port is still reported")
		}
	}
	s.OnReport("s1", rep) // same findings again: no new alerts
	if len(alerts.all()) != 2 {
		t.Fatalf("repeated alerts: %v", alerts.all())
	}
}

func TestLogins(t *testing.T) {
	s, alerts := newTest(t)
	base := time.Now().Add(-time.Hour).UnixMilli()
	s.storeLogins("s1", []proto.SSHLogin{
		{TS: base, User: "root", IP: "198.51.100.1", Method: "publickey", Key: "ED25519 SHA256:aaa"},
		{TS: base + 1, User: "root", IP: "10.0.0.2", Method: "publickey", Key: "ED25519 SHA256:aaa"},
	})
	if len(alerts.all()) != 0 {
		t.Fatalf("the first batch is the baseline: %v", alerts.all())
	}
	s.storeLogins("s1", []proto.SSHLogin{
		{TS: base, User: "root", IP: "198.51.100.1", Method: "publickey", Key: "ED25519 SHA256:aaa"}, // already stored
		{TS: base + 10, User: "root", IP: "198.51.100.9", Method: "publickey", Key: "ED25519 SHA256:aaa"},
		{TS: base + 20, User: "root", IP: "203.0.113.5", Method: "publickey", Key: "ED25519 SHA256:bbb"},
	})
	a := alerts.all()
	if len(a) != 1 || !strings.Contains(a[0], "first login from NL") || !strings.Contains(a[0], "a key never used by root") {
		t.Fatalf("alerts: %v", a)
	}
	all, _ := s.Logins("", false, false, 10)
	list := all.Items
	if len(list) != 4 || list[0].IP != "203.0.113.5" || !list[0].NewCountry || !list[0].NewKey || list[3].Country != "DE" {
		t.Fatalf("logins: %+v", list)
	}
	if lan, _ := s.Logins("s1", false, false, 10); lan.Items[2].Country != "LAN" {
		t.Fatalf("private address: %+v", lan.Items[2])
	}
	if only, _ := s.Logins("", true, false, 10); len(only.Items) != 1 {
		t.Fatalf("only new: %+v", only)
	}
}

func TestAutomatedLogins(t *testing.T) {
	s, alerts := newTest(t)
	base := time.Now().Add(-time.Hour).UnixMilli()
	s.storeLogins("s1", []proto.SSHLogin{{TS: base, User: "root", IP: "198.51.100.1", Method: "publickey", Key: "ED25519 SHA256:me"}}) // baseline
	s.storeLogins("s1", []proto.SSHLogin{
		// GitHub Actions runners: new countries and a new key, all expected.
		{TS: base + 1, User: "deploy", IP: "20.1.2.3", Method: "publickey", Key: "ED25519 SHA256:ci"},
		{TS: base + 2, User: "deploy", IP: "20.9.9.9", Method: "publickey", Key: "ED25519 SHA256:ci"},
	})
	if len(alerts.all()) != 0 {
		t.Fatalf("GitHub Actions logins raised alerts: %v", alerts.all())
	}
	// A person from the Netherlands is still news, even after runners were there.
	s.storeLogins("s1", []proto.SSHLogin{{TS: base + 3, User: "root", IP: "203.0.113.7", Method: "publickey", Key: "ED25519 SHA256:me"}})
	if a := alerts.all(); len(a) != 1 || !strings.Contains(a[0], "first login from NL") {
		t.Fatalf("person alert: %v", a)
	}
	// Another CI system, recognized by naming its key.
	if err := s.SetKeyLabel("ED25519 SHA256:gitlab", "GitLab deploy"); err != nil {
		t.Fatal(err)
	}
	s.storeLogins("s1", []proto.SSHLogin{{TS: base + 4, User: "deploy", IP: "203.0.113.99", Method: "publickey", Key: "ED25519 SHA256:gitlab"}})
	if len(alerts.all()) != 1 {
		t.Fatalf("a named key raised an alert: %v", alerts.all())
	}
	people, _ := s.Logins("", false, true, 10)
	if len(people.Items) != 2 || people.Hidden != 3 {
		t.Fatalf("people only: %+v", people)
	}
	all, _ := s.Logins("", false, false, 10)
	if all.Items[1].Automated {
		t.Fatalf("a person counted as automated: %+v", all.Items[1])
	}
	if all.Items[0].Label != "GitLab deploy" || all.Items[2].Source != "github" {
		t.Fatalf("labels: %+v", all.Items)
	}
}

// A port newly open to the internet is told within minutes, once.
func TestWatchPorts(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d)
	rows := []apps.PortRow{{Port: 22, Proto: "tcp", Bind: "0.0.0.0", Process: "sshd"}}
	s.Ports = func(string) ([]apps.PortRow, error) { return rows, nil }
	s.ServerIDs = func() []string { return []string{"s1"} }
	s.Name = func(string) string { return "web-1" }
	var told []string
	s.Notify = func(title, _, _ string) { told = append(told, title) }
	s.WatchPorts() // the first look only remembers
	rows = append(rows, apps.PortRow{Port: 5432, Proto: "tcp", Bind: "0.0.0.0", Process: "postgres"})
	s.WatchPorts()
	s.WatchPorts()
	if len(told) != 1 || !strings.Contains(told[0], "Port 5432") || !strings.Contains(told[0], "web-1") {
		t.Fatal(told)
	}
}
