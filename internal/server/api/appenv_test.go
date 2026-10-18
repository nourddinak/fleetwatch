package api

import "testing"

// .env files: owner only, and the password is asked for every time.
func TestAppEnvNeedsOwnerAndPassword(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)
	owner.do("POST", "/api/members", map[string]any{"username": "ops", "role": "operator", "password": "member-password-1"})
	ops := newClient(t, srv)
	ops.do("POST", "/api/auth/login", map[string]string{"username": "ops", "password": "member-password-1"})

	if st, _ := ops.do("POST", "/api/apps/app_x/env", map[string]string{"password": "member-password-1"}); st != 403 {
		t.Fatalf("a member opened .env files: %d", st)
	}
	for _, m := range []string{"POST", "DELETE"} {
		if st, _ := ops.do(m, "/api/apps/app_x/env/history", map[string]string{"password": "member-password-1"}); st != 403 {
			t.Fatalf("a member reached the history (%s): %d", m, st)
		}
	}
	if st, out := owner.do("DELETE", "/api/apps/app_x/env/history", map[string]string{}); st != 403 || out["code"] != "password_required" {
		t.Fatalf("history delete without password: %d %v", st, out)
	}
	if st, out := owner.do("POST", "/api/apps/app_x/env", map[string]string{}); st != 403 || out["code"] != "password_required" {
		t.Fatalf("no password: %d %v", st, out)
	}
	if st, _ := owner.do("PUT", "/api/apps/app_x/env", map[string]string{"password": "wrong-password", "name": ".env", "content": "A=1"}); st == 200 || st == 404 {
		t.Fatalf("wrong password accepted: %d", st)
	}
	if st, _ := owner.do("POST", "/api/apps/app_x/env", map[string]string{"password": "admin-password-1"}); st != 404 {
		t.Fatalf("right password, unknown app: %d", st)
	}
}
