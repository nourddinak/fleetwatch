package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/valid"
	"fleetwatch/internal/server/whatsapp"
)

// Reminders and watches on WhatsApp: "remind me at 6pm to check the backup", "call me
// in 30 minutes if the API is still down", "tell me when api-server's CPU drops below
// 50%", "when the deploy finishes". Checked every minute (checkWatches).

// cond is what a watch waits for, with the ids found when it was asked for.
type cond struct {
	Kind   string  `json:"kind"` // server | app | uptime | incident | site
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Metric string  `json:"metric,omitempty"` // cpu | memory | disk
	Op     string  `json:"op,omitempty"`     // above | below
	Value  float64 `json:"value,omitempty"`
	State  string  `json:"state,omitempty"` // running | down | up | resolved | live | finished
}

var condParams = map[string]any{
	"kind":   map[string]any{"type": "STRING", "enum": []string{"server", "app", "uptime", "incident", "site"}, "description": "What to check: a server's metric, an app, an uptime check, an incident, a site's deploy"},
	"target": map[string]any{"type": "STRING", "description": "Its name (server, app, uptime check, site) or an incident's id; empty for this call's incident or its server"},
	"metric": map[string]any{"type": "STRING", "enum": []string{"cpu", "memory", "disk"}, "description": "For a server"},
	"op":     map[string]any{"type": "STRING", "enum": []string{"above", "below"}, "description": "For a server's metric"},
	"value":  map[string]any{"type": "NUMBER", "description": "For a server's metric, in percent"},
	"state":  map[string]any{"type": "STRING", "enum": []string{"running", "down", "up", "resolved", "live", "finished"}, "description": "For an app (running, down), an uptime check (up, down), an incident (resolved), a site (live, finished: its deploy ended, either way)"},
}

