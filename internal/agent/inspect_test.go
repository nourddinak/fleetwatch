package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/agent/caddy"
	"fleetwatch/internal/agent/gitinfo"
	"fleetwatch/internal/agent/projects"
	"fleetwatch/internal/proto"
)

func TestStripCredentials(t *testing.T) {
	cases := map[string]string{
		"https://user:token@github.com/acme/api.git":   "https://github.com/acme/api.git",
		"https://ghp_abc123@github.com/acme/api.git":   "https://github.com/acme/api.git",
		"git@github.com:acme/api.git":                  "git@github.com:acme/api.git",
		"ssh://git:secret@gitlab.example.com/acme/api": "ssh://git@gitlab.example.com/acme/api",
		"": "",
	}
	for in, want := range cases {
		if got := gitinfo.StripCredentials(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestGitStatus(t *testing.T) {
	g := &proto.Git{}
	gitinfo.ParseStatus("## main...origin/main [ahead 2, behind 1]\n M server.js\nA  new.go\n D old.txt\nR  a -> b\n?? debug.log\n", g)
	if !g.Dirty || g.Ahead != 2 || g.Behind != 1 || len(g.Changes) != 5 {
		t.Fatalf("%+v", g)
	}
	want := []string{"Modified", "Added", "Deleted", "Renamed", "Untracked"}
	for i, c := range g.Changes {
		if c.State != want[i] {
			t.Errorf("change %d: %s want %s", i, c.State, want[i])
		}
	}
	clean := &proto.Git{}
	gitinfo.ParseStatus("## main\n", clean)
	if clean.Dirty || clean.Ahead != 0 {
		t.Fatalf("clean: %+v", clean)
	}
}

func TestCaddyfile(t *testing.T) {
	f := `{
	email admin@example.com
}

(common) {
	encode gzip
}

api.example.com {
	import common
	reverse_proxy 127.0.0.1:5002
}

https://app.example.com, www.app.example.com:443 {
	handle /api/* {
		reverse_proxy localhost:8081 {
			header_up Host {host}
		}
	}
	# reverse_proxy 127.0.0.1:9999
	file_server
}

:8080 {
	reverse_proxy 127.0.0.1:3000
}
`
	routes := caddy.ParseCaddyfile(strings.NewReader(f))
	got := map[string]int{}
	for _, r := range routes {
		got[r.Host] = r.Port
	}
	want := map[string]int{"api.example.com": 5002, "app.example.com": 8081, "www.app.example.com": 8081}
	if len(got) != len(want) {
		t.Fatalf("routes: %+v", routes)
	}
	for h, p := range want {
		if got[h] != p {
			t.Errorf("%s: port %d want %d", h, got[h], p)
		}
	}
}

func TestCaddyJSON(t *testing.T) {
	cfg := `{"apps":{"http":{"servers":{"srv0":{"routes":[
		{"match":[{"host":["api.example.com"]}],"handle":[{"handler":"subroute","routes":[{"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"127.0.0.1:5002"}]}]}]}]},
		{"match":[{"host":["static.example.com"]}],"handle":[{"handler":"file_server"}]}
	]}}}}}`
	routes := caddy.ParseJSONConfig([]byte(cfg))
	if len(routes) != 1 || routes[0].Host != "api.example.com" || routes[0].Port != 5002 {
		t.Fatalf("%+v", routes)
	}
	if caddy.ParseJSONConfig([]byte("not json")) != nil {
		t.Fatal("garbage parsed")
	}
}

func TestProjects(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, files ...string) string {
		dir := filepath.Join(root, rel)
		os.MkdirAll(dir, 0o755)
		for _, f := range files {
			os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
		}
		return dir
	}
	next := mk("shop", "package.json", "next.config.mjs")
	mk("shop/dist")
	mk("api", "go.mod")
	mk("notes") // no markers
	mk(".hidden", "package.json")

	if p := projects.Detect(next); p == nil || p.Runtime != "Next.js" || len(p.Markers) != 2 {
		t.Fatalf("detect: %+v", p)
	}
	if got := projects.FindRoot(filepath.Join(next, "dist")); got != next {
		t.Fatalf("FindRoot from subdir = %s", got)
	}
	list := projects.Scan([]string{root, "relative/ignored"}, 10)
	if len(list) != 2 {
		t.Fatalf("scan found %d projects: %+v", len(list), list)
	}
	for exe, want := range map[string]string{"/usr/bin/python3.12 app.py": "Python", "/home/u/.nvm/versions/node/v20/bin/node": "Node.js", "/opt/x/x": ""} {
		if got := projects.RuntimeFromExec(exe); got != want {
			t.Errorf("%s: %q want %q", exe, got, want)
		}
	}
}
