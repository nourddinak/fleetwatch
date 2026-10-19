package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/commands"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/hostadmin"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/security"
	"fleetwatch/internal/server/uptime"
	"fleetwatch/internal/server/valid"
	"fleetwatch/internal/server/whatsapp"
)

// The AI on WhatsApp calls: on-call calls about an incident, and calls the team makes
// to the dashboard's number to ask about anything. In both it sees what the person
// may see (their servers) and does what their role allows.

type callDeps struct {
	db        *sql.DB
	auth      *auth.Service
	commands  *commands.Service
	detector  *incidents.Detector
	ai        *ai.Service
	wa        *whatsapp.Service
	incidents *incidents.Store
	hub       *fleet.Hub
	apps      *apps.Service
	logs      *logs.Store
	uptime    *uptime.Service
	security  *security.Service
	admin     *hostadmin.Service
	people    func() []oncall.Person
}

const maxCall = 5 * time.Minute

// talker answers an outgoing call: about an incident, or a test ("" incident).
func (d callDeps) talker(person, incidentID string) whatsapp.Talker {
	return d.session(person, incidentID, false)
}

// inbound answers a call a team member makes to the dashboard's number.
func (d callDeps) inbound(person string) whatsapp.Talker {
	return d.session(person, "", true)
}

func (d callDeps) session(person, incidentID string, inbound bool) whatsapp.Talker {
	left := d.ai.Remaining()
	if left < 30*time.Second {
		return nil
	}
	name := person
	for _, p := range d.people() {
		if p.ID == person {
			name = p.Name
		}
	}
	wp, _ := d.wa.PersonOf(person)
	lang := "Start in English. If they speak another language, switch to it at once and keep to it."
	if n, ok := whatsapp.LanguageNames[wp.Language]; ok {
		lang = "Speak " + n + ". If they switch to another language, switch with them."
	}
	rules := `- This is a phone call: keep every answer short and plain, no lists, no markdown, no IDs read out unless asked.
- Use the tools for every fact (servers, apps, logs, incidents, uptime, security). Never invent numbers or events; if a tool has nothing, say so.
- You can start, stop, restart or reload an app. First call propose_action and tell them exactly what will happen; only after they clearly say yes, call do_action, then say how it went. If a tool refuses, tell them why.
- When they say they will handle an incident, are on it, or will check it, call acknowledge for it, then confirm in a few words.
- When they are done or say goodbye, say goodbye and call end_call.
` + lang
	return func(ctx context.Context, m whatsapp.Media) {
		talk := ai.Talk{In: m.In, Say: m.Say, Hush: m.Hush, MaxLength: min(maxCall, left)}
		kit := d.toolkit(person, name, incidentID)
		switch {
		case inbound:
			talk.Instructions = "You are Fleetwatch's voice assistant. " + name + " is calling the dashboard's WhatsApp number to ask about their servers, apps, sites, incidents, logs, uptime or security, or to have an app restarted.\n" + rules
			talk.Tools = kit
			talk.Opening = name + " is calling. Greet them by name, briefly mention anything urgent (call list_incidents first), and ask what they need."
		case incidentID == "":
			talk.Instructions = "You are Fleetwatch's on-call voice assistant, on a WhatsApp test call with " + name +
				". Say this is a test call from Fleetwatch to check that calls and the AI work; they may ask anything about their servers to try it.\n" + rules
			talk.Tools = kit
			talk.Opening = "The test call just connected."
		default:
			x, _ := d.incidents.Get(incidentID)
			talk.Instructions = fmt.Sprintf("You are Fleetwatch's on-call voice assistant, calling %s on WhatsApp about an incident: %q (%s, severity %s) on the server %s, first seen %s ago. get_incident has its details and acknowledge (without an id) acknowledges it.\n%s",
				name, x.Title, x.Type, x.Severity, d.serverName(x.ServerID), time.Since(time.UnixMilli(x.FirstAt)).Round(time.Minute), rules)
			talk.Tools = kit
			talk.Opening = "The call just connected. Greet " + name + ", say in one or two sentences what happened, and ask if they can handle it."
		}
		lines, took, err := d.ai.Converse(ctx, talk)
		d.ai.AddUsage(took)
		if err != nil {
			slog.Warn("ai call", "person", name, "err", err)
			if len(lines) == 0 && !inbound { // the AI could not start: at least they hear the alert
				m.Alert()
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
				}
			}
		}
		if len(lines) > 0 {
			var b strings.Builder
			for _, l := range lines {
				who := "AI"
				if l.Who == "person" {
					who = name
				}
				fmt.Fprintf(&b, "%s: %s\n", who, strings.TrimSpace(l.Text))
			}
			if incidentID != "" {
				d.incidents.AddComment(incidentID, "WhatsApp call with "+name+" ("+took.Round(time.Second).String()+")", b.String())
			}
			how := "outgoing"
			if inbound {
				how = "incoming"
			}
			audit.Record(d.db, audit.Event{Action: "whatsapp.call", OK: true, Actor: name, Detail: how + " AI call, " + took.Round(time.Second).String()})
		}
	}
}

