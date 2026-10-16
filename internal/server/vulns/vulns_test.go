package vulns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/live"
)

func TestLookup(t *testing.T) {
	var details atomic.Int32
	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/querybatch":
			var in struct{ Queries []osvQuery }
			json.NewDecoder(r.Body).Decode(&in)
			res := make([]map[string]any, len(in.Queries))
			for i, q := range in.Queries {
				res[i] = map[string]any{}
				if q.Package.Name == "lodash" && q.Version == "4.17.20" {
					res[i]["vulns"] = []map[string]string{{"id": "GHSA-35jh-r3h4-6jhm"}}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"results": res})
		case strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
			details.Add(1)
			w.Write([]byte(`{"id":"GHSA-35jh-r3h4-6jhm","summary":"Command Injection in lodash","aliases":["CVE-2021-23337"],
				"database_specific":{"severity":"HIGH"},
				"affected":[{"package":{"name":"lodash","ecosystem":"npm"},"ranges":[{"events":[{"introduced":"0"},{"fixed":"3.10.2"}]},{"events":[{"introduced":"4.0.0"},{"fixed":"4.17.21"}]}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer osv.Close()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS-1', 0)`)
	d.Exec(`INSERT INTO apps (id, server_id, name, manager, process_key, created_at) VALUES ('a1', 's1', 'API', 'pm2', 'api', 0)`)
	s := New(d, live.NewBus())
	s.OSV = osv.URL
	deps := []proto.Dep{{Ecosystem: "npm", Name: "lodash", Version: "4.17.20"}, {Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"}}
	vs, err := s.lookup(context.Background(), deps)
	if err != nil || len(vs) != 1 {
		t.Fatal(vs, err)
	}
	v := vs[0]
	if v.Severity != "high" || v.Fixed != "4.17.21" || v.Aliases[0] != "CVE-2021-23337" || v.Package != "lodash" {
		t.Fatalf("%+v", v)
	}
	s.lookup(context.Background(), deps)
	if details.Load() != 1 {
		t.Fatal("advisory not cached")
	}
	if got := s.newSerious("a1", vs); len(got) != 1 {
		t.Fatal(got)
	}
	s.save(AppReport{AppID: "a1", ServerID: "s1", Vulns: vs})
	if got := s.newSerious("a1", vs); len(got) != 0 {
		t.Fatal("known advisory reported again", got)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.10.0", "1.9.3", 1}, {"v0.7.0", "v0.17.0", -1}, {"3.2.14", "3.2.14", 0}, {"2.0", "2.0.1", -1}} {
		if got := compareVersions(c.a, c.b); (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("%s vs %s = %d", c.a, c.b, got)
		}
	}
}
