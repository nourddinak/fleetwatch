// Package uptime checks websites from the outside: from the dashboard server itself and
// from the agents of other servers ("locations"). A site that answers on its own server
// but not from the internet, or only from some places, shows up here.
//
// Agents run these checks with their HTTP health checker: the dashboard sends them as
// health checks whose app ID is "uptime:<check id>", so every agent version can do it.
package uptime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/agent/health"
	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/valid"
)

const (
	Dashboard = "dashboard" // the location of checks run by the dashboard server itself
	Prefix    = "uptime:"   // app ID prefix of the health checks agents run for us
	keep      = 30 * 24 * time.Hour
	downAfter = 2 // consecutive failures before a location counts as down
	maxChecks = 200
)

var ErrNotFound = errors.New("uptime check not found")

// Check is one monitored URL.
type Check struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	IntervalSec    int      `json:"intervalSec"`
	TimeoutMs      int      `json:"timeoutMs"`
	ExpectedStatus int      `json:"expectedStatus"`
	SlowMs         int      `json:"slowMs"`    // alert when the p95 response time is above; 0 = off
	Locations      []string `json:"locations"` // Dashboard and/or server IDs
	ServerID       string   `json:"serverId,omitempty"`
	Enabled        bool     `json:"enabled"`
	CreatedAt      int64    `json:"createdAt"`
}