func (d callDeps) serverName(id string) string {
	if rec, err := d.hub.Reg.Get(id); err == nil {
		return rec.Name
	}
	return id
}

// sees reports whether the person may see a server: the owner and members without a
// server list see them all.
func (d callDeps) sees(person string) func(serverID string) bool {
	if person == oncall.Owner {
		return func(string) bool { return true }
	}
	m, err := d.auth.GetMember(person)
	if err != nil || m.Disabled {
		return func(string) bool { return false }
	}
	return func(id string) bool { return m.Servers == nil || slices.Contains(m.Servers, id) }
}

// toolkit is what the AI may look at and do for a person, focused on an incident if any.
func (d callDeps) toolkit(person, name, incidentID string) []ai.Tool {
	sees := d.sees(person)
	focus := ""
	if incidentID != "" {
		if x, err := d.incidents.Get(incidentID); err == nil {
			focus = x.ServerID
		}
	}
	str := func(args map[string]any, k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	// server finds a server by name (or the incident's one when none is named).
	server := func(name string) (fleet.View, error) {
		views, err := d.hub.Views()
		if err != nil {
			return fleet.View{}, err
		}
		for _, v := range views {
			if ((name == "" && v.ID == focus) || (name != "" && strings.EqualFold(v.Name, name))) && sees(v.ID) {
				return v, nil
			}
		}
		if name == "" {
			return fleet.View{}, valid.Error("which server? list_servers names them")
		}
		return fleet.View{}, valid.Error("no server of theirs is called " + name)
	}
	optServer := map[string]any{"type": "STRING", "description": "A server's name; without it, the incident's server (or all of theirs)"}
	gate := &voiceActions{d: d, person: person, name: name, incidentServer: focus}
	incident := func(args map[string]any) (incidents.Incident, error) {
		id := str(args, "id")
		if id == "" {
			id = incidentID
		}
		if id == "" {
			return incidents.Incident{}, valid.Error("which incident? list_incidents names them")
		}
		x, err := d.incidents.Get(id)
		if err != nil || !sees(x.ServerID) {
			return incidents.Incident{}, valid.Error("no such incident")
		}
		return x, nil
	}
	return []ai.Tool{
		{Name: "list_servers", Description: "Their servers with status (online, warning, critical, disconnected), CPU, memory and disk use.",
			Run: func(context.Context, map[string]any) (any, error) {
				views, err := d.hub.Views()
				if err != nil {
					return nil, err
				}
				out := []map[string]any{}
				for _, v := range views {
					if sees(v.ID) {
						out = append(out, map[string]any{"name": v.Name, "status": v.Status, "cpuPercent": v.CPU, "memoryPercent": v.RAM, "diskPercent": v.Disk})
					}
				}
				return out, nil
			}},
		{Name: "get_server_status", Description: "One server's current load: CPU, memory, disk, network, load average, uptime, agent status.",
			Params: map[string]any{"server": optServer},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				v, err := server(str(args, "server"))
				if err != nil {
					return nil, err
				}
				return map[string]any{"name": v.Name, "status": v.Status, "cpuPercent": v.CPU, "memoryPercent": v.RAM, "diskPercent": v.Disk, "swapPercent": v.Swap,
					"netInMbps": v.NetRx, "netOutMbps": v.NetTx, "load": v.Load, "uptimeHours": v.UptimeSec / 3600, "cpuCores": v.CPUCores, "ramGB": v.RAMGB, "diskGB": v.DiskGB}, nil
			}},
		{Name: "list_apps", Description: "The apps on a server (or on all their servers) and whether each is running.",
			Params: map[string]any{"server": optServer},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				id := ""
				if n := str(args, "server"); n != "" || focus != "" {
					v, err := server(n)
					if err != nil {
						return nil, err
					}
					id = v.ID
				}
				views, err := d.apps.List(id)
				if err != nil {
					return nil, err
				}
				out := []map[string]string{}
				for _, v := range views {
					if sees(v.ServerID) {
						out = append(out, map[string]string{"name": v.Name, "server": d.serverName(v.ServerID), "status": v.Status, "runtime": v.Runtime})
					}
				}
				return out, nil
			}},
		{Name: "search_logs", Description: "Search the logs of a server or of one app: the newest matching lines.",
			Params: map[string]any{
				"server":  optServer,
				"app":     map[string]any{"type": "STRING", "description": "An app's name, to see only its logs"},
				"text":    map[string]any{"type": "STRING", "description": "Words the lines must contain"},
				"level":   map[string]any{"type": "STRING", "enum": []string{"error", "warning", "any"}, "description": "error: errors only (the default); warning: warnings and errors"},
				"minutes": map[string]any{"type": "INTEGER", "description": "How far back to look (default 60)"},
			},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				q := logs.Query{Text: str(args, "text"), Limit: 15}
				switch str(args, "level") {
				case "any":
				case "warning":
					q.Severities = []string{"warning", "error", "critical"}
				default:
					q.Severities = []string{"error", "critical"}
				}
				minutes := 60
				if f, ok := args["minutes"].(float64); ok && f > 0 {
					minutes = min(int(f), 7*24*60)
				}
				q.From = time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
				if n := str(args, "app"); n != "" {
					app, err := gate.findApp(n)
					if err != nil {
						return nil, err
					}
					if !sees(app.ServerID) {
						return nil, valid.Error("no app of theirs is called " + n)
					}
					q.AppID = app.ID
				} else if n := str(args, "server"); n != "" || focus != "" {
					v, err := server(n)
					if err != nil {
						return nil, err
					}
					q.ServerID = v.ID
				}
				lines, err := d.logs.Search(q)
				if err != nil {
					return nil, err
				}
				out := []string{}
				for _, l := range lines {
					if !sees(l.ServerID) {
						continue
					}
					msg := l.Message
					if len(msg) > 240 {
						msg = msg[:240]
					}
					out = append(out, time.UnixMilli(l.TS).UTC().Format("Jan 2 15:04:05")+" ["+l.Severity+"] "+d.serverName(l.ServerID)+": "+msg)
				}
				return out, nil
			}},
		{Name: "list_incidents", Description: "Their open incidents: what, where, how serious, since when, and whether someone acknowledged them.",
			Run: func(context.Context, map[string]any) (any, error) {
				list, err := d.incidents.List(incidents.Filter{Status: "open", Limit: 50})
				if err != nil {
					return nil, err
				}
				out := []map[string]any{}
				for _, x := range list {
					if x.Status == "resolved" || !sees(x.ServerID) || len(out) >= 15 {
						continue
					}
					out = append(out, map[string]any{"id": x.ID, "title": x.Title, "severity": x.Severity, "status": x.Status, "server": d.serverName(x.ServerID),
						"since": time.Since(time.UnixMilli(x.FirstAt)).Round(time.Minute).String()})
				}
				return out, nil
			}},
		{Name: "get_incident", Description: "An incident's details: what, where, since when, how often, and sample log lines. Without an id, the incident of this call.",
			Params: map[string]any{"id": map[string]any{"type": "STRING", "description": "The incident's id from list_incidents"}},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				x, err := incident(args)
				if err != nil {
					return nil, err
				}
				app := ""
				if x.AppID != nil {
					if v, err := d.apps.Get(*x.AppID); err == nil {
						app = v.Name
					}
				}
				return map[string]any{"title": x.Title, "type": x.Type, "severity": x.Severity, "status": x.Status, "server": d.serverName(x.ServerID), "app": app,
					"firstSeen": time.UnixMilli(x.FirstAt).UTC().Format(time.RFC3339), "lastSeen": time.UnixMilli(x.LastAt).UTC().Format(time.RFC3339),
					"occurrences": x.Occurrences, "logSamples": x.Samples[:min(len(x.Samples), 5)]}, nil
			}},
		{Name: "acknowledge", Description: "Acknowledge an incident for them (it stops the paging). Without an id, the incident of this call.",
			Params: map[string]any{"id": map[string]any{"type": "STRING", "description": "The incident's id from list_incidents"}},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				x, err := incident(args)
				if err != nil {
					return nil, err
				}
				if _, err := d.incidents.Act(x.ID, "acknowledge", "Acknowledged on a WhatsApp call", name); err != nil {
					return nil, err
				}
				return "acknowledged", nil
			}},
		{Name: "uptime_checks", Description: "Their uptime checks (websites and endpoints): up or down, uptime over 24 hours, 7 and 30 days, response times.",
			Run: func(context.Context, map[string]any) (any, error) {
				views, err := d.uptime.Views()
				if err != nil {
					return nil, err
				}
				out := []map[string]any{}
				for _, v := range views {
					if v.ServerID != "" && !sees(v.ServerID) {
						continue
					}
					out = append(out, map[string]any{"name": v.Name, "url": v.URL, "status": v.Status, "uptime24hPercent": v.Uptime24h,
						"uptime7dPercent": v.Uptime7d, "uptime30dPercent": v.Uptime30d, "p95ms": v.P95})
				}
				return out, nil
			}},
		{Name: "security_overview", Description: "Security of a server or all of theirs: the latest scan's warnings, recent SSH logins (new countries or keys flagged), and IP addresses the dashboard blocks.",
			Params: map[string]any{"server": optServer},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				only := ""
				if n := str(args, "server"); n != "" {
					v, err := server(n)
					if err != nil {
						return nil, err
					}
					only = v.ID
				}
				findings := []map[string]string{}
				for _, rep := range d.security.Reports() {
					if !sees(rep.ServerID) || (only != "" && rep.ServerID != only) {
						continue
					}
					for _, f := range rep.Findings {
						if f.Severity != "info" && len(findings) < 12 {
							findings = append(findings, map[string]string{"server": d.serverName(rep.ServerID), "severity": f.Severity, "title": f.Title})
						}
					}
				}
				logins := []map[string]any{}
				if list, err := d.security.Logins(only, false, false, 30); err == nil {
					for _, l := range list.Items {
						if sees(l.ServerID) && len(logins) < 8 {
							logins = append(logins, map[string]any{"server": d.serverName(l.ServerID), "user": l.User, "ip": l.IP, "country": l.Country,
								"when": time.UnixMilli(l.TS).UTC().Format("Jan 2 15:04"), "newCountry": l.NewCountry, "newKey": l.NewKey})
						}
					}
				}
				blocked := d.admin.Blocked()
				recent := []string{}
				for _, b := range blocked[max(0, len(blocked)-5):] {
					recent = append(recent, b.IP+" ("+b.Reason+")")
				}
				return map[string]any{"findings": findings, "recentSSHLogins": logins, "blockedIPs": len(blocked), "latestBlocked": recent}, nil
			}},
		{Name: "propose_action", Description: "Get an app action ready: start, stop, restart or reload. It runs only after the person confirms and you call do_action.",
			Params: map[string]any{
				"app":    map[string]any{"type": "STRING", "description": "The app's name, as list_apps gives it"},
				"action": map[string]any{"type": "STRING", "enum": []string{"start", "stop", "restart", "reload"}},
			}, Required: []string{"app", "action"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				return gate.propose(str(args, "app"), str(args, "action"))
			}},
		{Name: "do_action", Description: "Run the action propose_action got ready, once the person has clearly said yes to it.",
			Run: func(ctx context.Context, _ map[string]any) (any, error) { return gate.do(ctx) }},
	}
}

