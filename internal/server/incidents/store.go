// Package incidents detects problems from the data agents already send (state
// changes, health results, metrics, error logs), groups them into incidents and
// resolves them automatically when the condition clears.
package incidents

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/valid"
)

var ErrNotFound = errors.New("incident not found")

// Labels are the incident type names shown in the UI.
var Labels = map[string]string{
	"server_offline":    "Agent Offline",
	"app_offline":       "App Offline",
	"crash_loop":        "Crash Loop",
	"pm2_errored":       "PM2 Error",
	"systemd_failed":    "systemd Failure",
	"repeated_restarts": "Repeated Restarts",
	"port_unavailable":  "Port Unreachable",
	"health_failed":     "Health Check Failed",
	"high_cpu":          "High CPU",
	"high_ram":          "High RAM",
	"disk_full":         "Disk Full",
	"repeated_errors":   "Repeated Application Error",
	"ssl_expiring":      "SSL Certificate",
	"domain_expiring":   "Domain Expiry",
	"disk_filling":      "Disk Filling Up",
	"memory_leak":       "Possible Memory Leak",
	"security":          "Security",
	"uptime_down":       "Site Down",
	"uptime_slow":       "Slow Response",
	"anomaly":           "Unusual Activity",
	"log_alert":         "Log Alert",
	"cron_missed":       "Missed Job",
	"cron_failed":       "Job Failed",
	"backup_failed":     "Backup Failed",
}

type entry struct {
	TS   int64  `json:"ts"`
	Text string `json:"text"`
}

// Incident is a stored incident as the UI sees it.
type Incident struct {
	ID          string   `json:"id"`
	TypeKey     string   `json:"typeKey"`
	Type        string   `json:"type"` // label
	Key         string   `json:"-"`
	Severity    string   `json:"severity"`
	Status      string   `json:"status"`
	ResolvedBy  string   `json:"resolvedBy"`
	Title       string   `json:"title"`
	ServerID    string   `json:"serverId"`
	AppID       *string  `json:"appId"`
	Occurrences int      `json:"occurrences"`
	FirstAt     int64    `json:"firstAt"`
	LastAt      int64    `json:"lastAt"`
	ResolvedAt  int64    `json:"resolvedAt,omitempty"`
	MutedUntil  int64    `json:"mutedUntil,omitempty"`
	Resolution  string   `json:"resolution"`
	Samples     []string `json:"logSamples"`
	Timeline    []entry  `json:"timeline"`
}

// Signal describes a detected condition.
type Signal struct {
	Type     string
	Key      string // full grouping key
	Severity string
	Title    string
	ServerID string
	AppID    string
	Sample   string
}

// Event is what notifications react to.
type Event struct {
	Kind     string // opened | resolved
	Incident Incident
}

type Store struct {
	db     *sql.DB
	Bus    *live.Bus
	Now    func() time.Time
	Notify func(Event) // optional
	mu     sync.Mutex  // serializes open/close decisions
}

func NewStore(d *sql.DB, bus *live.Bus) *Store { return &Store{db: d, Bus: bus, Now: time.Now} }

const cols = `id, type, key, severity, status, resolved_by, title, COALESCE(server_id, ''), app_id, occurrences, first_at, last_at,
	COALESCE(resolved_at, 0), COALESCE(muted_until, 0), resolution, samples, timeline`

// ActedBy is who last acknowledged or muted the incident, from its timeline ("" if nobody).
func (x Incident) ActedBy() string {
	for i := len(x.Timeline) - 1; i >= 0; i-- {
		for _, p := range []string{"Acknowledged by ", "Muted by "} {
			if rest, ok := strings.CutPrefix(x.Timeline[i].Text, p); ok {
				name, _, _ := strings.Cut(rest, " for ")
				return strings.TrimSuffix(strings.TrimSuffix(name, " (phone)"), " (on call)")
			}
		}
	}
	return ""
}

func scan(sc interface{ Scan(...any) error }) (Incident, error) {
	var x Incident
	var app sql.NullString
	var samples, timeline string
	err := sc.Scan(&x.ID, &x.TypeKey, &x.Key, &x.Severity, &x.Status, &x.ResolvedBy, &x.Title, &x.ServerID, &app, &x.Occurrences,
		&x.FirstAt, &x.LastAt, &x.ResolvedAt, &x.MutedUntil, &x.Resolution, &samples, &timeline)
	if app.Valid {
		x.AppID = &app.String
	}
	x.Type = Labels[x.TypeKey]
	json.Unmarshal([]byte(samples), &x.Samples)
	json.Unmarshal([]byte(timeline), &x.Timeline)
	if x.Samples == nil {
		x.Samples = []string{}
	}
	return x, err
}