// Result is one check from one location.
type Result struct {
	Location  string `json:"location"`
	TS        int64  `json:"ts"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

// State is where a location stands now.
type State struct {
	Location  string `json:"location"`
	Status    string `json:"status"` // up | down | unknown
	TS        int64  `json:"ts,omitempty"`
	Code      int    `json:"code,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Day is one bar of the 30-day uptime history.
type Day struct {
	Day    int64   `json:"day"`    // unix ms, start of the UTC day
	Uptime float64 `json:"uptime"` // percent; -1 = no data
}

// View is a check with its current state, as the UI shows it.
type View struct {
	Check
	Status    string  `json:"status"` // up | down | partial | slow | paused | unknown
	States    []State `json:"states"`
	Uptime24h float64 `json:"uptime24h"` // percent; -1 = no data
	Uptime7d  float64 `json:"uptime7d"`
	Uptime30d float64 `json:"uptime30d"`
	P50       int64   `json:"p50"` // ms, last 24 hours
	P95       int64   `json:"p95"`
	Days      []Day   `json:"days"`
}

type Service struct {
	db        *sql.DB
	Incidents *incidents.Store
	Bus       *live.Bus
	Now       func() time.Time

	ServerExists func(id string) bool
	OnChange     func(serverID string)                   // the checks a server's agent runs changed
	Send         func(serverID, typ string, v any) error // ask an agent to check now

	probe   func(ctx context.Context, c proto.HealthCheck) proto.HealthResult
	mu      sync.Mutex
	lastRun map[string]time.Time // dashboard location
	running map[string]bool
	stats   map[string]stats // per check, recomputed at most once a minute
}

// stats are the slow parts of a View: uptime percentages, response times, daily bars.
type stats struct {
	at                             time.Time
	uptime24h, uptime7d, uptime30d float64
	p50, p95                       int64
	days                           []Day
}

func New(d *sql.DB, inc *incidents.Store, bus *live.Bus) *Service {
	return &Service{db: d, Incidents: inc, Bus: bus, Now: time.Now, probe: health.Probe, lastRun: map[string]time.Time{}, running: map[string]bool{}, stats: map[string]stats{},
		ServerExists: func(string) bool { return true }}
}

// ---------- checks ----------

func (s *Service) list() ([]Check, error) {
	rows, err := s.db.Query(`SELECT data FROM uptime_checks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Check{}
	for rows.Next() {
		var raw string
		var c Check
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &c) == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func (s *Service) Get(id string) (Check, error) {
	var raw string
	var c Check
	if err := s.db.QueryRow(`SELECT data FROM uptime_checks WHERE id = ?`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	return c, json.Unmarshal([]byte(raw), &c)
}

func (s *Service) validate(c Check) (Check, error) {
	c.URL = strings.TrimSpace(c.URL)
	c.Name = strings.TrimSpace(c.Name)
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(c.URL) > 2048 {
		return c, valid.Error("the URL must be an http(s) address without a password, e.g. https://example.com")
	}
	if c.Name == "" {
		c.Name = u.Host
	}
	if len(c.Name) > 80 {
		return c, valid.Error("the name can be 80 characters at most")
	}
	if c.IntervalSec == 0 {
		c.IntervalSec = 60
	}
	if c.TimeoutMs == 0 {
		c.TimeoutMs = 10000
	}
	if c.ExpectedStatus == 0 {
		c.ExpectedStatus = 200
	}
	switch {
	case c.IntervalSec < 30 || c.IntervalSec > 3600:
		return c, valid.Error("check every 30-3600 seconds")
	case c.TimeoutMs < 1000 || c.TimeoutMs > 30000:
		return c, valid.Error("the timeout must be 1-30 seconds")
	case c.ExpectedStatus < 100 || c.ExpectedStatus > 599:
		return c, valid.Error("the expected status must be an HTTP status code")
	case c.SlowMs != 0 && (c.SlowMs < 100 || c.SlowMs > 60000):
		return c, valid.Error("the slow limit must be 100-60000 ms (or 0 for no limit)")
	case len(c.Locations) == 0:
		return c, valid.Error("pick at least one place to check from")
	case len(c.Locations) > 10:
		return c, valid.Error("check from 10 places at most")
	case c.ServerID != "" && !s.ServerExists(c.ServerID):
		return c, valid.Error("unknown server")
	}
	seen := map[string]bool{}
	locs := []string{}
	for _, l := range c.Locations {
		if seen[l] {
			continue
		}
		seen[l] = true
		if l == Dashboard || s.ServerExists(l) { // a removed server just drops out
			locs = append(locs, l)
		}
	}
	if len(locs) == 0 {
		return c, valid.Error("pick at least one place to check from")
	}
	c.Locations = locs
	return c, nil
}

func (s *Service) save(c Check) error {
	raw, _ := json.Marshal(c)
	_, err := s.db.Exec(`INSERT INTO uptime_checks (id, data, created_at) VALUES (?, ?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data`,
		c.ID, string(raw), c.CreatedAt)
	return err
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "up_" + hex.EncodeToString(b)
}

// Create adds a check.
func (s *Service) Create(c Check) (View, error) {
	c, err := s.validate(c)
	if err != nil {
		return View{}, err
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM uptime_checks`).Scan(&n)
	if n >= maxChecks {
		return View{}, valid.Error(fmt.Sprintf("%d checks at most", maxChecks))
	}
	c.ID, c.CreatedAt = newID(), s.Now().UnixMilli()
	if err := s.save(c); err != nil {
		return View{}, err
	}
	s.changed(nil, c.Locations)
	s.CheckNow(c.ID)
	return s.View(c), nil
}

// Update replaces a check's settings.
func (s *Service) Update(id string, c Check) (View, error) {
	old, err := s.Get(id)
	if err != nil {
		return View{}, err
	}
	if c, err = s.validate(c); err != nil {
		return View{}, err
	}
	c.ID, c.CreatedAt = old.ID, old.CreatedAt
	if err := s.save(c); err != nil {
		return View{}, err
	}
	if !c.Enabled {
		s.clearIncidents(c, "check paused")
	}
	s.changed(old.Locations, c.Locations)
	s.evaluate(c)
	return s.View(c), nil
}

// Delete removes a check and its results.
func (s *Service) Delete(id string) error {
	c, err := s.Get(id)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM uptime_checks WHERE id = ?`, id); err != nil {
		return err
	}
	s.clearIncidents(c, "check deleted")
	s.changed(c.Locations, nil)
	s.mu.Lock()
	delete(s.stats, id)
	delete(s.lastRun, id)
	s.mu.Unlock()
	s.Bus.Publish("uptime.removed", map[string]string{"id": id})
	return nil
}

func (s *Service) clearIncidents(c Check, reason string) {
	s.Incidents.Clear("uptime|"+c.ID, reason)
	s.Incidents.Clear("uptime_slow|"+c.ID, reason)
}

// changed tells the agents whose checks changed.
func (s *Service) changed(before, after []string) {
	if s.OnChange == nil {
		return
	}
	for _, l := range slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(before), after...)))) {
		if l != Dashboard {
			s.OnChange(l)
		}
	}
}

// AgentChecks are the health checks one server's agent runs for us.
func (s *Service) AgentChecks(serverID string) []proto.HealthCheck {
	list, _ := s.list()
	out := []proto.HealthCheck{}
	for _, c := range list {
		if c.Enabled && slices.Contains(c.Locations, serverID) {
			out = append(out, proto.HealthCheck{AppID: Prefix + c.ID, URL: c.URL, IntervalSec: c.IntervalSec, TimeoutMs: c.TimeoutMs, ExpectedStatus: c.ExpectedStatus})
		}
	}
	return out
}

// CheckNow runs a check from every location right away.
func (s *Service) CheckNow(id string) error {
	c, err := s.Get(id)
	if err != nil {
		return err
	}
	for _, l := range c.Locations {
		if l == Dashboard {
			go s.runDashboard(c)
		} else if s.Send != nil {
			s.Send(l, proto.TypeHealthNow, Prefix+c.ID)
		}
	}
	return nil
}

// ---------- results ----------

// Ingest stores results an agent sent for its location (app IDs without the prefix).
func (s *Service) Ingest(location string, results []proto.HealthResult) {
	touched := map[string]Check{}
	for _, r := range results {
		c, ok := touched[r.AppID]
		if !ok {
			var err error
			if c, err = s.Get(r.AppID); err != nil || !slices.Contains(c.Locations, location) {
				continue // deleted, or not a check this server was asked to run
			}
		}
		s.store(c.ID, location, r)
		touched[c.ID] = c
	}
	for _, c := range touched {
		s.evaluate(c)
	}
}

func (s *Service) store(checkID, location string, r proto.HealthResult) {
	now := s.Now().UnixMilli()
	if r.TS <= 0 || r.TS > now+60_000 {
		r.TS = now
	}
	if len(r.Error) > 300 {
		r.Error = r.Error[:300]
	}
	s.db.Exec(`INSERT INTO uptime_results (check_id, location, ts, ok, status, latency_ms, error) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		checkID, location, r.TS, r.OK, r.Status, r.LatencyMs, r.Error)
}

func (s *Service) runDashboard(c Check) {
	s.mu.Lock()
	if s.running[c.ID] {
		s.mu.Unlock()
		return
	}
	s.running[c.ID], s.lastRun[c.ID] = true, s.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, c.ID)
		s.mu.Unlock()
	}()
	r := s.probe(context.Background(), proto.HealthCheck{AppID: c.ID, URL: c.URL, TimeoutMs: c.TimeoutMs, ExpectedStatus: c.ExpectedStatus})
	s.store(c.ID, Dashboard, r)
	s.evaluate(c)
}

