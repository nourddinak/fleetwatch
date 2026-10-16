// Package vulns finds known vulnerabilities in the apps' dependencies. Agents list the
// exact versions from lock files (npm, Python, Go, PHP); the dashboard looks them up in
// OSV (osv.dev), the open vulnerability database that GitHub, PyPI and Go publish to.
package vulns

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/release"
)

const (
	MinAgent  = "v0.9.38"
	cacheFor  = 7 * 24 * time.Hour
	batchSize = 500
)

// Vuln is one advisory affecting one dependency.
type Vuln struct {
	ID        string   `json:"id"`
	Aliases   []string `json:"aliases,omitempty"` // CVE-…
	Summary   string   `json:"summary"`
	Severity  string   `json:"severity"` // critical | high | moderate | low | unknown
	Package   string   `json:"package"`
	Version   string   `json:"version"`
	Ecosystem string   `json:"ecosystem"`
	Fixed     string   `json:"fixed,omitempty"` // first version with the fix
	URL       string   `json:"url"`
}

// AppReport is the result for one app.
type AppReport struct {
	AppID     string `json:"appId"`
	ServerID  string `json:"serverId"`
	ScannedAt int64  `json:"scannedAt"`
	Deps      int    `json:"deps"`
	Vulns     []Vuln `json:"vulns"`
	Error     string `json:"error,omitempty"`
}

type Service struct {
	db  *sql.DB
	Bus *live.Bus
	Now func() time.Time

	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string
	Connected    func(serverID string) bool
	ServerIDs    func() []string
	AppFolders   func(serverID string) map[string]string // app ID → folder
	AppName      func(appID string) string
	Notify       func(title, body, severity string)

	OSV    string // API base, https://api.osv.dev
	Client *http.Client

	mu       sync.Mutex
	scanning map[string]time.Time
}

func New(d *sql.DB, bus *live.Bus) *Service {
	return &Service{db: d, Bus: bus, Now: time.Now, OSV: "https://api.osv.dev", Client: &http.Client{Timeout: 30 * time.Second},
		scanning: map[string]time.Time{}, Notify: func(string, string, string) {}, AppName: func(id string) string { return id }}
}

// Scan asks a server's agent for its apps' dependencies.
func (s *Service) Scan(serverID string) error {
	if s.AgentVersion != nil && release.Newer(MinAgent, s.AgentVersion(serverID)) {
		return fmt.Errorf("agent too old")
	}
	apps := s.AppFolders(serverID)
	if len(apps) == 0 {
		return nil
	}
	if err := s.Send(serverID, proto.TypeDepsScan, proto.DepsScan{Apps: apps}); err != nil {
		return err
	}
	s.mu.Lock()
	s.scanning[serverID] = s.Now()
	s.mu.Unlock()
	s.Bus.Publish("vulns.scanning", map[string]any{"serverId": serverID, "scanning": true})
	return nil
}

// ScanAll scans every connected server; it returns how many were asked.
func (s *Service) ScanAll() int {
	n := 0
	for _, id := range s.ServerIDs() {
		if s.Connected(id) && s.Scan(id) == nil {
			n++
		}
	}
	return n
}

// Scanning lists the servers whose answer is awaited.
func (s *Service) Scanning() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for id, at := range s.scanning {
		if s.Now().Sub(at) < 10*time.Minute {
			out = append(out, id)
		}
	}
	return out
}

// Run scans every day (first a few minutes after start).
func (s *Service) Run(ctx context.Context) {
	t := time.NewTimer(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ScanAll()
			t.Reset(24 * time.Hour)
		}
	}
}

