package api

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/email"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
)

func testEmail(t *testing.T, d *sql.DB, box *secret.Box, bus *live.Bus) *email.Service {
	s, err := email.New(d, box, bus, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// postInbound sends a message as the Worker would.
func postInbound(t *testing.T, c *tc, secret, rcpt string, raw string, ts time.Time) (int, string) {
	t.Helper()
	tsS := strconv.FormatInt(ts.Unix(), 10)
	req, _ := http.NewRequest("POST", c.srv.URL+"/api/emails/inbound", strings.NewReader(raw))
	req.Header.Set("X-Fw-Timestamp", tsS)
	req.Header.Set("X-Fw-Rcpt", rcpt)
	req.Header.Set("X-Fw-From", "jane@example.com")
	req.Header.Set("X-Fw-Signature", InboundSignature(secret, tsS, rcpt, "jane@example.com", len(raw)))
	res, err := c.c.Do(req) // no Origin: the Worker is not a browser
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestEmailInboundSignatureAndScopes(t *testing.T) {
	srv := newTestServer(t)
	owner := newClient(t, srv)
	owner.do("POST", "/api/auth/login", creds)
	st, sec := owner.do("POST", "/api/emails/cloudflare/manual-secret", nil)
	if st != 200 {
		t.Fatalf("secret: %d %v", st, sec)
	}
	secret := sec["secret"].(string)
	_, d1 := owner.do("POST", "/api/emails/domains", map[string]string{"name": "nashop.store"})
	_, d2 := owner.do("POST", "/api/emails/domains", map[string]string{"name": "other.site"})
	_, a1 := owner.do("POST", "/api/emails/addresses", map[string]any{"domainId": d1["id"], "local": "contact", "receive": true, "send": true})
	_, a2 := owner.do("POST", "/api/emails/addresses", map[string]any{"domainId": d2["id"], "local": "admin", "receive": true, "send": true})
	if a1["address"] != "contact@nashop.store" || a2["address"] != "admin@other.site" {
		t.Fatalf("addresses: %v %v", a1, a2)
	}

	raw := func(to, subj string) string {
		return "From: Jane <jane@example.com>\r\nTo: " + to + "\r\nSubject: " + subj + "\r\n\r\nhello"
	}
	if st, _ := postInbound(t, owner, "wrong-secret", "contact@nashop.store", raw("contact@nashop.store", "x"), time.Now()); st != 401 {
		t.Fatalf("bad signature: %d", st)
	}
	if st, _ := postInbound(t, owner, secret, "contact@nashop.store", raw("contact@nashop.store", "x"), time.Now().Add(-10*time.Minute)); st != 401 {
		t.Fatalf("stale timestamp: %d", st)
	}
	if st, body := postInbound(t, owner, secret, "contact@nashop.store", raw("contact@nashop.store", "for contact"), time.Now()); st != 200 || !strings.Contains(body, `"stored":true`) {
		t.Fatalf("inbound: %d %s", st, body)
	}
	if st, body := postInbound(t, owner, secret, "nobody@nashop.store", raw("nobody@nashop.store", "x"), time.Now()); st != 200 || !strings.Contains(body, `"reject"`) {
		t.Fatalf("unknown recipient: %d %s", st, body)
	}
	postInbound(t, owner, secret, "admin@other.site", raw("admin@other.site", "for admin"), time.Now())

	// An operator granted one domain sees only its mail, by list, id or search.
	_, m := owner.do("POST", "/api/members", map[string]any{"username": "ops", "role": "operator", "password": "member-password-1"})
	owner.do("POST", "/api/members", map[string]any{"username": "view", "role": "viewer", "password": "member-password-1"})
	if st, b := owner.do("PUT", "/api/emails/grants/"+m["id"].(string), []map[string]string{{"scopeKind": "domain", "scopeId": d1["id"].(string), "level": "read"}}); st != 200 {
		t.Fatalf("grant: %d %v", st, b)
	}
	ops := newClient(t, srv)
	ops.do("POST", "/api/auth/login", map[string]string{"username": "ops", "password": "member-password-1"})
	_, page := ops.do("GET", "/api/emails/messages", nil)
	msgs := page["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["subject"] != "for contact" {
		t.Fatalf("operator list: %v", page)
	}
	_, all := owner.do("GET", "/api/emails/messages", nil)
	for _, x := range all["messages"].([]any) {
		mm := x.(map[string]any)
		st, _ := ops.do("GET", fmt.Sprintf("/api/emails/messages/%.0f", mm["id"]), nil)
		if want := map[string]int{"for contact": 200, "for admin": 404}[mm["subject"].(string)]; st != want {
			t.Fatalf("operator opened %v: %d", mm["subject"], st)
		}
	}
	if st, _ := ops.do("POST", "/api/emails/addresses", map[string]any{"domainId": d1["id"], "local": "sneaky", "receive": true}); st != 403 {
		t.Fatalf("read-only operator added an address: %d", st)
	}
	if st, _ := ops.do("GET", "/api/emails/cloudflare", nil); st != 403 {
		t.Fatalf("operator reached Cloudflare settings: %d", st)
	}
	view := newClient(t, srv)
	view.do("POST", "/api/auth/login", map[string]string{"username": "view", "password": "member-password-1"})
	if st, _ := view.do("GET", "/api/emails/messages", nil); st != 403 {
		t.Fatalf("viewer read mail: %d", st)
	}
	if st, b := view.do("GET", "/api/emails/counts", nil); st != 200 || b["enabled"] != false {
		t.Fatalf("viewer counts: %d %v", st, b)
	}
	// Browser writes still need the same origin.
	if st, _ := owner.do("POST", "/api/emails/messages/actions", map[string]any{"ids": []int{1}, "action": "read"}, "https://evil.example"); st != 403 {
		t.Fatalf("cross-origin mail action: %d", st)
	}
}
