package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCountryRule(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	rules := []map[string]string{} // the zone's custom rules; nil set = no entrypoint yet
	exists := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		ok := func(v any) { b, _ := json.Marshal(map[string]any{"success": true, "result": v}); w.Write(b) }
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/entrypoint"):
			if !exists {
				w.WriteHeader(404)
				w.Write([]byte(`{"success":false,"errors":[{"message":"Could not find entrypoint ruleset in the http_request_firewall_custom phase"}]}`))
				return
			}
			ok(map[string]any{"id": "rs1", "rules": rules})
		case r.Method == "PUT":
			exists = true
			rules = append(rules, map[string]string{"id": "r_ours", "description": geoRuleNote})
			ok(nil)
		case r.Method == "POST":
			rules = append(rules, map[string]string{"id": "r_ours", "description": geoRuleNote})
			ok(nil)
		default:
			ok(nil)
		}
	}))
	defer srv.Close()
	old := CloudflareAPI
	CloudflareAPI = srv.URL
	defer func() { CloudflareAPI = old }()
	c, z, ctx := cloudflare{token: "t"}, Zone{ID: "z1"}, context.Background()

	if err := c.CountryRule(ctx, z, `(ip.geoip.country in {"CN"})`); err != nil || calls[1] != "PUT /zones/z1/rulesets/phases/http_request_firewall_custom/entrypoint" {
		t.Fatal(err, calls)
	}
	calls = nil
	c.CountryRule(ctx, z, `(not (ip.geoip.country in {"MA"}))`)
	if calls[1] != "PATCH /zones/z1/rulesets/rs1/rules/r_ours" {
		t.Fatal(calls)
	}
	// Someone else's rule only: ours is added next to it.
	rules = []map[string]string{{"id": "r_theirs", "description": "my own rule"}}
	calls = nil
	c.CountryRule(ctx, z, `(ip.geoip.country in {"RU"})`)
	if calls[1] != "POST /zones/z1/rulesets/rs1/rules" {
		t.Fatal(calls)
	}
	calls = nil
	c.CountryRule(ctx, z, "")
	if len(calls) != 2 || calls[1] != "DELETE /zones/z1/rulesets/rs1/rules/r_ours" {
		t.Fatal(calls)
	}
}
