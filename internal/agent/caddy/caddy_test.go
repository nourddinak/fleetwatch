package caddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fleetwatch/internal/proto"
)

func TestCaddyStaticSites(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte(`import sites/*.caddy
import common
`), 0o644)
	os.MkdirAll(filepath.Join(dir, "sites"), 0o755)
	os.WriteFile(filepath.Join(dir, "sites", "admin.caddy"), []byte(`admin.example.com {
	root * /var/www/openapps_admin/dist/
	try_files {path} /index.html
	file_server
}
docs.example.com {
	root * {env.DOCS}
	file_server
}
`), 0o644)
	routes := parseFile(filepath.Join(dir, "Caddyfile"), 0)
	if len(routes) != 1 || routes[0].Host != "admin.example.com" || routes[0].Root != "/var/www/openapps_admin/dist" {
		t.Fatalf("caddyfile: %+v", routes)
	}

	cfg := `{"apps":{"http":{"servers":{"srv0":{"routes":[
		{"match":[{"host":["admin.example.com"]}],"handle":[{"handler":"subroute","routes":[
			{"handle":[{"handler":"vars","root":"/var/www/openapps_admin/dist"}]},
			{"handle":[{"handler":"file_server","root":"/var/www/openapps_admin/dist"}]}]}]}
	]}}}}}`
	routes = ParseJSONConfig([]byte(cfg))
	if len(routes) != 1 || routes[0].Root != "/var/www/openapps_admin/dist" || routes[0].Port != 0 {
		t.Fatalf("json: %+v", routes)
	}
}

func TestLocalCerts(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler()) // certificate for example.com
	defer srv.Close()
	c := &CertCache{Addr: srv.Listener.Addr().String()}
	got := c.Certs(context.Background(), []proto.Route{
		{Host: "example.com"}, {Host: "example.com"}, // duplicates are checked once
		{Host: "shop.example.org"},                                           // not covered by the certificate
		{Host: "app.localhost"}, {Host: "10.0.0.1"}, {Host: "*.example.com"}, // never checked
	})
	if len(got) != 2 {
		t.Fatalf("checked %d names: %+v", len(got), got)
	}
	if got[0].Host != "example.com" || got[0].NotAfter == 0 || got[0].Error != "" {
		t.Fatalf("valid name: %+v", got[0])
	}
	if got[1].Error == "" || got[1].NotAfter == 0 {
		t.Fatalf("uncovered name: %+v", got[1])
	}
	// Cached: a second call does not dial again even if the server is gone.
	srv.Close()
	if again := c.Certs(context.Background(), []proto.Route{{Host: "example.com"}}); len(again) != 2 {
		t.Fatal("results not cached")
	}
}