// OnReport looks up an agent's dependency lists (in the background: OSV can be slow).
func (s *Service) OnReport(serverID string, data json.RawMessage) {
	var rep proto.DepsReport
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var found []string
		for appID, deps := range rep.Apps {
			r := AppReport{AppID: appID, ServerID: serverID, ScannedAt: s.Now().UnixMilli(), Deps: len(deps), Vulns: []Vuln{}}
			vs, err := s.lookup(ctx, deps)
			if err != nil {
				r.Error = err.Error()
				slog.Warn("vulnerability lookup", "app", appID, "err", err)
				if old, ok := s.get(appID); ok { // keep the last good result
					r.Vulns = old.Vulns
				}
			} else {
				r.Vulns = vs
				found = append(found, s.newSerious(appID, vs)...)
			}
			s.save(r)
		}
		s.mu.Lock()
		delete(s.scanning, serverID)
		s.mu.Unlock()
		s.Bus.Publish("vulns", s.List())
		if len(found) > 0 {
			sort.Strings(found)
			s.Notify(fmt.Sprintf("%d new serious vulnerabilit%s in your apps' dependencies", len(found), map[bool]string{true: "y", false: "ies"}[len(found) == 1]),
				strings.Join(found, "\n"), "warning")
		}
	}()
}

// newSerious names the critical and high advisories the app did not have before.
func (s *Service) newSerious(appID string, vs []Vuln) []string {
	old, _ := s.get(appID)
	known := map[string]bool{}
	for _, v := range old.Vulns {
		known[v.ID+v.Package] = true
	}
	var out []string
	for _, v := range vs {
		if (v.Severity == "critical" || v.Severity == "high") && !known[v.ID+v.Package] {
			out = append(out, fmt.Sprintf("%s: %s %s (%s, %s)", s.AppName(appID), v.Package, v.Version, v.ID, v.Severity))
		}
	}
	if len(out) > 10 {
		out = append(out[:10], fmt.Sprintf("… and %d more", len(out)-10))
	}
	return out
}

func (s *Service) save(r AppReport) {
	raw, _ := json.Marshal(r)
	s.db.Exec(`INSERT INTO app_vulns (app_id, server_id, data, scanned_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (app_id) DO UPDATE SET server_id = excluded.server_id, data = excluded.data, scanned_at = excluded.scanned_at`,
		r.AppID, r.ServerID, string(raw), r.ScannedAt)
}

func (s *Service) get(appID string) (AppReport, bool) {
	var raw string
	var r AppReport
	if s.db.QueryRow(`SELECT data FROM app_vulns WHERE app_id = ?`, appID).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &r) != nil {
		return r, false
	}
	return r, true
}

// List is every app's latest result, most vulnerable first.
func (s *Service) List() []AppReport {
	out := []AppReport{}
	rows, err := s.db.Query(`SELECT data FROM app_vulns`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r AppReport
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &r) == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].Vulns) > len(out[j].Vulns) })
	return out
}

// ---------- OSV ----------

type osvQuery struct {
	Package struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
	} `json:"package"`
	Version string `json:"version"`
}

