package exec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfineDir(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	os.MkdirAll(filepath.Join(app, "web", "dist"), 0o755)
	os.MkdirAll(filepath.Join(root, "secret"), 0o755)
	os.WriteFile(filepath.Join(app, "file.txt"), []byte("x"), 0o644)

	realApp, _ := filepath.EvalSymlinks(app)
	if got, err := ConfineDir(app, ""); err != nil || got != realApp {
		t.Fatalf("app dir: %q %v", got, err)
	}
	if got, err := ConfineDir(app, "web/dist"); err != nil || got != filepath.Join(realApp, "web", "dist") {
		t.Fatalf("subdir: %q %v", got, err)
	}
	for _, bad := range []string{"..", "../secret", "web/../../secret", "/etc", "missing", "file.txt"} {
		if _, err := ConfineDir(app, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ConfineDir("", "x"); err == nil {
		t.Fatal("empty base accepted")
	}
	if _, err := ConfineDir("relative/app", ""); err == nil {
		t.Fatal("relative base accepted")
	}
	// A symlink inside the app pointing outside must not be a way out.
	if err := os.Symlink(filepath.Join(root, "secret"), filepath.Join(app, "escape")); err == nil {
		if _, err := ConfineDir(app, "escape"); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
}
