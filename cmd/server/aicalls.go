package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/commands"
	"fleetwatch/internal/server/disk"
	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/docker"
	"fleetwatch/internal/server/domains"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/hostadmin"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/maintenance"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/security"
	"fleetwatch/internal/server/sites"
	"fleetwatch/internal/server/statuspage"
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
	docker    *docker.Service
	sites     *sites.Service
	disk      *disk.Service
	maint     *maintenance.Service
	people    func() []oncall.Person
	oncall    *oncall.Service
	dns       *dns.Service
	domains   *domains.Checker
	status    *statuspage.Service
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
	return d.sessionOpening(person, incidentID, inbound, "")
}

// sessionWith is an incident call with its own opening (a follow-up).
func (d callDeps) sessionWith(person, incidentID, opening string) whatsapp.Talker {
	return d.sessionOpening(person, incidentID, false, opening)
}

func (d callDeps) sessionOpening(person, incidentID string, inbound bool, opening string) whatsapp.Talker {
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
	rules := `- This is a phone call: answer at once, keep every answer short and plain, no lists, no markdown, no IDs read out unless asked.
- Before you call a tool, say two or three words first (like "one moment" or "let me check") so they are not left in silence. Never leave them in silence while you wait.
- If they talk while you speak, stop and listen to them.
- Use the tools for every fact (servers, apps, logs, incidents, uptime, security). Never invent numbers or events; if a tool has nothing, say so.
- You can act: start, stop, restart or reload apps, restart containers, redeploy sites, clean disk space, mute an incident, put a server in maintenance, block an IP. First call propose_action and tell them exactly what will happen; only after they clearly say yes (and give their PIN if the tool asks for it), call do_action. App actions only start: say it is under way ("restarting now, give me a few seconds"), then call action_status and tell them how it went. If a tool refuses, tell them why.
- When they say they will handle an incident, are on it, or will check it, call acknowledge for it, then confirm in a few words.
- They can have messages passed on to teammates ("text Sara that…", "call Omar and tell him…", "when Omar calls, tell him…"): use pass_message.
- They can ask for reminders ("remind me at 6pm…", "call me in 30 minutes if the API is still down": remind_me) and to be told when something happens ("tell me when the CPU drops below 50%", "when the deploy finishes": watch_for).
- On-call cover: "can someone cover me Saturday?" is ask_cover; "I'll cover" (or yes to a teammate's request) is take_cover (cover_requests lists them).
- Public status page ("tell customers we're fixing it"): propose_status_update, read them the exact text, and post it with do_action only after their yes.
- If they are busy, stay silent or ask you to call later, say so naturally and use call_back (it rings again in a few minutes), then end the call.
- When they are done or say goodbye, say goodbye and call end_call.
` + lang + "\n" + d.nowLine(person)
	return func(ctx context.Context, m whatsapp.Media) {
		talk := ai.Talk{In: m.In, Say: m.Say, Hush: m.Hush, MaxLength: min(maxCall, left)}
		news, done := onCall(person)
		defer done()
		talk.News = news
		kit := d.toolkit(person, name, incidentID)
		notes := d.wa.Waiting(person)
		switch {
		case inbound:
			talk.Instructions = "You are Fleetwatch's voice assistant. " + name + " is calling the dashboard's WhatsApp number to ask about their servers, apps, sites, incidents, logs, uptime or security, or to have an app restarted.\n" + rules
			talk.Tools = kit
			talk.Opening = name + " is calling. Greet them by name, briefly mention anything urgent (call list_incidents first), and ask what they need."
		case incidentID == "" && opening != "":
			talk.Instructions = "You are Fleetwatch's voice assistant, calling " + name + " on WhatsApp for the reason in your first message (a teammate's message, a reminder they asked for). After that they may ask anything about their servers.\n" + rules
			talk.Tools = kit
		case incidentID == "":
			talk.Instructions = "You are Fleetwatch's on-call voice assistant, on a WhatsApp test call with " + name +
				". Say this is a test call from Fleetwatch to check that calls and the AI work; they may ask anything about their servers to try it.\n" + rules
			talk.Tools = kit
			talk.Opening = "The test call just connected."
		default:
			x, _ := d.incidents.Get(incidentID)
			talk.Instructions = fmt.Sprintf("You are Fleetwatch's on-call voice assistant, calling %s on WhatsApp about an incident: %q (%s, severity %s) on the server %s, first seen %s ago. get_incident has its details and acknowledge (without an id) acknowledges it.\n%s\n%s",
				name, x.Title, x.Type, x.Severity, d.serverName(x.ServerID), time.Since(time.UnixMilli(x.FirstAt)).Round(time.Minute), d.recentLogs(x)+d.history(x), rules)
			talk.Tools = kit
			talk.ProtectOpening = true
			talk.Opening = "The call just connected. Greet " + name + " and explain fully what happened: the incident, then what the latest app and server log lines above show (in plain words, the most telling ones). Then ask if they can handle it."
		}
		if opening != "" {
			talk.Opening = opening
		}
		talk.Instructions += d.notesBrief(notes)
		var report ai.CallReport
		talk.Report = &report
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
			d.delivered(person, notes)
			how := "outgoing"
			switch {
			case inbound:
				how = "incoming"
			case incidentID == "" && opening != "":
				how = "message"
			case opening != "":
				how = "follow-up"
			case incidentID == "":
				how = "test"
			}
			go d.afterCall(person, name, how, incidentID, lines, took, report) // the call ends now: the summary can wait
		}
	}
}

