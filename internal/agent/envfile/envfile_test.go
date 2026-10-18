package envfile

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

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
