package dns

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

// fakeCF is a tiny Cloudflare: one zone, A records, purges, the security level and
// access rules (one of them not Fleetwatch's).
type fakeCF struct {
	mu      sync.Mutex
	records map[string]Record // id → record
	rules   map[string][2]string
	purged  []string
	level   string
	next    int
	// sign-in
	challenge, refresh, revoked string
	hour                        string // the hour traffic is reported for
}

func (f *fakeCF) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/oauth2/") {
			r.ParseForm()
			if id, secret, _ := r.BasicAuth(); id != "cid" || secret != "csec" || r.Form.Get("client_secret") != "" {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
				return
			}
			give := func() {
				f.next++
				f.refresh = "rt" + string(rune('0'+f.next))
				json.NewEncoder(w).Encode(map[string]any{"access_token": "cf-token", "refresh_token": f.refresh, "expires_in": 3600})
			}
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			switch {
			case r.URL.Path == "/oauth2/revoke":
				f.revoked = r.Form.Get("token")
			case r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") == "good" &&
				base64.RawURLEncoding.EncodeToString(sum[:]) == f.challenge && r.Form.Get("redirect_uri") == "https://dash.test"+CallbackPath:
				give()
			case r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == f.refresh:
				give()
			default:
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "The provided authorization grant is invalid"})
			}
			return
		}
		if r.Header.Get("Authorization") == "Bearer cf-narrow" { // OAuth Clients only: lists nothing
			if r.Method == "GET" {
				json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
				return
			}
			r.Header.Set("Authorization", "Bearer cf-token")
		}
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]string{{"message": "Invalid API Token"}}})
			return
		}
		ok := func(result any) {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result, "result_info": map[string]int{"total_pages": 1}})
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		p := r.URL.Path
		switch {
		case p == "/zones":
			ok([]Zone{{ID: "z1", Name: "example.com"}})
		case p == "/graphql":
			q, _ := body["query"].(string)
			if hosts := body["variables"].(map[string]any)["hosts"].([]any); len(hosts) != 1 || hosts[0] != "shop.example.com" {
				t.Errorf("graphql hosts: %v", hosts)
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(q, "firewallEventsAdaptiveGroups") {
				w.Write([]byte(`{"data":{"viewer":{"zones":[{"threats":[{"count":7}]}]}}}`))
				return
			}
			w.Write([]byte(`{"data":{"viewer":{"zones":[{"hours":[{"count":10,"sum":{"edgeResponseBytes":1000,"visits":4},"dimensions":{"datetimeHour":"` +
				f.hour + `"}}],"countries":[{"count":9,"dimensions":{"clientCountryName":"MA"}},{"count":1,"dimensions":{"clientCountryName":"FR"}}]}]}}}`))
		case p == "/accounts":
			ok([]map[string]string{{"id": "acc1", "name": "Me"}})
		case (p == "/accounts/acc1/oauth_clients" || p == "/accounts/0123456789abcdef0123456789abcdef/oauth_clients") && r.Method == "POST":
			if body["token_endpoint_auth_method"] != "client_secret_basic" || len(body["scopes"].([]any)) != len(Scopes)+1 ||
				!strings.HasSuffix(body["redirect_uris"].([]any)[0].(string), CallbackPath) {
				t.Errorf("client: %v", body)
			}
			ok(map[string]string{"client_id": "cid", "client_secret": "csec"})
		case p == "/zones/z1/dns_records" && r.Method == "GET":
			out := []Record{}
			for _, rec := range f.records {
				if rec.Name == r.URL.Query().Get("name") {
					out = append(out, rec)
				}
			}
			ok(out)
		case p == "/zones/z1/dns_records" && r.Method == "POST":
			f.next++
			id := "r" + string(rune('0'+f.next))
			f.records[id] = Record{ID: id, Name: body["name"].(string), Content: body["content"].(string)}
			ok(f.records[id])
		case strings.HasPrefix(p, "/zones/z1/dns_records/") && r.Method == "PATCH":
			id := strings.TrimPrefix(p, "/zones/z1/dns_records/")
			rec := f.records[id]
			if c, has := body["content"].(string); has {
				rec.Content = c
			}
			if pr, has := body["proxied"].(bool); has {
				rec.Proxied = pr
			}
			f.records[id] = rec
			ok(rec)
		case strings.HasPrefix(p, "/zones/z1/dns_records/") && r.Method == "DELETE":
			delete(f.records, strings.TrimPrefix(p, "/zones/z1/dns_records/"))
			ok(nil)
		case p == "/zones/z1/purge_cache":
			for _, u := range body["files"].([]any) {
				f.purged = append(f.purged, u.(string))
			}
			ok(nil)
		case p == "/zones/z1/settings/security_level":
			if r.Method == "PATCH" {
				f.level = body["value"].(string)
			}
			ok(map[string]string{"value": f.level})
		case p == "/zones/z1/firewall/access_rules/rules" && r.Method == "GET":
			out := []map[string]any{}
			for id, rule := range f.rules {
				out = append(out, map[string]any{"id": id, "notes": rule[1], "configuration": map[string]string{"value": rule[0]}})
			}
			ok(out)
		case p == "/zones/z1/firewall/access_rules/rules" && r.Method == "POST":
			f.next++
			f.rules["a"+string(rune('0'+f.next))] = [2]string{body["configuration"].(map[string]any)["value"].(string), body["notes"].(string)}
			ok(nil)
		case strings.HasPrefix(p, "/zones/z1/firewall/access_rules/rules/") && r.Method == "DELETE":
			delete(f.rules, strings.TrimPrefix(p, "/zones/z1/firewall/access_rules/rules/"))
			ok(nil)
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	}))
}

