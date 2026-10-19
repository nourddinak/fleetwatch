package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fleetwatch/internal/server/domains"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/settings"
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
