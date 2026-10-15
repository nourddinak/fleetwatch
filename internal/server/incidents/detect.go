package incidents

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Rules is the "incidentRules" settings section.
type Rules struct {
	RestartThreshold   int `json:"restartThreshold"`
	RestartWindowMin   int `json:"restartWindowMin"`
	RepeatedErrorCount int `json:"repeatedErrorCount"`
	ErrorWindowMin     int `json:"errorWindowMin"`
	HealthFailures     int `json:"healthFailures"`
	CPUThreshold       int `json:"cpuThreshold"`
	CPUMinutes         int `json:"cpuMinutes"`
	RAMThreshold       int `json:"ramThreshold"`
	RAMMinutes         int `json:"ramMinutes"`
	DiskThreshold      int `json:"diskThreshold"`
	OfflineGraceSec    int `json:"offlineGraceSec"`
}

func DefaultRules() Rules {
	return Rules{RestartThreshold: 5, RestartWindowMin: 10, RepeatedErrorCount: 25, ErrorWindowMin: 5, HealthFailures: 3,
		CPUThreshold: 90, CPUMinutes: 5, RAMThreshold: 90, RAMMinutes: 5, DiskThreshold: 85, OfflineGraceSec: 60}
}

func (r Rules) Validate() error {
	in := func(v, lo, hi int) bool { return v >= lo && v <= hi }
	switch {
	case !in(r.RestartThreshold, 2, 100) || !in(r.RestartWindowMin, 1, 1440):
		return valid.Error("restart rule: 2-100 restarts within 1-1440 minutes")
	case !in(r.RepeatedErrorCount, 2, 100000) || !in(r.ErrorWindowMin, 1, 1440):
		return valid.Error("error rule: 2-100000 errors within 1-1440 minutes")
	case !in(r.HealthFailures, 1, 100):
		return valid.Error("health rule: 1-100 consecutive failures")
	case !in(r.CPUThreshold, 10, 100) || !in(r.CPUMinutes, 1, 1440) || !in(r.RAMThreshold, 10, 100) || !in(r.RAMMinutes, 1, 1440):
		return valid.Error("CPU/RAM rules: 10-100% for 1-1440 minutes")
	case !in(r.DiskThreshold, 10, 100):
		return valid.Error("disk threshold must be 10-100%")
	case !in(r.OfflineGraceSec, 10, 3600):
		return valid.Error("offline grace period must be 10-3600 seconds")
	}
	return nil
}

type appState struct {
	status   string
	restarts int
	restartT []time.Time // when the restart counter went up
	hadPorts bool
}

type serverState struct {
	cpuSince, ramSince time.Time
	offlineTimer       *time.Timer
}

// Detector turns observations into Ensure/Record/Clear calls on the store.
type Detector struct {
	Store      *Store
	db         *sql.DB
	Connected  func(serverID string) bool
	ServerName func(serverID string) string

	mu      sync.Mutex
	apps    map[string]*appState
	servers map[string]*serverState
	errs    map[string][]time.Time // repeated-error windows by key
	rules   Rules
	rulesAt time.Time
	grace   time.Duration // overrides OfflineGraceSec (tests)
}

func NewDetector(s *Store, d *sql.DB, connected func(string) bool, name func(string) string) *Detector {
	return &Detector{Store: s, db: d, Connected: connected, ServerName: name, apps: map[string]*appState{}, servers: map[string]*serverState{}, errs: map[string][]time.Time{}}
}

func (d *Detector) Rules() Rules {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rulesLocked()
}

func (d *Detector) rulesLocked() Rules {
	if time.Since(d.rulesAt) > 10*time.Second {
		r := DefaultRules()
		if settings.Load(d.db, "incidentRules", &r) != nil || r.Validate() != nil {
			r = DefaultRules()
		}
		d.rules, d.rulesAt = r, time.Now()
	}
	return d.rules
}

func (d *Detector) SaveRules(r Rules) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := settings.Save(d.db, "incidentRules", r); err != nil {
		return err
	}
	d.mu.Lock()
	d.rules, d.rulesAt = r, time.Now()
	d.mu.Unlock()
	return nil
}

