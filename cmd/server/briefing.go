package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/settings"
)

// The morning briefing: at the time each person chose, a WhatsApp message about the
// last 24 hours on their servers. Put together from the dashboard's data (no AI), so it
// costs nothing and always comes.

func (d *callDeps) briefings(ctx context.Context) {
	if !d.wa.Linked() {
		return
	}
	now := time.Now()
	for person, p := range d.wa.People() {
		if p.Phone == "" {
			continue
		}
		var last string
		settings.Load(d.db, "whatsapp.briefing."+person, &last)
		day, due := p.BriefingDue(now, last)
		if !due {
			continue
		}
		settings.Save(d.db, "whatsapp.briefing."+person, day) // once a day, even if the send fails
		if err := d.wa.Send(ctx, p.Phone, d.briefing(person)); err != nil {
			slog.Warn("whatsapp briefing", "err", err)
		}
	}
	for person, p := range d.wa.People() {
		if p.Phone == "" {
			continue
		}
		var last string
		settings.Load(d.db, "whatsapp.weekly."+person, &last)
		day, due := p.WeeklyDue(now, last)
		if !due {
			continue
		}
		settings.Save(d.db, "whatsapp.weekly."+person, day)
		if err := d.wa.Send(ctx, p.Phone, d.weekly(person)); err != nil {
			slog.Warn("whatsapp weekly summary", "err", err)
		}
	}
}