// voiceActions runs app actions asked for on a call, in two steps: propose, then do once
// the person has answered. It allows what their role allows: the owner everything, an
// operator their servers, a viewer nothing.
type voiceActions struct {
	d              callDeps
	person, name   string
	incidentServer string

	mu      sync.Mutex
	pending *voiceAction
}

type voiceAction struct {
	appID, appName, server, action string
	at                             time.Time
}

const confirmWithin = 90 * time.Second

func (g *voiceActions) allowed(serverID string) error {
	if g.person == oncall.Owner {
		return nil
	}
	m, err := g.d.auth.GetMember(g.person)
	if err != nil || m.Disabled {
		return valid.Error("this person is no longer on the team")
	}
	if m.Role != auth.RoleOperator {
		return valid.Error(g.name + " is a viewer: viewers cannot run actions")
	}
	if m.Servers != nil && !slices.Contains(m.Servers, serverID) {
		return valid.Error(g.name + " may not run actions on that server")
	}
	return nil
}

// findApp finds an app by name: on the incident's server first, then anywhere.
func (g *voiceActions) findApp(name string) (apps.View, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	match := func(v apps.View) bool {
		return strings.ToLower(v.Name) == name || strings.ToLower(v.Slug) == name || strings.ToLower(v.PM2Name) == name
	}
	if list, err := g.d.apps.List(g.incidentServer); err == nil {
		for _, v := range list {
			if match(v) {
				return v, nil
			}
		}
	}
	all, err := g.d.apps.List("")
	if err != nil {
		return apps.View{}, err
	}
	var found []apps.View
	for _, v := range all {
		if match(v) {
			found = append(found, v)
		}
	}
	switch len(found) {
	case 0:
		return apps.View{}, valid.Error("no app is called " + name + ": list_apps names them")
	case 1:
		return found[0], nil
	}
	return apps.View{}, valid.Error("several servers have an app called " + name + ": ask which server")
}

