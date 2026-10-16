package incidents

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/metrics"
)

type env struct {
	d      *Detector
	s      *Store
	events []Event
	mu     sync.Mutex
	conn   bool
}

func setup(t *testing.T) *env {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS-1', 0)`)
	database.Exec(`INSERT INTO apps (id, server_id, name, manager, process_key, created_at) VALUES ('a1', 's1', 'API', 'pm2', 'api', 0)`)
	e := &env{conn: true}
	e.s = NewStore(database, live.NewBus())
	e.s.Notify = func(ev Event) { e.mu.Lock(); e.events = append(e.events, ev); e.mu.Unlock() }
	e.d = NewDetector(e.s, database, func(string) bool { e.mu.Lock(); defer e.mu.Unlock(); return e.conn }, func(string) string { return "VPS-1" })
	return e
}

func (e *env) open(t *testing.T) map[string]Incident {
	t.Helper()
	list, err := e.s.List(Filter{Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Incident{}
	for _, x := range list {
		out[x.TypeKey] = x
	}
	return out
}

func view(status, raw string, restarts int) apps.View {
	return apps.View{ID: "a1", Name: "API", Manager: "PM2", Status: status, RawStatus: raw, Restarts: restarts, ProcessFound: true,
		Flags: apps.DefaultFlags(), Ports: []apps.PortView{{Port: 5002, Bind: "127.0.0.1", Proto: "TCP"}}}
}

func TestAppDownAndRecovery(t *testing.T) {
	e := setup(t)
	now := time.Now()
	// First sight of a deliberately stopped app: no incident.
	e.d.Apps("s1", []apps.View{view("stopped", "stopped", 0)}, now)
	if len(e.open(t)) != 0 {
		t.Fatal("incident for an app that was already stopped")
	}
	e.d.Apps("s1", []apps.View{view("online", "online", 0)}, now)
	e.d.Apps("s1", []apps.View{view("offline", "errored", 0)}, now)
	open := e.open(t)
	if x := open["pm2_errored"]; x.Severity != "critical" || x.Status != "active" || x.AppID == nil {
		t.Fatalf("pm2 errored: %+v", open)
	}
	// Re-observing keeps one incident, without inflating occurrences.
	e.d.Apps("s1", []apps.View{view("offline", "errored", 0)}, now)
	if x := e.open(t)["pm2_errored"]; x.Occurrences != 1 {
		t.Fatalf("occurrences %d", x.Occurrences)
	}
	// Crash loop supersedes it.
	e.d.Apps("s1", []apps.View{view("crash-loop", "waiting restart", 3)}, now)
	open = e.open(t)
	if _, ok := open["pm2_errored"]; ok || open["crash_loop"].ID == "" {
		t.Fatalf("crash loop: %+v", open)
	}
	e.d.Apps("s1", []apps.View{view("online", "online", 3)}, now)
	if len(e.open(t)) != 0 {
		t.Fatalf("not resolved on recovery: %+v", e.open(t))
	}
	if len(e.events) != 4 || e.events[0].Kind != "opened" || e.events[3].Kind != "resolved" {
		t.Fatalf("events: %+v", e.events)
	}
	// Unknown status (server unreachable) and disabled incidents are ignored.
	v := view("unknown", "", 3)
	e.d.Apps("s1", []apps.View{v}, now)
	v = view("offline", "", 3)
	v.Flags.Incidents = false
	e.d.Apps("s1", []apps.View{v}, now)
	if len(e.open(t)) != 0 {
		t.Fatal("incident despite unknown status / disabled detection")
	}
}

func TestRestartsPortsHealth(t *testing.T) {
	e := setup(t)
	now := time.Now()
	for i := 0; i <= 5; i++ {
		e.d.Apps("s1", []apps.View{view("online", "online", i)}, now.Add(time.Duration(i)*time.Second))
	}
	if x := e.open(t)["repeated_restarts"]; !strings.Contains(x.Title, "5 times") {
		t.Fatalf("restarts: %+v", e.open(t))
	}
	e.d.Apps("s1", []apps.View{view("online", "online", 5)}, now.Add(11*time.Minute))
	if _, ok := e.open(t)["repeated_restarts"]; ok {
		t.Fatal("restart incident not cleared after a quiet window")
	}

	noPorts := view("online", "online", 5)
	noPorts.Ports = nil
	e.d.Apps("s1", []apps.View{noPorts}, now)
	if _, ok := e.open(t)["port_unavailable"]; !ok {
		t.Fatal("port loss not detected")
	}
	e.d.Apps("s1", []apps.View{view("online", "online", 5)}, now)
	if _, ok := e.open(t)["port_unavailable"]; ok {
		t.Fatal("port incident not cleared")
	}

	h := view("online", "online", 5)
	h.Health = apps.HealthView{Enabled: true, URL: "http://127.0.0.1:5002/health"}
	h.HealthFailures, h.HealthError = 2, "connection refused"
	e.d.Apps("s1", []apps.View{h}, now)
	if _, ok := e.open(t)["health_failed"]; ok {
		t.Fatal("health incident below the threshold")
	}
	h.HealthFailures = 3
	e.d.Apps("s1", []apps.View{h}, now)
	if x := e.open(t)["health_failed"]; x.Severity != "critical" || !strings.Contains(x.Samples[0], "connection refused") {
		t.Fatalf("health: %+v", e.open(t))
	}
	h.HealthFailures, h.HealthStatus = 0, "healthy"
	e.d.Apps("s1", []apps.View{h}, now)
	if _, ok := e.open(t)["health_failed"]; ok {
		t.Fatal("health incident not cleared")
	}
}

func TestServerThresholdsAndOffline(t *testing.T) {
	e := setup(t)
	start := time.Now()
	for m := 0; m <= 5; m++ {
		e.d.ServerSample("s1", metrics.Sample{CPU: 97, Mem: 40, Disk: 50}, start.Add(time.Duration(m)*time.Minute))
	}
	if _, ok := e.open(t)["high_cpu"]; !ok {
		t.Fatal("sustained CPU not detected")
	}
	e.d.ServerSample("s1", metrics.Sample{CPU: 88, Disk: 50}, start.Add(6*time.Minute))
	if _, ok := e.open(t)["high_cpu"]; !ok {
		t.Fatal("cleared inside the hysteresis band")
	}
	e.d.ServerSample("s1", metrics.Sample{CPU: 20, Disk: 96}, start.Add(7*time.Minute))
	open := e.open(t)
	if _, ok := open["high_cpu"]; ok || open["disk_full"].Severity != "critical" {
		t.Fatalf("cpu cleared / disk critical: %+v", open)
	}

	e.d.grace = 50 * time.Millisecond
	e.mu.Lock()
	e.conn = false
	e.mu.Unlock()
	e.d.ServerState("s1", false)
	time.Sleep(150 * time.Millisecond)
	if _, ok := e.open(t)["server_offline"]; !ok {
		t.Fatal("offline after grace not detected")
	}
	e.mu.Lock()
	e.conn = true
	e.mu.Unlock()
	e.d.ServerState("s1", true)
	if _, ok := e.open(t)["server_offline"]; ok {
		t.Fatal("offline not cleared on reconnect")
	}
	// A blip shorter than the grace period never opens an incident.
	before := len(e.events)
	e.d.ServerState("s1", false)
	e.d.ServerState("s1", true)
	time.Sleep(100 * time.Millisecond)
	if len(e.events) != before {
		t.Fatal("short disconnect produced an incident")
	}
}

func TestRepeatedErrors(t *testing.T) {
	e := setup(t)
	now := time.Now()
	line := func(msg, sev string) logs.Line {
		return logs.Line{ServerID: "s1", AppID: "a1", Severity: sev, Message: msg}
	}
	var batch []logs.Line
	for i := 0; i < 24; i++ {
		batch = append(batch, line(fmt.Sprintf("Error: connect ECONNREFUSED 127.0.0.1:%d after %dms", 5000+i, i*7), "error"))
	}
	batch = append(batch, line("GET /health 200", "info"), line("TypeError: x is undefined", "error"))
	e.d.Logs(batch, now)
	if len(e.open(t)) != 0 {
		t.Fatal("incident below the error threshold")
	}
	e.d.Logs([]logs.Line{line("Error: connect ECONNREFUSED 10.0.0.9:6379 after 1ms", "error")}, now)
	x := e.open(t)["repeated_errors"]
	if x.Occurrences != 25 || !strings.Contains(x.Title, "ECONNREFUSED <ip>") {
		t.Fatalf("grouped incident: %+v", x)
	}
	e.d.Logs([]logs.Line{line("Error: connect ECONNREFUSED 10.0.0.1:1 after 2ms", "error")}, now)
	if x := e.open(t)["repeated_errors"]; x.Occurrences != 26 {
		t.Fatalf("occurrence not counted: %d", x.Occurrences)
	}
	e.s.Now = func() time.Time { return now.Add(11 * time.Minute) }
	e.d.Sweep(now.Add(11 * time.Minute))
	if len(e.open(t)) != 0 {
		t.Fatal("quiet error incident not resolved")
	}
}

func TestAdminActions(t *testing.T) {
	e := setup(t)
	e.s.Ensure(Signal{Type: "high_ram", Key: "high_ram|s1", Severity: "warning", Title: "RAM", ServerID: "s1"})
	x := e.open(t)["high_ram"]
	for _, a := range []string{"acknowledge", "mute"} {
		if y, err := e.s.Act(x.ID, a, "", ""); err != nil || (a == "mute" && !y.Muted(time.Now())) {
			t.Fatalf("%s: %+v %v", a, y, err)
		}
	}
	if y, _ := e.s.Get(x.ID); y.ActedBy() != "admin" {
		t.Fatalf("acted by %q", y.ActedBy())
	}
	if by := (Incident{Timeline: []entry{{Text: "Acknowledged by Sam (phone)"}, {Text: "Updated: RAM"}}}).ActedBy(); by != "Sam" {
		t.Fatalf("acted by %q", by)
	}
	if _, err := e.s.Act(x.ID, "explode", "", ""); err == nil {
		t.Fatal("unknown action accepted")
	}
	if y, _ := e.s.Act(x.ID, "resolve", "fixed swap", ""); y.Status != "resolved" || y.ResolvedBy != "admin" || y.Resolution != "fixed swap" {
		t.Fatalf("resolve: %+v", y)
	}
	e.s.Ensure(Signal{Type: "high_ram", Key: "high_ram|s1", Severity: "warning", Title: "RAM again", ServerID: "s1"})
	if _, err := e.s.Act(x.ID, "reopen", "", ""); err == nil {
		t.Fatal("reopened while a newer incident is open")
	}
}

func TestNormalize(t *testing.T) {
	a := Normalize(`2026-10-15T10:00:00Z user "alice" id 550e8400-e29b-41d4-a716-446655440000 failed after 120ms from 10.1.2.3:443 (0xdeadbeef)`)
	b := Normalize(`2026-10-16T11:22:33Z user "bob" id 123e4567-e89b-12d3-a456-426614174000 failed after 7ms from 192.168.0.1:80 (0xcafebabe)`)
	if a != b {
		t.Fatalf("not grouped:\n%s\n%s", a, b)
	}
}
