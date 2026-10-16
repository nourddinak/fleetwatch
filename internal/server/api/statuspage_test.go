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
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/statuspage"
)

func testStatus(d *sql.DB, inc *incidents.Store, hub *fleet.Hub, appSvc *apps.Service) *statuspage.Service {
	s := statuspage.New(d, inc)
	box, _ := secret.New(make([]byte, 32))
	s.Mail = statuspage.NewMailer(d, box)
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

func TestStatusPages(t *testing.T) {
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
		t.Fatalf("no page yet, answered %d", st)
	}
	if st, _ := get("", "/status"); st != 404 {
		t.Fatalf("no page yet, /status answered %d", st)
	}
	page := func(title, slug, domain string, enabled bool) map[string]any {
		return map[string]any{"enabled": enabled, "title": title, "slug": slug, "accent": "#ff0000", "theme": "dark", "historyDays": 30, "showUptime": true,
			"domain": domain, "groups": []any{}, "notices": []any{}}
	}
	if st, _ := anon.do("POST", "/api/status-pages", page("x", "", "", true)); st != 401 {
		t.Fatalf("anonymous create: %d", st)
	}

	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	serverID, _, _ := pairAgent(t, c)
	main := page("Acme <status>", "", "", true)
	main["groups"] = []any{map[string]any{"name": "Core", "components": []any{
		map[string]any{"kind": "server", "id": serverID, "name": "API", "description": "Public API"},
		map[string]any{"kind": "server", "id": "srv_gone", "name": "Old box"},
	}}}
	st, out := c.do("POST", "/api/status-pages", main)
	mainID, _ := out["id"].(string)
	if st != 201 || mainID == "" {
		t.Fatalf("create main: %d %v", st, out)
	}
	if st, out := c.do("POST", "/api/status-pages", page("Second main", "", "", true)); st != 400 {
		t.Fatalf("two main pages: %d %v", st, out)
	}
	st, out = c.do("POST", "/api/status-pages", page("Shop", "Shop", "Status.Shop.com", true))
	shopID, _ := out["id"].(string)
	if st != 201 || out["slug"] != "shop" || out["domain"] != "status.shop.com" {
		t.Fatalf("create shop: %d %v", st, out)
	}
	for name, p := range map[string]map[string]any{
		"same address": page("Dup", "shop", "", true),
		"same domain":  page("Dup", "dup", "status.shop.com", true),
		"bad address":  page("Dup", "../admin", "", true),
	} {
		if st, out := c.do("POST", "/api/status-pages", p); st != 400 {
			t.Errorf("%s accepted: %d %v", name, st, out)
		}
	}
	hidden := page("Hidden", "hidden", "", false)
	if st, _ := c.do("POST", "/api/status-pages", hidden); st != 201 {
		t.Fatal("create hidden")
	}
	if list := c.list("/api/status-pages"); len(list) != 3 || list[0]["components"] != float64(2) {
		t.Fatalf("list: %v", list)
	}

	st, body := get("", "/api/public/status")
	if st != 200 || !strings.Contains(body, `"name":"API"`) || !strings.Contains(body, `"overall":"operational"`) {
		t.Fatalf("public: %d %s", st, body)
	}
	// Only what was published: no IDs, hostnames or the server's own name.
	for _, leak := range []string{serverID, "VPS-logs", "srv_gone", `"id"`, mainID} {
		if strings.Contains(body, leak) {
			t.Errorf("public status leaks %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, `"status":"unknown"`) {
		t.Errorf("a deleted component should be unknown: %s", body)
	}
	if st, body := get("", "/api/public/status/shop"); st != 200 || !strings.Contains(body, `"title":"Shop"`) {
		t.Fatalf("shop: %d %s", st, body)
	}
	if st, _ := get("", "/api/public/status/hidden"); st != 404 {
		t.Errorf("a turned-off page answered %d", st)
	}
	if st, html := get("", "/status"); st != 200 || !strings.Contains(html, `data-page="status" data-slug=""`) || !strings.Contains(html, "<title>Acme &lt;status&gt;</title>") {
		t.Fatalf("/status: %d %s", st, html)
	}
	if st, html := get("", "/status/shop"); st != 200 || !strings.Contains(html, `data-slug="shop"`) || !strings.Contains(html, "<title>Shop</title>") {
		t.Fatalf("/status/shop: %d %s", st, html)
	}
	if st, _ := get("", "/status/hidden"); st != 404 {
		t.Errorf("/status/hidden: %d", st)
	}

	// A page's own domain serves that page and nothing of the dashboard or other pages.
	if st, html := get("status.shop.com", "/"); st != 200 || !strings.Contains(html, "<title>Shop</title>") {
		t.Fatalf("own domain root: %d %s", st, html)
	}
	if st, body := get("status.shop.com:443", "/api/public/status"); st != 200 || !strings.Contains(body, `"title":"Shop"`) {
		t.Errorf("own domain api: %d %s", st, body)
	}
	for _, p := range []string{"/api/servers", "/api/auth/me", "/index.html", "/api/status-pages", "/api/public/status/hidden"} {
		if st, _ := get("status.shop.com", p); st != 404 {
			t.Errorf("own domain %s: %d", p, st)
		}
	}
	if st, _ := get("status.shop.com", "/assets/app-1.js"); st != 200 {
		t.Errorf("own domain assets: %d", st)
	}

	// The setup check names the record to add; reached by IP, that is an A record.
	if st, out := c.do("GET", "/api/status-pages/setup?domain=Status.Example.co.uk", nil); st != 200 || out["recordType"] != "A" ||
		out["name"] != "status" || out["zone"] != "example.co.uk" {
		t.Fatalf("setup: %d %v", st, out)
	}
	if st, _ := anon.do("GET", "/api/status-pages/setup?domain=x.com", nil); st != 401 {
		t.Fatalf("anonymous setup check: %d", st)
	}
	if st, _ := c.do("DELETE", "/api/status-pages/"+shopID, nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	if st, _ := get("", "/api/public/status/shop"); st != 404 {
		t.Errorf("deleted page answered %d", st)
	}
	if st, _ := c.do("PUT", "/api/status-pages/"+shopID, page("Shop", "shop", "", true)); st != 404 {
		t.Errorf("saving a deleted page: %d", st)
	}
}

func TestPrivatePagesAndBadge(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	page := func(title, slug, access, password string) map[string]any {
		return map[string]any{"enabled": true, "title": title, "slug": slug, "accent": "#ff0000", "theme": "dark", "historyDays": 30,
			"access": access, "password": password, "groups": []any{}, "notices": []any{}}
	}
	if st, out := c.do("POST", "/api/status-pages", page("Open", "", "public", "")); st != 201 {
		t.Fatalf("create: %d %v", st, out)
	}
	st, out := c.do("POST", "/api/status-pages", page("Clients", "clients", "password", "hunter22"))
	if st != 201 || out["passwordHash"] != nil || out["hasPassword"] != true {
		t.Fatalf("password page: %d %v", st, out)
	}
	if st, _ := c.do("POST", "/api/status-pages", page("Team", "team", "team", "")); st != 201 {
		t.Fatal("team page")
	}

	anon := newClient(t, srv)
	if st, out := anon.do("GET", "/api/public/status/clients", nil); st != 401 || out["code"] != "locked" || out["title"] != "Clients" || out["groups"] != nil {
		t.Fatalf("locked page: %d %v", st, out)
	}
	if st, _ := anon.do("POST", "/api/public/unlock", map[string]string{"page": "clients", "password": "wrong"}); st != 401 {
		t.Fatal("wrong password accepted")
	}
	if st, _ := anon.do("POST", "/api/public/unlock", map[string]string{"page": "clients", "password": "hunter22"}); st != 204 {
		t.Fatal("unlock failed")
	}
	if st, out := anon.do("GET", "/api/public/status/clients", nil); st != 200 || out["title"] != "Clients" {
		t.Fatalf("unlocked page: %d %v", st, out)
	}
	if st, out := anon.do("GET", "/api/public/status/team", nil); st != 401 || out["code"] != "team" {
		t.Fatalf("team page for a visitor: %d %v", st, out)
	}
	if st, _ := c.do("GET", "/api/public/status/team", nil); st != 200 {
		t.Fatal("a signed-in user cannot see the team page")
	}

	badge := func(q string) (int, string) {
		res, err := srv.Client().Get(srv.URL + "/api/public/badge.svg" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Content-Type") + " " + string(b)
	}
	if st, b := badge("?label=API"); st != 200 || !strings.HasPrefix(b, "image/svg+xml") || !strings.Contains(b, ">API<") {
		t.Fatalf("badge: %d %s", st, b)
	}
	if st, _ := badge("?page=clients"); st != 404 {
		t.Fatal("a private page's badge is public")
	}
	// Mail apps post the one-click unsubscribe without an Origin header.
	req, _ := http.NewRequest("POST", srv.URL+"/api/public/subscribe/remove?t=nothing", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("one-click unsubscribe refused: %d", res.StatusCode)
	}
	// Tokens cannot read subscribers or email settings.
	if st, _ := anon.do("GET", "/api/settings/smtp", nil); st != 401 {
		t.Fatal("SMTP settings readable without sign-in")
	}
}
