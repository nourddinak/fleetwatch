package manager

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParseJList(t *testing.T) {
	raw, err := os.ReadFile("testdata/jlist.json")
	if err != nil {
		t.Fatal(err)
	}
	list, err := ParseJList(raw, "/root/.pm2")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d apps, want 3 (cluster instances merged)", len(list))
	}
	api, web, worker := list[0], list[1], list[2]
	if api.Key != "demo-api" || api.Status != "online" || api.PID != 7662 || api.Restarts != 2 || api.Cwd != "/var/www/demo-api" ||
		api.Runtime != "Node.js" || api.User != "root" || api.StartedAt != 1792062050291 || !api.Likely {
		t.Fatalf("demo-api: %+v", api)
	}
	if web.Instances != 2 || len(web.PM2IDs) != 2 || web.Status != "errored" || !web.Unstable || web.Restarts != 16 || web.PID != 8001 {
		t.Fatalf("cluster web: %+v", web)
	}
	if worker.Status != "stopped" || worker.StartedAt != 0 {
		t.Fatalf("worker: %+v", worker)
	}
	// The app environment must never leave the agent.
	out, _ := json.Marshal(list)
	if strings.Contains(string(out), "SUPERSECRET") || strings.Contains(string(out), "DATABASE_URL") {
		t.Fatalf("environment leaked: %s", out)
	}
	if _, err := ParseJList([]byte("pm2 crashed"), "/root/.pm2"); err == nil {
		t.Fatal("garbage accepted")
	}
}

const showOutput = `Id=demo-worker.service
Description=Demo worker
LoadState=loaded
ActiveState=active
SubState=running
MainPID=7776
FragmentPath=/etc/systemd/system/demo-worker.service
WorkingDirectory=/var/www/demo-worker
User=www-data
ExecStart={ path=/usr/bin/python3 ; argv[]=/usr/bin/python3 /var/www/demo-worker/worker.py ; ignore_errors=no ; start_time=[n/a] ; pid=7776 ; code=(null) ; status=0/0 }
Restart=on-failure
NRestarts=3
ActiveEnterTimestampMonotonic=665936508
UnitFileState=enabled

Id=rsyslog.service
LoadState=loaded
ActiveState=active
SubState=running
MainPID=200
FragmentPath=/usr/lib/systemd/system/rsyslog.service
ExecStart={ path=/usr/sbin/rsyslogd ; argv[]=/usr/sbin/rsyslogd -n ; }

Id=crashy.service
LoadState=loaded
ActiveState=activating
SubState=auto-restart
MainPID=0
FragmentPath=/etc/systemd/system/crashy.service
ExecStart={ path=/opt/crashy/crashy ; argv[]=/opt/crashy/crashy ; }
NRestarts=42

Id=gone.service
LoadState=not-found
ActiveState=inactive`

func TestParseShow(t *testing.T) {
	list := ParseShow(showOutput, 1_000_000)
	if len(list) != 3 {
		t.Fatalf("got %d units, want 3 (not-found skipped)", len(list))
	}
	w := list[0]
	if w.Key != "demo-worker.service" || w.Status != "online" || w.PID != 7776 || w.Restarts != 3 || w.User != "www-data" ||
		w.Cwd != "/var/www/demo-worker" || w.ExecStart != "/usr/bin/python3 /var/www/demo-worker/worker.py" ||
		w.Runtime != "Python" || !w.Likely || w.StartedAt != 1_000_000+665936 {
		t.Fatalf("demo-worker: %+v", w)
	}
	if list[1].Likely || list[1].User != "root" {
		t.Fatalf("rsyslog classified as app: %+v", list[1])
	}
	if c := list[2]; c.Status != "starting" || !c.Unstable || !c.Likely {
		t.Fatalf("crash-looping unit: %+v", c)
	}
}

func TestClassify(t *testing.T) {
	app := func(id, frag, exec, wd string) map[string]string {
		return map[string]string{"Id": id, "FragmentPath": frag, "ExecStart": "{ path=x ; argv[]=" + exec + " ; }", "WorkingDirectory": wd, "UnitFileState": "enabled"}
	}
	cases := []struct {
		props map[string]string
		want  bool
	}{
		{app("billing-api.service", "/etc/systemd/system/billing-api.service", "/opt/billing/api", ""), true},
		{app("site.service", "/etc/systemd/system/site.service", "/usr/bin/node server.js", "/var/www/site"), true},
		{app("rsyslog.service", "/usr/lib/systemd/system/rsyslog.service", "/usr/sbin/rsyslogd", ""), false},
		{app("ops-agent.service", "/etc/systemd/system/ops-agent.service", "/usr/local/bin/ops-agent run", ""), false},
		{app("ops-server-update.service", "/etc/systemd/system/ops-server-update.service", "/usr/local/bin/ops-server self-update", ""), false},
		{app("snap.docker.dockerd.service", "/etc/systemd/system/snap.docker.dockerd.service", "/usr/bin/snap run docker", ""), false},
		{app("pm2-root.service", "/etc/systemd/system/pm2-root.service", "/usr/lib/node_modules/pm2/bin/pm2 resurrect", ""), false},
		{app("custom-sshd.service", "/etc/systemd/system/custom-sshd.service", "/usr/sbin/sshd -D", ""), false},
		{app("nginx.service", "/etc/systemd/system/nginx.service", "/usr/sbin/nginx", ""), false},
		{map[string]string{"Id": "x.service", "FragmentPath": "/etc/systemd/system/x.service", "UnitFileState": "generated"}, false},
	}
	for _, c := range cases {
		if got, reason := Classify(c.props); got != c.want {
			t.Errorf("%s: got %v (%s), want %v", c.props["Id"], got, reason, c.want)
		}
	}
}
