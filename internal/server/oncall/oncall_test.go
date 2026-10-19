package oncall

import (
	"context"
	"path/filepath"
	"strings"
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
	store.Now = s.Now // incidents and pages on the same clock
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

	// The report: Ana was paged and missed it, Ben was paged after her and answered.
	stats, pages := s.Report(30)
	by := map[string]PersonStats{}
	for _, st := range stats {
		by[st.Person] = st
	}
	if len(pages) != 2 || by["ana"].Pages != 1 || by["ana"].Missed != 1 || by["ben"].Acked != 1 || by["ben"].Escalated != 1 || by["ana"].Night != 1 {
		t.Fatalf("%+v %+v", by, pages)
	}
	if by["ana"].HoursOnCall <= 0 {
		t.Fatal("no on-call hours")
	}

	// Handover: Ana leaves a note; Ben is told when his week starts.
	if _, err := s.SaveNote("web1 disk is at 80%, cleanup planned", "Ana"); err != nil {
		t.Fatal(err)
	}
	var told []string
	s.OnHandover = func(from, to Person, note *Note, open int) {
		told = append(told, from.ID+">"+to.ID+":"+note.Text)
	}
	s.watchHandover()
	mu.Lock()
	now = time.Date(2026, 10, 19, 9, 30, 0, 0, time.UTC)
	mu.Unlock()
	s.watchHandover()
	s.watchHandover()
	if len(told) != 1 || told[0] != "ana>ben:web1 disk is at 80%, cleanup planned" {
		t.Fatalf("%v", told)
	}
	h := s.Handoff()
	if h.Current == nil || h.Current.Person != "ben" || h.Note == nil || h.Note.By != "Ana" || h.Summary == nil || h.Summary.Opened != 2 || h.Summary.Pages != 2 {
		t.Fatalf("%+v %+v", h, h.Summary)
	}
}

func TestFixedOrder(t *testing.T) {
	c := Defaults()
	c.Rotation, c.FixedOrder = []string{"a", "b", "c"}, true
	now := time.Now()
	for w := range 4 { // the first is on call every week
		if got := c.OnCall(now.AddDate(0, 0, 7*w)); got != "a" {
			t.Fatal("week", w, got)
		}
	}
	if got := c.chain(now); strings.Join(got, ",") != "a,b,c,owner" {
		t.Fatal(got)
	}
	if s := c.Schedule(now, 4); len(s) != 1 || s[0].Person != "a" {
		t.Fatal("one long shift expected", s)
	}
	c.Overrides = []Override{{Person: "b", From: now.Add(-time.Hour).UnixMilli(), To: now.Add(time.Hour).UnixMilli()}}
	if got := c.OnCall(now); got != "b" {
		t.Fatal("cover still applies", got)
	}
	c.FixedOrder = false // weekly shifts again: the week after is someone else
	c.Overrides = nil
	if c.OnCall(now) == c.OnCall(now.AddDate(0, 0, 7)) {
		t.Fatal("shifts on: the person changes weekly")
	}
}

func TestDaysOffAndQuietNights(t *testing.T) {
	c := Defaults()
	c.Rotation, c.FixedOrder = []string{"a", "b", "c"}, true
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c.DaysOff = []Override{{Person: "a", From: now.Add(time.Hour).UnixMilli(), To: now.Add(25 * time.Hour).UnixMilli()}}
	if c.OnCall(now) != "a" || c.OnCall(now.Add(2*time.Hour)) != "b" || c.OnCall(now.Add(26*time.Hour)) != "a" {
		t.Fatal("b covers a's day off")
	}
	if got := strings.Join(c.chain(now.Add(2*time.Hour)), ","); got != "b,c,owner" {
		t.Fatal("nobody off is paged", got)
	}
	sched := c.Schedule(now, 1)
	if len(sched) != 3 || sched[1].Person != "b" || !sched[1].Override || sched[2].Person != "a" {
		t.Fatal("the schedule shows the day off", sched)
	}

	c.QuietNights = true // 22:00 → 08:00 UTC
	if _, q := c.QuietUntil(now); q {
		t.Fatal("noon is not quiet")
	}
	until, q := c.QuietUntil(time.Date(2026, 10, 14, 23, 30, 0, 0, time.UTC))
	if !q || !until.Equal(time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)) {
		t.Fatal("until 08:00 the next morning", until, q)
	}
	if until, q := c.QuietUntil(time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)); !q || until.Hour() != 8 || until.Day() != 15 {
		t.Fatal("after midnight, until 08:00 that day", until)
	}
}

func TestCoverRequests(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, make([]byte, 32))
	s.People = func() []Person { return []Person{{ID: Owner, Name: "Owner"}, {ID: "ana"}, {ID: "ben"}, {ID: "cy"}} }
	if _, err := s.Save(Config{Enabled: true, Rotation: []string{"ana", "ben"}, HandoverDay: 1, HandoverHour: 9, TimeZone: "UTC", EscalateAfterMin: 10, Severity: "critical"}); err != nil {
		t.Fatal(err)
	}
	asked := make(chan CoverRequest, 1)
	s.OnCoverAsked = func(r CoverRequest) { asked <- r }
	from := time.Now().Add(48 * time.Hour)
	r, err := s.AskCover("ana", from, from.Add(24*time.Hour), "a wedding")
	if err != nil || r.Status != "open" || (<-asked).ID != r.ID {
		t.Fatal(r, err)
	}
	if _, err := s.TakeCover(r.ID, "ana"); err == nil {
		t.Fatal("one's own request")
	}
	if r, err = s.TakeCover(r.ID, "cy"); err != nil || r.Status != "taken" || r.TakenBy != "cy" {
		t.Fatal(r, err)
	}
	if s.Config().OnCall(from.Add(time.Hour)) != "cy" {
		t.Fatal("the cover is an override")
	}
	if _, err := s.TakeCover(r.ID, "ben"); err == nil {
		t.Fatal("taken twice")
	}
	r2, _ := s.AskCover("ben", from, from.Add(time.Hour), "")
	if s.CancelCover(r2.ID, "ana") == nil || s.CancelCover(r2.ID, "ben") != nil || s.CoverRequests()[0].Status != "canceled" {
		t.Fatal("only its person (or the owner) cancels")
	}
}