// condOf reads a condition from a tool's arguments, finding what it names.
func (d callDeps) condOf(args map[string]any, gate *voiceActions, sees func(string) bool, incidentID string) (cond, error) {
	str := func(k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	c := cond{Kind: str("kind"), Metric: str("metric"), Op: str("op"), State: str("state")}
	c.Value, _ = args["value"].(float64)
	target := str("target")
	switch c.Kind {
	case "server":
		id, err := gate.serverID(target)
		if err != nil || !sees(id) {
			return c, valid.Error("which server?")
		}
		if c.Metric == "" || (c.Op != "above" && c.Op != "below") {
			return c, valid.Error("which metric (cpu, memory, disk), above or below what percent?")
		}
		c.ID, c.Name = id, d.serverName(id)
	case "app":
		a, err := gate.findApp(target)
		if err != nil {
			return c, err
		}
		if !sees(a.ServerID) {
			return c, valid.Error("no app is called " + target)
		}
		if c.State != "down" {
			c.State = "running"
		}
		c.ID, c.Name = a.ID, a.Name
	case "uptime":
		views, err := d.uptime.Views()
		if err != nil {
			return c, err
		}
		for _, v := range views {
			if (strings.EqualFold(v.Name, target) || strings.Contains(strings.ToLower(v.URL), strings.ToLower(target))) && (v.ServerID == "" || sees(v.ServerID)) {
				c.ID, c.Name = v.ID, v.Name
			}
		}
		if c.ID == "" {
			return c, valid.Error("no uptime check is called " + target)
		}
		if c.State != "down" {
			c.State = "up"
		}
	case "incident":
		id := target
		if id == "" {
			id = incidentID
		}
		x, err := d.incidents.Get(id)
		if err != nil || !sees(x.ServerID) {
			return c, valid.Error("which incident? list_incidents has their ids")
		}
		c.ID, c.Name, c.State = x.ID, x.Title, "resolved"
	case "site":
		for _, s := range d.sites.List() {
			if (strings.EqualFold(s.Name, target) || containsFold(s.Domains, target)) && sees(s.ServerID) {
				c.ID, c.Name = s.ID, s.Name
			}
		}
		if c.ID == "" {
			return c, valid.Error("no site is called " + target)
		}
		if c.State != "live" {
			c.State = "finished"
		}
	default:
		return c, valid.Error("what should I check: a server, an app, an uptime check, an incident or a site?")
	}
	return c, nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// holds checks a condition now, and says how things stand.
func (d callDeps) holds(c cond) (bool, string) {
	switch c.Kind {
	case "server":
		views, err := d.hub.Views()
		if err != nil {
			return false, "cannot read the servers"
		}
		for _, v := range views {
			if v.ID != c.ID {
				continue
			}
			if v.Status == "disconnected" {
				return false, c.Name + " is offline"
			}
			val := map[string]float64{"cpu": v.CPU, "memory": v.RAM, "disk": v.Disk}[c.Metric]
			now := fmt.Sprintf("%s's %s is at %.0f%%", c.Name, c.Metric, val)
			return (c.Op == "above" && val > c.Value) || (c.Op == "below" && val < c.Value), now
		}
		return false, c.Name + " is not there anymore"
	case "app":
		a, err := d.apps.Get(c.ID)
		if err != nil {
			return false, c.Name + " is not there anymore"
		}
		running := a.Status == "online" || a.Status == "warning"
		now := c.Name + " is running"
		if !running {
			now = c.Name + " is down (" + a.Status + ")"
		}
		return running == (c.State == "running"), now
	case "uptime":
		views, _ := d.uptime.Views()
		for _, v := range views {
			if v.ID == c.ID {
				up := v.Status == "up" || v.Status == "slow"
				return up == (c.State == "up"), c.Name + " is " + v.Status
			}
		}
		return false, c.Name + " is not there anymore"
	case "incident":
		x, err := d.incidents.Get(c.ID)
		if err != nil {
			return false, "the incident is not there anymore"
		}
		return x.Status == "resolved", "the incident " + c.Name + " is " + x.Status
	case "site":
		s, err := d.sites.Get(c.ID)
		if err != nil {
			return false, c.Name + " is not there anymore"
		}
		done := s.Status == "live" || s.Status == "failed"
		if c.State == "live" {
			done = s.Status == "live"
		}
		return done, "the site " + c.Name + " is " + s.Status
	}
	return false, ""
}

// watchTools are the AI's reminder and watch tools for someone.
func (d callDeps) watchTools(person string, gate *voiceActions, sees func(string) bool, incidentID string) []ai.Tool {
	str := func(args map[string]any, k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	how := func(args map[string]any) string {
		if str(args, "how") == "call" {
			return "call"
		}
		return "text"
	}
	withCond := func(p map[string]any) map[string]any {
		for k, v := range condParams {
			p[k] = v
		}
		return p
	}
	return []ai.Tool{
		{Name: "remind_me", Description: "Remind them later, by a WhatsApp text or a call: at a time of day (at, like 18:00, their time) or in some minutes. With a condition (kind and the rest) the reminder only comes if it is true then: \"call me in 30 minutes if the API is still down\" is in_minutes 30, how call, kind app, target api, state down.",
			Params: withCond(map[string]any{
				"what":       map[string]any{"type": "STRING", "description": "What to remind them of, in their words"},
				"at":         map[string]any{"type": "STRING", "description": "A time of day, 24h like 18:00, in their time zone"},
				"in_minutes": map[string]any{"type": "INTEGER", "description": "Or in how many minutes"},
				"how":        map[string]any{"type": "STRING", "enum": []string{"text", "call"}},
			}), Required: []string{"what"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				due, err := d.dueTime(person, str(args, "at"), args["in_minutes"])
				if err != nil {
					return nil, err
				}
				w := whatsapp.Watch{Person: person, What: str(args, "what"), Due: due.UnixMilli(), How: how(args), Expires: due.Add(time.Hour).UnixMilli()}
				if str(args, "kind") != "" {
					c, err := d.condOf(args, gate, sees, incidentID)
					if err != nil {
						return nil, err
					}
					b, _ := json.Marshal(c)
					w.Cond = string(b)
				}
				if _, err := d.wa.AddWatch(w); err != nil {
					return nil, err
				}
				return fmt.Sprintf("set for %s (in %s), by %s", d.localClock(person, due), time.Until(due).Round(time.Minute), w.How), nil
			}},
		{Name: "watch_for", Description: "Watch something and tell them (text or call) as soon as it happens: a server's CPU, memory or disk above or below a percent, an app running again or down, an uptime check up or down, an incident resolved, a site's deploy finished. Checked every minute, for up to hours (default 24).",
			Params: withCond(map[string]any{
				"what":  map[string]any{"type": "STRING", "description": "What they asked for, in their words"},
				"how":   map[string]any{"type": "STRING", "enum": []string{"text", "call"}},
				"hours": map[string]any{"type": "INTEGER", "description": "How long to keep watching, at most 72"},
			}), Required: []string{"what", "kind"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				c, err := d.condOf(args, gate, sees, incidentID)
				if err != nil {
					return nil, err
				}
				if ok, now := d.holds(c); ok {
					return "it is already so: " + now, nil
				}
				hours := 24.0
				if h, ok := args["hours"].(float64); ok && h > 0 {
					hours = min(h, 72)
				}
				b, _ := json.Marshal(c)
				if _, err := d.wa.AddWatch(whatsapp.Watch{Person: person, What: str(args, "what"), Cond: string(b), How: how(args), Expires: time.Now().Add(time.Duration(hours * float64(time.Hour))).UnixMilli()}); err != nil {
					return nil, err
				}
				_, now := d.holds(c)
				return fmt.Sprintf("watching for %.0f hours; now %s", hours, now), nil
			}},
		{Name: "my_reminders", Description: "Their reminders and watches still active, with ids.",
			Run: func(context.Context, map[string]any) (any, error) {
				out := []map[string]any{}
				for _, w := range d.wa.Watches(person) {
					if w.Status == "active" {
						m := map[string]any{"id": w.ID, "what": w.What, "how": w.How}
						if w.Due > 0 {
							m["at"] = d.localClock(person, time.UnixMilli(w.Due))
						}
						out = append(out, m)
					}
				}
				return out, nil
			}},
		{Name: "cancel_reminder", Description: "Cancel one of their reminders or watches (id from my_reminders).",
			Params: map[string]any{"id": map[string]any{"type": "INTEGER"}}, Required: []string{"id"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				id, _ := args["id"].(float64)
				if err := d.wa.CancelWatch(int64(id), person); err != nil {
					return nil, err
				}
				return "canceled", nil
			}},
	}
}

// zone is someone's time zone: theirs on WhatsApp, else UTC.
func (d callDeps) zone(person string) *time.Location {
	wp, _ := d.wa.PersonOf(person)
	if loc, err := time.LoadLocation(wp.TimeZone); err == nil && wp.TimeZone != "" {
		return loc
	}
	return time.UTC
}

func (d callDeps) localClock(person string, t time.Time) string {
	return t.In(d.zone(person)).Format("15:04")
}

// dueTime reads "at 18:00" (the next one, their time) or "in 30 minutes".
func (d callDeps) dueTime(person, at string, in any) (time.Time, error) {
	now := time.Now()
	if m, ok := in.(float64); ok && m > 0 {
		return now.Add(time.Duration(m) * time.Minute), nil
	}
	if at == "" {
		return time.Time{}, valid.Error("when: at what time, or in how many minutes?")
	}
	clock, err := time.Parse("15:04", strings.ReplaceAll(at, ".", ":"))
	if err != nil {
		return time.Time{}, valid.Error("a time of day like 18:00")
	}
	loc := d.zone(person)
	local := now.In(loc)
	due := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
	if !due.After(now) {
		due = due.AddDate(0, 0, 1)
	}
	return due, nil
}

// checkWatches runs every minute: due reminders and watches whose moment came.
func (d *callDeps) checkWatches(ctx context.Context) {
	now := time.Now()
	for _, w := range d.wa.ActiveWatches() {
		var c cond
		hasCond := w.Cond != "" && json.Unmarshal([]byte(w.Cond), &c) == nil
		switch {
		case w.Due > 0 && now.UnixMilli() < w.Due:
			continue
		case w.Expires <= now.UnixMilli():
			if d.wa.FinishWatch(w.ID, "expired") && w.Due == 0 {
				d.tell(w.Person, "👀 I stopped watching for this after the time you gave: "+w.What)
			}
			continue
		}
		state := ""
		if hasCond {
			ok, s := d.holds(c)
			state = s
			if w.Due == 0 && !ok {
				continue // a watch: not yet
			}
			if w.Due > 0 && !ok { // a reminder only if: no need now
				if d.wa.FinishWatch(w.ID, "not needed: "+s) {
					d.tell(w.Person, "⏰ "+w.What+": no need now, "+s+".")
				}
				continue
			}
		}
		if !d.wa.FinishWatch(w.ID, cmp.Or(state, "sent")) {
			continue
		}
		d.notifyWatch(ctx, w, state)
	}
}

func (d callDeps) notifyWatch(ctx context.Context, w whatsapp.Watch, state string) {
	icon := "⏰ Reminder: "
	if w.Due == 0 {
		icon = "👀 "
	}
	text := icon + w.What
	if state != "" {
		text += " (" + state + ")"
	}
	wp, ok := d.wa.PersonOf(w.Person)
	if w.How != "call" || !ok || wp.Phone == "" {
		d.tell(w.Person, text)
		return
	}
	opening := "The call just connected. You are calling " + d.nameOf(w.Person) + " because they asked you to: " + w.What + "."
	if state != "" {
		opening += " Now: " + state + "."
	}
	opening += " Tell them in a sentence, then ask if they need anything."
	talker := d.sessionOpening(w.Person, "", false, opening)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		if answered, err := d.wa.Ring(c, wp.Phone, 45*time.Second, talker); err != nil || !answered {
			d.tell(w.Person, text+" — I called, no answer.")
			if err != nil {
				slog.Warn("whatsapp reminder call", "err", err)
			}
		}
	}()
}