func key(parts ...string) string { return strings.Join(parts, "|") }

// ---------- servers ----------

// ServerState reacts to an agent connecting or disconnecting. Offline incidents wait
// for the grace period, so agent restarts and short blips do not alert.
func (d *Detector) ServerState(serverID string, connected bool) {
	d.mu.Lock()
	st := d.server(serverID)
	if st.offlineTimer != nil {
		st.offlineTimer.Stop()
		st.offlineTimer = nil
	}
	grace := time.Duration(d.rulesLocked().OfflineGraceSec) * time.Second
	if d.grace > 0 {
		grace = d.grace
	}
	if !connected {
		st.offlineTimer = time.AfterFunc(grace, func() {
			if !d.Connected(serverID) {
				name := d.ServerName(serverID)
				d.Store.Ensure(Signal{Type: "server_offline", Key: key("server_offline", serverID), Severity: "critical",
					Title: name + " is offline", ServerID: serverID, Sample: fmt.Sprintf("No agent connection for more than %s", grace)})
			}
		})
	}
	d.mu.Unlock()
	if connected {
		d.Store.Clear(key("server_offline", serverID), "Agent reconnected")
	}
}

func (d *Detector) server(id string) *serverState {
	st := d.servers[id]
	if st == nil {
		st = &serverState{}
		d.servers[id] = st
	}
	return st
}

// ServerSample checks resource thresholds for one metrics sample.
func (d *Detector) ServerSample(serverID string, s metrics.Sample, now time.Time) {
	d.mu.Lock()
	r := d.rulesLocked()
	st := d.server(serverID)
	cpuFire := track(&st.cpuSince, s.CPU, r.CPUThreshold, r.CPUMinutes, now)
	ramFire := track(&st.ramSince, s.Mem, r.RAMThreshold, r.RAMMinutes, now)
	d.mu.Unlock()
	name := d.ServerName(serverID)
	for _, c := range []struct {
		typ, label string
		v          float64
		fire       int
		thr, min   int
	}{{"high_cpu", "CPU", s.CPU, cpuFire, r.CPUThreshold, r.CPUMinutes}, {"high_ram", "RAM", s.Mem, ramFire, r.RAMThreshold, r.RAMMinutes}} {
		k := key(c.typ, serverID)
		switch c.fire {
		case 1:
			d.Store.Ensure(Signal{Type: c.typ, Key: k, Severity: "warning", Title: fmt.Sprintf("%s %s above %d%% for %d+ minutes", name, c.label, c.thr, c.min),
				ServerID: serverID, Sample: fmt.Sprintf("%s at %.0f%%", c.label, c.v)})
		case -1:
			d.Store.Clear(k, fmt.Sprintf("%s back to %.0f%%", c.label, c.v))
		}
	}
	k := key("disk_full", serverID)
	switch {
	case s.Disk >= float64(r.DiskThreshold):
		sev := "warning"
		if s.Disk >= 95 {
			sev = "critical"
		}
		d.Store.Ensure(Signal{Type: "disk_full", Key: k, Severity: sev, Title: fmt.Sprintf("%s disk %.0f%% full", name, s.Disk), ServerID: serverID,
			Sample: fmt.Sprintf("Root filesystem at %.1f%%", s.Disk)})
	case s.Disk < float64(r.DiskThreshold)-2:
		d.Store.Clear(k, fmt.Sprintf("Disk usage down to %.0f%%", s.Disk))
	}
}

// track returns 1 when v has been >= thr for minutes, -1 when it is clearly below
// (5 points of hysteresis), 0 otherwise.
func track(since *time.Time, v float64, thr, minutes int, now time.Time) int {
	if v >= float64(thr) {
		if since.IsZero() {
			*since = now
		}
		if now.Sub(*since) >= time.Duration(minutes)*time.Minute {
			return 1
		}
		return 0
	}
	if v < float64(thr)-5 {
		*since = time.Time{}
		return -1
	}
	return 0
}

// ---------- applications ----------

var downTypes = []string{"app_offline", "crash_loop", "pm2_errored", "systemd_failed"}

