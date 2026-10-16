// Package audit records important administrative actions.
package audit

import (
	"database/sql"
	"log/slog"
	"regexp"
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

var memberLogin = regexp.MustCompile(`team member ([^)\s]+)\)`)

// Backfill names who did the actions recorded before events had an actor: whoever
// last signed in from the same address and browser (a member's sign-in names them;
// otherwise it was the owner). It runs at start and only touches rows without one.
func Backfill(d *sql.DB, owner string) (int, error) {
	rows, err := d.Query(`SELECT id, action, ok, ip, user_agent, detail, actor FROM audit_events WHERE ts >= (SELECT COALESCE(MIN(ts), 1 << 62) FROM audit_events WHERE actor = '' AND ip <> '' AND action NOT IN ('login', 'logout')) - 30 * 86400000 ORDER BY ts, id`)
	if err != nil {
		return 0, err
	}
	type fix struct {
		id    int64
		actor string
	}
	var fixes []fix
	who := map[string]string{} // ip|user agent → who signed in there last
	for rows.Next() {
		var id int64
		var ok bool
		var action, ip, ua, detail, actor string
		if rows.Scan(&id, &action, &ok, &ip, &ua, &detail, &actor) != nil {
			continue
		}
		key := ip + "|" + ua
		if action == "login" && ok {
			if m := memberLogin.FindStringSubmatch(detail); m != nil {
				who[key] = m[1]
			} else {
				who[key] = owner
			}
			continue
		}
		if actor != "" {
			who[key] = actor
			continue
		}
		if ip == "" || action == "logout" {
			continue // not made from a browser (an agent, a link without signing in)
		}
		name, known := who[key]
		if !known {
			name = owner // no sign-in seen from there: the owner, the only account before teams
		}
		fixes = append(fixes, fix{id, name})
	}
	rows.Close()
	for _, f := range fixes {
		d.Exec(`UPDATE audit_events SET actor = ? WHERE id = ?`, f.actor, f.id)
	}
	return len(fixes), nil
}
