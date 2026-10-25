package api

import "testing"

// A client sees their portal and their own account; nothing of the dashboard.
func TestClientPortal(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)
	if st, _ := owner.do("POST", "/api/members", map[string]any{"username": "acme", "role": "client", "password": "client-password-1"}); st == 201 {
		t.Fatal("a client without anything to see")
	}
	st, m := owner.do("POST", "/api/members", map[string]any{"username": "acme", "displayName": "Acme Ltd", "role": "client", "password": "client-password-1",
		"servers": []string{"srv_x"}, "portal": map[string]any{"apps": []string{"app_missing"}, "checks": []string{}, "pages": []string{}}})
	if st != 201 || m["role"] != "client" || len(m["servers"].([]any)) != 0 {
		t.Fatalf("create client: %d %v", st, m)
	}
	acme := newClient(t, srv)
	acme.do("POST", "/api/auth/login", map[string]string{"username": "acme", "password": "client-password-1"})
	if st, out := acme.do("GET", "/api/portal", nil); st != 200 || out["name"] != "Acme Ltd" {
		t.Fatalf("portal: %d %v", st, out)
	}
	for _, path := range []string{"/api/servers", "/api/apps", "/api/incidents", "/api/notifications", "/api/status-pages", "/api/activity", "/api/members", "/ws", "/api/timeline?server=srv_x"} {
		if st, _ := acme.do("GET", path, nil); st != 403 {
			t.Errorf("client GET %s: %d", path, st)
		}
	}
	if st, _ := acme.do("POST", "/api/account/password", map[string]string{"current": "client-password-1", "new": "client-password-2"}); st == 403 {
		t.Error("a client cannot change their own password")
	}
	// The owner can look at a client's portal; others' portals do not exist for a client.
	if st, _ := owner.do("GET", "/api/portal?member="+m["id"].(string), nil); st != 200 {
		t.Fatal("owner preview")
	}
}
