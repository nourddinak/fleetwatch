package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMeasure(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, n int) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, n), 0o644)
	}
	write("node_modules/a/index.js", 5000)
	write("node_modules/b/index.js", 3000)
	write(".git/objects/pack", 2000)
	write("src/app.js", 100)
	write("README.md", 10)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "huge"), make([]byte, 100000), 0o644)
	os.Symlink(outside, filepath.Join(root, "link")) // must not be followed

	r := Measure(context.Background(), "app1", root)
	if r.Error != "" || r.TotalBytes != 10110 || r.Files != 5 || r.Truncated {
		t.Fatalf("result: %+v", r)
	}
	if r.Items[0].Name != "node_modules" || r.Items[0].Bytes != 8000 || !r.Items[0].Dir || r.Items[1].Name != ".git" {
		t.Fatalf("items: %+v", r.Items)
	}
	if bad := Measure(context.Background(), "x", filepath.Join(root, "README.md")); bad.Error == "" {
		t.Fatal("file accepted as directory")
	}
}

func TestMeasureRefusesSystemRoots(t *testing.T) {
	for _, root := range []string{"/", "/proc", "/sys/"} {
		if r := Measure(context.Background(), "a1", root); r.Error == "" || r.TotalBytes != 0 {
			t.Errorf("%s measured: %+v", root, r)
		}
	}
}