// weekly writes one person's summary of the last 7 days.
func (d *callDeps) weekly(person string) string {
	sees := d.sees(person)
	since := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	var lines []string

	// incidents: how many, how fast, where
	opened, resolved, open := 0, 0, 0
	var took time.Duration
	perServer := map[string]int{}
	if list, err := d.incidents.List(incidents.Filter{Status: "all", Limit: 2000}); err == nil {
		for _, x := range list {
			if !sees(x.ServerID) {
				continue
			}
			if x.Status != "resolved" {
				open++
			}
			if x.FirstAt < since {
				continue
			}
			opened++
			perServer[d.serverName(x.ServerID)]++
			if x.Status == "resolved" && x.ResolvedAt > x.FirstAt {
				resolved++
				took += time.Duration(x.ResolvedAt-x.FirstAt) * time.Millisecond
			}
		}
	}
	inc := fmt.Sprintf("Incidents: %d this week, %d resolved", opened, resolved)
	if resolved > 0 {
		inc += fmt.Sprintf(" (in %s on average)", (took / time.Duration(resolved)).Round(time.Minute))
	}
	if open > 0 {
		inc += fmt.Sprintf(", %d still open", open)
	}
	lines = append(lines, inc)
	if len(perServer) > 0 {
		type kv struct {
			k string
			v int
		}
		var top []kv
		for k, v := range perServer {
			top = append(top, kv{k, v})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
		var names []string
		for _, t := range top[:min(3, len(top))] {
			names = append(names, fmt.Sprintf("%s (%d)", t.k, t.v))
		}
		lines = append(lines, "Most incidents: "+strings.Join(names, ", "))
	}

	// uptime: the week's lowest, and the slowest now
	if views, err := d.uptime.Views(); err == nil {
		type up struct {
			name string
			pct  float64
			ms   int64
		}
		var all []up
		for _, v := range views {
			if v.ServerID != "" && !sees(v.ServerID) {
				continue
			}
			var ms, n int64
			for _, st := range v.States {
				if st.LatencyMs > 0 {
					ms, n = ms+st.LatencyMs, n+1
				}
			}
			if n > 0 {
				ms /= n
			}
			all = append(all, up{v.Name, v.Uptime7d, ms})
		}
		if len(all) > 0 {
			sort.Slice(all, func(i, j int) bool { return all[i].pct < all[j].pct })
			var low []string
			for _, u := range all[:min(3, len(all))] {
				if u.pct >= 0 && u.pct < 100 {
					low = append(low, fmt.Sprintf("%s %.2f%%", u.name, u.pct))
				}
			}
			if len(low) == 0 {
				lines = append(lines, "Uptime: 100% everywhere")
			} else {
				lines = append(lines, "Lowest uptime: "+strings.Join(low, ", "))
			}
			sort.Slice(all, func(i, j int) bool { return all[i].ms > all[j].ms })
			var slow []string
			for _, u := range all[:min(3, len(all))] {
				if u.ms > 0 {
					slow = append(slow, fmt.Sprintf("%s %d ms", u.name, u.ms))
				}
			}
			if len(slow) > 0 {
				lines = append(lines, "Slowest now: "+strings.Join(slow, ", "))
			}
		}
	}

	// servers now
	var srv []string
	if views, err := d.hub.Views(); err == nil {
		for _, v := range views {
			if sees(v.ID) && (v.Status == "disconnected" || v.Disk >= 85) {
				note := "offline"
				if v.Status != "disconnected" {
					note = fmt.Sprintf("disk %.0f%%", v.Disk)
				}
				srv = append(srv, v.Name+": "+note)
			}
		}
	}
	if len(srv) > 0 {
		lines = append(lines, "To look at: "+strings.Join(srv, "; "))
	}

	out := "📊 Your week, " + d.nameOf(person) + ":\n"
	for _, l := range lines {
		out += "• " + l + "\n"
	}
	return out + "Reply here to ask me anything."
}

// briefing writes one person's summary of the last 24 hours.
func (d *callDeps) briefing(person string) string {
	sees := d.sees(person)
	name := ""
	for _, p := range d.people() {
		if p.ID == person {
			name = p.Name
		}
	}
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	var lines []string

	// incidents
	opened, resolved := 0, 0
	var open []string
	if list, err := d.incidents.List(incidents.Filter{Status: "all", Limit: 500}); err == nil {
		for _, x := range list {
			if !sees(x.ServerID) {
				continue
			}
			if x.FirstAt >= since {
				opened++
			}
			if x.Status == "resolved" && x.ResolvedAt >= since {
				resolved++
			}
			if x.Status != "resolved" {
				open = append(open, x.Title+" ("+d.serverName(x.ServerID)+")")
			}
		}
	}
	inc := fmt.Sprintf("Incidents: %d opened, %d resolved", opened, resolved)
	if len(open) == 0 {
		inc += ", none open"
	} else {
		inc += fmt.Sprintf(", %d still open: %s", len(open), strings.Join(open[:min(len(open), 3)], "; "))
	}
	lines = append(lines, inc)

	// servers
	var srv []string
	if views, err := d.hub.Views(); err == nil {
		for _, v := range views {
			if !sees(v.ID) {
				continue
			}
			var notes []string
			if v.Status == "disconnected" {
				notes = append(notes, "offline")
			}
			if v.Disk >= 85 {
				notes = append(notes, fmt.Sprintf("disk %.0f%%", v.Disk))
			}
			if v.RAM >= 90 {
				notes = append(notes, fmt.Sprintf("memory %.0f%%", v.RAM))
			}
			if len(notes) > 0 {
				srv = append(srv, v.Name+": "+strings.Join(notes, ", "))
			}
		}
	}
	if len(srv) == 0 {
		lines = append(lines, "Servers: all fine")
	} else {
		lines = append(lines, "Servers: "+strings.Join(srv, "; "))
	}

	// uptime
	if views, err := d.uptime.Views(); err == nil {
		var down []string
		for _, v := range views {
			if (v.ServerID == "" || sees(v.ServerID)) && (v.Status == "down" || v.Status == "partial") {
				down = append(down, v.Name)
			}
		}
		if len(down) > 0 {
			lines = append(lines, "Uptime: down now: "+strings.Join(down, ", "))
		} else if len(views) > 0 {
			lines = append(lines, "Uptime: all up")
		}
	}

	// container updates
	if d.docker != nil {
		updates := 0
		if views, err := d.hub.Views(); err == nil {
			for _, v := range views {
				if st := d.docker.State(v.ID); sees(v.ID) && st.Report != nil {
					for _, c := range st.Report.Containers {
						if c.Update == "available" {
							updates++
						}
					}
				}
			}
		}
		if updates > 0 {
			lines = append(lines, fmt.Sprintf("Containers: %d image update(s) waiting", updates))
		}
	}

	// security
	if d.security != nil {
		critical := 0
		for _, rep := range d.security.Reports() {
			if sees(rep.ServerID) {
				for _, f := range rep.Findings {
					if f.Severity == "critical" {
						critical++
					}
				}
			}
		}
		newLogins := 0
		if list, err := d.security.Logins("", true, false, 100); err == nil {
			for _, l := range list.Items {
				if sees(l.ServerID) && l.TS >= since {
					newLogins++
				}
			}
		}
		switch {
		case critical > 0 || newLogins > 0:
			lines = append(lines, fmt.Sprintf("Security: %d critical finding(s), %d SSH login(s) from a new country or key", critical, newLogins))
		default:
			lines = append(lines, "Security: nothing new")
		}
	}

	out := "☀️ Good morning"
	if name != "" {
		out += " " + name
	}
	out += ". Your servers, last 24 hours:\n"
	for _, l := range lines {
		out += "• " + l + "\n"
	}
	return out + "Reply here to ask me anything."
}