// Apps inspects the current views of one server's apps.
func (d *Detector) Apps(serverID string, views []apps.View, now time.Time) {
	r := d.Rules()
	for _, v := range views {
		if !v.Flags.Monitoring || !v.Flags.Incidents || v.Status == "unknown" {
			continue // no incidents wanted, or the server itself is unreachable
		}
		d.mu.Lock()
		st := d.apps[v.ID]
		first := st == nil
		if first {
			st = &appState{status: v.Status, restarts: v.Restarts}
			d.apps[v.ID] = st
		}
		prev := st.status
		st.status = v.Status
		if v.Restarts > st.restarts {
			for i := 0; i < min(v.Restarts-st.restarts, 100); i++ {
				st.restartT = append(st.restartT, now)
			}
		}
		st.restarts = v.Restarts
		window := now.Add(-time.Duration(r.RestartWindowMin) * time.Minute)
		for len(st.restartT) > 0 && st.restartT[0].Before(window) {
			st.restartT = st.restartT[1:]
		}
		recentRestarts := len(st.restartT)
		hadPorts := st.hadPorts
		if v.Status == "online" && len(v.Ports) > 0 {
			st.hadPorts = true
		}
		d.mu.Unlock()

		d.appDown(serverID, v, prev, first)
		d.appRestarts(serverID, v, recentRestarts, r)
		d.appPorts(serverID, v, hadPorts)
		d.appHealth(serverID, v, r)
	}
}

func (d *Detector) appDown(serverID string, v apps.View, prev string, first bool) {
	down := v.Status == "offline" || v.Status == "stopped" || v.Status == "crash-loop"
	if !down {
		if v.Status == "online" {
			for _, t := range downTypes {
				d.Store.Clear(key(t, v.ID), "Application is online again")
			}
		}
		return
	}
	// Only a change to down raises, so an app added while stopped does not alert. After a
	// server restart (first sight) only real failures raise, not a deliberate stop.
	wasUp := prev == "online" || prev == "starting" || prev == "warning"
	if !wasUp && !(first && v.Status != "stopped") && !d.anyOpen(v.ID) {
		return
	}
	typ, sev, title := "app_offline", "critical", v.Name+" is offline"
	switch {
	case v.Status == "crash-loop":
		typ, title = "crash_loop", v.Name+" is crash-looping"
	case v.RawStatus == "errored":
		typ, title = "pm2_errored", v.Name+" errored in PM2"
	case strings.HasPrefix(v.RawStatus, "failed"):
		typ, title = "systemd_failed", v.Name+" failed (systemd)"
	case v.Status == "stopped":
		sev, title = "warning", v.Name+" is stopped"
	case !v.ProcessFound:
		title = v.Name + " is no longer known to " + v.Manager
	}
	for _, t := range downTypes {
		if t != typ {
			d.Store.Clear(key(t, v.ID), "Superseded by "+Labels[typ])
		}
	}
	d.Store.Ensure(Signal{Type: typ, Key: key(typ, v.ID), Severity: sev, Title: title, ServerID: serverID, AppID: v.ID,
		Sample: fmt.Sprintf("Manager state: %s, %d restarts", orDash(v.RawStatus), v.Restarts)})
}

