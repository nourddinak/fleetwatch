package api

import (
	"strings"
	"testing"
)

func TestCommandRunAttributionAndReauth(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)

	// Create an operator member
	st, m := owner.do("POST", "/api/members", map[string]any{"username": "ops_user", "role": "operator", "password": "member-secret-123"})
	if st != 201 && st != 200 {
		t.Fatalf("create member: %d %v", st, m)
	}

	ops := newClient(t, srv)
	if st, _ := ops.do("POST", "/api/auth/login", map[string]string{"username": "ops_user", "password": "member-secret-123"}); st != 200 {
		t.Fatalf("member login failed: %d", st)
	}

	st, srvRes := owner.do("POST", "/api/servers", map[string]string{"name": "VPS-cmd"})
	if st != 201 {
		t.Fatalf("create server: %d %v", st, srvRes)
	}
	serverID := srvRes["server"].(map[string]any)["id"].(string)

	_, appRes := owner.do("POST", "/api/apps", map[string]any{
		"serverId":   serverID,
		"name":       "API",
		"manager":    "pm2",
		"processKey": "api",
		"path":       "/var/www/api",
	})
	appID := appRes["id"].(string)

	// --- 1. Default (ReauthForCommands = false): Check Initiator Attribution ---
	// Operator executes builtin:restart
	ops.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", nil)
	runs := ops.list("/api/apps/" + appID + "/runs")
	if len(runs) == 0 {
		t.Fatal("expected at least 1 run record")
	}
	latest := runs[0]
	initiatedBy, _ := latest["initiatedBy"].(string)
	if !strings.HasPrefix(initiatedBy, "ops_user from") {
		t.Fatalf("expected run initiatedBy to start with 'ops_user from', got %q", initiatedBy)
	}
	if strings.Contains(initiatedBy, "admin") {
		t.Fatalf("operator command run was falsely attributed to owner: %q", initiatedBy)
	}

	// Owner executes builtin:restart
	owner.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", nil)
	runs = owner.list("/api/apps/" + appID + "/runs")
	latestOwnerRun := runs[0]
	ownerInitiatedBy, _ := latestOwnerRun["initiatedBy"].(string)
	if !strings.HasPrefix(ownerInitiatedBy, "admin from") {
		t.Fatalf("expected owner run initiatedBy to start with 'admin from', got %q", ownerInitiatedBy)
	}

	// --- 2. Enable ReauthForCommands: Test Password Checks ---
	st, sec := owner.do("GET", "/api/settings/security", nil)
	if st != 200 {
		t.Fatalf("get security settings: %d %v", st, sec)
	}
	sec["reauthForCommands"] = true
	if st, out := owner.do("PUT", "/api/settings/security", sec); st != 200 {
		t.Fatalf("update security settings: %d %v", st, out)
	}

	// Operator running without password -> 403 password_required
	if st, out := ops.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", nil); st != 403 || out["code"] != "password_required" {
		t.Fatalf("expected 403 password_required without password, got %d %v", st, out)
	}

	// Operator running with wrong password -> 400 wrong_password
	if st, out := ops.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", map[string]string{"password": "wrong-password"}); st != 400 || out["code"] != "wrong_password" {
		t.Fatalf("expected 400 wrong_password, got %d %v", st, out)
	}

	// Operator running with owner's password -> 400 wrong_password (must use member's own password)
	if st, out := ops.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", map[string]string{"password": "admin-password-1"}); st != 400 || out["code"] != "wrong_password" {
		t.Fatalf("expected 400 wrong_password for operator using owner password, got %d %v", st, out)
	}

	// Operator running with member's correct password -> succeeds past reauth (reaches runner, returns 409 offline since mock agent not connected)
	st, out := ops.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", map[string]string{"password": "member-secret-123"})
	if st != 202 && st != 409 {
		t.Fatalf("expected 202 or 409 for valid operator re-auth, got %d %v", st, out)
	}

	// Owner running with wrong password -> 400 wrong_password
	if st, out := owner.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", map[string]string{"password": "wrong-password"}); st != 400 || out["code"] != "wrong_password" {
		t.Fatalf("expected 400 wrong_password for owner with bad password, got %d %v", st, out)
	}

	// Owner running with member's password -> 400 wrong_password
	if st, out := owner.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", map[string]string{"password": "member-secret-123"}); st != 400 || out["code"] != "wrong_password" {
		t.Fatalf("expected 400 wrong_password for owner with member password, got %d %v", st, out)
	}

	// Owner running with owner's correct password -> succeeds past reauth
	st, out = owner.do("POST", "/api/apps/"+appID+"/commands/builtin:restart/run", map[string]string{"password": "admin-password-1"})
	if st != 202 && st != 409 {
		t.Fatalf("expected 202 or 409 for valid owner re-auth, got %d %v", st, out)
	}
}
