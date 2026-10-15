package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/commands"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/notify"
	"fleetwatch/internal/server/push"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/tokens"
)

var ui = fstest.MapFS{
	"index.html":      {Data: []byte("<html>ui</html>")},
	"assets/app-1.js": {Data: []byte("js")},
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(make([]byte, 32))
	svc := auth.NewService(d, box)
	h, _ := auth.HashPassword("admin-password-1")
	if _, err := svc.EnsureAdmin("admin", h); err != nil {
		t.Fatal(err)
	}
	store := metrics.NewStore(d, metrics.ServerKind)
	bus := live.NewBus()
	hub := fleet.NewHub(fleet.NewRegistry(d), store, bus, "test", func(string) proto.Config { return proto.Config{MetricsIntervalSec: 15} })
	appStore := metrics.NewStore(d, metrics.AppKind)
	appSvc := apps.New(d, bus, appStore, hub.Connected)
	hub.OnInventory = appSvc.Ingest
	logStore := logs.NewStore(d)
	hub.OnLogs = func(id string, l []proto.LogLine) { logStore.Submit(id, l) }
	ctx, cancel := context.WithCancel(context.Background())
	go logStore.Run(ctx)
	cmdSvc := commands.New(d, appSvc, bus, hub.Send)
	hub.Handlers[proto.TypeExecResult] = func(id string, data json.RawMessage) {
		var res proto.ExecResult
		json.Unmarshal(data, &res)
		cmdSvc.Result(id, res)
	}
	incStore := incidents.NewStore(d, bus)
	detector := incidents.NewDetector(incStore, d, hub.Connected, func(string) string { return "" })
	appSvc.OnViews = func(id string, v []apps.View) { detector.Apps(id, v, time.Now()) }
	srv := httptest.NewTLSServer(New(Deps{UI: ui, Version: "test", DB: d, Auth: svc, Fleet: hub, Metrics: store, Bus: bus, Apps: appSvc, AppMetrics: appStore, Logs: logStore, Commands: cmdSvc,
		Incidents: incStore, Detector: detector, Notify: notify.New(d, box, bus), Tokens: tokens.New(d), Push: push.New(d, box)}))
	t.Cleanup(func() { cancel(); hub.Shutdown(); srv.Close() })
	return srv
}

type tc struct {
	t   *testing.T
	srv *httptest.Server
	c   *http.Client
}

func newClient(t *testing.T, srv *httptest.Server) *tc {
	c := srv.Client()
	jar, _ := cookiejar.New(nil)
	c = &http.Client{Transport: c.Transport, Jar: jar}
	return &tc{t, srv, c}
}

// do sends a same-origin request (or one with the given Origin) and returns status + JSON body.
func (c *tc) do(method, path string, body any, origin ...string) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, rd)
	req.Header.Set("Origin", c.srv.URL)
	if len(origin) > 0 {
		req.Header.Set("Origin", origin[0])
	}
	res, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

func (c *tc) list(path string) []map[string]any {
	c.t.Helper()
	res, err := c.c.Get(c.srv.URL + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out []map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return out
}

var creds = map[string]string{"username": "admin", "password": "admin-password-1"}

func TestStaticAndHeaders(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	cases := []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/", "<html>ui</html>", "no-cache", 200},
		{"/assets/app-1.js", "js", "immutable", 200},
		{"/api/health", "test", "no-store", 200},
		{"/api/nope", "not found", "no-store", 404},
	}
	for _, x := range cases {
		res, err := c.c.Get(srv.URL + x.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != x.wantStatus || !strings.Contains(string(body), x.wantBody) {
			t.Errorf("%s: %d %q", x.path, res.StatusCode, body)
		}
		if !strings.Contains(res.Header.Get("Cache-Control"), x.wantCache) {
			t.Errorf("%s: cache %q", x.path, res.Header.Get("Cache-Control"))
		}
		if res.Header.Get("Content-Security-Policy") == "" || res.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: missing security headers", x.path)
		}
	}
	// Path traversal must stay inside the embedded FS.
	rec := httptest.NewRecorder()
	New(Deps{UI: ui}).ServeHTTP(rec, httptest.NewRequest("GET", "/../../etc/passwd", nil))
	if strings.Contains(rec.Body.String(), "root:") {
		t.Fatal("traversal leaked a host file")
	}
}

