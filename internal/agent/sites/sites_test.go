package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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

	// A config set in the dashboard wins over the repository's Caddyfile, and comes back.
	custom := "shop.example.com {\n\troot * /var/www/shop\n\theader X-Custom yes\n\tfile_server\n}"
	res = Deploy(context.Background(), proto.SiteDeploy{RunID: "r2b", Name: "shop", Archive: srv.URL, Token: "ghs_x", Dir: "dist", Domains: req.Domains, CaddyConfig: custom})
	if !res.OK || res.Caddy != "custom" || !strings.Contains(res.CaddyConfig, "X-Custom") {
		t.Fatalf("%+v", res)
	}
	if site, _ := os.ReadFile(filepath.Join(CaddyDir, "shop.caddy")); !strings.Contains(string(site), "X-Custom") {
		t.Fatalf("%s", site)
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

func TestStripPrefix(t *testing.T) {
	conf := "{\n\temail me@x.io\n}\n(common) {\n\tencode gzip\n}\n# a.io {\na.io, www.a.io {\n\troot * /var/www/a\n\t@assets path /assets/* # cached\n\theader @assets {\n\t\tCache-Control max-age=60\n\t}\n\tfile_server\n}\nb.io {\n\timport common\n}"
	got := stripPrefix(conf, "a")
	if n := strings.Count(got, "uri strip_prefix /sites/a"); n != 2 {
		t.Fatalf("%d site blocks got it:\n%s", n, got)
	}
	if !strings.Contains(got, "a.io, www.a.io {\n\turi strip_prefix /sites/a\n") || !strings.Contains(got, "b.io {\n\turi strip_prefix /sites/a\n") {
		t.Fatalf("%s", got)
	}
	if again := stripPrefix(got, "a"); again != got {
		t.Fatal("added twice")
	}
}

func TestDockerDeploy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sites are published on Linux servers (symlinks)")
	}
	root := t.TempDir()
	WWW, Caddyfile, CaddyDir = filepath.Join(root, "www"), filepath.Join(root, "Caddyfile"), filepath.Join(root, "sites")
	os.MkdirAll(WWW, 0o755)
	os.WriteFile(Caddyfile, []byte(""), 0o644)
	validate = func() ([]byte, error) { return nil, nil }
	reload = func() ([]byte, error) { return nil, nil }
	var calls []string
	var composeImageOverride string
	dockerCmd = func(_ context.Context, dir string, _ time.Duration, log *bytes.Buffer, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-f" && strings.HasPrefix(filepath.Base(args[i+1]), ".fleetwatch-compose-") {
				if b, err := os.ReadFile(filepath.Join(dir, args[i+1])); err == nil {
					composeImageOverride = string(b)
				}
			}
		}
		return nil
	}
	snap := tarball(t, map[string]string{"me-api-abc/Dockerfile": "FROM nginx\nEXPOSE 80", "me-api-abc/compose.yaml": "services: {}"})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(snap) }))
	defer srv.Close()
	client = srv.Client()

	req := proto.SiteDeploy{RunID: "r1", Name: "api", Archive: srv.URL + "/t", Domains: []string{"api.example.com"},
		Docker: &proto.SiteDocker{Port: 80, HostPort: 20001, Env: "SECRET=1\n", ImageBase: "ghcr.io/me/api", ImageTag: strings.Repeat("a", 40)}}
	res := Deploy(context.Background(), req)
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	all := strings.Join(calls, "\n")
	if !strings.Contains(all, "pull ghcr.io/me/api:"+strings.Repeat("a", 40)) ||
		strings.Contains(all, "build -t fw-api:") ||
		!strings.Contains(all, "-p 127.0.0.1:20001:80") || !strings.Contains(all, "--env-file") {
		t.Fatal(all)
	}
	env := filepath.Join(WWW, "api", ".env")
	if st, err := os.Stat(env); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal(".env", err)
	}
	site, _ := os.ReadFile(filepath.Join(CaddyDir, "api.caddy"))
	if !strings.Contains(string(site), "api.example.com {") || !strings.Contains(string(site), "reverse_proxy 127.0.0.1:20001") {
		t.Fatalf("%s", site)
	}

	// Compose services built by Actions are pulled with their immutable commit tags.
	calls = nil
	oldComposeBuildServices := composeBuildServices
	composeBuildServices = func(context.Context, string, string) ([]string, error) { return []string{"web"}, nil }
	t.Cleanup(func() { composeBuildServices = oldComposeBuildServices })
	req.Docker = &proto.SiteDocker{Compose: "compose.yaml", ImageBase: "ghcr.io/me/api", ImageTag: strings.Repeat("b", 40)}
	if res := Deploy(context.Background(), req); !res.OK ||
		!strings.Contains(strings.Join(calls, "\n"), "compose -p fw-api -f compose.yaml -f .fleetwatch-compose-") ||
		!strings.Contains(strings.Join(calls, "\n"), "pull") ||
		!strings.Contains(strings.Join(calls, "\n"), "up -d --remove-orphans --no-build") {
		t.Fatalf("%+v %v", res, calls)
	}
	if !strings.Contains(composeImageOverride, `"image":"ghcr.io/me/api:`+strings.Repeat("b", 40)+`-web"`) ||
		!strings.Contains(composeImageOverride, `"build":null`) {
		t.Fatalf("Compose image override: %s", composeImageOverride)
	}
	// If pulling a private GHCR image fails (e.g. unauthorized), fall back to building on the server.
	calls = nil
	oldDockerCmd := dockerCmd
	dockerCmd = func(ctx context.Context, dir string, d time.Duration, log *bytes.Buffer, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if slices.Contains(args, "pull") {
			return errors.New("docker compose failed: unauthorized")
		}
		return nil
	}
	if res := Deploy(context.Background(), req); !res.OK ||
		!strings.Contains(strings.Join(calls, "\n"), "compose -p fw-api -f compose.yaml up -d --build --remove-orphans") {
		t.Fatalf("fallback build did not run: %+v %v", res, calls)
	}
	dockerCmd = oldDockerCmd
	if _, err := os.Stat(filepath.Join(CaddyDir, "api.caddy")); err == nil {
		t.Fatal("the Caddy site stayed without a port")
	}
	req.Docker.Compose = "../evil.yml"
	if res := Deploy(context.Background(), req); res.OK {
		t.Fatal("a compose file outside the snapshot")
	}

	// A Docker app's repository Caddyfile takes precedence over generated proxy config
	// and stays in the Docker build context.
	snap = tarball(t, map[string]string{
		"me-api-def/Dockerfile": "FROM nginx\nEXPOSE 80",
		"me-api-def/Caddyfile":  "api.example.com {\n\treverse_proxy 127.0.0.1:20001\n}",
	})
	req.Docker = &proto.SiteDocker{Port: 80, HostPort: 20001}
	res = Deploy(context.Background(), req)
	if !res.OK || res.Caddy != "repository" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(WWW, "api", "Caddyfile")); err != nil {
		t.Fatal("a Docker app's Caddyfile must stay in its build context")
	}
	site, _ = os.ReadFile(filepath.Join(CaddyDir, "api.caddy"))
	if !strings.Contains(string(site), "from the Caddyfile in its repository") ||
		!strings.Contains(string(site), "reverse_proxy 127.0.0.1:20001") ||
		strings.Contains(string(site), "uri strip_prefix") {
		t.Fatalf("repository Caddyfile was not used as-is: %s", site)
	}
}