// Run checks from the dashboard when they are due and prunes old results.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	pruned := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			list, err := s.list()
			if err != nil {
				continue
			}
			for _, c := range list {
				if !c.Enabled || !slices.Contains(c.Locations, Dashboard) {
					continue
				}
				s.mu.Lock()
				due := now.Sub(s.lastRun[c.ID]) >= time.Duration(c.IntervalSec)*time.Second
				s.mu.Unlock()
				if due {
					go s.runDashboard(c)
				}
			}
			if now.Sub(pruned) > time.Hour {
				pruned = now
				if _, err := s.db.Exec(`DELETE FROM uptime_results WHERE ts < ?`, now.Add(-keep).UnixMilli()); err != nil {
					slog.Error("uptime retention", "err", err)
				}
			}
		}
	}
}

// states is where each location stands: the newest results within the last few intervals.
func (s *Service) states(c Check) []State {
	since := s.Now().Add(-time.Duration(3*c.IntervalSec)*time.Second - time.Duration(c.TimeoutMs)*time.Millisecond).UnixMilli()
	rows, err := s.db.Query(`SELECT location, ts, ok, status, latency_ms, error FROM uptime_results WHERE check_id = ? AND ts >= ? ORDER BY ts DESC`, c.ID, since)
	recent := map[string][]Result{}
	if err == nil {
		for rows.Next() {
			var r Result
			if rows.Scan(&r.Location, &r.TS, &r.OK, &r.Status, &r.LatencyMs, &r.Error) == nil {
				recent[r.Location] = append(recent[r.Location], r)
			}
		}
		rows.Close()
	}
	out := make([]State, 0, len(c.Locations))
	for _, l := range c.Locations {
		st := State{Location: l, Status: "unknown"}
		if list := recent[l]; len(list) > 0 {
			last := list[0]
			st.TS, st.Code, st.LatencyMs, st.Error = last.TS, last.Status, last.LatencyMs, last.Error
			st.Status = "up"
			if !last.OK {
				fails := 0
				for _, r := range list {
					if r.OK {
						break
					}
					fails++
				}
				if fails >= downAfter {
					st.Status = "down"
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// p95 of the recent successful response times; ok is false with too few results.
func (s *Service) recentP95(c Check) (int64, bool) {
	rows, err := s.db.Query(`SELECT latency_ms FROM uptime_results WHERE check_id = ? AND ok = 1 AND ts >= ? ORDER BY ts DESC LIMIT 20`,
		c.ID, s.Now().Add(-30*time.Minute).UnixMilli())
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	var l []int64
	for rows.Next() {
		var v int64
		rows.Scan(&v)
		l = append(l, v)
	}
	if len(l) < 5 {
		return 0, false
	}
	return percentile(l, 95), true
}

func percentile(l []int64, p int) int64 {
	if len(l) == 0 {
		return 0
	}
	l = slices.Clone(l)
	slices.Sort(l)
	i := (len(l)*p + 99) / 100
	return l[max(0, min(len(l)-1, i-1))]
}

// evaluate opens or clears the check's incidents and tells the UI.
func (s *Service) evaluate(c Check) {
	defer func() { s.Bus.Publish("uptime", s.View(c)) }()
	if !c.Enabled {
		return
	}
	var down, up []string
	lastErr := ""
	for _, st := range s.states(c) {
		switch st.Status {
		case "down":
			down = append(down, st.Location)
			if lastErr == "" {
				lastErr = st.Error
			}
		case "up":
			up = append(up, st.Location)
		}
	}
	key := "uptime|" + c.ID
	switch {
	case len(down) > 0 && len(up) == 0:
		title := c.Name + " is down"
		if len(down) > 1 {
			title += fmt.Sprintf(" from all %d places", len(down))
		}
		s.Incidents.Ensure(incidents.Signal{Type: "uptime_down", Key: key, Severity: "critical", Title: title, ServerID: c.ServerID, Sample: c.URL + ": " + lastErr})
	case len(down) > 0:
		s.Incidents.Ensure(incidents.Signal{Type: "uptime_down", Key: key, Severity: "warning", ServerID: c.ServerID,
			Title: fmt.Sprintf("%s is down from %d of %d places", c.Name, len(down), len(down)+len(up)), Sample: c.URL + ": " + lastErr})
	case len(up) > 0:
		s.Incidents.Clear(key, "responding again")
	}
	if c.SlowMs > 0 {
		if p95, ok := s.recentP95(c); ok {
			slowKey := "uptime_slow|" + c.ID
			if p95 > int64(c.SlowMs) {
				s.Incidents.Ensure(incidents.Signal{Type: "uptime_slow", Key: slowKey, Severity: "warning", ServerID: c.ServerID,
					Title: fmt.Sprintf("%s is slow: %s (limit %s)", c.Name, ms(p95), ms(int64(c.SlowMs))), Sample: "95% of responses within " + ms(p95)})
			} else if p95 < int64(c.SlowMs)*8/10 { // a margin, so it does not flap around the limit
				s.Incidents.Clear(slowKey, "fast again ("+ms(p95)+")")
			}
		}
	}
}

func ms(v int64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1f s", float64(v)/1000)
	}
	return fmt.Sprintf("%d ms", v)
}

// ---------- views ----------

func (s *Service) uptimeSince(id string, since time.Time) float64 {
	var ok, n int
	s.db.QueryRow(`SELECT COALESCE(SUM(ok), 0), COUNT(*) FROM uptime_results WHERE check_id = ? AND ts >= ?`, id, since.UnixMilli()).Scan(&ok, &n)
	if n == 0 {
		return -1
	}
	return float64(int(float64(ok)/float64(n)*10000)) / 100
}

func (s *Service) View(c Check) View {
	now := s.Now()
	s.mu.Lock()
	st, fresh := s.stats[c.ID]
	s.mu.Unlock()
	if !fresh || now.Sub(st.at) > time.Minute {
		st = s.computeStats(c, now)
		s.mu.Lock()
		s.stats[c.ID] = st
		s.mu.Unlock()
	}
	v := View{Check: c, States: s.states(c), Uptime24h: st.uptime24h, Uptime7d: st.uptime7d, Uptime30d: st.uptime30d, P50: st.p50, P95: st.p95, Days: st.days}
	var down, up int
	for _, st := range v.States {
		switch st.Status {
		case "down":
			down++
		case "up":
			up++
		}
	}
	return finish(v, c, down, up, s.Incidents.IsOpen("uptime_slow|"+c.ID))
}

func (s *Service) computeStats(c Check, now time.Time) stats {
	v := stats{at: now, uptime24h: s.uptimeSince(c.ID, now.Add(-24*time.Hour)),
		uptime7d: s.uptimeSince(c.ID, now.Add(-7*24*time.Hour)), uptime30d: s.uptimeSince(c.ID, now.Add(-30*24*time.Hour))}
	if rows, err := s.db.Query(`SELECT latency_ms FROM uptime_results WHERE check_id = ? AND ok = 1 AND ts >= ?`, c.ID, now.Add(-24*time.Hour).UnixMilli()); err == nil {
		var l []int64
		for rows.Next() {
			var x int64
			rows.Scan(&x)
			l = append(l, x)
		}
		rows.Close()
		v.p50, v.p95 = percentile(l, 50), percentile(l, 95)
	}
	const day = 24 * time.Hour
	today := now.UTC().Truncate(day)
	days := map[int64][2]int{}
	if rows, err := s.db.Query(`SELECT ts / 86400000, SUM(ok), COUNT(*) FROM uptime_results WHERE check_id = ? AND ts >= ? GROUP BY 1`,
		c.ID, today.Add(-29*day).UnixMilli()); err == nil {
		for rows.Next() {
			var d int64
			var ok, n int
			rows.Scan(&d, &ok, &n)
			days[d] = [2]int{ok, n}
		}
		rows.Close()
	}
	for i := 29; i >= 0; i-- {
		d := today.Add(-time.Duration(i) * day)
		x := Day{Day: d.UnixMilli(), Uptime: -1}
		if c := days[d.UnixMilli()/86400000]; c[1] > 0 {
			x.Uptime = float64(int(float64(c[0])/float64(c[1])*10000)) / 100
		}
		v.days = append(v.days, x)
	}
	return v
}

func finish(v View, c Check, down, up int, slow bool) View {
	switch {
	case !c.Enabled:
		v.Status = "paused"
	case down > 0 && up == 0:
		v.Status = "down"
	case down > 0:
		v.Status = "partial"
	case up == 0:
		v.Status = "unknown"
	case slow:
		v.Status = "slow"
	default:
		v.Status = "up"
	}
	return v
}

// Views lists every check with its state.
func (s *Service) Views() ([]View, error) {
	list, err := s.list()
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(list))
	for _, c := range list {
		out = append(out, s.View(c))
	}
	return out, nil
}

// Point is one bucket of the response time chart.
type Point struct {
	T     int64 `json:"t"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
	Total int   `json:"total"`
	Fails int   `json:"fails"`
}

// LocationStats sums up one location over the chart's range.
type LocationStats struct {
	Location string  `json:"location"`
	P50      int64   `json:"p50"`
	P95      int64   `json:"p95"`
	Uptime   float64 `json:"uptime"`
	Checks   int     `json:"checks"`
}

type History struct {
	Points    []Point         `json:"points"`
	Locations []LocationStats `json:"locations"`
	Failures  []Result        `json:"failures"` // newest first
}

var Spans = map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// History returns response times (p50/p95 per bucket), per-location stats and recent failures.
func (s *Service) History(id, rangeID, location string) (History, error) {
	h := History{Points: []Point{}, Locations: []LocationStats{}, Failures: []Result{}}
	span, ok := Spans[rangeID]
	if !ok {
		return h, valid.Error("unknown time range")
	}
	c, err := s.Get(id)
	if err != nil {
		return h, err
	}
	from := s.Now().Add(-span).UnixMilli()
	bucket := span.Milliseconds() / 60
	rows, err := s.db.Query(`SELECT location, ts, ok, status, latency_ms, error FROM uptime_results WHERE check_id = ? AND ts >= ? AND (? = '' OR location = ?) ORDER BY ts`,
		id, from, location, location)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	type acc struct {
		lat         []int64
		total, fail int
	}
	buckets := map[int64]*acc{}
	locs := map[string]*acc{}
	for rows.Next() {
		var r Result
		if rows.Scan(&r.Location, &r.TS, &r.OK, &r.Status, &r.LatencyMs, &r.Error) != nil {
			continue
		}
		b := (r.TS - from) / bucket * bucket
		if buckets[b] == nil {
			buckets[b] = &acc{}
		}
		if locs[r.Location] == nil {
			locs[r.Location] = &acc{}
		}
		for _, a := range []*acc{buckets[b], locs[r.Location]} {
			a.total++
			if r.OK {
				a.lat = append(a.lat, r.LatencyMs)
			} else {
				a.fail++
			}
		}
		if !r.OK {
			h.Failures = append(h.Failures, r)
		}
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		a := buckets[k]
		h.Points = append(h.Points, Point{T: from + k, P50: percentile(a.lat, 50), P95: percentile(a.lat, 95), Total: a.total, Fails: a.fail})
	}
	for _, l := range c.Locations {
		if a := locs[l]; a != nil {
			h.Locations = append(h.Locations, LocationStats{Location: l, P50: percentile(a.lat, 50), P95: percentile(a.lat, 95),
				Uptime: float64(int(float64(a.total-a.fail)/float64(a.total)*10000)) / 100, Checks: a.total})
		}
	}
	slices.Reverse(h.Failures)
	if len(h.Failures) > 50 {
		h.Failures = h.Failures[:50]
	}
	return h, rows.Err()
}
