package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
)

func tarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if strings.HasSuffix(name, "@link") {
			tw.WriteHeader(&tar.Header{Name: strings.TrimSuffix(name, "@link"), Typeflag: tar.TypeSymlink, Linkname: body})
			continue
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestDeployAndRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sites are published on Linux servers (symlinks)")
	}
	root := t.TempDir()
	WWW, Caddyfile, CaddyDir = filepath.Join(root, "www"), filepath.Join(root, "Caddyfile"), filepath.Join(root, "sites")
	os.MkdirAll(WWW, 0o755)
	os.WriteFile(Caddyfile, []byte("example.com {\n\trespond hi\n}"), 0o644)
	reloads := 0
	validate = func() ([]byte, error) { return nil, nil }
	reload = func() ([]byte, error) { reloads++; return nil, nil }

	snap := tarball(t, map[string]string{
		"me-shop-abc/README.md":          "not published",
		"me-shop-abc/dist/index.html":    "<h1>shop</h1>",
		"me-shop-abc/dist/assets/app.js": "console.log(1)",
		"me-shop-abc/dist/../../evil":    "escape",
		"me-shop-abc/dist/link@link":     "/etc/passwd",
		"me-shop-abc/dist/.git/config":   "secret",
	})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token ghs_x" {
			w.WriteHeader(401)
			return
		}
		w.Write(snap)
	}))
	defer srv.Close()
	client = srv.Client()

	req := proto.SiteDeploy{RunID: "r1", Name: "shop", Archive: srv.URL + "/tarball/abc", Token: "ghs_x", Dir: "dist", Domains: []string{"shop.203-0-113-5.sslip.io"}, SPA: true}
	res := Deploy(context.Background(), req)
	if !res.OK || res.Caddy != "generated" {
		t.Fatalf("%+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(WWW, "shop", "index.html")); string(b) != "<h1>shop</h1>" {
		t.Fatal("site not live")
	}
	for _, p := range []string{"README.md", "link", ".git/config"} {
		if _, err := os.Lstat(filepath.Join(WWW, "shop", p)); err == nil {
			t.Errorf("%s was published", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
		t.Fatal("a path left the site folder")
	}
	site, _ := os.ReadFile(filepath.Join(CaddyDir, "shop.caddy"))
	main, _ := os.ReadFile(Caddyfile)
	if !strings.Contains(string(site), "shop.203-0-113-5.sslip.io {") || !strings.Contains(string(site), "try_files") ||
		!strings.Contains(string(main), "import "+CaddyDir+"/*.caddy") || reloads != 1 {
		t.Fatalf("caddy: %s / %s / %d", site, main, reloads)
	}

	// A Caddyfile in the published folder replaces the generated site, and is not served.
	snap = tarball(t, map[string]string{"me-shop-def/dist/index.html": "v2", "me-shop-def/dist/Caddyfile": "shop.example.com {\n\troot * /var/www/shop\n\tfile_server\n}"})
	for i := 0; i < 4; i++ { // more deploys than releases kept
		os.MkdirAll(filepath.Join(WWW, ".fleetwatch", "shop", "20200101-00000"+string(rune('0'+i))), 0o755)
	}
	res = Deploy(context.Background(), proto.SiteDeploy{RunID: "r2", Name: "shop", Archive: srv.URL, Token: "ghs_x", Dir: "dist", Domains: req.Domains})
	if !res.OK || res.Caddy != "repository" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(WWW, "shop", "Caddyfile")); err == nil {
		t.Fatal("the Caddyfile is served")
	}
	if site, _ := os.ReadFile(filepath.Join(CaddyDir, "shop.caddy")); !strings.Contains(string(site), "shop.example.com") {
		t.Fatalf("%s", site)
	}
	if entries, _ := os.ReadDir(filepath.Join(WWW, ".fleetwatch", "shop")); len(entries) != keepReleases {
		t.Fatalf("%d releases kept", len(entries))
	}

	// A folder Fleetwatch did not make is never replaced.
	os.MkdirAll(filepath.Join(WWW, "blog"), 0o755)
	if res := Deploy(context.Background(), proto.SiteDeploy{RunID: "r3", Name: "blog", Archive: srv.URL, Token: "ghs_x"}); res.OK {
		t.Fatal("replaced an existing folder")
	}

	if res := Remove(proto.SiteRemove{RunID: "r4", Name: "shop"}); !res.OK {
		t.Fatal(res)
	}
	for _, p := range []string{filepath.Join(WWW, "shop"), filepath.Join(WWW, ".fleetwatch", "shop"), filepath.Join(CaddyDir, "shop.caddy")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
}