// afterCall keeps a call in the history with a summary, and puts both on its incident.
func (d callDeps) afterCall(person, name, how, incidentID string, lines []ai.Line, took time.Duration, report ai.CallReport) {
	var b strings.Builder
	for _, l := range lines {
		who := "AI"
		if l.Who == "person" {
			who = name
		}
		fmt.Fprintf(&b, "%s: %s\n", who, strings.TrimSpace(l.Text))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	summary, err := d.ai.Summarize(ctx, "phone call between Fleetwatch's on-call AI and "+name, b.String())
	if err != nil {
		slog.Warn("ai call summary", "err", err)
	}
	if err := d.ai.SaveCall(ai.CallRecord{At: report.At, Person: person, Name: name, Direction: how, IncidentID: incidentID,
		Seconds: int(took.Seconds()), Summary: summary, Transcript: lines, Report: report}); err != nil {
		slog.Warn("ai call history", "err", err)
	}
	if incidentID != "" {
		text := b.String()
		if summary != "" {
			text = summary + "\n\nTranscript:\n" + text
		}
		d.incidents.AddComment(incidentID, "WhatsApp call with "+name+" ("+took.Round(time.Second).String()+")", text)
	}
	audit.Record(d.db, audit.Event{Action: "whatsapp.call", OK: true, Actor: name, Detail: how + " AI call, " + took.Round(time.Second).String()})
}

// pastIncidents is how often this kind of incident happened on its server lately, and
// how each ended, with what was said on their calls.
func (d callDeps) pastIncidents(x incidents.Incident, limit int) []map[string]any {
	list, err := d.incidents.List(incidents.Filter{Status: "resolved", ServerID: x.ServerID, Type: x.TypeKey, Limit: 50})
	if err != nil {
		return nil
	}
	since := time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	out := []map[string]any{}
	for _, p := range list {
		if p.ID == x.ID || p.FirstAt < since || len(out) >= limit {
			continue
		}
		item := map[string]any{"when": time.UnixMilli(p.FirstAt).UTC().Format("Jan 2 15:04"), "lasted": time.UnixMilli(p.ResolvedAt).Sub(time.UnixMilli(p.FirstAt)).Round(time.Minute).String(),
			"resolvedBy": p.ResolvedBy, "resolution": p.Resolution}
		if calls, err := d.ai.Calls("", p.ID, 3); err == nil && len(calls) > 0 {
			var said []string
			for _, c := range calls {
				if c.Summary != "" {
					said = append(said, c.Summary)
				}
			}
			item["calls"] = said
		}
		out = append(out, item)
	}
	return out
}

// history tells the AI, in the brief, whether this happened before.
func (d callDeps) history(x incidents.Incident) string {
	past := d.pastIncidents(x, 5)
	if len(past) == 0 {
		return "This kind of incident did not happen on this server in the last 30 days.\n"
	}
	b, _ := json.Marshal(past)
	return fmt.Sprintf("This kind of incident happened %d time(s) on this server in the last 30 days (newest first): %s. Mention it in a few words if it helps (\"third time this week, a restart fixed it last time\").\n", len(past), b)
}

// recentLogs is the incident's latest warnings and errors, of its app and of its server,
// for the AI to explain them at once.
func (d callDeps) recentLogs(x incidents.Incident) string {
	since := time.Now().Add(-30 * time.Minute).UnixMilli()
	shown := map[int64]bool{} // a server line already listed as the app's
	format := func(title string, q logs.Query) string {
		q.Severities, q.From, q.Limit = []string{"warning", "error", "critical"}, since, 8
		lines, err := d.logs.Search(q)
		if err != nil || len(lines) == 0 {
			return title + ": none in the last 30 minutes.\n"
		}
		slices.SortFunc(lines, func(a, b logs.Line) int { return int(a.TS - b.TS) }) // oldest first
		var b strings.Builder
		b.WriteString(title + " (oldest first):\n")
		for _, l := range lines {
			if shown[l.ID] {
				continue
			}
			shown[l.ID] = true
			msg := l.Message
			if len(msg) > 220 {
				msg = msg[:220]
			}
			fmt.Fprintf(&b, "  %s [%s] %s\n", time.UnixMilli(l.TS).UTC().Format("15:04:05"), l.Severity, msg)
		}
		return b.String()
	}
	out := ""
	if x.AppID != nil {
		out += format("Latest app logs", logs.Query{AppID: *x.AppID})
	}
	return out + format("Latest server logs", logs.Query{ServerID: x.ServerID})
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
	return append([]ai.Tool{
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
		{Name: "call_teammate", Description: "Call a teammate on WhatsApp about the incident of this call, with the AI, while this call goes on. Use it when they ask for someone else to look at it.",
			Params: map[string]any{"name": map[string]any{"type": "STRING", "description": "The teammate's name"}}, Required: []string{"name"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				if incidentID == "" {
					return nil, valid.Error("there is no incident on this call to call someone about")
				}
				who, err := d.teammate(str(args, "name"))
				if err != nil {
					return nil, err
				}
				wp, err := d.reachable(who)
				if err != nil {
					return nil, err
				}
				go func() {
					c, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
					defer cancel()
					d.wa.Ring(c, wp.Phone, 45*time.Second, d.talker(who, incidentID))
				}()
				return "calling them now", nil
			}},
		{Name: "past_incidents", Description: "How often an incident like this one (same kind, same server) happened in the last 30 days, how each ended, and what was said on their calls.",
			Params: map[string]any{"id": map[string]any{"type": "STRING", "description": "An incident's id; without it, the incident of this call"}},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				x, err := incident(args)
				if err != nil {
					return nil, err
				}
				return d.pastIncidents(x, 10), nil
			}},
		{Name: "propose_action", Description: "Get an action ready; it runs only after the person confirms and you call do_action. Kinds: app (start, stop, restart, reload an app), container (start, stop, restart a Docker container; server needed), site (redeploy; rollback to its previous deploy; purge its Cloudflare cache; on or off: Cloudflare's Under attack mode), disk (clean: target is a cleanup from disk_cleanups; server needed), incident (mute an incident for an hour: target is its id), maintenance (on or off for a server: target is the server; amount = minutes, default 60), ip (block an IP address on every server: target is the IP; amount = hours, default 24), certificate (renew: get a fresh SSL certificate for a domain now, when Caddy failed to: target is the domain).",
			Params: map[string]any{
				"kind":   map[string]any{"type": "STRING", "enum": actionKinds},
				"target": map[string]any{"type": "STRING", "description": "What to act on: an app, container, site, cleanup, incident id, server or IP"},
				"action": map[string]any{"type": "STRING", "enum": []string{"start", "stop", "restart", "reload", "redeploy", "rollback", "renew", "purge", "clean", "mute", "on", "off", "block"}},
				"server": map[string]any{"type": "STRING", "description": "The server's name, where it matters (containers, disk); without it, the incident's server"},
				"amount": map[string]any{"type": "INTEGER", "description": "Minutes for maintenance, hours for an IP block"},
			}, Required: []string{"kind", "target", "action"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				amount := 0
				if f, ok := args["amount"].(float64); ok {
					amount = int(f)
				}
				return gate.propose(str(args, "kind"), str(args, "target"), str(args, "action"), str(args, "server"), amount)
			}},
		{Name: "do_action", Description: "Run the action propose_action got ready, once the person has clearly said yes (with their PIN when actions need one). App actions only start: then call action_status.",
			Params: map[string]any{"pin": map[string]any{"type": "STRING", "description": "Their PIN, when actions need one"}},
			Run:    func(ctx context.Context, args map[string]any) (any, error) { return gate.do(ctx, str(args, "pin")) }},
		{Name: "disk_cleanups", Description: "What can be cleaned on a server's disk to free space, with sizes (from its last disk scan).",
			Params: map[string]any{"server": optServer},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				id, err := gate.serverID(str(args, "server"))
				if err != nil || !sees(id) {
					return nil, valid.Error("which server?")
				}
				st := d.disk.State(id)
				if st.Report == nil {
					return "no disk scan yet for this server", nil
				}
				out := []map[string]string{}
				for _, it := range st.Report.Items {
					out = append(out, map[string]string{"cleanup": it.ID, "what": it.Label, "size": humanBytes(it.Bytes)})
				}
				return out, nil
			}},
		{Name: "action_status", Description: "How the action do_action started went: waits up to 25 seconds for it to finish.",
			Run: func(ctx context.Context, _ map[string]any) (any, error) { return gate.status(ctx) }},
	}, append(append(append(d.watchTools(person, gate, sees, incidentID), d.coverTools(person)...), d.statusTools(gate)...), d.passMessage(person))...)
}

