package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// With "2FA code for dangerous actions" on, they ask for the code (the password here:
// no 2FA), refuse a wrong one, and run with the right one.
func TestConfirmDangerous(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	st, cur := c.do("GET", "/api/settings/security", nil)
	if st != 200 {
		t.Fatal(st, cur)
	}
	cur["confirmDangerous"] = true
	if st, out := c.do("PUT", "/api/settings/security", cur); st != 200 {
		t.Fatal(st, out)
	}
	send := func(method, path, confirm string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Origin", srv.URL)
		if confirm != "" {
			req.Header.Set("X-Confirm", confirm)
		}
		res, err := c.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, body := send("DELETE", "/api/members/nope", ""); st != 428 || !strings.Contains(body, `"kind":"password"`) {
		t.Fatal("asked for", st, body)
	}
	if st, body := send("DELETE", "/api/members/nope", "wrong-password-1"); st == 428 || st < 400 || strings.Contains(body, "not found") {
		t.Fatal("a wrong password ran it", st, body)
	}
	if st, body := send("DELETE", "/api/members/nope", creds["password"]); st == 428 || st == 403 || st == 401 {
		t.Fatal("the right password was refused", st, body)
	}
	if st, _ := c.do("GET", "/api/servers", nil); st != 200 {
		t.Fatal("everything else as before", st)
	}
	if st, _ := c.do("PUT", "/api/settings/security", cur); st != 428 {
		t.Fatal("turning it off needs the code too", st)
	}
}
