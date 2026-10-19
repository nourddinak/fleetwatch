package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/valid"
)

// Cover requests on WhatsApp: "can someone cover me Saturday?" goes to the rest of the
// rotation; the first "I'll cover" gets the override.

func (d callDeps) coverTools(person string) []ai.Tool {
	if d.oncall == nil {
		return nil
	}
	str := func(args map[string]any, k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	return []ai.Tool{
		{Name: "ask_cover", Description: "Ask the rest of the on-call rotation to cover them for a while (\"can someone cover me Saturday?\"). Times like 2026-10-24 09:00, in their time zone; a whole day is 00:00 to 00:00 the next day. The team gets a WhatsApp message; the first who says yes is put on call.",
			Params: map[string]any{
				"from": map[string]any{"type": "STRING", "description": "Start, YYYY-MM-DD HH:MM"},
				"to":   map[string]any{"type": "STRING", "description": "End, YYYY-MM-DD HH:MM"},
				"note": map[string]any{"type": "STRING", "description": "Why, if they said"},
			}, Required: []string{"from", "to"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				loc := d.zone(person)
				from, err1 := time.ParseInLocation("2006-01-02 15:04", str(args, "from"), loc)
				to, err2 := time.ParseInLocation("2006-01-02 15:04", str(args, "to"), loc)
				if err1 != nil || err2 != nil {
					return nil, valid.Error("times like 2026-10-24 09:00")
				}
				r, err := d.oncall.AskCover(person, from, to, str(args, "note"))
				if err != nil {
					return nil, err
				}
				return fmt.Sprintf("asked the team (request %d); you will hear when someone takes it", r.ID), nil
			}},
		{Name: "cover_requests", Description: "Open requests from teammates to cover their on-call, with ids.",
			Run: func(context.Context, map[string]any) (any, error) {
				out := []map[string]any{}
				for _, r := range d.oncall.CoverRequests() {
					if r.Status == "open" {
						out = append(out, map[string]any{"id": r.ID, "who": d.nameOf(r.Person), "from": d.when(person, r.From), "to": d.when(person, r.To), "note": r.Note})
					}
				}
				return out, nil
			}},
		{Name: "take_cover", Description: "Take a teammate's cover request (they said \"I'll cover\" or yes to it): they are put on call for that time.",
			Params: map[string]any{"id": map[string]any{"type": "INTEGER", "description": "From cover_requests"}}, Required: []string{"id"},
			Run: func(_ context.Context, args map[string]any) (any, error) {
				id, _ := args["id"].(float64)
				r, err := d.oncall.TakeCover(int64(id), person)
				if err != nil {
					return nil, err
				}
				return "done: on call for " + d.nameOf(r.Person) + " from " + d.when(person, r.From) + " to " + d.when(person, r.To), nil
			}},
	}
}

// when writes a moment in someone's time zone.
func (d callDeps) when(person string, ms int64) string {
	return time.UnixMilli(ms).In(d.zone(person)).Format("Mon 2 Jan 15:04")
}

// coverAsked tells the rest of the rotation, on WhatsApp.
func (d callDeps) coverAsked(r oncall.CoverRequest) {
	for _, id := range d.oncall.Config().Rotation {
		if id == r.Person {
			continue
		}
		text := fmt.Sprintf("🙋 %s asks: can someone cover their on-call from %s to %s?", d.nameOf(r.Person), d.when(id, r.From), d.when(id, r.To))
		if r.Note != "" {
			text += " (" + r.Note + ")"
		}
		d.text(id, text+"\nReply \"I'll cover\" to take it.")
	}
}

// coverTaken tells who asked, and the rest of the rotation.
func (d callDeps) coverTaken(r oncall.CoverRequest) {
	for _, id := range d.oncall.Config().Rotation {
		switch id {
		case r.TakenBy:
		case r.Person:
			d.tell(id, fmt.Sprintf("✅ %s covers your on-call from %s to %s.", d.nameOf(r.TakenBy), d.when(id, r.From), d.when(id, r.To)))
		default:
			d.text(id, fmt.Sprintf("✅ %s took %s's cover (%s to %s).", d.nameOf(r.TakenBy), d.nameOf(r.Person), d.when(id, r.From), d.when(id, r.To)))
		}
	}
}

// text sends someone a WhatsApp message, if they have a number.
func (d callDeps) text(person, text string) {
	wp, ok := d.wa.PersonOf(person)
	if !ok || wp.Phone == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.wa.Send(ctx, wp.Phone, text); err != nil {
		slog.Warn("whatsapp text", "err", err)
	}
}

// nowLine tells the AI the date and time, in the person's time zone.
func (d callDeps) nowLine(person string) string {
	loc := d.zone(person)
	return "It is now " + time.Now().In(loc).Format("Monday 2 January 2006, 15:04") + " in their time zone (" + loc.String() + ")."
}
