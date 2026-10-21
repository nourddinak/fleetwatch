package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"

	"fleetwatch/internal/proto"
)

type configKey struct{}

// WithAuth gives the docker commands run with the returned context a Docker config of
// their own: the server's own (its `docker login`s, credential helpers) plus the
// dashboard's registry logins. done deletes it.
func WithAuth(ctx context.Context, auths []proto.RegistryAuth) (_ context.Context, done func()) {
	done = func() {}
	if len(auths) == 0 {
		return ctx, done
	}
	dir, err := os.MkdirTemp("", "fw-docker-")
	if err != nil {
		return ctx, done
	}
	conf := map[string]any{}
	if b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".docker", "config.json")); err == nil {
		json.Unmarshal(b, &conf)
	}
	all, _ := conf["auths"].(map[string]any)
	if all == nil {
		all = map[string]any{}
	}
	for _, a := range auths {
		key := a.Host
		if key == "docker.io" {
			key = "https://index.docker.io/v1/" // how the docker CLI names Docker Hub
		}
		all[key] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password))}
	}
	conf["auths"] = all
	b, _ := json.Marshal(conf)
	if os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600) != nil {
		os.RemoveAll(dir)
		return ctx, done
	}
	return context.WithValue(ctx, configKey{}, dir), func() { os.RemoveAll(dir) }
}

// ConfigEnv is the environment that points docker at WithAuth's config (none without it).
func ConfigEnv(ctx context.Context) []string {
	if dir, ok := ctx.Value(configKey{}).(string); ok {
		return []string{"DOCKER_CONFIG=" + dir}
	}
	return nil
}
