package main

import (
	"context"
	"fmt"
	"log/slog"
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
