package oncall

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
)

func TestRotationAndOverrides(t *testing.T) {
	// Monday 2026-10-12 09:00 Paris is a handover.
	paris, _ := time.LoadLocation("Europe/Paris")
	c := Config{Rotation: []string{"ana", "ben", "cy"}, HandoverDay: 1, HandoverHour: 9, TimeZone: "Europe/Paris",
		Start: time.Date(2026, 10, 12, 9, 0, 0, 0, paris).UnixMilli()}
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, paris) }
	for when, want := range map[time.Time]string{
		at(2026, 10, 12, 9):  "ana",
		at(2026, 10, 19, 8):  "ana", // an hour before handover
		at(2026, 10, 19, 9):  "ben",
		at(2026, 10, 26, 10): "cy", // across the daylight-saving change
		at(2026, 11, 2, 9):   "ana",
		at(2026, 10, 11, 12): "cy", // the week before the start
	} {
		if got := c.OnCall(when); got != want {
			t.Errorf("%s: %s, want %s", when, got, want)
		}
	}
	c.Overrides = []Override{{Person: "cy", From: at(2026, 10, 14, 0).UnixMilli(), To: at(2026, 10, 16, 0).UnixMilli()}}
	if got := c.OnCall(at(2026, 10, 15, 3)); got != "cy" {
		t.Fatalf("override: %s", got)
	}
	sched := c.Schedule(at(2026, 10, 13, 0), 2)
	want := []string{"ana", "cy", "ana", "ben", "cy"} // the last shift starts inside the two weeks
	if len(sched) != len(want) {
		t.Fatalf("schedule: %+v", sched)
	}
	for i, s := range sched {
		if s.Person != want[i] || (i == 1) != s.Override {
			t.Fatalf("shift %d: %+v", i, s)
		}
	}
	if ch := c.chain(at(2026, 10, 20, 0)); len(ch) != 4 || ch[0] != "ben" || ch[1] != "cy" || ch[2] != "ana" || ch[3] != Owner {
		t.Fatalf("chain: %v", ch)
	}
}

func TestEscalation(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	store := incidents.NewStore(d, live.NewBus())
	d.Exec(`INSERT INTO servers (id, name, provider, created_at) VALUES ('s1', 'web1', '', 0)`)
	s := New(d, make([]byte, 32))
	now := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s.Incidents = store
	s.People = func() []Person {
		return []Person{{ID: Owner, Name: "Owner"}, {ID: "ana", Name: "Ana"}, {ID: "ben", Name: "Ben"}}
	}
	var paged []string
	s.Page = func(_ context.Context, p Person, _ incidents.Incident, _ string, _ int) error {
		paged = append(paged, p.ID)
		return nil
	}
	c, err := s.Save(Config{Enabled: true, Rotation: []string{"ana", "ben"}, HandoverDay: 1, HandoverHour: 9, TimeZone: "UTC", EscalateAfterMin: 10, Severity: "critical"})
	if err != nil {
		t.Fatal(err)
	}
	if c.OnCall(now) != "ana" {
		t.Fatalf("on call: %s", c.OnCall(now))
	}
	store.Ensure(incidents.Signal{Type: "app_offline", Key: "k1", Severity: "critical", Title: "api down", ServerID: "s1"})
	list, _ := store.List(incidents.Filter{Status: "all"})
	x := list[0]
	s.OnEvent(incidents.Event{Kind: "opened", Incident: x})
	s.tick(context.Background())
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock(); s.tick(context.Background()) }
	advance(5 * time.Minute)
	if len(paged) != 1 || paged[0] != "ana" {
		t.Fatalf("first page: %v", paged)
	}
	advance(6 * time.Minute) // 11 minutes without an acknowledgement
	if len(paged) != 2 || paged[1] != "ben" {
		t.Fatalf("escalation: %v", paged)
	}
	// Ben acknowledges from the link in his page.
	if _, _, err := s.Ack(x.ID, "ben", "forged"); err == nil {
		t.Fatal("forged link accepted")
	}
	sig := s.sign(x.ID, "ben")
	y, who, err := s.Ack(x.ID, "ben", sig)
	if err != nil || y.Status != "acknowledged" || who != "Ben" {
		t.Fatalf("ack: %v %+v %s", err, y, who)
	}
	advance(20 * time.Minute)
	if len(paged) != 2 {
		t.Fatalf("paged after acknowledgement: %v", paged)
	}

	// Warnings do not page when only critical incidents should.
	store.Ensure(incidents.Signal{Type: "high_cpu", Key: "k2", Severity: "warning", Title: "cpu", ServerID: "s1"})
	list, _ = store.List(incidents.Filter{Status: "all"})
	for _, w := range list {
		if w.Severity == "warning" {
			s.OnEvent(incidents.Event{Kind: "opened", Incident: w})
		}
	}
	advance(time.Minute)
	if len(paged) != 2 {
		t.Fatalf("a warning paged someone: %v", paged)
	}
}
