package caddy

import (
	"os"
	"path/filepath"
	"testing"
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
