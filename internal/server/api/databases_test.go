package api

import "testing"

// Databases: owner only, and only after the password is entered again.
func TestDatabasesNeedPassword(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)
	if st, out := owner.do("GET", "/api/servers/srv_x/db", nil); st != 403 || out["code"] != "password_required" {
		t.Fatalf("opened without the password: %d %v", st, out)
	}
	if st, _ := owner.do("POST", "/api/db/unlock", map[string]string{"password": "wrong-password"}); st == 200 {
		t.Fatal("unlocked with a wrong password")
	}
	if st, out := owner.do("POST", "/api/db/unlock", map[string]string{"password": "admin-password-1"}); st != 200 {
		t.Fatalf("unlock: %d %v", st, out)
	}
	if st, out := owner.do("GET", "/api/servers/srv_x/db", nil); out["code"] == "password_required" {
		t.Fatalf("still locked: %d %v", st, out)
	}
	// Another sign-in of the owner is not unlocked by this one.
	other := newClient(t, srv)
	other.do("POST", "/api/auth/login", creds)
	if _, out := other.do("GET", "/api/servers/srv_x/db", nil); out["code"] != "password_required" {
		t.Fatal("the unlock reached another session")
	}
	owner.do("POST", "/api/members", map[string]any{"username": "ops", "role": "operator", "password": "member-password-1"})
	ops := newClient(t, srv)
	ops.do("POST", "/api/auth/login", map[string]string{"username": "ops", "password": "member-password-1"})
	if st, _ := ops.do("POST", "/api/db/unlock", map[string]string{"password": "member-password-1"}); st != 403 {
		t.Fatalf("a member unlocked the databases: %d", st)
	}
}
