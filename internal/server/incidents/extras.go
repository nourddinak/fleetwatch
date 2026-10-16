package incidents

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// ---------- comments ----------

// Comment is one message in an incident's discussion.
type Comment struct {
	ID         int64  `json:"id"`
	IncidentID string `json:"incidentId"`
	TS         int64  `json:"ts"`
	Author     string `json:"author"`
	Text       string `json:"text"`
}

// AddComment adds a message to the incident's thread.
func (s *Store) AddComment(id, author, text string) (Comment, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 4000 {
		return Comment{}, valid.Error("a comment is 1-4000 characters")
	}
	x, err := s.Get(id)
	if err != nil {
		return Comment{}, err
	}
	c := Comment{IncidentID: x.ID, TS: s.Now().UnixMilli(), Author: author, Text: text}
	res, err := s.db.Exec(`INSERT INTO incident_comments (incident_id, ts, author, text) VALUES (?, ?, ?, ?)`, c.IncidentID, c.TS, c.Author, c.Text)
	if err != nil {
		return Comment{}, err
	}
	c.ID, _ = res.LastInsertId()
	s.Bus.Publish("incident.comment", c)
	return c, nil
}

// Comments lists an incident's thread, oldest first.
func (s *Store) Comments(id string) ([]Comment, error) {
	rows, err := s.db.Query(`SELECT id, incident_id, ts, author, text FROM incident_comments WHERE incident_id = ? ORDER BY ts, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.IncidentID, &c.TS, &c.Author, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------- runbooks ----------

// Runbooks are "how to fix" steps per incident type (the "runbooks" settings section).
func Runbooks(d *sql.DB) map[string]string {
	m := map[string]string{}
	settings.Load(d, "runbooks", &m)
	return m
}

// SaveRunbook sets (or with an empty text, removes) the steps for one incident type.
func SaveRunbook(d *sql.DB, typ, text string) error {
	if _, ok := Labels[typ]; !ok {
		return valid.Error("unknown incident type")
	}
	text = strings.TrimSpace(text)
	if len(text) > 8000 {
		return valid.Error("a runbook is 8000 characters at most")
	}
	m := Runbooks(d)
	if text == "" {
		delete(m, typ)
	} else {
		m[typ] = text
	}
	return settings.Save(d, "runbooks", m)
}

// ---------- postmortems ----------

// Notes are the parts of a postmortem people write.
type Notes struct {
	Impact      string `json:"impact"`
	RootCause   string `json:"rootCause"`
	Resolution  string `json:"resolution"`
	ActionItems string `json:"actionItems"`
	UpdatedAt   int64  `json:"updatedAt,omitempty"`
	UpdatedBy   string `json:"updatedBy,omitempty"`
}

// Moment is one line of the postmortem timeline.
type Moment struct {
	TS   int64  `json:"ts"`
	Kind string `json:"kind"` // incident | event | deploy | notification | comment | action
	Text string `json:"text"`
	Who  string `json:"who,omitempty"`
}

// Postmortem is an incident's report: what happened when, built from everything the
// dashboard recorded, plus the notes people add.
type Postmortem struct {
	Incident    Incident `json:"incident"`
	DurationMs  int64    `json:"durationMs"`            // first seen → resolved (or now)
	TimeToAckMs int64    `json:"timeToAckMs,omitempty"` // first seen → acknowledged
	AckBy       string   `json:"ackBy,omitempty"`
	Timeline    []Moment `json:"timeline"`
	Notes       Notes    `json:"notes"`
}

// Postmortem builds the report for one incident.
func (s *Store) Postmortem(id string) (Postmortem, error) {
	x, err := s.Get(id)
	if err != nil {
		return Postmortem{}, err
	}
	end := x.ResolvedAt
	if end == 0 {
		end = s.Now().UnixMilli()
	}
	p := Postmortem{Incident: x, DurationMs: end - x.FirstAt, Timeline: []Moment{}}
	for _, e := range x.Timeline {
		p.Timeline = append(p.Timeline, Moment{TS: e.TS, Kind: "incident", Text: e.Text})
		if who, ok := strings.CutPrefix(e.Text, "Acknowledged by "); ok && p.TimeToAckMs == 0 {
			p.TimeToAckMs, p.AckBy = e.TS-x.FirstAt, who
		}
	}
	from, to := x.FirstAt-int64(30*time.Minute/time.Millisecond), end+int64(10*time.Minute/time.Millisecond)
	app := ""
	if x.AppID != nil {
		app = *x.AppID
	}
	add := func(q string, kind func(string) string, args ...any) {
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var m Moment
			var k string
			if rows.Scan(&m.TS, &k, &m.Text, &m.Who) == nil {
				m.Kind = kind(k)
				p.Timeline = append(p.Timeline, m)
			}
		}
	}
	// Server and app events (deploys, status changes, reboots, the agent coming and going).
	add(`SELECT ts, kind, text, '' FROM events WHERE ts BETWEEN ? AND ? AND (server_id = ? OR (? <> '' AND app_id = ?)) ORDER BY ts LIMIT 200`,
		func(k string) string {
			if k == "deploy" {
				return "deploy"
			}
			return "event"
		}, from, to, x.ServerID, app, app)
	// Who was told, and how.
	add(`SELECT ts, provider, CASE result WHEN 'delivered' THEN 'Notified by ' || provider ELSE 'Notification by ' || provider || ' failed: ' || error END, '' FROM notification_history WHERE incident_id = ? ORDER BY ts LIMIT 100`,
		func(string) string { return "notification" }, x.ID)
	// What people did on the dashboard around it (restarts, commands, maintenance…).
	// (only actions aimed at this incident, its server or its app: never sign-ins or settings)
	add(`SELECT ts, action, action || CASE WHEN detail <> '' THEN ': ' || detail ELSE '' END, actor FROM audit_events WHERE ts BETWEEN ? AND ? AND ok = 1 AND target <> '' AND target IN (?, ?, ?) ORDER BY ts LIMIT 100`,
		func(string) string { return "action" }, from, to, x.ID, x.ServerID, app)
	if cs, err := s.Comments(x.ID); err == nil {
		for _, c := range cs {
			p.Timeline = append(p.Timeline, Moment{TS: c.TS, Kind: "comment", Text: c.Text, Who: c.Author})
		}
	}
	sortMoments(p.Timeline)
	var raw string
	if s.db.QueryRow(`SELECT data FROM postmortems WHERE incident_id = ?`, x.ID).Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &p.Notes)
	}
	return p, nil
}

func sortMoments(l []Moment) {
	for i := 1; i < len(l); i++ { // insertion sort: stable, and the lists are short
		for j := i; j > 0 && l[j].TS < l[j-1].TS; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// SaveNotes stores the written parts of a postmortem.
func (s *Store) SaveNotes(id string, n Notes, by string) (Notes, error) {
	if _, err := s.Get(id); err != nil {
		return Notes{}, err
	}
	for _, f := range []string{n.Impact, n.RootCause, n.Resolution, n.ActionItems} {
		if len(f) > 20000 {
			return Notes{}, valid.Error("each part is 20000 characters at most")
		}
	}
	n.UpdatedAt, n.UpdatedBy = s.Now().UnixMilli(), by
	raw, _ := json.Marshal(n)
	_, err := s.db.Exec(`INSERT INTO postmortems (incident_id, data, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (incident_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, id, string(raw), n.UpdatedAt)
	return n, err
}
