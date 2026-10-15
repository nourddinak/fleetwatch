package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAPITokens(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	pairAgent(t, c)

	if st, _ := c.do("POST", "/api/tokens", map[string]string{"name": "grafana", "password": "wrong-password"}); st == 201 {
		t.Fatal("token created without the right password")
	}
	st, out := c.do("POST", "/api/tokens", map[string]string{"name": "grafana", "password": "admin-password-1"})
	token, _ := out["token"].(string)
	if st != 201 || !strings.HasPrefix(token, "fwt_") {
		t.Fatalf("create: %d %v", st, out)
	}
	list := c.list("/api/tokens")
	if len(list) != 1 || strings.Contains(strings.Join([]string{list[0]["prefix"].(string)}, ""), token) {
		t.Fatalf("list: %v", list)
	}

	call := func(method, path, tok string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := srv.Client().Do(req) // no cookies: the token alone
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, _ := call("GET", "/api/servers", token); st != 200 {
		t.Fatalf("read with token: %d", st)
	}
	for _, path := range []string{"/api/sessions", "/api/tokens", "/api/audit", "/api/auth/me"} {
		if st, _ := call("GET", path, token); st != 403 && st != 401 {
			t.Errorf("%s with token: %d, want it refused", path, st)
		}
	}
	if st, _ := call("POST", "/api/servers", token); st != 403 {
		t.Errorf("write with token: %d, want 403", st)
	}
	if st, _ := call("GET", "/api/servers", "fwt_not-a-real-token"); st != 401 {
		t.Errorf("bad token: %d, want 401", st)
	}
	st, body := call("GET", "/metrics", token)
	if st != 200 || !strings.Contains(body, `fleetwatch_info{version="test"} 1`) || !strings.Contains(body, "fleetwatch_server_up{") || !strings.Contains(body, "# TYPE fleetwatch_incidents_open gauge") {
		t.Fatalf("metrics: %d\n%s", st, body)
	}
	if st, _ := call("GET", "/metrics", ""); st != 401 {
		t.Errorf("metrics without token: %d", st)
	}

	if st, _ := c.do("DELETE", "/api/tokens/"+list[0]["id"].(string), nil); st != 200 {
		t.Fatalf("revoke: %d", st)
	}
	if st, _ := call("GET", "/api/servers", token); st != 401 {
		t.Errorf("revoked token still works: %d", st)
	}
}

func TestPromEscape(t *testing.T) {
	var p prom
	p.metric("x", "gauge", "h", 1, "name", "a\"b\\c\nd")
	if !strings.Contains(p.b.String(), `x{name="a\"b\\c\nd"} 1`) {
		t.Fatalf("escaping: %s", p.b.String())
	}
}