type osvVuln struct {
	ID               string   `json:"id"`
	Summary          string   `json:"summary"`
	Details          string   `json:"details"`
	Aliases          []string `json:"aliases"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	Affected []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		Ranges []struct {
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

func (s *Service) post(ctx context.Context, path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.OSV+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.do(req, out)
}

func (s *Service) do(req *http.Request, out any) error {
	req.Header.Set("User-Agent", "fleetwatch")
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach osv.dev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("osv.dev answered HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// lookup returns the advisories affecting these exact versions.
func (s *Service) lookup(ctx context.Context, deps []proto.Dep) ([]Vuln, error) {
	out := []Vuln{}
	for start := 0; start < len(deps); start += batchSize {
		chunk := deps[start:min(len(deps), start+batchSize)]
		qs := make([]osvQuery, len(chunk))
		for i, d := range chunk {
			qs[i].Package.Name, qs[i].Package.Ecosystem, qs[i].Version = d.Name, d.Ecosystem, d.Version
		}
		var res struct {
			Results []struct {
				Vulns []struct {
					ID string `json:"id"`
				} `json:"vulns"`
			} `json:"results"`
		}
		if err := s.post(ctx, "/v1/querybatch", map[string]any{"queries": qs}, &res); err != nil {
			return nil, err
		}
		// Fetch the advisories a few at a time: an old project can have hundreds.
		var mu sync.Mutex
		var firstErr error
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for i, r := range res.Results {
			if i >= len(chunk) {
				break
			}
			for _, v := range r.Vulns {
				wg.Add(1)
				sem <- struct{}{}
				go func(id string, d proto.Dep) {
					defer func() { <-sem; wg.Done() }()
					full, err := s.vuln(ctx, id)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						firstErr = cmp.Or(firstErr, err)
						return
					}
					out = append(out, describe(full, d))
				}(v.ID, chunk[i])
			}
		}
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
	}
	rank := map[string]int{"critical": 0, "high": 1, "moderate": 2, "low": 3, "unknown": 4}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		return out[i].Package+out[i].ID < out[j].Package+out[j].ID
	})
	return out, nil
}

// vuln fetches one advisory, from the cache when it is fresh.
func (s *Service) vuln(ctx context.Context, id string) (osvVuln, error) {
	var v osvVuln
	var raw string
	var at int64
	if s.db.QueryRow(`SELECT data, fetched_at FROM vuln_cache WHERE id = ?`, id).Scan(&raw, &at) == nil &&
		s.Now().Sub(time.UnixMilli(at)) < cacheFor && json.Unmarshal([]byte(raw), &v) == nil {
		return v, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.OSV+"/v1/vulns/"+id, nil)
	if err != nil {
		return v, err
	}
	if err := s.do(req, &v); err != nil {
		return v, err
	}
	v.Details = "" // long; the summary and the link are enough
	b, _ := json.Marshal(v)
	s.db.Exec(`INSERT INTO vuln_cache (id, data, fetched_at) VALUES (?, ?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data, fetched_at = excluded.fetched_at`,
		id, string(b), s.Now().UnixMilli())
	return v, nil
}

func describe(v osvVuln, d proto.Dep) Vuln {
	out := Vuln{ID: v.ID, Summary: v.Summary, Package: d.Name, Version: d.Version, Ecosystem: d.Ecosystem, URL: "https://osv.dev/vulnerability/" + v.ID}
	for _, a := range v.Aliases {
		if strings.HasPrefix(a, "CVE-") {
			out.Aliases = append(out.Aliases, a)
		}
	}
	if out.Summary == "" && len(out.Aliases) > 0 {
		out.Summary = out.Aliases[0]
	}
	out.Severity = strings.ToLower(v.DatabaseSpecific.Severity)
	if out.Severity == "medium" {
		out.Severity = "moderate"
	}
	if !slices.Contains([]string{"critical", "high", "moderate", "low"}, out.Severity) {
		out.Severity = "unknown"
	}
	for _, a := range v.Affected {
		if !strings.EqualFold(a.Package.Name, d.Name) {
			continue
		}
		// Advisories list a fix per release line (2.2.28, 3.2.14, 4.0.6…): the one to
		// upgrade to is the lowest fixed version above the installed one.
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if f := e["fixed"]; f != "" && compareVersions(f, d.Version) > 0 && (out.Fixed == "" || compareVersions(f, out.Fixed) < 0) {
					out.Fixed = f
				}
			}
		}
	}
	return out
}

// compareVersions orders versions by their numeric parts (1.10.0 > 1.9.3), which is right
// for the semver-like versions of npm, PyPI, Go and Packagist.
func compareVersions(a, b string) int {
	num := func(s string) []int {
		var out []int
		n, in := 0, false
		for _, c := range s + "." {
			if c >= '0' && c <= '9' {
				n, in = n*10+int(c-'0'), true
			} else if in {
				out, n, in = append(out, n), 0, false
			}
		}
		return out
	}
	x, y := num(a), num(b)
	for i := 0; i < max(len(x), len(y)); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p - q
		}
	}
	return 0
}