func (s *Store) open(key string) (Incident, bool) {
	x, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM incidents WHERE key = ? AND status <> 'resolved'`, key))
	return x, err == nil
}

func (s *Store) Get(id string) (Incident, error) {
	x, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM incidents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "inc_" + hex.EncodeToString(b)
}

func addSample(list []string, sample string) []string {
	if sample == "" {
		return list
	}
	if len(sample) > 500 {
		sample = sample[:500]
	}
	for _, x := range list {
		if x == sample {
			return list
		}
	}
	list = append(list, sample)
	if len(list) > 10 {
		list = list[len(list)-10:]
	}
	return list
}

func addTimeline(list []entry, ts int64, text string) []entry {
	list = append(list, entry{ts, text})
	if len(list) > 50 {
		list = list[len(list)-50:]
	}
	return list
}

func (s *Store) save(x Incident) {
	samples, _ := json.Marshal(x.Samples)
	timeline, _ := json.Marshal(x.Timeline)
	var app any
	if x.AppID != nil {
		app = *x.AppID
	}
	var resolved, muted any
	if x.ResolvedAt > 0 {
		resolved = x.ResolvedAt
	}
	if x.MutedUntil > 0 {
		muted = x.MutedUntil
	}
	s.db.Exec(`INSERT INTO incidents (id, type, key, severity, status, resolved_by, title, server_id, app_id, occurrences, first_at, last_at, resolved_at, muted_until, resolution, samples, timeline)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET severity = excluded.severity, status = excluded.status, resolved_by = excluded.resolved_by, title = excluded.title,
			occurrences = excluded.occurrences, last_at = excluded.last_at, resolved_at = excluded.resolved_at, muted_until = excluded.muted_until,
			resolution = excluded.resolution, samples = excluded.samples, timeline = excluded.timeline`,
		x.ID, x.TypeKey, x.Key, x.Severity, x.Status, x.ResolvedBy, x.Title, nullStr(x.ServerID), app, x.Occurrences, x.FirstAt, x.LastAt,
		resolved, muted, x.Resolution, string(samples), string(timeline))
	x.Type = Labels[x.TypeKey]
	s.Bus.Publish("incident", x)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Ensure opens an incident for a condition that is true now (offline, failing check).
// While it stays open, repeated calls only refresh last_at.
func (s *Store) Ensure(sig Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().UnixMilli()
	if x, ok := s.open(sig.Key); ok {
		changed := x.Severity != sig.Severity || x.Title != sig.Title
		x.Severity, x.Title, x.LastAt = sig.Severity, sig.Title, now
		x.Samples = addSample(x.Samples, sig.Sample)
		if changed {
			x.Timeline = addTimeline(x.Timeline, now, "Updated: "+sig.Title)
		}
		s.save(x)
		return
	}
	s.create(sig, 1, now, nil)
}

// Record counts one more occurrence of an event (an error line, a restart), opening
// the incident when it is not open yet. initial is the occurrence count to start at.
func (s *Store) Record(sig Signal, initial int, samples []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().UnixMilli()
	if x, ok := s.open(sig.Key); ok {
		x.Occurrences++
		x.LastAt = now
		x.Samples = addSample(x.Samples, sig.Sample)
		s.save(x)
		return
	}
	s.create(sig, initial, now, samples)
}

// IsOpen reports whether an incident with this key is open.
func (s *Store) IsOpen(key string) bool {
	_, ok := s.open(key)
	return ok
}

func (s *Store) create(sig Signal, occurrences int, now int64, samples []string) {
	x := Incident{ID: newID(), TypeKey: sig.Type, Key: sig.Key, Severity: sig.Severity, Status: "active", Title: sig.Title,
		ServerID: sig.ServerID, Occurrences: max(1, occurrences), FirstAt: now, LastAt: now, Samples: []string{}}
	if sig.AppID != "" {
		id := sig.AppID
		x.AppID = &id
	}
	for _, sm := range samples {
		x.Samples = addSample(x.Samples, sm)
	}
	x.Samples = addSample(x.Samples, sig.Sample)
	x.Timeline = addTimeline(nil, now, "Detected: "+sig.Title)
	s.save(x)
	if s.Notify != nil {
		x.Type = Labels[x.TypeKey]
		s.Notify(Event{Kind: "opened", Incident: x})
	}
}

// Clear resolves the open incident for key, if any.
func (s *Store) Clear(key, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.open(key)
	if !ok {
		return
	}
	now := s.Now().UnixMilli()
	x.Status, x.ResolvedBy, x.ResolvedAt, x.Resolution = "resolved", "auto", now, reason
	x.Timeline = addTimeline(x.Timeline, now, "Resolved automatically: "+reason)
	s.save(x)
	if s.Notify != nil {
		x.Type = Labels[x.TypeKey]
		s.Notify(Event{Kind: "resolved", Incident: x})
	}
}

// ClearStale resolves open event-type incidents with no occurrence since before cutoff.
func (s *Store) ClearStale(typ string, cutoff time.Time, reason string) {
	rows, err := s.db.Query(`SELECT key FROM incidents WHERE type = ? AND status <> 'resolved' AND last_at < ?`, typ, cutoff.UnixMilli())
	if err != nil {
		return
	}
	var keys []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		s.Clear(k, reason)
	}
}

// Act applies an admin action: acknowledge, mute (24 h), resolve or reopen.
// Act changes an incident's status; by is who did it ("admin", a member, an on-call person).
func (s *Store) Act(id, action, note, by string) (Incident, error) {
	if by == "" {
		by = "admin"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := s.Get(id)
	if err != nil {
		return x, err
	}
	now := s.Now().UnixMilli()
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		note = note[:500]
	}
	switch action {
	case "acknowledge":
		if x.Status == "resolved" {
			return x, valid.Error("incident is already resolved")
		}
		x.Status = "acknowledged"
		x.Timeline = addTimeline(x.Timeline, now, "Acknowledged by "+by)
	case "mute":
		if x.Status == "resolved" {
			return x, valid.Error("incident is already resolved")
		}
		x.Status, x.MutedUntil = "muted", now+24*3600_000
		x.Timeline = addTimeline(x.Timeline, now, "Muted by "+by+" for 24 hours (no notifications)")
	case "mute1h": // "not now" from a phone notification
		if x.Status == "resolved" {
			return x, valid.Error("incident is already resolved")
		}
		x.Status, x.MutedUntil = "muted", now+3600_000
		x.Timeline = addTimeline(x.Timeline, now, "Muted by "+by+" for 1 hour (no notifications)")
	case "resolve":
		if x.Status == "resolved" {
			return x, valid.Error("incident is already resolved")
		}
		if note == "" {
			note = "Marked resolved by " + by
		}
		x.Status, x.ResolvedBy, x.ResolvedAt, x.Resolution = "resolved", by, now, note
		x.Timeline = addTimeline(x.Timeline, now, "Resolved by "+by+": "+note)
	case "reopen":
		if x.Status != "resolved" {
			return x, valid.Error("incident is not resolved")
		}
		if _, open := s.open(x.Key); open {
			return x, valid.Error("a newer incident for the same problem is already open")
		}
		x.Status, x.ResolvedBy, x.ResolvedAt, x.Resolution = "active", "", 0, ""
		x.Timeline = addTimeline(x.Timeline, now, "Reopened by "+by)
	default:
		return x, valid.Error("unknown action")
	}
	s.save(x)
	return x, nil
}

// Muted reports whether notifications for the incident are suppressed.
func (x Incident) Muted(now time.Time) bool {
	return x.Status == "muted" && x.MutedUntil > now.UnixMilli()
}

// Filter selects incidents for listing.
type Filter struct {
	Status, ServerID, AppID, Type, Severity string // Status: open | resolved | all | active | acknowledged | muted
	Limit                                   int
}

func (s *Store) List(f Filter) ([]Incident, error) {
	var where []string
	var args []any
	switch f.Status {
	case "", "all":
	case "open":
		where = append(where, "status <> 'resolved'")
	default:
		where, args = append(where, "status = ?"), append(args, f.Status)
	}
	for _, c := range []struct{ col, v string }{{"server_id", f.ServerID}, {"app_id", f.AppID}, {"type", f.Type}, {"severity", f.Severity}} {
		if c.v != "" {
			where, args = append(where, c.col+" = ?"), append(args, c.v)
		}
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 500
	}
	q := `SELECT ` + cols + ` FROM incidents`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY (status = 'resolved'), last_at DESC LIMIT %d", f.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		x, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Prune removes resolved incidents older than keep.
func (s *Store) Prune(now time.Time, keep time.Duration) error {
	_, err := s.db.Exec(`DELETE FROM incidents WHERE status = 'resolved' AND resolved_at < ?`, now.Add(-keep).UnixMilli())
	return err
}
