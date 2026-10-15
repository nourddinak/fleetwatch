package apps

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/valid"
)

func newService(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('srv1', 'VPS', 0)`); err != nil {
		t.Fatal(err)
	}
	s := New(d, live.NewBus(), metrics.NewStore(d, metrics.AppKind), func(string) bool { return true })
	s.Now = func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	return s
}

func inventory(withAPI, withWorker bool, apiStatus string) proto.Inventory {
	inv := proto.Inventory{
		Projects: []proto.Project{
			{Path: "/var/www/demo-api", Markers: []string{".git", "package.json"}, Runtime: "Node.js", Git: &proto.Git{Branch: "main", Commit: "abc1234", Remote: "https://github.com/example/demo-api.git"}},
			{Path: "/var/www/demo-worker", Markers: []string{"requirements.txt"}, Runtime: "Python"},
			{Path: "/var/www/static-site", Markers: []string{"index.html"}, Runtime: "Static"},
		},
		Routes: []proto.Route{{Host: "demo.example.test", Upstream: "127.0.0.1:5002", Port: 5002}},
	}
	if withAPI {
		inv.Processes = append(inv.Processes, proto.Process{Manager: "pm2", Key: "demo-api", Status: apiStatus, PID: 100, Likely: true, Project: "/var/www/demo-api",
			CPU: 12.34, MemBytes: 50 << 20, Restarts: 2, StartedAt: 1_800_000_000_000 - 60_000, Runtime: "Node.js",
			Ports: []proto.Port{{Proto: "TCP", Addr: "127.0.0.1", Port: 5002, PID: 100}}})
	}
	if withWorker {
		inv.Processes = append(inv.Processes, proto.Process{Manager: "systemd", Key: "demo-worker.service", Status: "online", PID: 200, Likely: true, Project: "/var/www/demo-worker",
			Ports: []proto.Port{{Proto: "TCP", Addr: "0.0.0.0", Port: 8081, PID: 200}}})
	}
	inv.Processes = append(inv.Processes, proto.Process{Manager: "systemd", Key: "rsyslog.service", Status: "online", Likely: false})
	return inv
}

func pending(t *testing.T, s *Service) map[string]DiscoveryView {
	t.Helper()
	list, err := s.Discoveries("pending")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]DiscoveryView{}
	for _, d := range list {
		out[d.Name] = d
	}
	return out
}

func TestDiscoveryLifecycle(t *testing.T) {
	s := newService(t)
	s.Ingest("srv1", inventory(true, true, "online"))
	p := pending(t, s)
	if len(p) != 3 {
		t.Fatalf("pending: %v", p)
	}
	api, worker, site := p["demo-api"], p["demo-worker"], p["static-site"]
	if api.Kind != "pm2" || api.SuggestedDomain != "demo.example.test" || !api.Git || api.Public {
		t.Fatalf("api discovery: %+v", api)
	}
	if worker.Kind != "systemd" || worker.Unit != "demo-worker.service" || !worker.Public {
		t.Fatalf("worker discovery: %+v", worker)
	}
	if site.Kind != "folder" || site.Manager != "standalone" {
		t.Fatalf("folder discovery: %+v", site)
	}

	// Ignored items stay ignored across scans.
	if err := s.IgnoreDiscovery(worker.ID); err != nil {
		t.Fatal(err)
	}
	s.Ingest("srv1", inventory(true, true, "online"))
	if _, ok := pending(t, s)["demo-worker"]; ok {
		t.Fatal("ignored discovery came back")
	}

	// Adding creates a monitored app with live state.
	v, err := s.AddDiscovery(api.ID, "Demo API")
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "Demo API" || v.Manager != "PM2" || v.Status != "online" || v.CPU != 12.3 || v.RAM != 50 || v.UptimeSec != 60 ||
		v.Git == nil || v.Git.Repo != "example/demo-api" || v.SuggestedDomain != "demo.example.test" || len(v.Ports) != 1 {
		t.Fatalf("app view: %+v", v)
	}
	if _, err := s.AddDiscovery(api.ID, ""); err == nil {
		t.Fatal("added twice")
	}
	s.Ingest("srv1", inventory(true, true, "online"))
	if _, ok := pending(t, s)["demo-api"]; ok {
		t.Fatal("monitored app still listed as discovery")
	}

	// Status change is recorded; a vanished process means offline.
	s.Ingest("srv1", inventory(true, true, "stopped"))
	s.Ingest("srv1", inventory(false, true, ""))
	got, _ := s.Get(v.ID)
	if got.Status != "offline" || got.PID != nil {
		t.Fatalf("after process vanished: %+v", got)
	}
	ev, _ := s.Events(v.ID, 10)
	texts := []string{}
	for _, e := range ev {
		texts = append(texts, e["text"].(string))
	}
	joined := strings.Join(texts, " | ")
	if !strings.Contains(joined, "online → stopped") || !strings.Contains(joined, "stopped → offline") {
		t.Fatalf("events: %s", joined)
	}

	// Deleting the app while the process runs brings the discovery back.
	s.Ingest("srv1", inventory(true, true, "online"))
	if _, err := s.Delete(v.ID); err != nil {
		t.Fatal(err)
	}
	s.Ingest("srv1", inventory(true, true, "online"))
	if _, ok := pending(t, s)["demo-api"]; !ok {
		t.Fatal("discovery did not return after app deletion")
	}
	// Reset brings ignored back too.
	if n, _ := s.ResetIgnored(); n != 1 {
		t.Fatalf("reset restored %d", n)
	}
	// A pending discovery whose process disappears is dropped; its folder remains
	// as a folder discovery.
	s.Ingest("srv1", inventory(false, false, ""))
	if d := pending(t, s)["demo-worker"]; d.Kind != "folder" {
		t.Fatalf("after the service vanished: %+v", d)
	}
}

func TestDisconnectedServerIsUnknown(t *testing.T) {
	s := newService(t)
	s.Ingest("srv1", inventory(true, false, "online"))
	v, err := s.Create(Input{ServerID: "srv1", Name: "API", Manager: "pm2", ProcessKey: "demo-api"}, "manual")
	if err != nil || v.Status != "online" {
		t.Fatalf("create: %+v %v", v, err)
	}
	s.Connected = func(string) bool { return false }
	if got, _ := s.Get(v.ID); got.Status != "unknown" {
		t.Fatalf("status while disconnected: %s", got.Status)
	}
}

func TestInputValidation(t *testing.T) {
	s := newService(t)
	bad := []Input{
		{ServerID: "srv1", Name: "", Manager: "pm2", ProcessKey: "x"},
		{ServerID: "srv1", Name: "x", Manager: "docker", ProcessKey: "x"},
		{ServerID: "srv1", Name: "x", Manager: "pm2", ProcessKey: "a; rm -rf /"},
		{ServerID: "srv1", Name: "x", Manager: "systemd", ProcessKey: "../../etc/passwd"},
		{ServerID: "srv1", Name: "x", Manager: "systemd", ProcessKey: "ok.service", Path: "relative/path"},
		{ServerID: "srv1", Name: "x", Manager: "systemd", ProcessKey: "ok.service", Path: "/var/www/../../etc"},
		{ServerID: "srv1", Name: "x", Manager: "standalone"},
		{ServerID: "srv1", Name: "x", Manager: "pm2", ProcessKey: "x", Domain: "not a domain"},
		{ServerID: "nope", Name: "x", Manager: "pm2", ProcessKey: "x"},
	}
	for _, in := range bad {
		var ve valid.Error
		if _, err := s.Create(in, "manual"); !errors.As(err, &ve) {
			t.Errorf("accepted %+v (err %v)", in, err)
		}
	}
	v, err := s.Create(Input{ServerID: "srv1", Name: "Billing", Manager: "systemd", ProcessKey: "billing", Path: "/opt/billing/", Domain: "Billing.Example.com"}, "manual")
	if err != nil || v.Unit != "billing.service" || v.Path != "/opt/billing" || *v.Domain != "billing.example.com" {
		t.Fatalf("normalized: %+v %v", v, err)
	}
	if _, err := s.Create(Input{ServerID: "srv1", Name: "Dup", Manager: "systemd", ProcessKey: "billing.service"}, "manual"); err == nil {
		t.Fatal("duplicate accepted")
	}
	ds := DiscoverySettings{Roots: []string{"/var/www", "/var/www/", "/"}}
	if ds.Validate() == nil {
		t.Fatal("root / accepted")
	}
	ds = DiscoverySettings{Roots: []string{"/var/www", "/var/www/", "/srv"}}
	if ds.Validate() != nil || len(ds.Roots) != 2 {
		t.Fatalf("roots: %+v", ds.Roots)
	}
}

func TestHealth(t *testing.T) {
	s := newService(t)
	v, _ := s.Create(Input{ServerID: "srv1", Name: "API", Manager: "pm2", ProcessKey: "api"}, "manual")
	for _, bad := range []HealthView{
		{Enabled: true, URL: "javascript:alert(1)"},
		{Enabled: true, URL: "file:///etc/passwd"},
		{Enabled: true, URL: "http://user:pw@127.0.0.1/health"},
		{Enabled: true, URL: "http://127.0.0.1/health", IntervalSec: 1},
		{Enabled: true, URL: "http://127.0.0.1/health", ExpectedStatus: 999},
	} {
		if _, err := s.SetHealth(v.ID, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	got, err := s.SetHealth(v.ID, HealthView{Enabled: true, URL: "http://127.0.0.1:5002/health"})
	if err != nil || got.HealthStatus != "unknown" || got.Health.IntervalSec != 30 {
		t.Fatalf("set: %+v %v", got, err)
	}
	if checks := s.HealthChecks("srv1"); len(checks) != 1 || checks[0].URL != "http://127.0.0.1:5002/health" {
		t.Fatalf("checks: %+v", checks)
	}
	now := s.Now().UnixMilli()
	s.IngestHealth("srv1", []proto.HealthResult{{AppID: v.ID, TS: now, OK: true, Status: 200, LatencyMs: 12}})
	s.IngestHealth("srv2", []proto.HealthResult{{AppID: v.ID, TS: now, OK: false}}) // other server: ignored
	if got, _ := s.Get(v.ID); got.HealthStatus != "healthy" || *got.Latency != 12 {
		t.Fatalf("healthy: %+v", got)
	}
	s.IngestHealth("srv1", []proto.HealthResult{{AppID: v.ID, TS: now + 1, OK: false, Error: "connection refused"}, {AppID: v.ID, TS: now + 2, OK: false, Error: "connection refused"}})
	got, _ = s.Get(v.ID)
	if got.HealthStatus != "failed" || got.HealthFailures != 2 || got.HealthError != "connection refused" {
		t.Fatalf("failed: %+v", got)
	}
	// A fresh service (server restart) rebuilds the summary from stored results.
	s2 := New(s.db, s.Bus, s.Metrics, s.Connected)
	if got, _ := s2.Get(v.ID); got.HealthFailures != 2 || got.HealthLastOK != now {
		t.Fatalf("reloaded: %+v", got)
	}
	ev, _ := s.Events(v.ID, 10)
	if len(ev) < 2 || !strings.Contains(ev[0]["text"].(string), "failing") {
		t.Fatalf("events: %+v", ev)
	}
}
