package incidents

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Log alerts: "alert me when `payment failed` shows up more than 5 times in 10 minutes".
// Each rule watches the logs as they are stored and opens an incident when its text
// shows up often enough; the incident goes to the usual notifications and on-call.

const logAlertsKey = "logAlerts"

type LogAlert struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Match     string `json:"match"`     // text to look for (any case), or a regular expression
	Regex     bool   `json:"regex"`     // Match is a regular expression
	Count     int    `json:"count"`     // this many matching lines...
	WindowMin int    `json:"windowMin"` // ...within this many minutes
	ServerID  string `json:"serverId"`  // "" = every server
	AppID     string `json:"appId"`     // "" = every app (and system logs)
	Severity  string `json:"severity"`  // warning | critical
	Enabled   bool   `json:"enabled"`
}

type compiledAlert struct {
	LogAlert
	re *regexp.Regexp
}

func (a LogAlert) key() string { return key("log_alert", a.ID) }

// LogAlerts lists the rules.
func (d *Detector) LogAlerts() []LogAlert {
	list := []LogAlert{}
	settings.Load(d.db, logAlertsKey, &list)
	return list
}

// SaveLogAlerts replaces the rules; new rules get an ID.
func (d *Detector) SaveLogAlerts(list []LogAlert) ([]LogAlert, error) {
	if len(list) > 50 {
		return nil, valid.Error("at most 50 log alerts")
	}
	compiled := make([]compiledAlert, 0, len(list))
	for i := range list {
		a := &list[i]
		a.Name, a.Match = strings.TrimSpace(a.Name), strings.TrimSpace(a.Match)
		if a.ID == "" {
			b := make([]byte, 6)
			rand.Read(b)
			a.ID = "la_" + hex.EncodeToString(b)
		}
		if a.Severity != "critical" {
			a.Severity = "warning"
		}
		switch {
		case a.Name == "" || len(a.Name) > 80:
			return nil, valid.Error("each log alert needs a name (up to 80 characters)")
		case a.Match == "" || len(a.Match) > 300:
			return nil, valid.Error("each log alert needs text to look for (up to 300 characters)")
		case a.Count < 1 || a.Count > 10000:
			return nil, valid.Error("the count must be between 1 and 10000")
		case a.WindowMin < 1 || a.WindowMin > 1440:
			return nil, valid.Error("the time window must be between 1 and 1440 minutes")
		}
		c, err := compile(*a)
		if err != nil {
			return nil, valid.Error(a.Name + ": not a valid regular expression")
		}
		compiled = append(compiled, c)
	}
	if err := settings.Save(d.db, logAlertsKey, list); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.alerts, d.alertsAt = compiled, time.Now()
	d.mu.Unlock()
	return list, nil
}

func compile(a LogAlert) (compiledAlert, error) {
	pattern := regexp.QuoteMeta(a.Match)
	if a.Regex {
		pattern = a.Match
	}
	re, err := regexp.Compile("(?i)" + pattern)
	return compiledAlert{a, re}, err
}

func (d *Detector) alertsLocked() []compiledAlert {
	if d.alertsAt.IsZero() || time.Since(d.alertsAt) > time.Minute {
		var out []compiledAlert
		for _, a := range d.LogAlerts() {
			if c, err := compile(a); err == nil {
				out = append(out, c)
			}
		}
		d.alerts, d.alertsAt = out, time.Now()
	}
	return d.alerts
}

// logAlerts counts the lines each enabled rule matches, and opens its incident once the
// count is reached within its window.
func (d *Detector) logAlerts(lines []logs.Line, now time.Time) {
	d.mu.Lock()
	alerts := d.alertsLocked()
	d.mu.Unlock()
	for _, a := range alerts {
		if !a.Enabled {
			continue
		}
		var hit *logs.Line
		n := 0
		for i, l := range lines {
			if (a.ServerID == "" || l.ServerID == a.ServerID) && (a.AppID == "" || l.AppID == a.AppID) && a.re.MatchString(l.Message) {
				hit, n = &lines[i], n+1
			}
		}
		if hit == nil {
			continue
		}
		k := a.key()
		sig := Signal{Type: "log_alert", Key: k, Severity: a.Severity, ServerID: hit.ServerID, Sample: hit.Message,
			Title: fmt.Sprintf("%s: “%s” %d+ times in %d min", a.Name, a.Match, a.Count, a.WindowMin)}
		if a.AppID != "" {
			sig.AppID = a.AppID
		}
		if d.Store.IsOpen(k) {
			d.Store.Record(sig, n, nil)
			continue
		}
		d.mu.Lock()
		ts := d.errs[k]
		for range n {
			ts = append(ts, now)
		}
		for len(ts) > 0 && now.Sub(ts[0]) > time.Duration(a.WindowMin)*time.Minute {
			ts = ts[1:]
		}
		d.errs[k] = ts
		total := len(ts)
		if total >= a.Count {
			delete(d.errs, k)
		}
		d.mu.Unlock()
		if total >= a.Count {
			d.Store.Record(sig, total, []string{hit.Message})
		}
	}
}

// sweepLogAlerts resolves a rule's incident once its text has been quiet for two
// windows, and the incidents of rules deleted or turned off.
func (d *Detector) sweepLogAlerts(now time.Time) {
	d.mu.Lock()
	alerts := d.alertsLocked()
	d.mu.Unlock()
	active := map[string]time.Duration{}
	for _, a := range alerts {
		if a.Enabled {
			active[a.key()] = 2 * time.Duration(a.WindowMin) * time.Minute
		}
	}
	rows, err := d.db.Query(`SELECT key, last_at FROM incidents WHERE type = 'log_alert' AND status <> 'resolved'`)
	if err != nil {
		return
	}
	type open struct {
		key  string
		last int64
	}
	var list []open
	for rows.Next() {
		var o open
		if rows.Scan(&o.key, &o.last) == nil {
			list = append(list, o)
		}
	}
	rows.Close()
	for _, o := range list {
		quiet, ok := active[o.key]
		switch {
		case !ok:
			d.Store.Clear(o.key, "The log alert was turned off or deleted")
		case now.Sub(time.UnixMilli(o.last)) > quiet:
			d.Store.Clear(o.key, fmt.Sprintf("Not seen for %d minutes", int(quiet.Minutes())))
		}
	}
}
