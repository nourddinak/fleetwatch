package api

import (
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"

	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/statuspage"
)

func testStatus(d *sql.DB, inc *incidents.Store, hub *fleet.Hub, appSvc *apps.Service) *statuspage.Service {
	s := statuspage.New(d, inc)
	s.AppState = func(id string) (string, string, int64, bool) {
		v, err := appSvc.Get(id)
		return "operational", v.ServerID, v.DiscoveredAt, err == nil
	}
	s.ServerState = func(id string) (string, int64, bool) {
		rec, err := hub.Reg.Get(id)
		if err != nil {
			return "", 0, false
		}
		if hub.Connected(id) {
			return "operational", rec.CreatedAt, true
		}
		return "outage", rec.CreatedAt, true
	}
	return s
}

func TestStatusPage(t *testing.T) {
	srv := newTestServer(t)
	anon := newClient(t, srv)
	get := func(host, path string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if host != "" {
			req.Host = host
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, _ := get("", "/api/public/status"); st != 404 {
		t.Fatalf("disabled page answered %d", st)
	}
	if st, _ := get("", "/status"); st != 404 {
		t.Fatalf("disabled /status answered %d", st)
	}
	cfg := map[string]any{"enabled": true, "title": "Acme <status>", "accent": "#ff0000", "theme": "dark", "historyDays": 30, "showUptime": true,
		"domain": "Status.Example.com", "groups": []any{}, "notices": []any{}}
	if st, _ := anon.do("PUT", "/api/status-page", cfg); st != 401 {
		t.Fatalf("anonymous save: %d", st)
	}

	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	serverID, _, _ := pairAgent(t, c)
	cfg["groups"] = []any{map[string]any{"name": "Core", "components": []any{
		map[string]any{"kind": "server", "id": serverID, "name": "API", "description": "Public API"},
		map[string]any{"kind": "server", "id": "srv_gone", "name": "Old box"},
	}}}
	if st, out := c.do("PUT", "/api/status-page", map[string]any{"enabled": true, "accent": "red", "theme": "auto", "historyDays": 30}); st != 400 {
		t.Fatalf("bad accent accepted: %d %v", st, out)
	}
	if st, out := c.do("PUT", "/api/status-page", cfg); st != 200 || out["domain"] != "status.example.com" {
		t.Fatalf("save: %d %v", st, out)
	}

	st, body := get("", "/api/public/status")
	if st != 200 || !strings.Contains(body, `"name":"API"`) || !strings.Contains(body, `"overall":"operational"`) {
		t.Fatalf("public: %d %s", st, body)
	}
	// Only what was published: no IDs, hostnames or the server's own name.
	for _, leak := range []string{serverID, "VPS-logs", "srv_gone", `"id"`, "status.example.com"} {
		if strings.Contains(body, leak) {
			t.Errorf("public status leaks %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, `"status":"unknown"`) {
		t.Errorf("a deleted component should be unknown: %s", body)
	}
	if st, html := get("", "/status"); st != 200 || !strings.Contains(html, `data-page="status"`) || !strings.Contains(html, "<title>Acme &lt;status&gt;</title>") {
		t.Fatalf("/status: %d %s", st, html)
	}
	// The status domain serves the page and nothing of the dashboard.
	if st, html := get("status.example.com", "/"); st != 200 || !strings.Contains(html, `data-page="status"`) {
		t.Fatalf("status domain root: %d %s", st, html)
	}
	for _, p := range []string{"/api/servers", "/api/auth/me", "/index.html", "/api/status-page"} {
		if st, _ := get("status.example.com", p); st != 404 {
			t.Errorf("status domain %s: %d", p, st)
		}
	}
	if st, _ := get("status.example.com:443", "/api/public/status"); st != 200 {
		t.Errorf("status domain api: %d", st)
	}
	if st, _ := get("status.example.com", "/assets/app-1.js"); st != 200 {
		t.Errorf("status domain assets: %d", st)
	}
}