func TestCSRFAndAuthRequired(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if st, _ := c.do("GET", "/api/sessions", nil); st != 401 {
		t.Fatalf("unauthenticated sessions: %d", st)
	}
	if st, b := c.do("POST", "/api/auth/login", creds, "https://evil.example"); st != 403 || b["code"] != "forbidden_origin" {
		t.Fatalf("cross-origin login: %d %v", st, b)
	}
	if st, _ := c.do("POST", "/api/auth/login", creds, ""); st != 403 {
		t.Fatalf("login without Origin: %d", st)
	}
	if st, _ := c.do("POST", "/api/auth/login", map[string]any{"username": "admin", "password": "x", "admin": true}); st != 400 {
		t.Fatalf("unknown field accepted: %d", st)
	}
	if st, b := c.do("POST", "/api/auth/login", creds); st != 200 || b["status"] != "ok" {
		t.Fatalf("login: %d %v", st, b)
	}
	// Signed in, a cross-origin write is still refused.
	if st, _ := c.do("POST", "/api/sessions/revoke-others", nil, "http://evil.example"); st != 403 {
		t.Fatalf("cross-origin authed write: %d", st)
	}
}

func TestLoginSessionsLogout(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if st, b := c.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "nope"}); st != 401 || b["code"] != "invalid_credentials" {
		t.Fatalf("bad login: %d %v", st, b)
	}
	if st, _ := c.do("POST", "/api/auth/login", creds); st != 200 {
		t.Fatal("login failed")
	}
	st, me := c.do("GET", "/api/auth/me", nil)
	if st != 200 || me["username"] != "admin" || me["twofaEnabled"] != false {
		t.Fatalf("me: %d %v", st, me)
	}
	if st, b := c.do("PUT", "/api/account", map[string]string{"displayName": "Bob", "email": "not-an-email"}); st != 400 || b["code"] != "invalid_input" {
		t.Fatalf("bad email: %d %v", st, b)
	}
	if st, b := c.do("PUT", "/api/settings/security", map[string]any{"sessionTimeoutMin": 1, "lockoutAfter": 5}); st != 400 {
		t.Fatalf("bad security settings: %d %v", st, b)
	}
	if len(c.list("/api/audit/logins")) == 0 {
		t.Fatal("login history is empty")
	}

	// A second browser; the first can see and revoke it.
	c2 := newClient(t, srv)
	c2.do("POST", "/api/auth/login", creds)
	list := c.list("/api/sessions")
	if len(list) != 2 {
		t.Fatalf("sessions: %v", list)
	}
	for _, s := range list {
		if s["current"] == false {
			if st, _ := c.do("DELETE", "/api/sessions/"+s["id"].(string), nil); st != 200 {
				t.Fatal("revoke failed")
			}
		}
	}
	if st, _ := c2.do("GET", "/api/auth/me", nil); st != 401 {
		t.Fatalf("revoked browser still signed in: %d", st)
	}
	if st, _ := c.do("DELETE", "/api/sessions/doesnotexist", nil); st != 404 {
		t.Fatalf("revoke unknown: %d", st)
	}
	if st, _ := c.do("POST", "/api/auth/logout", nil); st != 200 {
		t.Fatal("logout failed")
	}
	if st, _ := c.do("GET", "/api/auth/me", nil); st != 401 {
		t.Fatal("still signed in after logout")
	}
}

func TestLockout(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	for i := 0; i < 5; i++ {
		c.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "nope"})
	}
	st, b := c.do("POST", "/api/auth/login", creds)
	if st != 429 || b["code"] != "locked" {
		t.Fatalf("expected lockout: %d %v", st, b)
	}
}

