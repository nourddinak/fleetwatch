package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
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

func TestRegistryLogins(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	r := NewRegistries(d, box)
	if _, err := r.Save(Registry{Host: "ghcr.io", Username: "me"}); err == nil {
		t.Fatal("saved without a password")
	}
	r.Save(Registry{Host: "https://GHCR.io/", Username: "me", Password: "old"})
	r.Save(Registry{Host: "ghcr.io", Username: "me", Password: "ghp_new"}) // same login: replaced
	r.Save(Registry{Host: "index.docker.io", Username: "hub", Password: "dckr"})
	list := r.List()
	if len(list) != 2 || list[0].Password != "" || !list[0].HasPassword || list[1].Host != "docker.io" {
		t.Fatalf("%+v", list)
	}
	if u, p := r.cred("ghcr.io"); u != "me" || p != "ghp_new" {
		t.Fatal(u, p)
	}
	if u, _ := r.cred("registry-1.docker.io"); u != "hub" {
		t.Fatal("Docker Hub login not found by the update check's host")
	}

	// A private image: the token comes only with the login.
	var host string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/token":
			if u, p, ok := req.BasicAuth(); !ok || u != "me" || p != "ghp_new" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"token":"t2"}`))
		case req.Header.Get("Authorization") != "Bearer t2":
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+host+`/token",service="reg",scope="repository:me/private:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Header().Set("Docker-Content-Digest", "sha256:p")
		}
	}))
	defer srv.Close()
	host = strings.TrimPrefix(srv.URL, "https://")
	cred := func(string) (string, string) { return r.cred("ghcr.io") }
	if dg, err := latestDigest(context.Background(), srv.Client(), host+"/me/private:v1", cred); err != nil || dg != "sha256:p" {
		t.Fatal(dg, err)
	}
	if _, err := latestDigest(context.Background(), srv.Client(), host+"/me/private:v1", nil); err == nil {
		t.Fatal("a private image answered without a login")
	}
	if r.Delete(list[0].ID) != nil || len(r.List()) != 1 {
		t.Fatal("delete")
	}
}
