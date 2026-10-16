package statuspage

import (
	"path/filepath"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/settings"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/incidents"
)

func TestHistory(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	today := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return today.Add(d).UnixMilli() }
	list := []incidents.Incident{
		// Overlapping outages yesterday 10:00-11:00 and 10:30-11:30: 90 minutes, not 120.
		{TypeKey: "app_offline", FirstAt: at(-14 * time.Hour), ResolvedAt: at(-13 * time.Hour)},
		{TypeKey: "health_failed", FirstAt: at(-13*time.Hour - 30*time.Minute), ResolvedAt: at(-12*time.Hour - 30*time.Minute)},
		// Degraded two days ago; unrelated types change nothing.
		{TypeKey: "high_cpu", FirstAt: at(-40 * time.Hour), ResolvedAt: at(-39 * time.Hour)},
		{TypeKey: "ssl_expiry", FirstAt: at(-60 * time.Hour)},
		// Still open since 11:00 today: counts until now.
		{TypeKey: "crash_loop", FirstAt: at(11 * time.Hour)},
	}
	days, up := History(list, at(-3*24*time.Hour), now, 5)
	want := []string{"nodata", "operational", "degraded", "outage", "outage"}
	for i, d := range days {
		if d.Status != want[i] {
			t.Errorf("day %d (%s): %s, want %s", i, d.Date, d.Status, want[i])
		}
	}
	if days[3].DownMinutes != 90 || days[4].DownMinutes != 60 || days[4].Date != "2026-10-16" {
		t.Errorf("minutes: %+v", days)
	}
	// Observed 3.5 days = 5040 minutes, 150 of them down.
	if up == nil || *up != 97.023 {
		t.Errorf("uptime %v", up)
	}
	if _, up := History(nil, now.Add(time.Hour).UnixMilli(), now, 30); up != nil {
		t.Error("a component from the future has no uptime")
	}
}

func TestValidate(t *testing.T) {
	ok := Defaults()
	ok.Domain = " Status.Example.COM "
	ok.Logo = "https://cdn.example.com/brand/logo.svg?v=2"
	ok.Notices = []Notice{{Title: "Upgrade", Status: "maintenance", Start: 1}}
	if err := ok.Validate(); err != nil || ok.Domain != "status.example.com" || ok.Notices[0].ID == "" {
		t.Fatalf("%v %+v", err, ok)
	}
	bad := map[string]func(c *Config){
		"accent":    func(c *Config) { c.Accent = "red;background:url(x)" },
		"theme":     func(c *Config) { c.Theme = "neon" },
		"link":      func(c *Config) { c.HomeURL = "javascript:alert(1)" },
		"logo":      func(c *Config) { c.Logo = "data:text/html;base64,PHNjcmlwdD4=" },
		"http logo": func(c *Config) { c.Logo = "http://example.com/logo.png" },
		"js logo":   func(c *Config) { c.Logo = "javascript:alert(1)" },
		"odd logo":  func(c *Config) { c.Logo = `https://example.com/a.png" onerror="x` },
		"big logo":  func(c *Config) { c.Logo = "data:image/png;base64," + strings.Repeat("A", 400_000) },
		"domain":    func(c *Config) { c.Domain = "status.example.com/evil" },
		"days":      func(c *Config) { c.HistoryDays = 365 },
		"kind": func(c *Config) {
			c.Groups = []Group{{Name: "x", Components: []Component{{Kind: "db", ID: "1", Name: "x"}}}}
		},
		"name":   func(c *Config) { c.Groups = []Group{{Name: "x", Components: []Component{{Kind: "app", ID: "1"}}}} },
		"notice": func(c *Config) { c.Notices = []Notice{{Title: "x", Status: "panic", Start: 1}} },
		"range":  func(c *Config) { c.Notices = []Notice{{Title: "x", Status: "info", Start: 10, End: 5}} },
	}
	for name, f := range bad {
		c := Defaults()
		f(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMigrateSinglePage(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	old := Defaults()
	old.Enabled, old.Title = true, "Old page"
	settings.Save(d, legacyKey, old)
	s := New(d, nil)
	if p := s.Pages(); len(p) != 1 || p[0].Title != "Old page" || p[0].Slug != "" || p[0].ID == "" {
		t.Fatalf("migrated: %+v", p)
	}
	if err := s.Delete(s.Pages()[0].ID); err != nil {
		t.Fatal(err)
	}
	if p := New(d, nil).Pages(); len(p) != 0 {
		t.Fatalf("a deleted page came back: %+v", p)
	}
}
