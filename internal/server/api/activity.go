package api

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
)

// Activity is one thing someone did, as the team feed shows it.
type Activity struct {
	TS       int64  `json:"ts"`
	Actor    string `json:"actor"`
	Action   string `json:"action"`
	Text     string `json:"text"` // "restarted api on web-1"
	ServerID string `json:"serverId,omitempty"`
	AppID    string `json:"appId,omitempty"`
}

// private actions stay out of the team feed (sign-ins, 2FA, personal devices, tokens).
var private = []string{"login", "logout", "2fa.", "password.", "session", "push.", "phone.", "token.", "account.", "member."}

func hidden(action string) bool {
	for _, p := range private {
		if strings.HasPrefix(action, p) {
			return true
		}
	}
	return false
}

func (s *server) activityOf(r audit.Row) Activity {
	a := Activity{TS: r.TS, Actor: cmp.Or(r.Actor, "Someone"), Action: r.Action}
	srv := func(id string) string {
		if s.Fleet != nil {
			if rec, err := s.Fleet.Reg.Get(id); err == nil {
				a.ServerID = id
				return rec.Name
			}
		}
		return cmp.Or(id, "a server")
	}
	app := func(id string) string {
		if s.Apps != nil {
			if v, err := s.Apps.Get(id); err == nil {
				a.AppID = id
				return v.Name + " on " + srv(v.ServerID)
			}
		}
		return cmp.Or(id, "an app")
	}
	verb, rest, _ := strings.Cut(r.Action, ".")
	switch {
	case r.Action == "command.run":
		name, _, _ := strings.Cut(r.Detail, ":")
		a.Text = "ran “" + name + "” on " + app(r.Target)
	case verb == "incident":
		if s.Incidents != nil {
			if x, err := s.Incidents.Get(r.Target); err == nil && x.ServerID != "" {
				a.ServerID = x.ServerID
			}
		}
		did := map[string]string{"acknowledge": "acknowledged", "resolve": "resolved", "mute": "muted", "reopen": "reopened"}[rest]
		a.Text = cmp.Or(did, rest) + " “" + r.Detail + "”"
	case r.Action == "app.create":
		a.Text = "added " + r.Detail + " to monitoring"
		app(r.Target)
	case r.Action == "app.delete":
		a.Text = "removed " + r.Detail + " from monitoring"
	case verb == "app":
		a.Text = "changed the " + map[string]string{"health": "health check", "update": "settings"}[rest] + " of " + app(r.Target)
	case verb == "maintenance":
		if rest == "server" {
			a.Text = "set maintenance on " + srv(r.Target) + " (" + r.Detail + ")"
		} else {
			a.Text = "set maintenance on " + app(r.Target) + " (" + r.Detail + ")"
		}
	case r.Action == "disk.clean":
		a.Text = "cleaned up " + r.Detail + " on " + srv(r.Target)
	case verb == "docker":
		a.Text = map[string]string{"start": "started", "stop": "stopped", "restart": "restarted", "update": "updated", "remove": "removed", "prune": "cleaned up"}[rest] + " docker " + r.Detail + " on " + srv(r.Target)
	case verb == "firewall":
		a.Text = "changed the firewall on " + srv(r.Target) + " (" + rest + " " + strings.TrimSpace(r.Detail) + ")"
	case r.Action == "ip.block":
		a.Text = "blocked " + r.Target + " on every server"
	case r.Action == "ip.unblock":
		a.Text = "unblocked " + r.Target
	case r.Action == "updates.install":
		a.Text = "installed " + r.Detail + " updates on " + srv(r.Target)
	case r.Action == "server.create":
		a.Text = "added the server " + r.Detail
	case r.Action == "server.delete":
		a.Text = "removed the server " + r.Detail
	case verb == "server":
		a.Text = map[string]string{"update": "changed the settings of", "restart_agent": "restarted the agent on", "update_agent": "updated the agent on",
			"revoke": "revoked the agent of", "enroll_token": "made an install command for"}[rest] + " " + srv(r.Target)
	case r.Action == "system.update":
		a.Text = "updated the dashboard to " + r.Target
	case verb == "settings":
		a.Text = "changed the " + strings.ReplaceAll(rest, "_", " ") + " settings"
	case verb == "statuspage":
		a.Text = map[string]string{"notice": "posted a status page notice", "save": "edited a status page"}[rest]
	case r.Action == "runbook.save":
		a.Text = "updated the runbook for " + incidents.Labels[r.Target]
	case r.Action == "postmortem.save":
		a.Text = "wrote the postmortem of “" + r.Detail + "”"
	}
	if a.Text == "" || strings.HasPrefix(a.Text, " ") {
		a.Text = strings.TrimSpace(strings.ReplaceAll(r.Action, ".", " ") + " " + r.Detail)
	}
	return a
}

func (s *server) activityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/activity", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		rows, err := audit.List(s.DB, "", 1000)
		if err != nil {
			fail(w, err)
			return
		}
		out := []Activity{}
		for _, row := range rows {
			if row.OK && !hidden(row.Action) && len(out) < limit {
				out = append(out, s.activityOf(row))
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))
}
