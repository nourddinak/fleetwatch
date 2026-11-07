package email

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Cloudflare's own view of mail: every domain in the account, with the routing rules it
// has (address → Gmail), the way Cloudflare's Email Routing page shows them. A domain can
// be adopted in one click: its rules become Fleetwatch addresses with the same Gmail
// copies, and the domain's mail goes through the Worker from then on.

// CFRule is one Cloudflare routing rule.
type CFRule struct {
	ID      string   `json:"id"`
	Enabled bool     `json:"enabled"`
	Address string   `json:"address"` // "" for the catch-all
	Action  string   `json:"action"`  // forward | worker | drop
	To      []string `json:"to"`      // forward: destinations; worker: script names
	Ours    bool     `json:"ours"`    // goes to the Fleetwatch Worker
}

// CFDomain is one Cloudflare zone and its mail routing.
type CFDomain struct {
	ZoneID   string   `json:"zoneId"`
	Name     string   `json:"name"`
	DomainID string   `json:"domainId,omitempty"` // turned on in Fleetwatch
	Routing  string   `json:"routing"`            // on | off | unknown
	Rules    []CFRule `json:"rules"`
	CatchAll *CFRule  `json:"catchAll,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type cfRuleJSON struct {
	ID       string `json:"id"`
	Tag      string `json:"tag"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
	Matchers []struct {
		Type  string `json:"type"`
		Field string `json:"field,omitempty"`
		Value string `json:"value,omitempty"`
	} `json:"matchers"`
	Actions []struct {
		Type  string   `json:"type"`
		Value []string `json:"value"`
	} `json:"actions"`
}

func (r cfRuleJSON) view(worker string) CFRule {
	out := CFRule{ID: cmpOr(r.ID, r.Tag), Enabled: r.Enabled, To: []string{}}
	for _, m := range r.Matchers {
		if m.Type == "literal" && m.Field == "to" {
			out.Address = strings.ToLower(m.Value)
		}
	}
	if len(r.Actions) > 0 {
		out.Action = r.Actions[0].Type
		for _, v := range r.Actions[0].Value {
			out.To = append(out.To, strings.ToLower(v))
		}
		out.Ours = out.Action == "worker" && containsStr(r.Actions[0].Value, worker)
	}
	return out
}

func (c cfClient) rules(ctx context.Context, zone string) ([]cfRuleJSON, error) {
	var out []cfRuleJSON
	for page := 1; page <= 10; page++ {
		var rs []cfRuleJSON
		env, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/email/routing/rules?per_page=50&page=%d", zone, page), nil, &rs)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
		if page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return out, nil
}

// CloudflareDomains lists the account's domains with their mail routing.
func (s *Service) CloudflareDomains(ctx context.Context) ([]CFDomain, error) {
	cl, c, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := cl.zones(ctx)
	if err != nil {
		return nil, err
	}
	doms, _ := s.Domains()
	out := make([]CFDomain, len(zones))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6) // a few at a time: Cloudflare limits request rates
	for i, z := range zones {
		out[i] = CFDomain{ZoneID: z.ID, Name: z.Name, Routing: "unknown", Rules: []CFRule{}}
		for _, d := range doms {
			if strings.EqualFold(d.Name, z.Name) {
				out[i].DomainID = d.ID
			}
		}
		wg.Add(1)
		go func(d *CFDomain) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var st struct {
				Enabled bool `json:"enabled"`
			}
			if _, err := cl.call(ctx, http.MethodGet, "/zones/"+d.ZoneID+"/email/routing", nil, &st); err != nil {
				d.Error = err.Error()
				return
			}
			d.Routing = map[bool]string{true: "on", false: "off"}[st.Enabled]
			rs, err := cl.rules(ctx, d.ZoneID)
			if err != nil {
				d.Error = err.Error()
				return
			}
			for _, r := range rs {
				if v := r.view(c.Worker); v.Address != "" {
					d.Rules = append(d.Rules, v)
				}
			}
			var ca cfRuleJSON
			if _, err := cl.call(ctx, http.MethodGet, "/zones/"+d.ZoneID+"/email/routing/rules/catch_all", nil, &ca); err == nil {
				v := ca.view(c.Worker)
				d.CatchAll = &v
			}
		}(&out[i])
	}
	wg.Wait()
	return out, nil
}