// voiceActions runs actions asked for on a call or in a chat, in two steps: propose,
// then do once the person has answered (and given their PIN when the owner asks for
// one). It allows what their role allows: the owner everything, an operator their
// servers (blocking an IP is the owner's alone, as in the dashboard), a viewer nothing.
type voiceActions struct {
	d              callDeps
	person, name   string
	incidentServer string

	mu      sync.Mutex
	pending *voiceAction
	running *voiceRun // the app action started last
}

type voiceAction struct {
	what string // "restart api on web-1", as told to them
	at   time.Time
	run  func(ctx context.Context) (any, error)
}

type voiceRun struct{ id, what string }

const (
	confirmWithin = 90 * time.Second
)

// Kinds of actions, for the tool's description.
var actionKinds = []string{"app", "container", "site", "disk", "incident", "maintenance", "ip", "certificate"}

func (g *voiceActions) allowed(serverID string, ownerOnly bool) error {
	if g.person == oncall.Owner {
		return nil
	}
	m, err := g.d.auth.GetMember(g.person)
	if err != nil || m.Disabled {
		return valid.Error("this person is no longer on the team")
	}
	if ownerOnly {
		return valid.Error("only the dashboard owner can do that")
	}
	if m.Role != auth.RoleOperator {
		return valid.Error(g.name + " is a viewer: viewers cannot run actions")
	}
	if serverID != "" && m.Servers != nil && !slices.Contains(m.Servers, serverID) {
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

// serverID finds a server by name, or the incident's.
func (g *voiceActions) serverID(name string) (string, error) {
	views, err := g.d.hub.Views()
	if err != nil {
		return "", err
	}
	for _, v := range views {
		if (name == "" && v.ID == g.incidentServer) || (name != "" && strings.EqualFold(v.Name, name)) {
			return v.ID, nil
		}
	}
	if name == "" {
		return "", valid.Error("which server?")
	}
	return "", valid.Error("no server is called " + name)
}

// propose gets an action ready: what kind, on what (target), which action; server and
// hours/minutes where they matter.
func (g *voiceActions) propose(kind, target, action, server string, amount int) (any, error) {
	var a voiceAction
	switch kind {
	case "app", "":
		if !slices.Contains([]string{"start", "stop", "restart", "reload"}, action) {
			return nil, valid.Error("an app can be started, stopped, restarted or reloaded")
		}
		app, err := g.findApp(target)
		if err != nil {
			return nil, err
		}
		if err := g.allowed(app.ServerID, false); err != nil {
			return nil, err
		}
		cmds, err := g.d.commands.List(app.ID)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(cmds, func(c commands.View) bool { return c.ID == "builtin:"+action }) {
			return nil, valid.Error(app.Name + " is not run by PM2 or systemd, so it cannot be " + action + "ed from here")
		}
		a.what = action + " " + app.Name + " on " + g.d.serverName(app.ServerID)
		a.run = func(context.Context) (any, error) {
			run, err := g.d.commands.Start(app.ID, "builtin:"+action, g.name+" by voice or chat, on WhatsApp")
			if err != nil {
				return nil, err
			}
			if action != "start" {
				g.d.detector.Intended(app.ID, time.Now()) // stopping it on purpose is not an incident
			}
			g.mu.Lock()
			g.running = &voiceRun{id: run.ID, what: action + " " + app.Name}
			g.mu.Unlock()
			return "started " + action + " of " + app.Name + ". Tell them it is under way and to give you a few seconds, then call action_status.", nil
		}
	case "container":
		if !slices.Contains([]string{"start", "stop", "restart"}, action) {
			return nil, valid.Error("a container can be started, stopped or restarted")
		}
		id, err := g.serverID(server)
		if err != nil {
			return nil, err
		}
		st := g.d.docker.State(id)
		if st.Report == nil || !slices.ContainsFunc(st.Report.Containers, func(c proto.DockerContainer) bool { return c.Name == target }) {
			return nil, valid.Error("no container called " + target + " on " + g.d.serverName(id))
		}
		if err := g.allowed(id, false); err != nil {
			return nil, err
		}
		a.what = action + " the container " + target + " on " + g.d.serverName(id)
		a.run = func(context.Context) (any, error) {
			if _, err := g.d.docker.Act(id, proto.DockerAction{Kind: "container", Target: target, Action: action}); err != nil {
				return nil, err
			}
			return "the container is being " + action + "ed; it takes a few seconds", nil
		}
	case "certificate":
		if g.d.domains == nil {
			return nil, valid.Error("certificates are not checked here")
		}
		if err := g.allowed("", false); err != nil {
			return nil, err
		}
		host := strings.ToLower(target)
		a.what = "get a fresh SSL certificate for " + host + " now (Caddy on its server restarts for a second)"
		a.run = func(ctx context.Context) (any, error) {
			if _, err := g.d.domains.Renew(host, g.name+" (WhatsApp)"); err != nil {
				return nil, err
			}
			return "renewing; it takes up to two minutes, and the old certificate stays if it fails", nil
		}
	case "site":
		if action != "redeploy" && action != "rollback" && action != "purge" && action != "on" && action != "off" {
			return nil, valid.Error("a site can be redeployed, rolled back to its previous deploy, its cache purged, or Under attack mode turned on or off")
		}
		var site *sites.Site
		for _, st := range g.d.sites.List() {
			if strings.EqualFold(st.Name, target) || containsFold(st.Domains, target) {
				site = &st
			}
		}
		if site == nil {
			return nil, valid.Error("no site is called " + target)
		}
		if err := g.allowed(site.ServerID, false); err != nil {
			return nil, err
		}
		if action == "rollback" {
			id := site.ID
			a.what = "roll the site " + site.Name + " back to its previous deploy"
			a.run = func(ctx context.Context) (any, error) {
				d, err := g.d.sites.Rollback(ctx, id, 0, g.name+" (WhatsApp)")
				if err != nil {
					return nil, err
				}
				return "rolling back to " + d.Commit[:min(7, len(d.Commit))] + " (" + d.Message + "); it takes a minute", nil
			}
			break
		}
		if action != "redeploy" {
			if g.d.dns == nil {
				return nil, valid.Error("Cloudflare is not connected")
			}
			owner, domains := sites.DNSOwner(*site), site.Domains
			switch action {
			case "purge":
				a.what = "purge Cloudflare's cache for " + site.Name
				a.run = func(ctx context.Context) (any, error) {
					done, err := g.d.dns.PurgeSite(ctx, owner, domains)
					if err != nil {
						return nil, err
					}
					return "purged: " + strings.Join(done, ", "), nil
				}
			default:
				on := action == "on"
				a.what = "turn Cloudflare's Under attack mode " + action + " for " + site.Name
				a.run = func(ctx context.Context) (any, error) {
					if _, err := g.d.dns.SiteUnderAttack(ctx, owner, domains, &on); err != nil {
						return nil, err
					}
					return "Under attack mode is " + action, nil
				}
			}
			break
		}
		a.what = "redeploy the site " + site.Name
		id := site.ID
		a.run = func(ctx context.Context) (any, error) {
			if err := g.d.sites.Redeploy(ctx, id, g.name+" (WhatsApp)"); err != nil {
				return nil, err
			}
			return "the site is being redeployed; it takes a minute or two", nil
		}
	case "disk":
		if action != "clean" {
			return nil, valid.Error("disk space is cleaned with action clean")
		}
		id, err := g.serverID(server)
		if err != nil {
			return nil, err
		}
		if err := g.allowed(id, false); err != nil {
			return nil, err
		}
		st := g.d.disk.State(id)
		var item *proto.DiskItem
		if st.Report != nil {
			for _, it := range st.Report.Items {
				if strings.EqualFold(it.ID, target) || strings.EqualFold(it.Label, target) {
					item = &it
				}
			}
		}
		if item == nil {
			return nil, valid.Error("no such cleanup on " + g.d.serverName(id) + ": disk_cleanups lists them")
		}
		a.what = fmt.Sprintf("clean %s on %s (about %s)", item.Label, g.d.serverName(id), humanBytes(item.Bytes))
		itemID := item.ID
		a.run = func(context.Context) (any, error) {
			if err := g.d.disk.Clean(id, itemID); err != nil {
				return nil, err
			}
			return "cleaning started; it takes a little while", nil
		}
	case "incident":
		if action != "mute" {
			return nil, valid.Error("an incident can be muted for an hour")
		}
		x, err := g.d.incidents.Get(target)
		if err != nil {
			return nil, valid.Error("no such incident: list_incidents names them")
		}
		if err := g.allowed(x.ServerID, false); err != nil {
			return nil, err
		}
		a.what = "mute the incident \"" + x.Title + "\" for an hour (no paging)"
		a.run = func(context.Context) (any, error) {
			if _, err := g.d.incidents.Act(x.ID, "mute1h", "", g.name); err != nil {
				return nil, err
			}
			return "muted for an hour", nil
		}
	case "maintenance":
		if action != "on" && action != "off" {
			return nil, valid.Error("maintenance is turned on or off")
		}
		id, err := g.serverID(cmp.Or(target, server))
		if err != nil {
			return nil, err
		}
		if err := g.allowed(id, false); err != nil {
			return nil, err
		}
		minutes := cmp.Or(amount, 60)
		if action == "off" {
			minutes = 0
			a.what = "end maintenance on " + g.d.serverName(id)
		} else {
			a.what = fmt.Sprintf("put %s in maintenance for %d minutes (no alerts)", g.d.serverName(id), minutes)
		}
		a.run = func(context.Context) (any, error) {
			if _, err := g.d.maint.Set("server", id, minutes); err != nil {
				return nil, err
			}
			return "done", nil
		}
	case "ip":
		if action != "block" {
			return nil, valid.Error("an IP address can be blocked")
		}
		if err := g.allowed("", true); err != nil {
			return nil, err
		}
		hours := cmp.Or(amount, 24)
		a.what = fmt.Sprintf("block %s on every server for %d hours", target, hours)
		a.run = func(context.Context) (any, error) {
			if _, err := g.d.admin.Block(target, "blocked by "+g.name+" on WhatsApp", g.name, "", hours); err != nil {
				return nil, err
			}
			return "blocked", nil
		}
	default:
		return nil, valid.Error("unknown kind of action")
	}
	a.at = time.Now()
	g.mu.Lock()
	g.pending = &a
	g.mu.Unlock()
	ask := "Ask them to confirm: " + a.what + "? Only after they clearly say yes, call do_action."
	if g.d.wa != nil && g.d.wa.Options().RequirePIN {
		ask += " Actions need their PIN: ask for it and pass it to do_action."
	}
	return ask, nil
}

func (g *voiceActions) do(ctx context.Context, pin string) (any, error) {
	g.mu.Lock()
	p := g.pending
	g.mu.Unlock()
	if p == nil || time.Since(p.at) > confirmWithin {
		return nil, valid.Error("there is nothing to confirm (or it was too long ago): call propose_action again")
	}
	if strings.TrimSpace(ai.HeardSince(ctx, p.at)) == "" {
		return nil, valid.Error("they have not answered yet: ask them to confirm and wait for their yes")
	}
	if g.d.wa != nil && g.d.wa.Options().RequirePIN {
		if whatsapp.InGroup(ctx) {
			return nil, valid.Error("actions need a PIN, and a PIN is never given in the team group: message me directly for this")
		}
		if !g.d.wa.HasPIN(g.person) {
			return nil, valid.Error("actions need a PIN and " + g.name + " has not set one (Integrations → WhatsApp)")
		}
		if err := g.d.wa.TryPIN(g.person, pin); err != nil {
			return nil, err
		}
	}
	g.mu.Lock()
	g.pending = nil
	g.mu.Unlock()
	out, err := p.run(ctx)
	audit.Record(g.d.db, audit.Event{Action: "whatsapp.action", OK: err == nil, Actor: g.name, Detail: p.what + " (on WhatsApp)"})
	return out, err
}

// status waits for the app action started last to finish, and says how it went.
func (g *voiceActions) status(ctx context.Context) (any, error) {
	g.mu.Lock()
	r := g.running
	g.mu.Unlock()
	if r == nil {
		return "nothing to wait for: the last action's answer is what do_action said", nil
	}
	for range 25 {
		run, err := g.d.commands.GetRun(r.id)
		if err == nil && run.Status != "running" {
			out := strings.TrimSpace(run.Stderr + "\n" + run.Stdout)
			if len(out) > 300 {
				out = out[len(out)-300:]
			}
			return map[string]any{"action": r.what, "status": run.Status, "exitCode": run.ExitCode, "error": run.Error, "output": out}, nil
		}
		select {
		case <-ctx.Done():
			return r.what + " is still running", nil
		case <-time.After(time.Second):
		}
	}
	return r.what + " is still running: say so, and offer to check again", nil
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%d MB", b>>20)
	}
	return fmt.Sprintf("%d KB", b>>10)
}

// chats are the team's WhatsApp conversations with the AI, by person.
var (
	chatsMu sync.Mutex
	chats   = map[string]*ai.Chat{}
)

const chatIdle = 30 * time.Minute // a quiet conversation starts over

// message answers a team member's WhatsApp message with the AI.
func (d *callDeps) message(ctx context.Context, person, text string, audio []byte, mime string) string {
	if !d.ai.Status().Configured {
		return "" // no AI: nothing answers, as before
	}
	name := person
	for _, p := range d.people() {
		if p.ID == person {
			name = p.Name
		}
	}
	chatsMu.Lock()
	c := chats[person]
	if c == nil || c.Idle(time.Now()) > chatIdle {
		wp, _ := d.wa.PersonOf(person)
		lang := "Answer in the language they write in."
		if n, ok := whatsapp.LanguageNames[wp.Language]; ok {
			lang = "Answer in " + n + ", or in the language they write in if it is another."
		}
		c = &ai.Chat{Tools: d.toolkit(person, name, ""), Instructions: "You are Fleetwatch's assistant on WhatsApp, chatting with " + name +
			" about their servers, apps, sites, incidents, logs, uptime and security. They may also send voice notes: answer what they say.\n" +
			`- This is a chat: short, plain answers; a few short lines or a small list is fine, no markdown headings or tables.
- Use the tools for every fact. Never invent numbers or events; if a tool has nothing, say so.
- You can act (apps, containers, site redeploys, disk cleanups, muting incidents, maintenance, blocking IPs): call propose_action, then ask them to reply "yes" (and their PIN if the tool says actions need one); only after that, call do_action (then action_status for apps) and tell them how it went. If a tool refuses, tell them why.
- When they say they will handle an incident, are on it, or will check it, call acknowledge for it.
- They can have messages passed on to teammates ("text Sara that…", "call Omar and tell him…", "when Omar calls, tell him…"): use pass_message.
- They can ask for reminders ("remind me at 6pm…", "call me in 30 minutes if the API is still down": remind_me) and to be told when something happens ("tell me when the CPU drops below 50%", "when the deploy finishes": watch_for).
- On-call cover: "can someone cover me Saturday?" is ask_cover; "I'll cover" (or yes to a teammate's request) is take_cover (cover_requests lists them).
- Public status page ("tell customers we're fixing it"): propose_status_update, read them the exact text, and post it with do_action only after their yes.
` + lang + "\n" + d.nowLine(person)}
		chats[person] = c
	}
	chatsMu.Unlock()
	notes := d.wa.Waiting(person)
	if len(notes) > 0 {
		text = strings.TrimSpace(d.notesBrief(notes)) + "\n\nTheir message:\n" + text
	}
	reply, err := d.ai.Reply(ctx, c, text, audio, mime)
	if err != nil {
		slog.Warn("whatsapp chat", "person", name, "err", err)
		return "Sorry, I could not answer that: " + err.Error()
	}
	d.delivered(person, notes)
	return reply
}