func (g *voiceActions) propose(appName, action string) (any, error) {
	if !slices.Contains([]string{"start", "stop", "restart", "reload"}, action) {
		return nil, valid.Error("the action must be start, stop, restart or reload")
	}
	app, err := g.findApp(appName)
	if err != nil {
		return nil, err
	}
	if err := g.allowed(app.ServerID); err != nil {
		return nil, err
	}
	cmds, err := g.d.commands.List(app.ID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(cmds, func(c commands.View) bool { return c.ID == "builtin:"+action }) {
		return nil, valid.Error(app.Name + " is not run by PM2 or systemd, so it cannot be " + action + "ed from here")
	}
	server := app.ServerID
	if rec, err := g.d.hub.Reg.Get(app.ServerID); err == nil {
		server = rec.Name
	}
	g.mu.Lock()
	g.pending = &voiceAction{appID: app.ID, appName: app.Name, server: server, action: action, at: time.Now()}
	g.mu.Unlock()
	return fmt.Sprintf("Ready to %s %s on %s. Ask them to confirm; only after they clearly say yes, call do_action.", action, app.Name, server), nil
}

func (g *voiceActions) do(ctx context.Context) (any, error) {
	g.mu.Lock()
	p := g.pending
	g.mu.Unlock()
	if p == nil || time.Since(p.at) > confirmWithin {
		return nil, valid.Error("there is nothing to confirm (or it was too long ago): call propose_action again")
	}
	if strings.TrimSpace(ai.HeardSince(ctx, p.at)) == "" {
		return nil, valid.Error("they have not answered yet: ask them to confirm and wait for their yes")
	}
	g.mu.Lock()
	g.pending = nil
	g.mu.Unlock()
	run, err := g.d.commands.Start(p.appID, "builtin:"+p.action, g.name+" by voice, on a WhatsApp call")
	if err != nil {
		return nil, err
	}
	if p.action != "start" {
		g.d.detector.Intended(p.appID, time.Now()) // stopping it on purpose is not an incident
	}
	audit.Record(g.d.db, audit.Event{Action: "command.run", OK: true, Actor: g.name, Detail: p.appName + ": " + p.action + " (by voice, on a WhatsApp call)"})
	for range 30 { // say how it went, when the agent answers in time
		select {
		case <-ctx.Done():
			return "started " + p.action + "; it is still running", nil
		case <-time.After(time.Second):
		}
		r, err := g.d.commands.GetRun(run.ID)
		if err != nil || r.Status == "running" {
			continue
		}
		out := strings.TrimSpace(r.Stderr + "\n" + r.Stdout)
		if len(out) > 300 {
			out = out[len(out)-300:]
		}
		return map[string]any{"status": r.Status, "exitCode": r.ExitCode, "error": r.Error, "output": out}, nil
	}
	return "started " + p.action + "; it is still running", nil
}