// Adopt turns a Cloudflare domain on for email, keeping what Cloudflare did: each
// "address → Gmail" rule becomes an address with those Gmail copies, a forwarding
// catch-all becomes the domain's Gmail copies (and Fleetwatch's catch-all), then the
// domain is set up (catch-all → Worker, the imported rules turned off). It answers
// what it did, step by step.
func (s *Service) Adopt(ctx context.Context, zoneID string) ([]string, error) {
	cl, c, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := cl.zones(ctx)
	if err != nil {
		return nil, err
	}
	var zone *Zone
	for i := range zones {
		if zones[i].ID == zoneID {
			zone = &zones[i]
		}
	}
	if zone == nil {
		return nil, ErrNotFound
	}
	var did []string
	var d Domain
	if existing, err := s.domainByName(zone.Name); err == nil {
		d = existing
	} else if d, err = s.AddDomain(zone.Name, zone.ID); err != nil {
		return nil, err
	} else {
		did = append(did, "turned email on for "+zone.Name)
	}
	rs, err := cl.rules(ctx, zone.ID)
	if err != nil {
		return did, err
	}
	var first string // an inbox for the catch-all
	for _, r := range rs {
		v := r.view(c.Worker)
		if v.Address == "" || !strings.HasSuffix(v.Address, "@"+strings.ToLower(zone.Name)) {
			continue
		}
		local := strings.TrimSuffix(v.Address, "@"+strings.ToLower(zone.Name))
		if a, err := s.addressByEmail(v.Address); err == nil {
			if first == "" && a.InboxID == "" {
				first = a.ID
			}
			continue
		}
		in := AddressInput{DomainID: d.ID, Local: local, Receive: v.Enabled && v.Action != "drop", Send: true, Notify: true, ForwardMode: "off"}
		if v.Action == "forward" {
			in.ForwardMode, in.ForwardTo = "custom", v.To
		}
		a, err := s.CreateAddress(in)
		if err != nil {
			did = append(did, fmt.Sprintf("skipped %s: %v", v.Address, err))
			continue
		}
		if first == "" {
			first = a.ID
		}
		if v.Action == "forward" {
			did = append(did, fmt.Sprintf("added %s, copied to %s", a.Address, strings.Join(v.To, ", ")))
		} else {
			did = append(did, "added "+a.Address)
		}
	}
	var ca cfRuleJSON
	if _, err := cl.call(ctx, http.MethodGet, "/zones/"+zone.ID+"/email/routing/rules/catch_all", nil, &ca); err == nil {
		v := ca.view(c.Worker)
		if v.Enabled && v.Action == "forward" && len(v.To) > 0 {
			// Mail to any address reached Gmail before: keep that, through an inbox here.
			if first == "" {
				a, err := s.CreateAddress(AddressInput{DomainID: d.ID, Local: "contact", Receive: true, Send: true, Notify: true})
				if err != nil {
					return did, err
				}
				first = a.ID
				did = append(did, "added "+a.Address+" to receive mail sent to any address")
			}
			up := DomainUpdate{ForwardTo: v.To, SpamThreshold: d.SpamThreshold, SenderID: d.SenderID, CatchAll: true, CatchAllTo: first}
			if d, err = s.UpdateDomain(d.ID, up); err != nil {
				return did, err
			}
			did = append(did, "mail to any other address still arrives, copied to "+strings.Join(v.To, ", "))
		}
	}
	steps, err := s.Setup(ctx, d.ID)
	return append(did, steps...), err
}

func (s *Service) domainByName(name string) (Domain, error) {
	var id string
	if s.db.QueryRow(`SELECT id FROM mail_domains WHERE name = ?`, name).Scan(&id) != nil {
		return Domain{}, ErrNotFound
	}
	return s.Domain(id)
}

// handOver turns off the domain's Cloudflare rules for addresses Fleetwatch now has:
// they would send that mail straight to Gmail, past the Worker and this inbox. They are
// turned off, not deleted, so turning them back on in Cloudflare undoes it.
func (s *Service) handOver(ctx context.Context, cl cfClient, zone, worker string) []string {
	rs, err := cl.rules(ctx, zone)
	if err != nil {
		return nil
	}
	var off []string
	for _, r := range rs {
		v := r.view(worker)
		if !v.Enabled || v.Ours || v.Address == "" {
			continue
		}
		if _, err := s.addressByEmail(v.Address); err != nil {
			continue // not an address here: Cloudflare keeps handling it
		}
		body := map[string]any{"name": r.Name, "enabled": false, "priority": r.Priority, "matchers": r.Matchers, "actions": r.Actions}
		if _, err := cl.call(ctx, http.MethodPut, "/zones/"+zone+"/email/routing/rules/"+v.ID, body, nil); err == nil {
			off = append(off, v.Address)
		}
	}
	if len(off) == 0 {
		return nil
	}
	return []string{"turned off Cloudflare's own rules for " + strings.Join(off, ", ") + " (now handled here, with the same Gmail copies)"}
}
