// Package anomaly spots servers behaving unlike themselves: CPU, memory or network
// traffic far above what that server normally does at this time of day, judged against
// its own last two weeks. This catches "80% CPU at 3am" on a server that idles at 10%,
// which a fixed 90% threshold never sees.
package anomaly

import (
	"database/sql"
	"fmt"
	"math"
	"sync"
	"time"

	"fleetwatch/internal/server/incidents"
)

type metric struct {
	id, expr, label, unit string
	scale                 float64 // stored value → shown value
	minValue, minDelta    float64 // in shown units: never alert below minValue, or less than minDelta above normal
}

var watched = []metric{
	{"cpu", "cpu", "CPU", "%", 1, 40, 25},
	{"mem", "mem", "Memory", "%", 1, 50, 20},
	{"net", "net_rx + net_tx", "Network traffic", " Mbps", 8 / 1e6, 20, 10},
}

const (
	history    = 14 * 24 * time.Hour
	recent     = 15 * time.Minute
	minSamples = 15 // hourly averages at this time of day (±1 hour): about five days
)

type Service struct {
	DB        *sql.DB
	Incidents *incidents.Store
	ServerIDs func() []string
	Name      func(serverID string) string
	Enabled   func() bool
	Now       func() time.Time

	mu      sync.Mutex
	learned map[string]learnedAt
}

// judge compares the last minutes with the usual values. open: alert now; clear: back to
// normal (a lower bar, so it does not flap around the limit).
func judge(m metric, usual []float64, now float64) (open, clear bool, mean float64) {
	if len(usual) < minSamples {
		return false, true, 0
	}
	var sum, sq float64
	for _, v := range usual {
		sum += v
	}
	mean = sum / float64(len(usual))
	for _, v := range usual {
		sq += (v - mean) * (v - mean)
	}
	sd := math.Sqrt(sq / float64(len(usual)))
	open = now >= m.minValue && now > mean+max(3*sd, m.minDelta)
	clear = now < mean+max(1.5*sd, m.minDelta/2)
	return open, clear, mean
}

func fmtVal(m metric, v float64) string {
	if m.unit == "%" {
		return fmt.Sprintf("%.0f%%", v)
	}
	return fmt.Sprintf("%.1f%s", v, m.unit)
}

// Check looks at every server once.
func (s *Service) Check() {
	now := s.Now()
	if !s.Enabled() {
		s.Incidents.ClearStale("anomaly", now.AddDate(100, 0, 0), "unusual-activity alerts are turned off")
		return
	}
	for _, id := range s.ServerIDs() {
		s.server(id, now)
	}
}

// usual are a server's hourly averages at this time of day (±1 hour), last two weeks.
func (s *Service) usual(id string, m metric, now time.Time) []float64 {
	hour := now.UTC().Hour()
	rows, err := s.DB.Query(fmt.Sprintf(`SELECT ts, %s FROM server_metrics_1h WHERE server_id = ? AND ts >= ? AND ts < ?`, m.expr),
		id, now.Add(-history).UnixMilli(), now.Add(-2*time.Hour).UnixMilli())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var usual []float64
	for rows.Next() {
		var ts int64
		var v float64
		if rows.Scan(&ts, &v) != nil {
			continue
		}
		if d := (time.UnixMilli(ts).UTC().Hour() - hour + 24) % 24; d <= 1 || d == 23 { // this hour and the ones around it
			usual = append(usual, v*m.scale)
		}
	}
	return usual
}

// Range is what a server normally does now: Low to High (mean ± 2 sd), in shown units.
type Range struct {
	Label string  `json:"label"`
	Unit  string  `json:"unit"`
	Mean  float64 `json:"mean"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
	Alert float64 `json:"alert"` // the level that opens "Unusual activity"
}

// Normal is each watched metric's usual range at this time of day, for those learned.
func (s *Service) Normal(id string) map[string]Range {
	now := s.Now()
	out := map[string]Range{}
	for _, m := range watched {
		usual := s.usual(id, m, now)
		if len(usual) < minSamples {
			continue
		}
		mean, sd := stats(usual)
		r := Range{Label: m.label, Unit: m.unit, Mean: mean, Low: max(0, mean-2*sd), High: mean + 2*sd, Alert: max(m.minValue, mean+max(3*sd, m.minDelta))}
		if m.unit == "%" {
			r.High, r.Alert = min(100, r.High), min(100, r.Alert)
		}
		out[m.id] = r
	}
	return out
}

// Learned reports whether a server's normal CPU and memory are known (about five days
// of history), cached for an hour.
func (s *Service) Learned(id string) bool {
	s.mu.Lock()
	if c, ok := s.learned[id]; ok && s.Now().Sub(c.at) < time.Hour {
		s.mu.Unlock()
		return c.ok
	}
	s.mu.Unlock()
	n := s.Normal(id)
	_, cpu := n["cpu"]
	_, mem := n["mem"]
	s.mu.Lock()
	if s.learned == nil {
		s.learned = map[string]learnedAt{}
	}
	s.learned[id] = learnedAt{s.Now(), cpu && mem}
	s.mu.Unlock()
	return cpu && mem
}

type learnedAt struct {
	at time.Time
	ok bool
}

func stats(v []float64) (mean, sd float64) {
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	for _, x := range v {
		sd += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(sd / float64(len(v)))
}

func (s *Service) server(id string, now time.Time) {
	for _, m := range watched {
		var cur float64
		var n int
		if s.DB.QueryRow(fmt.Sprintf(`SELECT COALESCE(AVG(%s), 0), COUNT(*) FROM server_metrics_1m WHERE server_id = ? AND ts >= ?`, m.expr),
			id, now.Add(-recent).UnixMilli()).Scan(&cur, &n) != nil || n < 10 {
			continue // offline or just added: nothing to judge
		}
		usual := s.usual(id, m, now)
		cur *= m.scale
		key := "anomaly|" + id + "|" + m.id
		open, clear, mean := judge(m, usual, cur)
		switch {
		case open:
			s.Incidents.Ensure(incidents.Signal{Type: "anomaly", Key: key, Severity: "warning", ServerID: id,
				Title:  fmt.Sprintf("Unusual %s on %s: %s", lower(m.label), s.Name(id), fmtVal(m, cur)),
				Sample: fmt.Sprintf("%s has averaged %s for the last 15 minutes; at this time of day it is normally about %s.", m.label, fmtVal(m, cur), fmtVal(m, mean))})
		case clear:
			s.Incidents.Clear(key, "back to its usual level")
		}
	}
}

func lower(s string) string {
	if s == "CPU" {
		return s
	}
	return string(s[0]+'a'-'A') + s[1:]
}
