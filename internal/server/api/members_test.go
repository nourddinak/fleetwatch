package api

import (
	"strings"
	"testing"
)

func TestTeamMembers(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)
	serverA, _, _ := pairAgent(t, owner)
	_, b := owner.do("POST", "/api/servers", map[string]string{"name": "Other"})
	serverB := b["server"].(map[string]any)["id"].(string)

	if st, out := owner.do("POST", "/api/members", map[string]any{"username": "admin", "role": "viewer", "password": "member-password-1"}); st != 400 {
		t.Fatalf("the owner's name was reused: %d %v", st, out)
	}
	st, op := owner.do("POST", "/api/members", map[string]any{"username": "ops", "role": "operator", "servers": []string{serverA}, "password": "member-password-1"})
	if st != 201 || op["role"] != "operator" {
		t.Fatalf("create operator: %d %v", st, op)
	}
	if st, _ := owner.do("POST", "/api/members", map[string]any{"username": "look", "role": "viewer", "password": "member-password-2"}); st != 201 {
		t.Fatal("create viewer")
	}

	ops := newClient(t, srv)
	if st, me := ops.do("POST", "/api/auth/login", map[string]string{"username": "ops", "password": "member-password-1"}); st != 200 {
		t.Fatalf("member login: %d %v", st, me)
	}
	if st, me := ops.do("GET", "/api/auth/me", nil); st != 200 || me["role"] != "operator" || me["username"] != "ops" {
		t.Fatalf("me: %d %v", st, me)
	}
	if list := ops.list("/api/servers"); len(list) != 1 || list[0]["id"] != serverA {
		t.Fatalf("a limited member sees: %v", list)
	}
	for path, want := range map[string]int{
		"/api/servers/" + serverA:         200,
		"/api/servers/" + serverB:         403,
		"/api/members":                    403,
		"/api/tokens":                     403,
		"/api/settings/security":          403,
		"/api/status-pages":               403,
		"/api/metrics?range=1h":           403, // fleet-wide
		"/api/servers/" + serverA + "/db": 403, // databases: owner only, even on its own server
		"/api/servers/" + serverA + "/db/tables?engine=sqlite&db=/x": 403,
		"/api/integrations/docker-registries":                        403,
	} {
		if st, _ := ops.do("GET", path, nil); st != want {
			t.Errorf("operator GET %s: %d, want %d", path, st, want)
		}
	}
	if st, _ := ops.do("PUT", "/api/maintenance", map[string]any{"kind": "server", "id": serverA, "minutes": 30}); st != 200 {
		t.Errorf("operator maintenance on own server: %d", st)
	}
	if st, _ := ops.do("PUT", "/api/maintenance", map[string]any{"kind": "server", "id": serverB, "minutes": 30}); st != 403 {
		t.Errorf("operator maintenance on another server: %d", st)
	}
	if st, _ := ops.do("DELETE", "/api/servers/"+serverA, nil); st != 403 {
		t.Errorf("operator deleted a server: %d", st)
	}
	if _, m := ops.do("GET", "/api/maintenance", nil); m["servers"] == nil || len(m["servers"].(map[string]any)) != 1 {
		t.Errorf("maintenance map not filtered: %v", m)
	}

	look := newClient(t, srv)
	look.do("POST", "/api/auth/login", map[string]string{"username": "look", "password": "member-password-2"})
	if list := look.list("/api/servers"); len(list) != 2 {
		t.Fatalf("a viewer for every server sees %d", len(list))
	}
	if st, out := look.do("PUT", "/api/maintenance", map[string]any{"kind": "server", "id": serverA, "minutes": 0}); st != 403 || !strings.Contains(out["error"].(string), "only look") {
		t.Errorf("viewer wrote: %d %v", st, out)
	}
	if st, _ := look.do("POST", "/api/account/password", map[string]string{"current": "member-password-2", "next": "member-password-3"}); st != 200 {
		t.Errorf("viewer could not change own password: %d", st)
	}

	// Disabling a member ends their session at once.
	id := op["id"].(string)
	if st, _ := owner.do("PUT", "/api/members/"+id, map[string]any{"role": "operator", "disabled": true}); st != 200 {
		t.Fatal("disable")
	}
	if st, _ := ops.do("GET", "/api/servers", nil); st != 401 {
		t.Fatalf("disabled member still signed in: %d", st)
	}
	if st, _ := ops.do("POST", "/api/auth/login", map[string]string{"username": "ops", "password": "member-password-1"}); st == 200 {
		t.Fatal("disabled member signed in")
	}
}
