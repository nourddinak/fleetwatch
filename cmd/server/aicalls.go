package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/whatsapp"
)

// What the AI may look at and do during an on-call call: the incident, the server's
// numbers, its apps and recent errors, and acknowledging the incident.

type callDeps struct {
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
			talk.Instructions, talk.Tools = d.brief(incidentID, name, lang)
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
func (d callDeps) brief(incidentID, name, lang string) (string, []ai.Tool) {
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
- You cannot run commands or restart anything yet; say so if asked.
- When they are done or say goodbye, say goodbye and call end_call.
%s`, name, x.Title, x.Type, x.Severity, server, time.Since(time.UnixMilli(x.FirstAt)).Round(time.Minute), lang)

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
