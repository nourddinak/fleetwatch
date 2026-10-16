// Package audit records important administrative actions.
package audit

import (
	"database/sql"
	"log/slog"
	"time"
)

// Event is one audited action. IP and UserAgent come from the request that caused it.
type Event struct {
	Action    string
	OK        bool
	IP        string
	UserAgent string
	Target    string
	Detail    string
	Actor     string // who: a member's username or the owner's name
}

// Record stores e. Audit failures are logged, never returned: they must not block the action.
func Record(d *sql.DB, e Event) {
	ok := 0
	if e.OK {
		ok = 1
	}
	if len(e.UserAgent) > 300 {
		e.UserAgent = e.UserAgent[:300]
	}
	_, err := d.Exec(`INSERT INTO audit_events (ts, action, ok, ip, user_agent, target, detail, actor) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UnixMilli(), e.Action, ok, e.IP, e.UserAgent, e.Target, e.Detail, e.Actor)
	if err != nil {
		slog.Error("audit record failed", "action", e.Action, "err", err)
	}
}

// Row is a stored event as returned to the UI.
type Row struct {
	TS        int64  `json:"ts"`
	Action    string `json:"action"`
	OK        bool   `json:"ok"`
	IP        string `json:"ip"`
	UserAgent string `json:"userAgent"`
	Target    string `json:"target"`
	Detail    string `json:"detail"`
	Actor     string `json:"actor"`
}

// List returns the newest events, optionally only those with the given action.
func List(d *sql.DB, action string, limit int) ([]Row, error) {
	rows, err := d.Query(`SELECT ts, action, ok, ip, user_agent, target, detail, actor FROM audit_events
		WHERE (? = '' OR action = ?) ORDER BY ts DESC, id DESC LIMIT ?`, action, action, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.TS, &r.Action, &r.OK, &r.IP, &r.UserAgent, &r.Target, &r.Detail, &r.Actor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
