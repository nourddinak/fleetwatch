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
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/valid"
	"fleetwatch/internal/server/whatsapp"
)

// What the AI may look at and do during an on-call call: the incident, the server's
// numbers, its apps and recent errors, and acknowledging the incident.

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
	people    func() []oncall.Person
}

const maxCall = 5 * time.Minute

// talker answers a WhatsApp call with the AI, when it is set up and minutes are left.
func (d callDeps) talker(person, incidentID string) whatsapp.Talker {
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
	return func(ctx context.Context, m whatsapp.Media) {
		talk := ai.Talk{In: m.In, Say: m.Say, Hush: m.Hush, MaxLength: min(maxCall, left)}
		if incidentID == "" {
			talk.Instructions = "You are Fleetwatch's on-call voice assistant, on a WhatsApp test call with " + name +
				". Say this is a test call from Fleetwatch to check that calls and the AI work, and that during a real incident you would explain the problem and answer questions. Keep it short, answer what they ask, then say goodbye and end the call. " + lang
			talk.Opening = "The test call just connected."
		} else {
			talk.Instructions, talk.Tools = d.brief(incidentID, person, name, lang)
			talk.Opening = "The call just connected. Greet " + name + ", say in one or two sentences what happened, and ask if they can handle it."
		}
		lines, took, err := d.ai.Converse(ctx, talk)
		d.ai.AddUsage(took)
		if err != nil {
			slog.Warn("ai call", "person", name, "err", err)
			if len(lines) == 0 { // the AI could not start: at least they hear the alert
				m.Alert()
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
				}
			}
		}
		if incidentID != "" && len(lines) > 0 {
			var b strings.Builder
			for _, l := range lines {
				who := "AI"
				if l.Who == "person" {
					who = name
				}
				fmt.Fprintf(&b, "%s: %s\n", who, strings.TrimSpace(l.Text))
			}
			d.incidents.AddComment(incidentID, "WhatsApp call with "+name+" ("+took.Round(time.Second).String()+")", b.String())
		}
	}
}

// brief is what the AI knows and may do about an incident.
func (d callDeps) brief(incidentID, person, name, lang string) (string, []ai.Tool) {
	x, _ := d.incidents.Get(incidentID)
	server := x.ServerID
	if rec, err := d.hub.Reg.Get(x.ServerID); err == nil {
		server = rec.Name
	}
	instructions := fmt.Sprintf(`You are Fleetwatch's on-call voice assistant, calling %s on WhatsApp about an incident on their servers.
The incident: %q (%s, severity %s) on the server %s, first seen %s ago.
- This is a phone call: keep every answer short and plain, no lists, no markdown.
- Tell them briefly what happened, then help: use the tools for any fact (server load, apps, errors). Never invent numbers.
- When they say they will handle it, are on it, or will check it, call acknowledge, then confirm in a few words.
- You can start, stop, restart or reload an app (list_apps names them). First call propose_action and tell them exactly what will happen; only after they clearly say yes, call do_action, then say how it went. If a tool refuses, tell them why.
- When they are done or say goodbye, say goodbye and call end_call.
%s`, name, x.Title, x.Type, x.Severity, server, time.Since(time.UnixMilli(x.FirstAt)).Round(time.Minute), lang)

	gate := &voiceActions{d: d, person: person, name: name, incidentServer: x.ServerID}
	tools := []ai.Tool{
		{Name: "get_incident", Description: "The incident's details: what, where, since when, how often, and sample log lines.",
			Run: func(context.Context, map[string]any) (any, error) {
				x, err := d.incidents.Get(incidentID)
				if err != nil {
					return nil, err
				}
				app := ""
				if x.AppID != nil {
					if v, err := d.apps.Get(*x.AppID); err == nil {
						app = v.Name
					}
				}
				return map[string]any{"title": x.Title, "type": x.Type, "severity": x.Severity, "status": x.Status, "server": server, "app": app,
					"firstSeen": time.UnixMilli(x.FirstAt).UTC().Format(time.RFC3339), "lastSeen": time.UnixMilli(x.LastAt).UTC().Format(time.RFC3339),
					"occurrences": x.Occurrences, "logSamples": x.Samples[:min(len(x.Samples), 5)]}, nil
			}},
		{Name: "get_server_status", Description: "The server's current load: CPU, memory, disk, load average, uptime and whether its agent is connected. Without a name, the incident's server.",
			Params: map[string]any{"server": map[string]any{"type": "STRING", "description": "A server's name, if not the incident's"}},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				want, _ := args["server"].(string)
				views, err := d.hub.Views()
				if err != nil {
					return nil, err
				}
				for _, v := range views {
					if (want == "" && v.ID == x.ServerID) || (want != "" && strings.EqualFold(v.Name, want)) {
						return map[string]any{"name": v.Name, "status": v.Status, "cpuPercent": v.CPU, "memoryPercent": v.RAM, "diskPercent": v.Disk,
							"load": v.Load, "uptimeHours": v.UptimeSec / 3600}, nil
					}
				}
				return nil, fmt.Errorf("no server named %q", want)
			}},
		{Name: "list_apps", Description: "The apps on the incident's server and whether each is running.",
			Run: func(context.Context, map[string]any) (any, error) {
				views, err := d.apps.List(x.ServerID)
				if err != nil {
					return nil, err
				}
				out := []map[string]string{}
				for _, v := range views {
					out = append(out, map[string]string{"name": v.Name, "status": v.Status, "runtime": v.Runtime})
				}
				return out, nil
			}},
		{Name: "get_recent_errors", Description: "The latest error log lines (last hour) of the incident's app, or of its server.",
			Run: func(context.Context, map[string]any) (any, error) {
				q := logs.Query{ServerID: x.ServerID, Severities: []string{"error"}, From: time.Now().Add(-time.Hour).UnixMilli(), Limit: 12}
				if x.AppID != nil {
					q.AppID = *x.AppID
				}
				lines, err := d.logs.Search(q)
				if err != nil {
					return nil, err
				}
				out := []string{}
				for _, l := range lines {
					msg := l.Message
					if len(msg) > 240 {
						msg = msg[:240]
					}
					out = append(out, time.UnixMilli(l.TS).UTC().Format("15:04:05")+" "+msg)
				}
				return out, nil
			}},
		{Name: "propose_action", Description: "Get an app action ready: start, stop, restart or reload. It runs only after the person confirms and you call do_action.",
			Params: map[string]any{
				"app":    map[string]any{"type": "STRING", "description": "The app's name, as list_apps gives it"},
				"action": map[string]any{"type": "STRING", "enum": []string{"start", "stop", "restart", "reload"}},
			}, Required: []string{"app", "action"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				appName, _ := args["app"].(string)
				action, _ := args["action"].(string)
				return gate.propose(appName, action)
			}},
		{Name: "do_action", Description: "Run the action propose_action got ready, once the person has clearly said yes to it.",
			Run: func(ctx context.Context, _ map[string]any) (any, error) { return gate.do(ctx) }},
		{Name: "acknowledge", Description: "Acknowledge the incident for them: it stops the paging. Use it once they say they will handle it.",
			Run: func(context.Context, map[string]any) (any, error) {
				if _, err := d.incidents.Act(incidentID, "acknowledge", "Acknowledged on a WhatsApp call", name); err != nil {
					return nil, err
				}
				return "acknowledged", nil
			}},
	}
	return instructions, tools
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