func (d *Detector) anyOpen(appID string) bool {
	for _, t := range downTypes {
		if d.Store.IsOpen(key(t, appID)) {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (d *Detector) appRestarts(serverID string, v apps.View, recent int, r Rules) {
	k := key("repeated_restarts", v.ID)
	if recent >= r.RestartThreshold {
		d.Store.Ensure(Signal{Type: "repeated_restarts", Key: k, Severity: "warning",
			Title: fmt.Sprintf("%s restarted %d times in %d minutes", v.Name, recent, r.RestartWindowMin), ServerID: serverID, AppID: v.ID,
			Sample: fmt.Sprintf("Restart counter now %d", v.Restarts)})
	} else if recent == 0 {
		d.Store.Clear(k, fmt.Sprintf("No restarts in the last %d minutes", r.RestartWindowMin))
	}
}

func (d *Detector) appPorts(serverID string, v apps.View, hadPorts bool) {
	k := key("port_unavailable", v.ID)
	switch {
	case v.Status == "online" && v.ProcessFound && hadPorts && len(v.Ports) == 0:
		d.Store.Ensure(Signal{Type: "port_unavailable", Key: k, Severity: "warning", Title: v.Name + " is running but no longer listening on its port",
			ServerID: serverID, AppID: v.ID, Sample: "The process is up, but no listening socket was found in the last scan"})
	case len(v.Ports) > 0:
		d.Store.Clear(k, "Listening again on "+portList(v.Ports))
	}
}

func portList(ps []apps.PortView) string {
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s:%d", p.Bind, p.Port))
	}
	return strings.Join(out, ", ")
}

func (d *Detector) appHealth(serverID string, v apps.View, r Rules) {
	k := key("health_failed", v.ID)
	switch {
	case v.Health.Enabled && v.HealthFailures >= r.HealthFailures:
		d.Store.Ensure(Signal{Type: "health_failed", Key: k, Severity: "critical",
			Title: fmt.Sprintf("%s health check failing (%d in a row)", v.Name, v.HealthFailures), ServerID: serverID, AppID: v.ID,
			Sample: v.Health.URL + " → " + v.HealthError})
	case !v.Health.Enabled || v.HealthStatus == "healthy":
		d.Store.Clear(k, "Health check passing again")
	}
}

// ---------- logs ----------

var (
	uuidRe   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	hexRe    = regexp.MustCompile(`(?i)\b(0x)?[0-9a-f]{8,}\b`)
	ipRe     = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	tsRe     = regexp.MustCompile(`\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:?\d\d)?`)
	numRe    = regexp.MustCompile(`\d+`)
	quotedRe = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// Normalize reduces an error message to its shape, so the same error with different
// ids, numbers or timestamps groups together.
func Normalize(msg string) string {
	s := tsRe.ReplaceAllString(msg, "<ts>")
	s = uuidRe.ReplaceAllString(s, "<id>")
	s = ipRe.ReplaceAllString(s, "<ip>")
	s = hexRe.ReplaceAllString(s, "<hex>")
	s = quotedRe.ReplaceAllString(s, `"…"`)
	s = numRe.ReplaceAllString(s, "#")
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// Logs counts error lines per app and message shape; reaching the threshold within the
// window opens one grouped incident, further matches add occurrences.
func (d *Detector) Logs(lines []logs.Line, now time.Time) {
	r := d.Rules()
	window := time.Duration(r.ErrorWindowMin) * time.Minute
	for _, l := range lines {
		if l.AppID == "" || (l.Severity != "error" && l.Severity != "critical") {
			continue
		}
		shape := Normalize(l.Message)
		k := key("repeated_errors", l.AppID, fingerprint(shape))
		sig := Signal{Type: "repeated_errors", Key: k, Severity: "warning", Title: "Repeated error: " + shape, ServerID: l.ServerID, AppID: l.AppID, Sample: l.Message}
		if l.Severity == "critical" {
			sig.Severity = "critical"
		}
		if d.Store.IsOpen(k) {
			d.Store.Record(sig, 1, nil)
			continue
		}
		d.mu.Lock()
		ts := append(d.errs[k], now)
		for len(ts) > 0 && now.Sub(ts[0]) > window {
			ts = ts[1:]
		}
		d.errs[k] = ts
		n := len(ts)
		if n >= r.RepeatedErrorCount {
			delete(d.errs, k)
		}
		d.mu.Unlock()
		if n >= r.RepeatedErrorCount {
			d.Store.Record(sig, n, nil)
		}
	}
}

// Sweep resolves event incidents that went quiet and forgets old error windows.
func (d *Detector) Sweep(now time.Time) {
	r := d.Rules()
	quiet := 2 * time.Duration(r.ErrorWindowMin) * time.Minute
	d.Store.ClearStale("repeated_errors", now.Add(-quiet), fmt.Sprintf("No new occurrences for %d minutes", 2*r.ErrorWindowMin))
	d.mu.Lock()
	for k, ts := range d.errs {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > quiet {
			delete(d.errs, k)
		}
	}
	d.mu.Unlock()
}
