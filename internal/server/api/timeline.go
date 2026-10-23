package api

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
)

// Moment is one thing on an app's, site's or server's timeline.
type Moment struct {
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`           // deploy | change | incident | status | backup | event
	Tone string `json:"tone,omitempty"` // ok | warn | crit
	Text string `json:"text"`
	Who  string `json:"who,omitempty"`
	Link string `json:"link,omitempty"` // a page of the dashboard
}

const timelinePage = 200

// timeline: everything that happened to one app, site or server, newest first: deploys,
// status and health changes, incidents, what people did (with who) and backups. ?before=
// pages back.
func (s *server) timelineRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/timeline", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		q := r.URL.Query()
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		if before <= 0 {
			before = time.Now().UnixMilli() + 1
		}
		sc := s.scopeOf(sess)
		var out []Moment
		switch {
		case q.Get("app") != "":
			v, err := s.Apps.Get(q.Get("app"))
			if err != nil || !sc.app(v.ID) {
				writeError(w, http.StatusNotFound, "not_found", "Application not found.")
				return
			}
			out = append(out, s.momentsEvents(`app_id = ?`, before, v.ID)...)
			out = append(out, s.momentsIncidents(incidents.Filter{AppID: v.ID}, before)...)
			out = append(out, s.momentsAudit(before, []string{v.ID}, "", "")...)
		case q.Get("site") != "" && s.Sites != nil:
			st, err := s.Sites.Get(q.Get("site"))
			if err != nil || !sc.server(st.ServerID) {
				writeError(w, http.StatusNotFound, "not_found", "Site not found.")
				return
			}
			for _, d := range s.Sites.History(st.ID) {
				if d.TS >= before {
					continue
				}
				m := Moment{TS: d.TS, Kind: "deploy", Tone: "ok", Who: d.By, Text: "Deployed " + shortCommit(d.Commit) + ": " + d.Message}
				if !d.OK {
					m.Tone, m.Text = "crit", "Deploy of "+shortCommit(d.Commit)+" failed: "+d.Error
				}
				out = append(out, m)
			}
			out = append(out, s.momentsAudit(before, []string{st.ServerID}, "site.", st.Name)...)
		case q.Get("server") != "":
			id := q.Get("server")
			if _, err := s.Fleet.Reg.Get(id); err != nil || !sc.server(id) {
				writeError(w, http.StatusNotFound, "not_found", "Server not found.")
				return
			}
			targets := []string{id}
			if views, err := s.Apps.List(id); err == nil {
				for _, v := range views {
					targets = append(targets, v.ID)
				}
			}
			out = append(out, s.momentsEvents(`server_id = ?`, before, id)...)
			out = append(out, s.momentsIncidents(incidents.Filter{ServerID: id}, before)...)
			out = append(out, s.momentsAudit(before, targets, "", "")...)
			if s.Backups != nil && sess.Owner() {
				for _, run := range s.Backups.Runs(func(srv string) bool { return srv == id }) {
					if run.Started >= before {
						continue
					}
					m := Moment{TS: run.Started, Kind: "backup", Tone: "ok", Text: "Backup done", Link: "/backups"}
					if run.Manual {
						m.Text = "Backup done (started by hand)"
					}
					switch run.Status {
					case "running":
						m.Tone, m.Text = "", "Backup running"
					case "failed", "partial":
						m.Tone, m.Text = "crit", "Backup "+map[string]string{"failed": "failed", "partial": "partly failed"}[run.Status]
						if run.Error != "" {
							m.Text += ": " + run.Error
						}
					}
					out = append(out, m)
				}
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid_input", "Which app, site or server?")
			return
		}
		slices.SortStableFunc(out, func(a, b Moment) int { return cmp.Compare(b.TS, a.TS) })
		more := len(out) > timelinePage
		if more {
			out = out[:timelinePage]
		}
		if out == nil {
			out = []Moment{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out, "more": more})
	}))
}

// momentsEvents are the recorded events (status, health, deploys seen in git, reboots…).
func (s *server) momentsEvents(where string, before int64, arg string) []Moment {
	rows, err := s.DB.Query(`SELECT ts, kind, text FROM events WHERE `+where+` AND ts < ? ORDER BY ts DESC LIMIT ?`, arg, before, timelinePage+1)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Moment
	for rows.Next() {
		var m Moment
		var kind string
		if rows.Scan(&m.TS, &kind, &m.Text) != nil {
			continue
		}
		switch kind {
		case "deploy":
			m.Kind, m.Tone = "deploy", "ok"
		case "status", "health":
			m.Kind = "status"
			low := strings.ToLower(m.Text)
			switch {
			case strings.Contains(low, "failing") || strings.Contains(low, "offline") || strings.Contains(low, "errored") || strings.Contains(low, "crash") || strings.Contains(low, "stopped"):
				m.Tone = "crit"
			case strings.Contains(low, "recovered") || strings.HasSuffix(low, "→ online") || strings.HasSuffix(low, "→ running"):
				m.Tone = "ok"
			}
		default:
			m.Kind = "event"
		}
		out = append(out, m)
	}
	return out
}

// momentsIncidents: when each incident started and ended.
func (s *server) momentsIncidents(f incidents.Filter, before int64) []Moment {
	f.Status, f.Limit = "all", timelinePage
	list, err := s.Incidents.List(f)
	if err != nil {
		return nil
	}
	var out []Moment
	for _, x := range list {
		link := "/incidents/" + x.ID
		if x.FirstAt < before {
			tone := "warn"
			if x.Severity == "critical" {
				tone = "crit"
			}
			out = append(out, Moment{TS: x.FirstAt, Kind: "incident", Tone: tone, Text: "Incident: " + x.Title, Link: link})
		}
		if x.ResolvedAt > 0 && x.ResolvedAt < before {
			m := Moment{TS: x.ResolvedAt, Kind: "incident", Tone: "ok", Text: "Resolved: " + x.Title, Link: link}
			if x.ResolvedBy != "" && x.ResolvedBy != "auto" {
				m.Who = x.ResolvedBy
			}
			out = append(out, m)
		}
	}
	return out
}

// momentsAudit: what people did to these targets (restarts, commands, .env and config
// edits, cron changes, maintenance…), worded like the team feed. prefix and detail
// narrow it down (a site's actions are aimed at its server, with its name first).
func (s *server) momentsAudit(before int64, targets []string, prefix, detail string) []Moment {
	if len(targets) == 0 {
		return nil
	}
	args := []any{before}
	marks := make([]string, len(targets))
	for i, t := range targets {
		marks[i] = "?"
		args = append(args, t)
	}
	q := `SELECT ts, action, target, detail, actor FROM audit_events WHERE ok = 1 AND ts < ? AND target IN (` + strings.Join(marks, ",") + `)`
	if prefix != "" {
		q += ` AND action LIKE ? AND detail LIKE ?`
		args = append(args, prefix+"%", detail+"%")
	}
	q += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, timelinePage+1)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Moment
	for rows.Next() {
		var row audit.Row
		if rows.Scan(&row.TS, &row.Action, &row.Target, &row.Detail, &row.Actor) != nil || hidden(row.Action) {
			continue
		}
		row.OK = true
		a := s.activityOf(row)
		m := Moment{TS: row.TS, Kind: "change", Text: a.Text, Who: a.Actor}
		if strings.HasPrefix(row.Action, "incident.") {
			m.Kind = "incident"
		}
		out = append(out, m)
	}
	return out
}
