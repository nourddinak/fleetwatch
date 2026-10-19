package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/whatsapp"
)

// After a page: the people paged get a WhatsApp message when the incident is resolved,
// and whoever acknowledged it gets a follow-up call when it is still open a while later.
// It goes by the pages on-call recorded, so it sees every way an incident ends.

func (d *callDeps) followUps(ctx context.Context) {
	if !d.wa.Linked() {
		return
	}
	o := d.wa.Options()
	rows, err := d.db.Query(`SELECT DISTINCT p.incident_id, p.person, COALESCE(f.resolved_sent, 0), COALESCE(f.followed_up, 0)
		FROM oncall_pages p LEFT JOIN whatsapp_followups f ON f.incident_id = p.incident_id AND f.person = p.person
		WHERE p.ok = 1 AND p.ts > ?`, time.Now().Add(-48*time.Hour).UnixMilli())
	if err != nil {
		return
	}
	type page struct {
		incident, person   string
		resolved, followed bool
	}
	var pages []page
	for rows.Next() {
		var p page
		if rows.Scan(&p.incident, &p.person, &p.resolved, &p.followed) == nil {
			pages = append(pages, p)
		}
	}
	rows.Close()
	mark := func(p page, col string) {
		d.db.Exec(`INSERT INTO whatsapp_followups (incident_id, person, `+col+`) VALUES (?, ?, 1)
			ON CONFLICT (incident_id, person) DO UPDATE SET `+col+` = 1`, p.incident, p.person)
	}
	now := time.Now()
	for _, p := range pages {
		x, err := d.incidents.Get(p.incident)
		if err != nil {
			continue
		}
		wp, ok := d.wa.PersonOf(p.person)
		if !ok || wp.Quiet(now) {
			continue // tried again later: once their quiet hours end
		}
		switch {
		case x.Status == "resolved" && !p.resolved:
			mark(p, "resolved_sent")
			if !o.ResolvedMessages {
				continue
			}
			took := time.UnixMilli(x.ResolvedAt).Sub(time.UnixMilli(x.FirstAt)).Round(time.Minute)
			text := fmt.Sprintf("✅ Resolved: %s on %s, after %s.", x.Title, d.serverName(x.ServerID), took)
			if x.Resolution != "" {
				text += "\n" + x.Resolution
			}
			if err := d.wa.Send(ctx, wp.Phone, text); err != nil {
				slog.Warn("whatsapp: resolved message", "err", err)
			}
		case x.Status == "acknowledged" && !p.followed && o.FollowUpMinutes > 0:
			at, by := acknowledged(x)
			if at.IsZero() || now.Sub(at) < time.Duration(o.FollowUpMinutes)*time.Minute || d.personByName(by) != p.person {
				continue
			}
			mark(p, "followed_up")
			talk := d.followUp(p.person, x.ID, now.Sub(at))
			go func() {
				c, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
				defer cancel()
				if _, err := d.wa.Ring(c, wp.Phone, 45*time.Second, talk); err != nil {
					slog.Warn("whatsapp: follow-up call", "err", err)
				}
			}()
		}
	}
}

// acknowledged finds when an incident was acknowledged, and by whom.
func acknowledged(x incidents.Incident) (time.Time, string) {
	for _, e := range x.Timeline {
		if rest, ok := strings.CutPrefix(e.Text, "Acknowledged by "); ok {
			who, _, _ := strings.Cut(rest, ":")
			who = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(who), " (phone)"), " (on call)")
			return time.UnixMilli(e.TS), who
		}
	}
	return time.Time{}, ""
}

func (d *callDeps) personByName(name string) string {
	for _, p := range d.people() {
		if strings.EqualFold(p.Name, name) || strings.EqualFold(p.Username, name) {
			return p.ID
		}
	}
	return ""
}

// followUp is the call back to whoever acknowledged an incident that is still open.
func (d *callDeps) followUp(person, incidentID string, since time.Duration) whatsapp.Talker {
	base := d.session(person, incidentID, false)
	if base == nil {
		return nil
	}
	return d.sessionWith(person, incidentID, fmt.Sprintf(
		"The call just connected. This is a follow-up: they acknowledged this incident %s ago and it is still open. Say so briefly, say what the latest logs show now, and ask whether they want you to restart something, to call a teammate (call_teammate), or more time.",
		since.Round(time.Minute)))
}
