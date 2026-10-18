package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
}

func (f *fakeCF) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
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
	CloudflareAPI = srv.URL
	s := setup(t)
	ctx := context.Background()

	if _, err := s.Add(ctx, "mem_1", "cloudflare", "", map[string]string{"token": "wrong"}); err == nil {
		t.Fatal("bad token accepted")
	}
	a, err := s.Add(ctx, "mem_1", "cloudflare", "", map[string]string{"token": "cf-token"})
	if err != nil || len(a.Zones) != 1 || a.Label != "Cloudflare" {
		t.Fatal(a, err)
	}
	if len(s.Accounts("mem_2")) != 0 || len(s.Accounts("mem_1")) != 1 {
		t.Fatal("accounts are personal")
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
	s.SyncBlocks(ctx, []string{"198.51.100.7", "203.0.113.0/24"})
	if len(f.rules) != 3 {
		t.Fatalf("%v", f.rules)
	}
	s.SyncBlocks(ctx, []string{"198.51.100.7"})
	if len(f.rules) != 2 || f.rules["theirs"][0] != "192.0.2.1" {
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
	if err := s.Remove("mem_1", a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPorkbunAndDesec(t *testing.T) {
	var mu sync.Mutex
	pb := map[string]string{} // id → content
	porkbunSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["apikey"] != "pk" || body["secretapikey"] != "sk" {
			json.NewEncoder(w).Encode(map[string]string{"status": "ERROR", "message": "Invalid API key."})
			return
		}
		switch {
		case r.URL.Path == "/domain/listAll":
			json.NewEncoder(w).Encode(map[string]any{"status": "SUCCESS", "domains": []map[string]string{{"domain": "shop.dev"}}})
		case r.URL.Path == "/dns/retrieveByNameType/shop.dev/A/www":
			recs := []Record{}
			for id, c := range pb {
				recs = append(recs, Record{ID: id, Name: "www.shop.dev", Content: c})
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "SUCCESS", "records": recs})
		case r.URL.Path == "/dns/create/shop.dev" && body["name"] == "www":
			pb["77"] = body["content"].(string)
			w.Write([]byte(`{"status":"SUCCESS","id":77}`))
		default:
			t.Errorf("porkbun: %s", r.URL.Path)
		}
	}))
	defer porkbunSrv.Close()
	desecRR := map[string][]string{}
	desecSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Token ds" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/domains/":
			w.Write([]byte(`[{"name":"blog.eu"}]`))
		case r.URL.Path == "/domains/blog.eu/rrsets/@/A/" && r.Method == "GET":
			if recs, ok := desecRR["@"]; ok {
				json.NewEncoder(w).Encode(map[string]any{"records": recs})
				return
			}
			w.WriteHeader(404)
		case r.URL.Path == "/domains/blog.eu/rrsets/" && r.Method == "POST":
			desecRR["@"] = []string{body["records"].([]any)[0].(string)}
			w.WriteHeader(201)
		case r.URL.Path == "/domains/blog.eu/rrsets/@/A/" && r.Method == "DELETE":
			delete(desecRR, "@")
			w.WriteHeader(204)
		default:
			t.Errorf("desec: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer desecSrv.Close()
	PorkbunAPI, DesecAPI = porkbunSrv.URL, desecSrv.URL
	s := setup(t)
	ctx := context.Background()
	if _, err := s.Add(ctx, "owner", "porkbun", "", map[string]string{"apiKey": "pk", "secretKey": "bad"}); err == nil {
		t.Fatal("bad Porkbun keys accepted")
	}
	if _, err := s.Add(ctx, "owner", "porkbun", "", map[string]string{"apiKey": "pk", "secretKey": "sk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, "owner", "desec", "", map[string]string{"token": "ds"}); err != nil {
		t.Fatal(err)
	}
	got := s.Point(ctx, "owner", "203.0.113.5", []string{"www.shop.dev", "blog.eu"})
	if got["www.shop.dev"].Status != "pointed" || pb["77"] != "203.0.113.5" || got["blog.eu"].Status != "pointed" || desecRR["@"][0] != "203.0.113.5" {
		t.Fatalf("%+v %v %v", got, pb, desecRR)
	}
	s.Forget(ctx, "owner", map[string]State{"blog.eu": got["blog.eu"]})
	if _, ok := desecRR["@"]; ok {
		t.Fatal("deSEC record not removed")
	}
	if no := s.Point(ctx, "owner", "", []string{"blog.eu"}); no["blog.eu"].Status != "error" {
		t.Fatal("no IP to point at")
	}
}
