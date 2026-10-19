package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
)

func TestParseRef(t *testing.T) {
	for ref, want := range map[string]string{
		"nginx":                   "registry-1.docker.io library/nginx latest",
		"me/app":                  "registry-1.docker.io me/app latest",
		"docker.io/nginx:1.27":    "registry-1.docker.io library/nginx 1.27",
		"ghcr.io/a/b:1.2":         "ghcr.io a/b 1.2",
		"localhost:5000/x":        "localhost:5000 x latest",
		"nginx@sha256:abc":        "",
		"sha256:0123456789abcdef": "",
		"0123456789ab":            "",
	} {
		r, repo, tag, ok := parseRef(ref)
		got := ""
		if ok {
			got = r + " " + repo + " " + tag
		}
		if got != want {
			t.Errorf("%s: %q, want %q", ref, got, want)
		}
	}
}

func TestImageUpdates(t *testing.T) {
	var host string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token" && r.URL.Query().Get("scope") == "repository:me/app:pull":
			w.Write([]byte(`{"token":"t1"}`))
		case r.Method == http.MethodHead && r.URL.Path == "/v2/me/app/manifests/v1":
			if r.Header.Get("Authorization") != "Bearer t1" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+host+`/token",service="reg",scope="repository:me/app:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:new")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	host = strings.TrimPrefix(srv.URL, "https://")
	ref := host + "/me/app:v1"
	s := New(live.NewBus())
	s.Client = srv.Client()
	rep, _ := json.Marshal(proto.DockerReport{Available: true, Containers: []proto.DockerContainer{
		{ID: "c1", Image: ref, Digests: []string{"sha256:old"}},
		{ID: "c2", Image: ref, Digests: []string{"sha256:new"}},
		{ID: "c3", Image: "0123456789ab"},
	}})
	s.OnReport("srv1", rep)
	s.CheckUpdates(context.Background(), true)
	var got []string
	for i := 0; i < 50; i++ {
		got = nil
		for _, c := range s.State("srv1").Report.Containers {
			got = append(got, c.Update)
		}
		if got[0] != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Join(got, ",") != "available,current," {
		t.Fatal(got)
	}
}
