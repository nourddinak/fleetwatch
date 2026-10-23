package geo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
)

type fakeDNS struct{ calls []string }

func (f *fakeDNS) CountryRule(_ context.Context, _, account, zone, mode string, countries []string) error {
	f.calls = append(f.calls, account+" "+zone+" "+mode+" "+strings.Join(countries, ","))
	return nil
}

func TestGeo(t *testing.T) {
	var mu sync.Mutex
	up, fetches := true, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fetches++
		switch {
		case !up:
			w.WriteHeader(503)
		case r.URL.Path == "/v4/ma.zone":
			w.Write([]byte("41.92.0.0/16\n105.128.0.0/11\n# comment\n"))
		case r.URL.Path == "/v6/ma.zone":
			w.Write([]byte("2c0f:f000::/24\n"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	IPv4List, IPv6List = srv.URL+"/v4/%s.zone", srv.URL+"/v6/%s.zone"
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, dir)
	var sent []proto.GeoApply
	s.Send = func(_, _ string, v any) error {
		mu.Lock()
		sent = append(sent, v.(proto.GeoApply))
		mu.Unlock()
		return nil
	}
	s.ServerIDs = func() []string { return []string{"s1"} }
	s.Safe = func() []string { return []string{"203.0.113.9"} }
	ver := "0.9.158"
	s.AgentVersion = func(string) string {
		mu.Lock()
		defer mu.Unlock()
		return ver
	}
	ctx := context.Background()

	for _, bad := range []Rule{{Mode: "block", Countries: []string{"Morocco"}}, {Mode: "allow"}, {Mode: "maybe", Countries: []string{"MA"}}, {Mode: "block", Countries: []string{"ZZ"}}, {Mode: "allow", Countries: []string{"MA"}, Ports: []int{70000}}} {
		if _, err := s.SaveRule(ctx, bad); err == nil {
			t.Errorf("%+v saved", bad)
		}
	}
	r, err := s.SaveRule(ctx, Rule{Mode: "allow", Countries: []string{"ma"}, Ports: []int{22}})
	if err != nil || r.Countries[0] != "MA" {
		t.Fatal(err, r)
	}
	s.Push("s1")
	mu.Lock()
	last := sent[len(sent)-1]
	mu.Unlock()
	if len(last.Rules) != 1 || len(last.Rules[0].V4) != 2 || last.Rules[0].V6[0] != "2c0f:f000::/24" || last.Safe[0] != "203.0.113.9" {
		t.Fatalf("%+v", last)
	}
	// ipdeny down: the cached list is used; nothing is fetched within the week.
	mu.Lock()
	up, fetches = false, 0
	mu.Unlock()
	s.Push("s1")
	mu.Lock()
	fCount := fetches
	mu.Unlock()
	if fCount != 0 {
		t.Fatal("fetched again within the week")
	}
	// An old agent gets nothing, and says so.
	mu.Lock()
	ver = "0.9.157"
	n := len(sent)
	mu.Unlock()
	s.Push("s1")
	mu.Lock()
	sentNow := len(sent)
	mu.Unlock()
	if sentNow != n || s.Status()["s1"].Error == "" {
		t.Fatal("an old agent was sent rules")
	}
	mu.Lock()
	ver = "0.9.158"
	mu.Unlock()
	if s.DeleteRule(r.ID) != nil || len(s.Rules()) != 0 {
		t.Fatal("delete")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		empty := len(sent) > 0 && len(sent[len(sent)-1].Rules) == 0
		mu.Unlock()
		if empty {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if last := sent[len(sent)-1]; len(last.Rules) != 0 {
		t.Fatal("rules stayed after deleting")
	}
	mu.Unlock()
	// Cloudflare.
	f := &fakeDNS{}
	s.DNS = f
	if err := s.SaveZone(ctx, Zone{Account: "a1", Zone: "shop.ma", Mode: "allow", Countries: []string{"ma"}}); err != nil || f.calls[0] != "a1 shop.ma allow MA" || len(s.Zones()) != 1 {
		t.Fatal(err, f.calls)
	}
	if s.SaveZone(ctx, Zone{Account: "a1", Zone: "shop.ma", Mode: "allow"}) != nil || len(s.Zones()) != 0 {
		t.Fatal("removing the zone rule")
	}
	var st proto.GeoStatus
	b, _ := json.Marshal(proto.GeoStatus{Rules: 1})
	s.OnStatus("s1", b)
	json.Unmarshal(b, &st)
	if s.Status()["s1"].Rules != 1 {
		t.Fatal("status")
	}
}
