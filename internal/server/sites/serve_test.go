package sites

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServeLocalSite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sites are published on Linux servers (symlinks)")
	}
	s, _, _ := setup(t)
	s.IsLocal = func(string) bool { return true }
	LocalRoot = t.TempDir()
	rel := filepath.Join(LocalRoot, ".fleetwatch", "shop", "20261017-100000")
	os.MkdirAll(filepath.Join(rel, "assets"), 0o755)
	os.WriteFile(filepath.Join(rel, "index.html"), []byte("<h1>shop</h1>"), 0o644)
	os.WriteFile(filepath.Join(rel, "assets", "app.js"), []byte("x"), 0o644)
	os.Symlink(rel, filepath.Join(LocalRoot, "shop"))
	os.MkdirAll(filepath.Join(LocalRoot, "notours"), 0o755) // a folder Fleetwatch did not publish
	os.WriteFile(filepath.Join(LocalRoot, "notours", "index.html"), []byte("private"), 0o644)
	s.save(Site{ID: "site_1", Name: "shop", ServerID: "s1", Plan: Plan{Kind: "build", SPA: true}})
	s.save(Site{ID: "site_2", Name: "notours", ServerID: "s1", Plan: Plan{Kind: "static"}})

	h := s.Files()
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w
	}
	if w := get("/sites/shop"); w.Code != http.StatusMovedPermanently {
		t.Fatal(w.Code)
	}
	w := get("/sites/shop/")
	if w.Code != 200 || w.Body.String() != "<h1>shop</h1>" || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox") ||
		strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatalf("%d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := get("/sites/shop/assets/app.js"); w.Code != 200 || w.Body.String() != "x" {
		t.Fatal(w.Code)
	}
	if w := get("/sites/shop/products/42"); w.Code != 200 || w.Body.String() != "<h1>shop</h1>" {
		t.Fatalf("single-page fallback: %d", w.Code)
	}
	for _, p := range []string{"/sites/shop/../../../etc/passwd", "/sites/notours/", "/sites/missing/", "/sites/shop/%2e%2e/notours/index.html"} {
		if w := get(p); w.Code == 200 && !strings.Contains(w.Body.String(), "<h1>shop</h1>") {
			t.Errorf("%s served %q", p, w.Body.String())
		}
	}
	if w := get("/sites/notours/"); w.Code != 404 {
		t.Fatalf("a folder Fleetwatch did not make was served: %d", w.Code)
	}
	_ = context.Background()
}

func TestBuildUnderPath(t *testing.T) {
	p := underPath(Plan{Kind: "build", Framework: "Vite", Install: "npm ci", Build: "npm run build", Output: "dist", SPA: true}, "/sites/shop/")
	wf := workflow(p, "main")
	for _, want := range []string{"npm run build -- --base='/sites/shop/'", `BASE_PATH: "/sites/shop/"`, `PUBLIC_URL: "/sites/shop"`} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q:\n%s", want, wf)
		}
	}
	if !strings.Contains(p.Note, "basename") {
		t.Error("no router note")
	}
	if p := underPath(Plan{Kind: "build", Framework: "Vite", Build: "pnpm run build"}, "/sites/x/"); !strings.Contains(workflow(p, "main"), "pnpm run build --base='/sites/x/'") {
		t.Error("pnpm base option")
	}
	if p := underPath(Plan{Kind: "build", Framework: "Next.js (static export)", Build: "npm run build"}, "/sites/x/"); !strings.Contains(p.Note, "basePath") {
		t.Error("no Next.js note")
	}
}

func TestRelayRemoteSite(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.URL.Path == "/docs" {
			http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "x", Value: "y"})
		w.Write([]byte("remote " + r.URL.Path))
	}))
	defer upstream.Close()
	old := relayTransport
	relayTransport = upstream.Client().Transport.(*http.Transport).Clone()
	relayTransport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	}
	relayTransport.TLSClientConfig.ServerName = "example.com" // the test certificate's name
	defer func() { relayTransport = old }()

	s, _, _ := setup(t)
	s.PublicIP = func(string) string { return "203.0.113.7" }
	s.save(Site{ID: "site_1", Name: "shop", ServerID: "s1", Plan: Plan{Kind: "build"}})
	if st, _ := s.Get("site_1"); !st.AtPath || st.Preview != "shop.203-0-113-7.sslip.io" {
		t.Fatalf("%+v", st)
	}
	h := s.Files()
	req := httptest.NewRequest(http.MethodGet, "/sites/shop/assets/app.js?v=1", nil)
	req.Header.Set("Cookie", "session=secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || got == nil {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
	if w.Body.String() != "remote /assets/app.js" || got.Host != "shop.203-0-113-7.sslip.io" || got.URL.RawQuery != "v=1" {
		t.Fatalf("%d %q host=%s", w.Code, w.Body.String(), got.Host)
	}
	if got.Header.Get("Cookie") != "" || w.Header().Get("Set-Cookie") != "" || !strings.HasPrefix(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("cookies or sandbox: %v / %v", got.Header, w.Header())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sites/shop/docs", nil))
	if w.Header().Get("Location") != "/sites/shop/docs/" {
		t.Fatalf("redirect: %q", w.Header().Get("Location"))
	}
	// No public IPv4: nothing to relay from.
	s.PublicIP = func(string) string { return "" }
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sites/shop/", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