func setup(t *testing.T) *Service {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(make([]byte, 32))
	return New(d, box, nil)
}

func TestCloudflare(t *testing.T) {
	f := &fakeCF{records: map[string]Record{"x": {ID: "x", Name: "old.example.com", Content: "198.51.100.9"}},
		rules: map[string][2]string{"theirs": {"192.0.2.1", "someone else's rule"}}, level: "medium"}
	srv := f.server(t)
	defer srv.Close()
	CloudflareAPI, CloudflareWeb = srv.URL, srv.URL
	s := setup(t)
	ctx := context.Background()

	if _, err := s.SignInURL("https://dash.test", "mem_1"); err == nil {
		t.Fatal("sign-in without the OAuth client set up")
	}
	if _, err := s.AutoApp(ctx, "wrong", "", "https://dash.test"); err == nil {
		t.Fatal("a bad API token made a client")
	}
	if _, err := s.AutoApp(ctx, "cf-narrow", "", "https://dash.test"); err == nil || !strings.Contains(err.Error(), "Account ID") {
		t.Fatal("a token that lists nothing should ask for the Account ID", err)
	}
	if _, err := s.AutoApp(ctx, "cf-narrow", "0123456789ABCDEF0123456789ABCDEF", "https://dash.test"); err != nil || s.AppClientID() != "cid" {
		t.Fatal("with the Account ID", err)
	}
	if name, err := s.AutoApp(ctx, "cf-token", "", "https://dash.test"); err != nil || name != "Me" || s.AppClientID() != "cid" {
		t.Fatal(name, err)
	}
	start := func(owner string) string {
		u, err := s.SignInURL("https://dash.test", owner)
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(u)
		q := pu.Query()
		if !strings.Contains(q.Get("scope"), AnalyticsScope) != s.AppNoAnalytics() {
			t.Fatal("traffic permission: ", q.Get("scope"))
		}
		if pu.Path != "/oauth2/auth" || q.Get("redirect_uri") != "https://dash.test"+CallbackPath || !strings.Contains(q.Get("scope"), "dns.write") || q.Get("code_challenge_method") != "S256" {
			t.Fatal(u)
		}
		f.challenge = q.Get("code_challenge")
		return q.Get("state")
	}
	// A client without the traffic permission: the sign-in starts over without it.
	if u, err := s.RetryWithoutAnalytics(start("mem_1"), "https://dash.test"); err != nil || strings.Contains(u, "analytics") || !s.AppNoAnalytics() {
		t.Fatal(u, err)
	}
	if s.AllowAnalytics(); s.AppNoAnalytics() {
		t.Fatal("not allowed again")
	}
	state := start("mem_1")
	if _, err := s.FinishSignIn(ctx, "bad", state); err == nil {
		t.Fatal("a wrong code was accepted")
	}
	if _, err := s.FinishSignIn(ctx, "good", state); err == nil {
		t.Fatal("a sign-in state was used twice")
	}
	a, err := s.FinishSignIn(ctx, "good", start("mem_1"))
	if err != nil || len(a.Zones) != 1 || a.Label != "example.com" || a.Owner != "mem_1" {
		t.Fatal(a, err)
	}
	if again, err := s.FinishSignIn(ctx, "good", start("mem_1")); err != nil || again.ID != a.ID {
		t.Fatal("signing in again added a second account", again, err)
	}
	if len(s.Accounts("mem_2")) != 0 || len(s.Accounts("mem_1")) != 1 {
		t.Fatal("accounts are personal")
	}

	// An expired access token is renewed with the refresh token, which Cloudflare rotates.
	before := f.refresh
	s.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := s.Refresh(ctx, "mem_1", a.ID); err != nil || f.refresh == before {
		t.Fatal("token not refreshed", err)
	}

	// A new name is created; one that pointed elsewhere is moved; another person's
	// account is never used; a domain no account holds is left alone.
	got := s.Point(ctx, "mem_1", "203.0.113.5", []string{"shop.example.com", "old.example.com", "other.org"})
	if got["shop.example.com"].Status != "pointed" || !got["shop.example.com"].Created || got["old.example.com"].Status != "updated" ||
		got["old.example.com"].Previous != "198.51.100.9" || got["old.example.com"].Created {
		t.Fatalf("%+v", got)
	}
	if _, ok := got["other.org"]; ok {
		t.Fatal("a domain no account holds")
	}
	if len(s.Point(ctx, "mem_2", "203.0.113.5", []string{"shop.example.com"})) != 0 {
		t.Fatal("another person's account was used")
	}
	if again := s.Point(ctx, "mem_1", "203.0.113.5", []string{"shop.example.com"}); again["shop.example.com"].Status != "pointed" || len(f.records) != 2 {
		t.Fatalf("pointed twice: %+v %d", again, len(f.records))
	}

	f.hour = time.Now().UTC().Add(4 * time.Hour).Truncate(time.Hour).Format(time.RFC3339) // s.Now is 2 hours ahead here
	tr, err := s.Traffic(ctx, "mem_1", []string{"shop.example.com", "other.org"})
	if err != nil || tr.Requests != 10 || tr.Visits != 4 || tr.Bytes != 1000 || tr.Threats == nil || *tr.Threats != 7 ||
		len(tr.Hours) != 24 || tr.Countries[0].Code != "MA" {
		t.Fatalf("%+v %v", tr, err)
	}
	if _, err := s.Traffic(ctx, "mem_2", []string{"shop.example.com"}); err != ErrNoZone {
		t.Fatal("another person's account was used for traffic", err)
	}
	s.Purge(ctx, "mem_1", []string{"shop.example.com"})
	if len(f.purged) == 0 || f.purged[0] != "https://shop.example.com/" {
		t.Fatal(f.purged)
	}
	if st, err := s.SetProxied(ctx, "mem_1", got["shop.example.com"], true); err != nil || !st.Proxied {
		t.Fatal(st, err)
	}
	on := true
	if v, err := s.UnderAttack(ctx, "mem_1", a.ID, "example.com", &on); err != nil || !v || f.level != "under_attack" {
		t.Fatal(v, err, f.level)
	}

	// Blocked addresses: added, then removed; someone else's rule stays.
	if _, err := s.SetSyncBlocks("mem_1", a.ID, true); err != nil {
		t.Fatal(err)
	}
	late, cancel := context.WithCancel(ctx) // out of time: a progress note, not an error
	cancel()
	s.SyncBlocks(late, []string{"198.51.100.7"})
	if e := s.Accounts("mem_1")[0].Error; !strings.Contains(e, "follow by themselves") {
		t.Fatal(e)
	}
	s.SyncBlocks(ctx, []string{"198.51.100.7", "203.0.113.0/24"})
	if len(f.rules) != 3 {
		t.Fatalf("%v", f.rules)
	}
	s.SyncBlocks(ctx, []string{"198.51.100.7"})
	if len(f.rules) != 2 || f.rules["theirs"][0] != "192.0.2.1" || s.Accounts("mem_1")[0].Error != "" {
		t.Fatalf("%v", f.rules)
	}
	s.SetSyncBlocks("mem_1", a.ID, false)
	s.SyncBlocks(ctx, []string{"198.51.100.7"})
	if len(f.rules) != 1 {
		t.Fatalf("mirroring off left rules: %v", f.rules)
	}

	// Forget removes only what Fleetwatch made.
	s.Forget(ctx, "mem_1", got)
	if _, ok := f.records["x"]; !ok || len(f.records) != 1 {
		t.Fatalf("%v", f.records)
	}
	if err := s.Remove("mem_2", a.ID); err == nil {
		t.Fatal("removed someone else's account")
	}
	if err := s.Remove("mem_1", a.ID); err != nil || f.revoked != f.refresh {
		t.Fatal("not signed out of Cloudflare", err, f.revoked)
	}
}
