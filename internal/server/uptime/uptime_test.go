package uptime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
)

func setup(t *testing.T) (*Service, *incidents.Store) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS-1', 0), ('s2', 'VPS-2', 0)`)
	inc := incidents.NewStore(d, live.NewBus())
	s := New(d, inc, live.NewBus())
	s.ServerExists = func(id string) bool { return id == "s1" || id == "s2" }
	s.probe = func(context.Context, proto.HealthCheck) proto.HealthResult {
		return proto.HealthResult{OK: true, Status: 200, LatencyMs: 50}
	}
	return s, inc
}

func open(t *testing.T, inc *incidents.Store) map[string]incidents.Incident {
	list, err := inc.List(incidents.Filter{Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]incidents.Incident{}
	for _, x := range list {
		out[x.TypeKey] = x
	}
	return out
}

func TestValidate(t *testing.T) {
	s, _ := setup(t)
	for _, c := range []Check{
		{URL: "ftp://x", Locations: []string{Dashboard}},
		{URL: "https://user:pw@x.com", Locations: []string{Dashboard}},
		{URL: "https://x.com"},
		{URL: "https://x.com", Locations: []string{"nope"}},
		{URL: "https://x.com", Locations: []string{Dashboard}, IntervalSec: 5},
	} {
		if _, err := s.Create(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	v, err := s.Create(Check{URL: " https://shop.example.com/ ", Locations: []string{Dashboard, "s1", "s1"}, Enabled: true})
	if err != nil || v.Name != "shop.example.com" || v.IntervalSec != 60 || len(v.Locations) != 2 || v.ExpectedStatus != 200 {
		t.Fatalf("%+v %v", v, err)
	}
	if got := s.AgentChecks("s1"); len(got) != 1 || got[0].AppID != Prefix+v.ID {
		t.Fatalf("agent checks %+v", got)
	}
	if got := s.AgentChecks("s2"); len(got) != 0 {
		t.Fatalf("s2 runs %+v", got)
	}
}

func TestDownPartialAndSlow(t *testing.T) {
	s, inc := setup(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	v, err := s.Create(Check{Name: "Shop", URL: "https://shop.example.com", Locations: []string{"s1", "s2"}, Enabled: true, SlowMs: 500})
	if err != nil {
		t.Fatal(err)
	}
	send := func(loc string, ok bool, lat int64) {
		now = now.Add(time.Second)
		s.Ingest(loc, []proto.HealthResult{{AppID: v.ID, TS: now.UnixMilli(), OK: ok, Status: map[bool]int{true: 200, false: 502}[ok], LatencyMs: lat, Error: "bad gateway"}})
	}
	send("s1", true, 100)
	send("s2", false, 0)
	if len(open(t, inc)) != 0 {
		t.Fatal("one failure opened an incident")
	}
	send("s2", false, 0)
	x, ok := open(t, inc)["uptime_down"]
	if !ok || x.Severity != "warning" {
		t.Fatalf("partial outage: %+v", open(t, inc))
	}
	send("s1", false, 0)
	send("s1", false, 0)
	if x := open(t, inc)["uptime_down"]; x.Severity != "critical" {
		t.Fatalf("full outage: %+v", x)
	}
	if got := s.View(v.Check).Status; got != "down" {
		t.Fatalf("status %s", got)
	}
	// Agents may only report checks they were given.
	s.Ingest("s3", []proto.HealthResult{{AppID: v.ID, OK: true}})
	for range 3 {
		send("s1", true, 900)
		send("s2", true, 900)
	}
	got := open(t, inc)
	if _, down := got["uptime_down"]; down {
		t.Fatal("still down after recovering")
	}
	if _, slow := got["uptime_slow"]; !slow {
		t.Fatalf("not slow: %+v", got)
	}
	for range 10 {
		send("s1", true, 100)
		send("s2", true, 100)
	}
	if len(open(t, inc)) != 0 {
		t.Fatalf("not cleared: %+v", open(t, inc))
	}
	h, err := s.History(v.ID, "1h", "")
	if err != nil || len(h.Locations) != 2 || len(h.Failures) != 4 || len(h.Points) == 0 {
		t.Fatalf("%+v %v", h, err)
	}
	if err := s.Delete(v.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPercentile(t *testing.T) {
	l := []int64{5, 1, 4, 2, 3, 6, 7, 8, 9, 10}
	if percentile(l, 50) != 5 || percentile(l, 95) != 10 || percentile(nil, 95) != 0 {
		t.Fatal(percentile(l, 50), percentile(l, 95))
	}
}

func TestRemovedServerLocation(t *testing.T) {
	s, _ := setup(t)
	v, err := s.Create(Check{URL: "https://x.example.com", Locations: []string{Dashboard, "s1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	s.ServerExists = func(id string) bool { return id == "s2" } // s1 was removed
	u, err := s.Update(v.ID, Check{URL: v.URL, Locations: v.Locations, Enabled: false})
	if err != nil || len(u.Locations) != 1 || u.Locations[0] != Dashboard {
		t.Fatalf("%+v %v", u.Locations, err)
	}
}
