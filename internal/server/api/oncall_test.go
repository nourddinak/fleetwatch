package api

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/oncall"
)

func testOnCall(d *sql.DB, inc *incidents.Store, a *auth.Service) *oncall.Service {
	s := oncall.New(d, nil)
	s.Incidents = inc
	s.Page = func(context.Context, oncall.Person, incidents.Incident, string, int) error { return nil }
	s.People = func() []oncall.Person {
		out := []oncall.Person{{ID: oncall.Owner, Name: "Owner"}}
		list, _ := a.ListMembers()
		for _, m := range list {
			out = append(out, oncall.Person{ID: m.ID, Name: m.Username})
		}
		return out
	}
	return s
}

func TestOnCallRoutes(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)
	_, m := owner.do("POST", "/api/members", map[string]any{"username": "ops", "role": "operator", "password": "member-password-1"})
	cfg := map[string]any{"enabled": true, "rotation": []string{m["id"].(string), "owner"}, "handoverDay": 1, "handoverHour": 9, "timeZone": "Europe/Paris",
		"escalateAfterMin": 10, "severity": "critical", "overrides": []any{}, "start": 0}
	st, out := owner.do("PUT", "/api/oncall", cfg)
	if st != 200 || out["now"] != m["id"] || len(out["schedule"].([]any)) == 0 {
		t.Fatalf("save: %d %v", st, out)
	}
	cfg["timeZone"] = "Mars/Olympus"
	if st, _ := owner.do("PUT", "/api/oncall", cfg); st != 400 {
		t.Fatal("unknown time zone accepted")
	}
	ops := newClient(t, srv)
	ops.do("POST", "/api/auth/login", map[string]string{"username": "ops", "password": "member-password-1"})
	if st, _ := ops.do("GET", "/api/oncall", nil); st != 403 {
		t.Fatalf("a member read the on-call settings: %d", st)
	}
	// The acknowledge link works without signing in, but only when signed.
	req, _ := http.NewRequest("POST", srv.URL+"/api/public/ack?i=x&p=owner&s=forged", nil)
	req.Header.Set("Origin", srv.URL)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("forged acknowledge: %d", res.StatusCode)
	}
}
