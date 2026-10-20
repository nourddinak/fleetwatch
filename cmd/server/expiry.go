package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/domains"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/whatsapp"
)

// SSL certificates and domain registrations about to expire: a WhatsApp message to
// the owner (and the site's owner) 14, 7 and 1 days before, once each.

var expiryMarks = []int{14, 7, 1}

// expiring is one certificate or registration close to its end.
type expiring struct {
	key, what string // key: what is remembered; what: said in the message
	until     int64
	people    []string
}

func (d *callDeps) expiryAlerts(ctx context.Context) {
	if d.domains == nil || !d.wa.Linked() {
		return
	}
	now := time.Now()
	for _, e := range d.expiring(d.domains.List(), now) {
		left := int(time.UnixMilli(e.until).Sub(now).Hours() / 24)
		mark := markFor(left)
		if mark == 0 {
			continue
		}
		var sent struct {
			Until int64 `json:"until"`
			Mark  int   `json:"mark"`
		}
		settings.Load(d.db, "whatsapp.expiry."+e.key, &sent)
		if sent.Until == e.until && sent.Mark != 0 && sent.Mark <= mark {
			continue // already told at this mark (or a closer one) for this expiry
		}
		settings.Save(d.db, "whatsapp.expiry."+e.key, map[string]any{"until": e.until, "mark": mark})
		when := "today"
		if left >= 1 {
			when = fmt.Sprintf("in %d day%s", left, map[bool]string{true: "", false: "s"}[left == 1])
		}
		text := fmt.Sprintf("⚠️ %s expires %s (%s).", e.what, when, time.UnixMilli(e.until).UTC().Format("Mon 2 Jan 2006"))
		for _, p := range e.people {
			d.text(p, text)
		}
	}
}

// expiring lists what ends within the furthest mark, with whom to tell.
func (d *callDeps) expiring(list []domains.Domain, now time.Time) []expiring {
	horizon := now.Add(time.Duration(expiryMarks[0]) * 24 * time.Hour).UnixMilli()
	ownerOf := map[string]string{} // host → member owning the site
	if d.sites != nil {
		for _, s := range d.sites.List() {
			for _, h := range s.Domains {
				if s.OwnerKey != "" {
					ownerOf[strings.ToLower(h)] = s.OwnerKey
				}
			}
		}
	}
	var out []expiring
	seen := map[string]bool{}
	for _, dm := range list {
		people := []string{oncall.Owner}
		if m := ownerOf[strings.ToLower(dm.Host)]; m != "" {
			people = append(people, m)
		}
		until := int64(0)
		for _, c := range []*domains.Cert{&dm.Public, dm.Origin} {
			if c != nil && c.NotAfter > now.UnixMilli() && !c.AutoRenew && c.NotAfter <= horizon && (until == 0 || c.NotAfter < until) {
				until = c.NotAfter
			}
		}
		if until > 0 {
			out = append(out, expiring{"ssl|" + dm.Host, "The SSL certificate of " + dm.Host, until, people})
		}
		if dm.Registered != "" && !seen[dm.Registered] && dm.Expires > now.UnixMilli() && dm.Expires <= horizon {
			seen[dm.Registered] = true
			what := "The domain " + dm.Registered
			if dm.Registrar != "" {
				what += " (at " + dm.Registrar + ")"
			}
			out = append(out, expiring{"domain|" + dm.Registered, what, dm.Expires, people})
		}
	}
	return out
}

// markFor is the closest mark a number of days left has passed (0: none yet).
func markFor(left int) int {
	mark := 0
	for _, m := range expiryMarks {
		if left < m {
			mark = m
		}
	}
	return mark
}

// securityText tells the owner about a security event on WhatsApp (blocks, new open
// ports, new findings), in a few lines.
func (d *callDeps) securityText(title, body string) {
	if d.wa == nil || !d.wa.Linked() {
		return
	}
	if len(body) > 600 {
		body = body[:600] + "…"
	}
	d.text(oncall.Owner, "🛡️ "+title+"\n"+body)
}

// groupIncident posts an incident to the team group, as its options say.
func (d *callDeps) groupIncident(ev incidents.Event) {
	if d.wa == nil || !d.wa.Linked() {
		return
	}
	g := d.wa.Group()
	x := ev.Incident
	if g.JID == "" || g.Incidents == "" || (g.Incidents == "critical" && x.Severity != "critical") || x.Severity == "info" {
		return
	}
	var text string
	switch ev.Kind {
	case "opened":
		icon := "⚠️"
		if x.Severity == "critical" {
			icon = "🚨"
		}
		text = fmt.Sprintf("%s *%s*\n%s · %s", icon, x.Title, x.Severity, d.serverName(x.ServerID))
		if len(x.Samples) > 0 {
			text += "\n" + x.Samples[0]
		}
		if g.Answer {
			text += "\nMention me to ask about it, or say who takes it."
		}
	case "resolved":
		text = fmt.Sprintf("✅ Resolved: %s (after %s)", x.Title, time.Since(time.UnixMilli(x.FirstAt)).Round(time.Minute))
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.wa.PostToGroup(ctx, text); err != nil {
		slog.Warn("whatsapp group post", "err", err)
	}
}

// Addresses blocked by themselves are counted, and the owner gets the day's total on
// WhatsApp at 23:00 their time instead of a message for each one.

var blockedMu sync.Mutex

func (d *callDeps) countBlocked(ips []string) {
	blockedMu.Lock()
	defer blockedMu.Unlock()
	var list []string
	settings.Load(d.db, "whatsapp.blocked", &list)
	list = append(list, ips...)
	if len(list) > 5000 {
		list = list[len(list)-5000:]
	}
	settings.Save(d.db, "whatsapp.blocked", list)
}

func (d *callDeps) blockedDigest() {
	if d.wa == nil || !d.wa.Linked() {
		return
	}
	owner, _ := d.wa.PersonOf(oncall.Owner)
	var last string
	settings.Load(d.db, "whatsapp.blocked.day", &last)
	day, due := whatsapp.Person{Briefing: "23:00", TimeZone: owner.TimeZone}.BriefingDue(time.Now(), last)
	if !due {
		return
	}
	blockedMu.Lock()
	var list []string
	settings.Load(d.db, "whatsapp.blocked", &list)
	settings.Save(d.db, "whatsapp.blocked", []string{})
	settings.Save(d.db, "whatsapp.blocked.day", day)
	blockedMu.Unlock()
	if len(list) == 0 {
		return
	}
	d.text(oncall.Owner, blockedSummary(list))
}

func blockedSummary(list []string) string {
	text := fmt.Sprintf("🛡️ Today I blocked %d address(es) guessing SSH logins on your servers.", len(list))
	if len(list) == 1 {
		return text + "\n" + list[0]
	}
	latest := list[max(0, len(list)-10):]
	text += "\nLatest: " + strings.Join(latest, ", ")
	if n := len(list) - len(latest); n > 0 {
		text += fmt.Sprintf(" and %d more", n)
	}
	return text + "\nThe full list is in Security → Blocked addresses."
}
