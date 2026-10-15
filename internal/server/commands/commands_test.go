package commands

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/valid"
)

type sent struct {
	serverID string
	req      proto.Exec
}

func setup(t *testing.T) (*Service, *[]sent, string, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'one', 0), ('s2', 'two', 0)`)
	bus := live.NewBus()
	a := apps.New(d, bus, metrics.NewStore(d, metrics.AppKind), func(string) bool { return true })
	pm2, err := a.Create(apps.Input{ServerID: "s1", Name: "API", Manager: "pm2", ProcessKey: "api", Path: "/var/www/api"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	site, _ := a.Create(apps.Input{ServerID: "s1", Name: "Site", Manager: "standalone", Path: "/var/www/site"}, "manual")
	var log []sent
	s := New(d, a, bus, func(serverID, typ string, v any) error {
		if typ != proto.TypeExec {
			t.Fatalf("unexpected frame %s", typ)
		}
		log = append(log, sent{serverID, v.(proto.Exec)})
		return nil
	})
	return s, &log, pm2.ID, site.ID
}

func TestValidation(t *testing.T) {
	s, _, appID, _ := setup(t)
	bad := []Input{
		{Name: "", Command: "ls"},
		{Name: "x", Command: ""},
		{Name: "x", Command: "ls", Cwd: "../etc"},
		{Name: "x", Command: "ls", Cwd: "a/../../b"},
		{Name: "x", Command: "ls", Cwd: "./a"},
		{Name: "x", Command: "ls", TimeoutSec: 99999},
		{Name: "x", Command: "ls", Env: []string{"BAD KEY=1"}},
		{Name: "x", Command: "ls", Env: []string{"NOEQUALS"}},
		{Name: "x", Command: "ls\x00rm"},
	}
	for _, in := range bad {
		var ve valid.Error
		if _, err := s.Create(appID, in); !errors.As(err, &ve) {
			t.Errorf("accepted %+v (%v)", in, err)
		}
	}
	c, err := s.Create(appID, Input{Name: "Build", Command: "npm run build", Cwd: "/web/", Env: []string{"NODE_ENV=production"}})
	if err != nil || c.Cwd != "web" || c.TimeoutSec != 300 {
		t.Fatalf("create: %+v %v", c, err)
	}
	if _, err := s.Create("app_missing", Input{Name: "x", Command: "ls"}); err == nil {
		t.Fatal("command for unknown app accepted")
	}
}

func TestRunLifecycle(t *testing.T) {
	s, log, appID, siteID := setup(t)
	c, _ := s.Create(appID, Input{Name: "Build", Command: "npm run build", Env: []string{"A=1"}})

	list, _ := s.List(appID)
	if len(list) != 5 || list[0].ID != "builtin:start" || list[2].Preview != "pm2 restart api" || list[4].Kind != "custom" || list[4].EnvKeys[0] != "A" {
		t.Fatalf("list: %+v", list)
	}
	if site, _ := s.List(siteID); len(site) != 0 {
		t.Fatalf("standalone app has built-ins: %+v", site)
	}

	run, err := s.Start(appID, "builtin:restart", "admin")
	if err != nil || run.Status != "running" || len(*log) != 1 {
		t.Fatalf("start builtin: %+v %v", run, err)
	}
	if req := (*log)[0].req; req.Kind != "builtin" || req.Action != "restart" || req.Key != "api" || req.Manager != "pm2" || (*log)[0].serverID != "s1" {
		t.Fatalf("exec: %+v", req)
	}
	// Output and results from another server's agent are ignored.
	s.Output("s2", proto.ExecOutput{RunID: run.ID, Stream: "stdout", Data: "spoofed"})
	s.Result("s2", proto.ExecResult{RunID: run.ID, Status: "succeeded"})
	s.Output("s1", proto.ExecOutput{RunID: run.ID, Stream: "stdout", Data: "[PM2] restarted\n"})
	if r, _ := s.GetRun(run.ID); r.Status != "running" || r.Stdout != "[PM2] restarted\n" {
		t.Fatalf("live run: %+v", r)
	}
	s.Result("s1", proto.ExecResult{RunID: run.ID, Status: "succeeded", RunAs: "root"})
	r, _ := s.GetRun(run.ID)
	if r.Status != "succeeded" || *r.ExitCode != 0 || r.FinishedAt == 0 || r.RunAs != "root" || strings.Contains(r.Stdout, "spoofed") {
		t.Fatalf("finished: %+v", r)
	}

	// Custom command carries its definition; ids from another app are refused.
	if _, err := s.Start(siteID, c.ID, "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-app command: %v", err)
	}
	if _, err := s.Start(appID, "builtin:format-disk", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown builtin: %v", err)
	}
	run2, err := s.Start(appID, c.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if req := (*log)[1].req; req.Kind != "custom" || req.Command != "npm run build" || req.AppPath != "/var/www/api" || req.Env[0] != "A=1" {
		t.Fatalf("custom exec: %+v", req)
	}
	// The agent disconnecting fails the run instead of leaving it running forever.
	s.AgentGone("s1")
	if r, _ := s.GetRun(run2.ID); r.Status != "error" || !strings.Contains(r.Error, "disconnected") {
		t.Fatalf("after disconnect: %+v", r)
	}
	if h, _ := s.History(appID, 10); len(h) != 2 {
		t.Fatalf("history: %d", len(h))
	}
	list, _ = s.List(appID)
	if list[2].LastRun == nil || list[2].LastRun.Status != "succeeded" {
		t.Fatalf("last run: %+v", list[2].LastRun)
	}
}

func TestOfflineAgent(t *testing.T) {
	s, _, appID, _ := setup(t)
	s.Send = func(string, string, any) error { return errors.New("offline") }
	if _, err := s.Start(appID, "builtin:start", "admin"); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline: %v", err)
	}
	h, _ := s.History(appID, 10)
	if len(h) != 1 || h[0].Status != "error" {
		t.Fatalf("offline run recorded as: %+v", h)
	}
}
