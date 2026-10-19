package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/mobile"
)

var testMobile *mobile.Service

func testPhones(d *sql.DB, a *auth.Service) *mobile.Service {
	m := mobile.New(d)
	m.Scope = func(id string) ([]string, bool) {
		if id == "" {
			return nil, true
		}
		x, err := a.GetMember(id)
		if err != nil || x.Disabled {
			return nil, false
		}
		return x.Servers, true
	}
	testMobile = m
	return m
}

// appCall is the phone app: no cookies, no Origin, just its token.
func appCall(t *testing.T, srv *httptest.Server, method, path, token string, body string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := srv.Client().Do(req) // no cookie jar, no Origin: like the app
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestPhoneApp(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)
	serverA, _, _ := pairAgent(t, owner)
	_, b := owner.do("POST", "/api/servers", map[string]string{"name": "Other"})
	serverB := b["server"].(map[string]any)["id"].(string)

	st, p := owner.do("POST", "/api/phones/pairing", nil)
	code, _ := p["code"].(string)
	if st != 200 || len(code) != 9 || !strings.HasPrefix(p["qr"].(string), "fleetwatch://pair?u=") || !strings.Contains(p["qr"].(string), "c="+code) {
		t.Fatalf("pairing: %d %v", st, p)
	}
	if st, _ := appCall(t, srv, "POST", "/api/mobile/pair", "", `{"code":"WRONG-CODE","name":"x","platform":"android"}`); st != 400 {
		t.Fatalf("wrong code: %d", st)
	}
	st, paired := appCall(t, srv, "POST", "/api/mobile/pair", "", `{"code":"`+strings.ToLower(code)+`","name":"Pixel","platform":"android","appVersion":"1.0.0"}`)
	token, _ := paired["token"].(string)
	if st != 200 || !strings.HasPrefix(token, "fwm_") {
		t.Fatalf("pair: %d %v", st, paired)
	}
	if st, _ := appCall(t, srv, "POST", "/api/mobile/pair", "", `{"code":"`+code+`","name":"again"}`); st != 400 {
		t.Fatal("a pairing code worked twice")
	}
	if st, me := appCall(t, srv, "GET", "/api/mobile/me", token, ""); st != 200 || me["role"] != "owner" {
		t.Fatalf("me: %d %v", st, me)
	}

	// A member limited to server A pairs a phone too.
	_, mem := owner.do("POST", "/api/members", map[string]any{"username": "ops", "role": "viewer", "servers": []string{serverA}, "password": "member-password-1"})
	ops := newClient(t, srv)
	ops.do("POST", "/api/auth/login", map[string]string{"username": "ops", "password": "member-password-1"})
	_, op := ops.do("POST", "/api/phones/pairing", nil)
	_, opPaired := appCall(t, srv, "POST", "/api/mobile/pair", "", `{"code":"`+op["code"].(string)+`","name":"ops phone"}`)
	opsToken := opPaired["token"].(string)

	// Alerts queued while the phones are offline arrive on connect.
	testMobile.Alert(mobile.Message{Kind: "incident", Title: "api down", Severity: "critical", ServerID: serverA, IncidentID: "i1"})
	testMobile.Alert(mobile.Message{Kind: "incident", Title: "db down", Severity: "critical", ServerID: serverB})
	testMobile.Alert(mobile.Message{Kind: "notice", Title: "security: new SSH key", Severity: "warning"})
	read := func(tok string, after string) []mobile.Message {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "https", "wss", 1)+"/api/mobile/ws?after="+after,
			&websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.CloseNow()
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var env struct{ Data []mobile.Message }
		json.Unmarshal(data, &env)
		return env.Data
	}
	got := read(token, "0")
	if len(got) != 3 {
		t.Fatalf("owner phone got %d: %+v", len(got), got)
	}
	opsGot := read(opsToken, "0")
	if len(opsGot) != 1 || opsGot[0].Title != "api down" {
		t.Fatalf("member phone (server A only, no owner notices): %+v", opsGot)
	}
	// After the last id the app saw, nothing is sent again.
	testMobile.Alert(mobile.Message{Kind: "incident", Title: "web slow", Severity: "warning", ServerID: serverA})
	if again := read(token, itoa(got[2].ID)); len(again) != 1 || again[0].Title != "web slow" {
		t.Fatalf("resume: %+v", again)
	}

	// "On-call only" phones skip ordinary alerts.
	opsDevice := opPaired["device"].(map[string]any)["id"].(string)
	if st, _ := ops.do("PATCH", "/api/phones/"+opsDevice, map[string]string{"alerts": "oncall"}); st != 200 {
		t.Fatalf("set on-call only: %d", st)
	}
	before := len(testMobile.Pending(opsDevice, 0))
	testMobile.Alert(mobile.Message{Kind: "incident", Title: "cpu", Severity: "warning", ServerID: serverA})
	testMobile.ToPerson(mem["id"].(string), mobile.Message{Kind: "oncall", Title: "On call: api down", Severity: "critical", ServerID: serverA})
	if p := testMobile.Pending(opsDevice, 0); len(p) != before+1 || p[len(p)-1].Kind != "oncall" {
		t.Fatalf("on-call only phone: %+v", p)
	}

	// The fleet at a glance (widget), limited to what each person can see.
	if st, out := appCall(t, srv, "GET", "/api/mobile/status", token, ""); st != 200 || len(out["servers"].([]any)) != 1 || out["incidents"] == nil {
		t.Fatalf("status: %d %v", st, out)
	}
	if st, _ := appCall(t, srv, "GET", "/api/mobile/servers/"+serverB+"/metrics?range=1h", opsToken, ""); st != 404 {
		t.Fatalf("member phone read another server's charts: %d", st)
	}
	if st, _ := appCall(t, srv, "GET", "/api/mobile/servers/"+serverA+"/metrics?range=1h", opsToken, ""); st != 200 {
		t.Fatalf("charts: %d", st)
	}
	// Personal rules: only critical alerts on this phone; on-call pages still come.
	if st, r := appCall(t, srv, "PUT", "/api/mobile/me/rules", token, `{"minSeverity":"critical","servers":[],"quietFrom":"","quietTo":"","timeZone":"UTC"}`); st != 200 || r["minSeverity"] != "critical" {
		t.Fatalf("rules: %d %v", st, r)
	}
	ownerDevice := paired["device"].(map[string]any)["id"].(string)
	n := len(testMobile.Pending(ownerDevice, 0))
	testMobile.Alert(mobile.Message{Kind: "incident", Title: "cpu", Severity: "warning", ServerID: serverA})
	testMobile.Alert(mobile.Message{Kind: "incident", Title: "down", Severity: "critical", ServerID: serverA})
	if p := testMobile.Pending(ownerDevice, 0); len(p) != n+1 || p[len(p)-1].Title != "down" {
		t.Fatalf("rules not applied: %+v", p[n:])
	}

	// Phones list in the dashboard, then unpairing locks the app out.
	if list := owner.list("/api/phones"); len(list) != 1 || list[0]["name"] != "Pixel" {
		t.Fatalf("owner phones: %v", list)
	}
	if list := ops.list("/api/phones"); len(list) != 1 {
		t.Fatalf("member sees only own phones: %v", list)
	}
	if st, _ := owner.do("DELETE", "/api/phones/"+opsDevice, nil); st != 404 {
		t.Fatal("the owner removed someone else's phone through their own list")
	}
	if st, _ := appCall(t, srv, "DELETE", "/api/mobile/me", token, ""); st != 204 {
		t.Fatal("unpair")
	}
	if st, _ := appCall(t, srv, "GET", "/api/mobile/me", token, ""); st != 401 {
		t.Fatal("unpaired phone still in")
	}
	// Removing a member unpairs their phones.
	owner.do("DELETE", "/api/members/"+mem["id"].(string), nil)
	if st, _ := appCall(t, srv, "GET", "/api/mobile/me", opsToken, ""); st != 401 {
		t.Fatal("removed member's phone still in")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
