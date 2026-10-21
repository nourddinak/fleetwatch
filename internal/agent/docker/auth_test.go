package docker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
)

func TestWithAuth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".docker"), 0o700)
	os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(`{"auths":{"registry.example.com":{"auth":"bWU6cHc="}},"credsStore":"pass"}`), 0o600)
	if ctx, _ := WithAuth(context.Background(), nil); ConfigEnv(ctx) != nil {
		t.Fatal("a config without logins")
	}
	ctx, done := WithAuth(context.Background(), []proto.RegistryAuth{{Host: "ghcr.io", Username: "me", Password: "ghp_x"}, {Host: "docker.io", Username: "hub", Password: "d"}})
	env := ConfigEnv(ctx)
	if len(env) != 1 || !strings.HasPrefix(env[0], "DOCKER_CONFIG=") {
		t.Fatal(env)
	}
	dir := strings.TrimPrefix(env[0], "DOCKER_CONFIG=")
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	var conf struct {
		Auths      map[string]struct{ Auth string }
		CredsStore string
	}
	json.Unmarshal(b, &conf)
	if conf.Auths["ghcr.io"].Auth != "bWU6Z2hwX3g=" || conf.Auths["https://index.docker.io/v1/"].Auth == "" || conf.Auths["registry.example.com"].Auth == "" || conf.CredsStore != "pass" {
		t.Fatalf("%s", b)
	}
	done()
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the logins stayed on disk")
	}
}
