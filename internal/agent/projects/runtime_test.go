package projects

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntime(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"go api with react at the top", map[string]string{"package.json": `{"dependencies":{"react":"18"}}`, "backend/go.mod": "module x"}, "Go + React"},
		{"go root, vue frontend folder", map[string]string{"go.mod": "module x", "frontend/package.json": `{"devDependencies":{"vue":"3"}}`}, "Go + Vue"},
		{"rust with plain node tooling", map[string]string{"Cargo.toml": "", "package.json": `{"dependencies":{"esbuild":"1"}}`}, "Rust + Node.js"},
		{"express", map[string]string{"package.json": `{"dependencies":{"express":"4"}}`}, "Node.js"},
		{"next", map[string]string{"package.json": `{"dependencies":{"next":"14","react":"18"}}`}, "Next.js"},
		{"go only", map[string]string{"go.mod": "module x"}, "Go"},
		{"static", map[string]string{"index.html": "<h1>"}, "Static"},
		{"dependencies are not the app", map[string]string{"go.mod": "module x", "node_modules/react/package.json": `{}`}, "Go"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		for name, body := range c.files {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		}
		p := Detect(dir)
		if p == nil || p.Runtime != c.want {
			t.Errorf("%s: got %+v, want %q", c.name, p, c.want)
		}
	}
}

func TestBinaryRuntime(t *testing.T) {
	self, err := os.Executable() // this test binary is a Go program
	if err != nil {
		t.Fatal(err)
	}
	if rt := BinaryRuntime(self); rt != "Go" {
		t.Fatalf("go binary: %q", rt)
	}
	script := filepath.Join(t.TempDir(), "start.sh")
	os.WriteFile(script, []byte("#!/bin/sh\nexec node server.js\n"), 0o755)
	if rt := BinaryRuntime(script); rt != "" {
		t.Fatalf("script: %q", rt)
	}
	if rt := ExecRuntime("/usr/bin/node /srv/app/server.js", 0); rt != "Node.js" {
		t.Fatalf("interpreter: %q", rt)
	}
	if rt := ExecRuntime(self+" --port 5000", 0); rt != "Go" {
		t.Fatalf("exec start of a go binary: %q", rt)
	}
}
