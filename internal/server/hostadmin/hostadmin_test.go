package hostadmin

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/live"
)

func setup(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s := New(d, live.NewBus())
	s.Protected = func() []string { return []string{"198.51.100.7"} } // a server's own address
	return s
}

func fails(s *Service, server string, list ...proto.SSHFailure) {
	raw, _ := json.Marshal(list)
	s.OnFailures(server, raw)
}

func TestAutoBlockAcrossServers(t *testing.T) {
	s := setup(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	var notes []string
	s.Notify = func(title, body, sev string) { notes = append(notes, title) }
	configs := 0
	s.BroadcastConfig = func() { configs++ }

	// Off by default: counted, never blocked.
	fails(s, "s1", proto.SSHFailure{IP: "203.0.113.9", Count: 50})
	if on, list := s.BlockedIPs(); on || len(list) != 0 || len(s.Blocked()) != 0 {
		t.Fatal("blocked while turned off")
	}
	cfg := DefaultBlockSettings()
	cfg.Enabled = true
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	// 6 on one server and 5 on another within the window: blocked everywhere.
	fails(s, "s1", proto.SSHFailure{IP: "192.0.2.10", Count: 6})
	if len(s.Blocked()) != 0 {
		t.Fatal("blocked below the threshold")
	}
	now = now.Add(2 * time.Minute)
	fails(s, "s2", proto.SSHFailure{IP: "192.0.2.10", Count: 5}, proto.SSHFailure{IP: "198.51.100.7", Count: 99}, proto.SSHFailure{IP: "10.0.0.5", Count: 99})
	b := s.Blocked()
	if len(b) != 1 || b[0].IP != "192.0.2.10/32" || b[0].Source != "auto" || len(b[0].Servers) != 2 || len(notes) != 1 {
		t.Fatalf("%+v %v", b, notes)
	}
	if on, list := s.BlockedIPs(); !on || len(list) != 1 || configs < 2 {
		t.Fatal("agents not told", on, list, configs)
	}
	// Old failures fall out of the window.
	now = now.Add(time.Hour)
	fails(s, "s1", proto.SSHFailure{IP: "192.0.2.77", Count: 9})
	now = now.Add(11 * time.Minute)
	fails(s, "s1", proto.SSHFailure{IP: "192.0.2.77", Count: 9})
	if len(s.Blocked()) != 1 {
		t.Fatal("failures outside the window added up")
	}
	// Expiry.
	now = now.Add(25 * time.Hour)
	s.Sweep()
	if len(s.Blocked()) != 0 {
		t.Fatal("block did not expire")
	}
}

func TestManualBlock(t *testing.T) {
	s := setup(t)
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "198.51.100.7", "203.0.113.50", "not-an-ip", "1.0.0.0/4"} {
		if _, err := s.Block(ip, "", "admin", "203.0.113.50", 0); err == nil {
			t.Errorf("blocked %s", ip)
		}
	}
	b, err := s.Block("203.0.113.0/28", "scanner", "admin", "192.0.2.1", 0)
	if err != nil || b.ExpiresAt != 0 || b.IP != "203.0.113.0/28" {
		t.Fatal(b, err)
	}
	if err := s.Unblock(b.IP); err != nil || len(s.Blocked()) != 0 {
		t.Fatal("unblock", err)
	}
}

func TestAutoUpdatesWindow(t *testing.T) {
	s := setup(t)
	var sent []string
	s.Send = func(id, typ string, v any) error { sent = append(sent, id+" "+typ); return nil }
	s.AgentVersion = func(string) string { return "v0.9.38" }
	at := time.Date(2026, 10, 19, 4, 10, 0, 0, time.UTC) // a Monday
	s.Now = func() time.Time { return at }
	a := DefaultAutoUpdates()
	a.Enabled, a.Days = true, []int{1}
	if err := s.SaveAutoUpdates(a); err != nil {
		t.Fatal(err)
	}
	s.Tick([]string{"s1", "s2"})
	s.Tick([]string{"s1", "s2"}) // once per day
	if len(sent) != 2 || s.Updates([]string{"s1"})["s1"].Installing != "security" {
		t.Fatal(sent)
	}
	at = at.Add(24 * time.Hour) // Tuesday: not a chosen day
	s.Tick([]string{"s1"})
	if len(sent) != 2 {
		t.Fatal("ran on another day", sent)
	}
}

func TestAutoBlockedHook(t *testing.T) {
	s := setup(t)
	s.Notify = func(title, body, sev string) { t.Fatal("notified each time:", title) }
	var got []string
	s.AutoBlocked = func(title, body string, ips []string) { got = append(got, ips...) }
	cfg := DefaultBlockSettings()
	cfg.Enabled = true
	s.SaveSettings(cfg)
	fails(s, "s1", proto.SSHFailure{IP: "192.0.2.10", Count: 20}, proto.SSHFailure{IP: "192.0.2.11", Count: 20})
	if len(got) != 2 || got[0] != "192.0.2.10" {
		t.Fatal(got)
	}
}
