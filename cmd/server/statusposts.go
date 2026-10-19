package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/statuspage"
	"fleetwatch/internal/server/valid"
)

// Status page updates from WhatsApp ("tell customers we're fixing it"): the AI gets the
// exact public text ready, reads it back, and posts it once the owner says yes (with
// their PIN when actions need one), like any other action.

func (d callDeps) statusTools(gate *voiceActions) []ai.Tool {
	if d.status == nil || gate.person != "owner" {
		return nil // status pages are the owner's
	}
	str := func(args map[string]any, k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	return []ai.Tool{
		{Name: "status_pages", Description: "The public status pages, with their open notices (ids) customers see.",
			Run: func(context.Context, map[string]any) (any, error) {
				out := []map[string]any{}
				for _, c := range d.status.Pages() {
					var open []map[string]string
					for _, n := range c.Notices {
						if n.Status != "resolved" && !n.Draft {
							open = append(open, map[string]string{"id": n.ID, "title": n.Title, "status": n.Status})
						}
					}
					out = append(out, map[string]any{"page": c.Title, "id": c.ID, "openNotices": open})
				}
				return out, nil
			}},
		{Name: "propose_status_update", Description: "Get a public status page post ready: a new notice (title and impact) or an update to an open one (notice id from status_pages). Then read them the exact text, and only after they say yes call do_action. Write it for customers: short, calm, no internal details.",
			Params: map[string]any{
				"page":    map[string]any{"type": "STRING", "description": "The page's title or id; empty when there is one"},
				"notice":  map[string]any{"type": "STRING", "description": "An open notice's id, to post an update to it; empty for a new notice"},
				"title":   map[string]any{"type": "STRING", "description": "A new notice's title, like \"Checkout is slow\""},
				"impact":  map[string]any{"type": "STRING", "enum": []string{"degraded", "outage", "maintenance", "info"}, "description": "A new notice's impact"},
				"stage":   map[string]any{"type": "STRING", "enum": []string{"investigating", "identified", "monitoring", "resolved", "update"}},
				"message": map[string]any{"type": "STRING", "description": "The text customers will read"},
			}, Required: []string{"message"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				if err := gate.allowed("", true); err != nil {
					return nil, err
				}
				page, err := d.statusPage(str(args, "page"))
				if err != nil {
					return nil, err
				}
				msg, stage, notice := str(args, "message"), str(args, "stage"), str(args, "notice")
				if msg == "" {
					return nil, valid.Error("what should customers read?")
				}
				var what string
				var run func(context.Context) (any, error)
				if notice != "" {
					if stage == "" {
						stage = "update"
					}
					what = fmt.Sprintf("post on the status page %q, as %s: %q", page.Title, stage, msg)
					run = func(context.Context) (any, error) {
						if _, err := d.status.PostUpdate(page.ID, notice, statuspage.NoticeUpdate{Stage: stage, Body: msg}, true); err != nil {
							return nil, err
						}
						return "posted; subscribers are told", nil
					}
				} else {
					title, impact := str(args, "title"), str(args, "impact")
					if title == "" {
						return nil, valid.Error("a new notice needs a title")
					}
					if impact == "" {
						impact = "degraded"
					}
					what = fmt.Sprintf("open a notice on the status page %q: %q (%s): %q", page.Title, title, impact, msg)
					run = func(context.Context) (any, error) {
						if _, err := d.status.AddNotice(page.ID, statuspage.Notice{Title: title, Body: msg, Status: impact}, true); err != nil {
							return nil, err
						}
						return "posted; subscribers are told", nil
					}
				}
				gate.mu.Lock()
				gate.pending = &voiceAction{what: what, at: time.Now(), run: run}
				gate.mu.Unlock()
				return map[string]any{"ready": what, "next": "read it to them; after their yes, call do_action"}, nil
			}},
	}
}

// statusPage finds a page by title or id ("" when there is just one).
func (d callDeps) statusPage(name string) (statuspage.Config, error) {
	pages := d.status.Pages()
	if name == "" && len(pages) == 1 {
		return pages[0], nil
	}
	for _, c := range pages {
		if strings.EqualFold(c.Title, name) || c.ID == name || strings.EqualFold(c.Slug, name) {
			return c, nil
		}
	}
	if len(pages) == 0 {
		return statuspage.Config{}, valid.Error("there is no status page")
	}
	return statuspage.Config{}, valid.Error("which status page? status_pages lists them")
}
