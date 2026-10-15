package apps

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/valid"
)

const healthKeep = 7 * 24 * time.Hour

// healthState is the in-memory summary of an app's recent checks.
type healthState struct {
	last     *proto.HealthResult
	failures int // consecutive
	lastOK   int64
	lastFail int64
	loaded   bool
}

func (h HealthView) validate() (HealthView, error) {
	h.URL = strings.TrimSpace(h.URL)
	if h.IntervalSec == 0 {
		h.IntervalSec = 30
	}
	if h.TimeoutMs == 0 {
		h.TimeoutMs = 5000
	}
	if h.ExpectedStatus == 0 {
		h.ExpectedStatus = 200
	}
	if h.Enabled || h.URL != "" {
		u, err := url.Parse(h.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(h.URL) > 2048 || u.User != nil {
			return h, valid.Error("health URL must be an http(s) URL without credentials, e.g. http://127.0.0.1:5002/health")
		}
	}
	switch {
	case h.IntervalSec < 10 || h.IntervalSec > 3600:
		return h, valid.Error("health interval must be 10-3600 seconds")
	case h.TimeoutMs < 500 || h.TimeoutMs > 60000:
		return h, valid.Error("health timeout must be 500-60000 ms")
	case h.ExpectedStatus < 100 || h.ExpectedStatus > 599:
		return h, valid.Error("expected status must be an HTTP status code")
	}
	return h, nil
}

func (s *Service) healthConfig(appID string) HealthView {
	h := HealthView{IntervalSec: 30, TimeoutMs: 5000, ExpectedStatus: 200}
	var raw string
	if s.db.QueryRow(`SELECT health FROM apps WHERE id = ?`, appID).Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &h)
	}
	return h
}

// SetHealth saves an app's health check and pushes it to the agent.
func (s *Service) SetHealth(appID string, h HealthView) (View, error) {
	a, err := s.get(appID)
	if err != nil {
		return View{}, err
	}
	if h, err = h.validate(); err != nil {
		return View{}, err
	}
	raw, _ := json.Marshal(h)
	if _, err := s.db.Exec(`UPDATE apps SET health = ? WHERE id = ?`, string(raw), appID); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	delete(s.health, appID) // reset the summary for the new target
	s.mu.Unlock()
	s.changed(a.ServerID)
	return s.Get(appID)
}

// HealthChecks lists the enabled checks of a server's apps (agent config).
func (s *Service) HealthChecks(serverID string) []proto.HealthCheck {
	out := []proto.HealthCheck{}
	list, err := s.list(serverID)
	if err != nil {
		return out
	}
	for _, a := range list {
		h := s.healthConfig(a.ID)
		if h.Enabled && a.Flags.Monitoring {
			out = append(out, proto.HealthCheck{AppID: a.ID, URL: h.URL, IntervalSec: h.IntervalSec, TimeoutMs: h.TimeoutMs, ExpectedStatus: h.ExpectedStatus})
		}
	}
	return out
}

// healthOf returns the summary, loading recent history after a restart.
func (s *Service) healthOf(appID string) healthState {
	s.mu.Lock()
	st, ok := s.health[appID]
	s.mu.Unlock()
	if ok && st.loaded {
		return st
	}
	st = healthState{loaded: true}
	rows, err := s.db.Query(`SELECT ts, ok, status, latency_ms, error FROM health_results WHERE app_id = ? ORDER BY ts DESC LIMIT 200`, appID)
	if err == nil {
		counting := true
		for rows.Next() {
			var r proto.HealthResult
			if rows.Scan(&r.TS, &r.OK, &r.Status, &r.LatencyMs, &r.Error) != nil {
				break
			}
			r.AppID = appID
			if st.last == nil {
				x := r
				st.last = &x
			}
			if r.OK {
				counting = false
				if st.lastOK == 0 {
					st.lastOK = r.TS
				}
			} else {
				if counting {
					st.failures++
				}
				if st.lastFail == 0 {
					st.lastFail = r.TS
				}
			}
		}
		rows.Close()
	}
	s.mu.Lock()
	s.health[appID] = st
	s.mu.Unlock()
	return st
}

// IngestHealth stores check results from a server's agent and records state changes.
func (s *Service) IngestHealth(serverID string, results []proto.HealthResult) {
	list, err := s.list(serverID)
	if err != nil {
		return
	}
	own := map[string]App{}
	for _, a := range list {
		own[a.ID] = a
	}
	changed := false
	for _, r := range results {
		a, ok := own[r.AppID]
		if !ok {
			continue // not this server's app
		}
		if r.TS <= 0 || r.TS > s.Now().UnixMilli()+60_000 {
			r.TS = s.Now().UnixMilli()
		}
		if len(r.Error) > 500 {
			r.Error = r.Error[:500]
		}
		s.db.Exec(`INSERT INTO health_results (app_id, ts, ok, status, latency_ms, error) VALUES (?, ?, ?, ?, ?, ?)`, r.AppID, r.TS, r.OK, r.Status, r.LatencyMs, r.Error)
		st := s.healthOf(r.AppID)
		wasOK := st.last == nil || st.last.OK
		x := r
		st.last = &x
		if r.OK {
			st.failures, st.lastOK = 0, r.TS
		} else {
			st.failures++
			st.lastFail = r.TS
		}
		s.mu.Lock()
		s.health[r.AppID] = st
		s.mu.Unlock()
		if wasOK && !r.OK {
			s.event(serverID, a.ID, "health", fmt.Sprintf("Health check failing: %s", r.Error))
		} else if !wasOK && r.OK {
			s.event(serverID, a.ID, "health", fmt.Sprintf("Health check recovered (HTTP %d in %d ms)", r.Status, r.LatencyMs))
		}
		changed = true
	}
	if changed {
		s.PublishServer(serverID)
	}
}

// HealthHistory returns an app's recent results, newest first.
func (s *Service) HealthHistory(appID string, limit int) ([]proto.HealthResult, error) {
	rows, err := s.db.Query(`SELECT ts, ok, status, latency_ms, error FROM health_results WHERE app_id = ? ORDER BY ts DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []proto.HealthResult{}
	for rows.Next() {
		r := proto.HealthResult{AppID: appID}
		if err := rows.Scan(&r.TS, &r.OK, &r.Status, &r.LatencyMs, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneHealth deletes results older than a week.
func (s *Service) PruneHealth(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM health_results WHERE ts < ?`, now.Add(-healthKeep).UnixMilli())
	return err
}

// applyHealth fills the health fields of a view.
func (s *Service) applyHealth(v *View) {
	v.Health = s.healthConfig(v.ID)
	if !v.Health.Enabled {
		v.HealthStatus = "disabled"
		return
	}
	st := s.healthOf(v.ID)
	v.HealthFailures, v.HealthLastOK, v.HealthLastFail = st.failures, st.lastOK, st.lastFail
	switch {
	case st.last == nil:
		v.HealthStatus = "unknown"
	case st.last.OK:
		v.HealthStatus = "healthy"
		lat := int(st.last.LatencyMs)
		v.Latency = &lat
	default:
		v.HealthStatus = "failed"
		v.HealthError = st.last.Error
	}
}
