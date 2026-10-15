package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckServerURL(t *testing.T) {
	ok := map[string]string{
		"https://ops.example.com":   "https://ops.example.com",
		"https://ops.example.com/ ": "https://ops.example.com",
		"http://127.0.0.1:8090":     "http://127.0.0.1:8090",
		"http://localhost:8090":     "http://localhost:8090",
		"http://[::1]:8090":         "http://[::1]:8090",
	}
	for in, want := range ok {
		if got, err := CheckServerURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ops.example.com", "http://ops.example.com", "ftp://x", "https://user:pw@x", "https://x?a=1", "http://10.0.0.1"} {
		if _, err := CheckServerURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if _, err := Load(path); err == nil {
		t.Fatal("missing file loaded")
	}
	c := Config{ServerURL: "https://x", ServerID: "srv_1", Credential: "fwa.a.b", AllowCustomCommands: true}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got != c {
		t.Fatalf("load: %+v %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
}
