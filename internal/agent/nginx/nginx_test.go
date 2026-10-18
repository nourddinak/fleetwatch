package nginx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoutes(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sites-enabled"), 0o755)
	os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755)
	os.WriteFile(filepath.Join(dir, "nginx.conf"), []byte(`
events {}
http {
    upstream api_backend { server 127.0.0.1:4000; }
    include conf.d/*.conf;   # relative to the main file
    include /nonexistent/*.conf;
    include sites-enabled/*;
}`), 0o644)
	os.WriteFile(filepath.Join(dir, "conf.d", "api.conf"), []byte(`
server {
    listen 443 ssl;
    server_name api.example.com www.api.example.com;
    location / { proxy_pass http://api_backend; }
}`), 0o644)
	os.WriteFile(filepath.Join(dir, "sites-enabled", "shop"), []byte(`
server {
    listen 80;
    server_name shop.example.com;   # the shop
    root /var/www/shop/;
    location /api/ { proxy_pass http://127.0.0.1:3000/; }
    location / { try_files $uri /index.html; }
}
server {
    listen 80 default_server;
    server_name _;
    return 444;
}
server {
    server_name "app.example.com";
    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
    }
}`), 0o644)
	got := Routes(filepath.Join(dir, "nginx.conf"))
	want := map[string]string{
		"api.example.com":     "port 4000",
		"www.api.example.com": "port 4000",
		"shop.example.com":    "root", // files from a folder; only /api/ goes to an app
		"app.example.com":     "port 8080",
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for _, r := range got {
		if r.Server != "nginx" {
			t.Errorf("%s: server %q", r.Host, r.Server)
		}
		if w, ok := want[r.Host]; !ok || (w == "port 4000" && r.Port != 4000) || (w == "root" && (r.Root != "/var/www/shop" || r.Port != 0)) || (w == "port 8080" && r.Port != 8080) {
			t.Errorf("%+v", r)
		}
	}
	// A static site: root and no forwarding.
	os.WriteFile(filepath.Join(dir, "sites-enabled", "shop"), []byte(`server { server_name static.example.com; root /srv/static; location / { try_files $uri =404; } }`), 0o644)
	for _, r := range Routes(filepath.Join(dir, "nginx.conf")) {
		if r.Host == "static.example.com" && (r.Root != "/srv/static" || r.Port != 0) {
			t.Fatalf("%+v", r)
		}
	}
	if r := Routes(filepath.Join(dir, "missing.conf")); len(r) != 0 {
		t.Fatal(r)
	}
}
