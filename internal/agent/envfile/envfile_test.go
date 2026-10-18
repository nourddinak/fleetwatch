package envfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"fleetwatch/internal/agent/sites"
	"fleetwatch/internal/proto"
)

func TestReadWrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n"), 0o640)
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("A=\n"), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.txt"), []byte("no"), 0o600)

	r := Handle(proto.EnvFile{RunID: "1", Dir: dir, Name: ".env"})
	if r.Error != "" || !r.Exists || r.Content != "A=1\n" || !slices.Equal(r.Files, []string{".env", ".env.example"}) {
		t.Fatalf("%+v", r)
	}
	r = Handle(proto.EnvFile{RunID: "2", Dir: dir, Name: ".env", Write: true, Content: "A=2\n"})
	if r.Error != "" || r.Content != "A=2\n" {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".env.bak")); string(b) != "A=1\n" {
		t.Fatalf("backup: %q", b)
	}
	if slices.Contains(r.Files, ".env.bak") {
		t.Fatal("backup listed")
	}
	// A new file, and names that are not .env files or leave the folder.
	if r = Handle(proto.EnvFile{RunID: "3", Dir: dir, Name: ".env.production", Write: true, Content: "B=1"}); r.Error != "" || !r.Exists {
		t.Fatalf("%+v", r)
	}
	for _, n := range []string{"../secret.txt", "config.json", ".env/../../secret.txt", "/etc/passwd"} {
		if r := Handle(proto.EnvFile{RunID: "4", Dir: dir, Name: n}); r.Error == "" {
			t.Errorf("%s was opened", n)
		}
	}
	if r := Handle(proto.EnvFile{RunID: "5", Dir: "relative/dir", Name: ".env"}); r.Error == "" {
		t.Error("relative folder accepted")
	}
}

// The server's Caddyfile: found by the agent itself, checked by Caddy, put back if rejected.
func TestServerCaddyfile(t *testing.T) {
	dir := t.TempDir()
	old := sites.Caddyfile
	sites.Caddyfile = filepath.Join(dir, "Caddyfile")
	defer func() { sites.Caddyfile = old }()
	if r := Handle(proto.EnvFile{RunID: "1", Caddy: true}); r.Error == "" {
		t.Fatal("missing Caddyfile not reported")
	}
	os.WriteFile(sites.Caddyfile, []byte("example.com {\n}\n"), 0o644)
	accept := true
	checkCaddy = func() error {
		if !accept {
			return errors.New("Caddy rejected the configuration: bad")
		}
		return nil
	}
	defer func() { checkCaddy = sites.CheckAndReload }()

	// Dir and Name from the request are ignored.
	if r := Handle(proto.EnvFile{RunID: "2", Caddy: true, Dir: "/etc", Name: "passwd"}); r.Error != "" || r.Content != "example.com {\n}\n" || r.Name != "Caddyfile" {
		t.Fatalf("%+v", r)
	}
	accept = false
	if r := Handle(proto.EnvFile{RunID: "3", Caddy: true, Write: true, Content: "broken {"}); r.Error == "" {
		t.Fatal("rejected config accepted")
	}
	if b, _ := os.ReadFile(sites.Caddyfile); string(b) != "example.com {\n}\n" {
		t.Fatalf("not put back: %q", b)
	}
	accept = true
	if r := Handle(proto.EnvFile{RunID: "4", Caddy: true, Write: true, Content: "new.example.com {\n}\n"}); r.Error != "" || r.Content != "new.example.com {\n}\n" {
		t.Fatalf("%+v", r)
	}
	// An app's own Caddyfile is just a file in its folder.
	app := t.TempDir()
	if r := Handle(proto.EnvFile{RunID: "5", Dir: app, Name: "Caddyfile", Write: true, Content: ":8080 {\n}\n"}); r.Error != "" || !r.Exists {
		t.Fatalf("%+v", r)
	}
}