func totpNow(t *testing.T, secretB32 string) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(time.Now().Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

func TestTwoFactorOverHTTP(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	st, setup := c.do("POST", "/api/2fa/setup", map[string]string{"password": "admin-password-1"})
	if st != 200 || !strings.HasPrefix(setup["qr"].(string), "data:image/png;base64,") {
		t.Fatalf("setup: %d %v", st, setup)
	}
	st, en := c.do("POST", "/api/2fa/enable", map[string]string{"code": totpNow(t, setup["secret"].(string))})
	if st != 200 || len(en["recoveryCodes"].([]any)) != 10 {
		t.Fatalf("enable: %d %v", st, en)
	}
	codes := en["recoveryCodes"].([]any)

	// New browser: password only gets a pending session that cannot reach the API.
	c2 := newClient(t, srv)
	if st, b := c2.do("POST", "/api/auth/login", creds); st != 200 || b["status"] != "2fa" {
		t.Fatalf("login with 2FA: %d %v", st, b)
	}
	if st, b := c2.do("GET", "/api/auth/me", nil); st != 401 || b["code"] != "mfa_required" {
		t.Fatalf("pending me: %d %v", st, b)
	}
	if st, _ := c2.do("GET", "/api/sessions", nil); st != 401 {
		t.Fatalf("pending session reached API: %d", st)
	}
	if st, _ := c2.do("POST", "/api/auth/2fa", map[string]any{"code": "000000"}); st != 400 {
		t.Fatalf("bad code: %d", st)
	}
	if st, _ := c2.do("POST", "/api/auth/2fa", map[string]any{"code": codes[0], "recovery": true}); st != 200 {
		t.Fatalf("recovery code: %d", st)
	}
	if st, b := c2.do("GET", "/api/auth/me", nil); st != 200 || b["recoveryCodesLeft"] != float64(9) {
		t.Fatalf("after 2FA: %d %v", st, b)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct{ remote, xff, cf, want string }{
		{"127.0.0.1:5000", "198.51.100.7", "", "198.51.100.7"},
		{"127.0.0.1:5000", "10.0.0.1, 198.51.100.7", "", "198.51.100.7"}, // only the proxy-appended entry counts
		{"127.0.0.1:5000", "not-an-ip", "", "127.0.0.1"},
		{"203.0.113.9:5000", "198.51.100.7", "", "203.0.113.9"}, // never trust XFF from a remote peer
		{"[::1]:5000", "2001:db8::1", "", "2001:db8::1"},
		// Behind Cloudflare: Caddy reports a Cloudflare edge, Cloudflare reports the client.
		{"127.0.0.1:5000", "172.70.246.23", "203.0.113.50", "203.0.113.50"},
		{"127.0.0.1:5000", "2606:4700:10::ac43:1", "2001:db8::50", "2001:db8::50"},
		{"162.158.1.1:443", "", "203.0.113.50", "203.0.113.50"}, // no Caddy, Cloudflare straight in
		{"127.0.0.1:5000", "172.70.246.23", "garbage", "172.70.246.23"},
		// Spoofing: the header is ignored unless the request came from Cloudflare.
		{"127.0.0.1:5000", "198.51.100.7", "203.0.113.50", "198.51.100.7"},
		{"203.0.113.9:5000", "", "1.2.3.4", "203.0.113.9"},
		{"127.0.0.1:5000", "172.70.246.23, 198.51.100.7", "1.2.3.4", "198.51.100.7"}, // fake Cloudflare hop in XFF
	}
	for _, x := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = x.remote
		if x.xff != "" {
			r.Header.Set("X-Forwarded-For", x.xff)
		}
		if x.cf != "" {
			r.Header.Set("CF-Connecting-IP", x.cf)
		}
		if got := clientIP(r); got != x.want {
			t.Errorf("%s + XFF %q + CF %q: got %s want %s", x.remote, x.xff, x.cf, got, x.want)
		}
	}
}
