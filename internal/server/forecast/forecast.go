// Package forecast reads trends from the stored metrics: when a disk will be full at
// its current pace, and apps whose memory keeps growing (a likely leak). It raises
// warnings as incidents before things break.
package forecast

import (
	"fmt"
	"math"
	"sync"
	"time"

	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/metrics"
)

const day = 24 * time.Hour

// Disk is a server's disk outlook.
type Disk struct {
	Now      float64 `json:"now"`      // % used
	PerDay   float64 `json:"perDay"`   // percentage points per day
	FullDays float64 `json:"fullDays"` // days until 100%, at this pace
}

// Memory is an app whose memory keeps growing since it started.
type Memory struct {
	FromMB    float64 `json:"fromMb"`
	NowMB     float64 `json:"nowMb"`
	PctPerDay float64 `json:"pctPerDay"`
	Since     int64   `json:"since"` // process start (unix ms)
}

type Result struct {
	Servers map[string]Disk   `json:"servers"`
	Apps    map[string]Memory `json:"apps"`
	At      int64             `json:"at"`
}

// fit is a least-squares line through (x, y); r2 tells how straight the data is.
func fit(xs, ys []float64) (slope, intercept, r2 float64) {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx, sy, sxx, sxy = sx+xs[i], sy+ys[i], sxx+xs[i]*xs[i], sxy+xs[i]*ys[i]
	}
	den := n*sxx - sx*sx
	if n < 2 || den == 0 {
		return 0, 0, 0
	}
	slope = (n*sxy - sx*sy) / den
	intercept = (sy - slope*sx) / n
	mean := sy / n
	var tot, res float64
	for i := range xs {
		f := slope*xs[i] + intercept
		tot += (ys[i] - mean) * (ys[i] - mean)
		res += (ys[i] - f) * (ys[i] - f)
	}
	if tot == 0 {
		return slope, intercept, 0
	}
	return slope, intercept, 1 - res/tot
}

// DiskOutlook fits the disk use of the last days. ok is false without a clear,
// rising trend over at least a day.
func DiskOutlook(rows []metrics.Row, col int) (Disk, bool) {
	if len(rows) < 6 || rows[len(rows)-1].T-rows[0].T < day.Milliseconds() {
		return Disk{}, false
	}
	xs, ys := make([]float64, len(rows)), make([]float64, len(rows))
	for i, r := range rows {
		xs[i], ys[i] = float64(r.T-rows[0].T)/float64(day.Milliseconds()), r.V[col]
	}
	slope, _, r2 := fit(xs, ys)
	now := ys[len(ys)-1]
	if slope < 0.05 || r2 < 0.5 || now >= 100 {
		return Disk{Now: now, PerDay: slope}, false
	}
	return Disk{Now: now, PerDay: math.Round(slope*100) / 100, FullDays: math.Round((100-now)/slope*10) / 10}, true
}

// MemoryGrowth looks at an app's memory since its process started. A leak shows as
// memory rising steadily for at least 12 hours without a restart.
func MemoryGrowth(rows []metrics.Row, col int, since int64) (Memory, bool) {
	var xs, ys []float64
	for _, r := range rows {
		if r.T >= since && r.V[col] > 0 {
			xs = append(xs, float64(r.T-since)/float64(day.Milliseconds()))
			ys = append(ys, r.V[col])
		}
	}
	if len(xs) < 6 || xs[len(xs)-1]-xs[0] < 0.5 {
		return Memory{}, false
	}
	slope, intercept, r2 := fit(xs, ys)
	start := max(intercept+slope*xs[0], 1)
	pct := slope / start * 100
	now := ys[len(ys)-1]
	if r2 < 0.7 || pct < 3 || now-ys[0] < 50 {
		return Memory{}, false
	}
	return Memory{FromMB: math.Round(ys[0]), NowMB: math.Round(now), PctPerDay: math.Round(pct*10) / 10, Since: since}, true
}

// Service recomputes hourly and keeps incidents in line.
type Service struct {
	Servers   *metrics.Store
	AppStore  *metrics.Store
	Apps      *apps.Service
	ServerIDs func() []string
	Name      func(serverID string) string
	Incidents *incidents.Store
	Now       func() time.Time

	mu   sync.Mutex
	last Result
}

func (s *Service) Last() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.Servers == nil { // not computed yet
		return Result{Servers: map[string]Disk{}, Apps: map[string]Memory{}}
	}
	return s.last
}

func (s *Service) Run() Result {
	now := s.Now()
	res := Result{Servers: map[string]Disk{}, Apps: map[string]Memory{}, At: now.UnixMilli()}
	for _, id := range s.ServerIDs() {
		rows, err := s.Servers.Rows(id, "7d", now)
		if err != nil {
			continue
		}
		key := "disk_filling|" + id
		d, ok := DiskOutlook(rows, 3)
		if !ok || d.FullDays > 60 {
			s.Incidents.Clear(key, "Disk use is no longer heading for full")
			continue
		}
		res.Servers[id] = d
		switch {
		case d.FullDays <= 3:
			s.Incidents.Ensure(incidents.Signal{Type: "disk_filling", Key: key, Severity: "critical", ServerID: id,
				Title: fmt.Sprintf("%s disk will be full in about %s", s.Name(id), days(d.FullDays)), Sample: fmt.Sprintf("%.0f%% used, +%.1f points a day", d.Now, d.PerDay)})
		case d.FullDays <= 14:
			s.Incidents.Ensure(incidents.Signal{Type: "disk_filling", Key: key, Severity: "warning", ServerID: id,
				Title: fmt.Sprintf("%s disk will be full in about %s", s.Name(id), days(d.FullDays)), Sample: fmt.Sprintf("%.0f%% used, +%.1f points a day", d.Now, d.PerDay)})
		default:
			s.Incidents.Clear(key, "Disk use is no longer heading for full soon")
		}
	}
	views, _ := s.Apps.List("")
	for _, v := range views {
		key := "memory_leak|" + v.ID
		if v.UptimeSec <= 0 {
			s.Incidents.Clear(key, "The app restarted")
			continue
		}
		rows, err := s.AppStore.Rows(v.ID, "7d", now)
		if err != nil {
			continue
		}
		m, ok := MemoryGrowth(rows, 1, now.Add(-time.Duration(v.UptimeSec)*time.Second).UnixMilli())
		if !ok {
			s.Incidents.Clear(key, "Memory is no longer growing steadily")
			continue
		}
		res.Apps[v.ID] = m
		s.Incidents.Ensure(incidents.Signal{Type: "memory_leak", Key: key, Severity: "warning", ServerID: v.ServerID, AppID: v.ID,
			Title:  fmt.Sprintf("%s memory keeps growing (%.0f → %.0f MB, +%.0f%% a day): possible memory leak", v.Name, m.FromMB, m.NowMB, m.PctPerDay),
			Sample: "Memory has risen steadily since the process started; a restart frees it for a while, a fix in the code ends it."})
	}
	s.mu.Lock()
	s.last = res
	s.mu.Unlock()
	return res
}

func days(d float64) string {
	switch {
	case d < 1:
		return fmt.Sprintf("%.0f hours", d*24)
	case d < 2:
		return "1 day"
	}
	return fmt.Sprintf("%.0f days", d)
}
