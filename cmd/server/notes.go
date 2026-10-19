package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/valid"
)

// Messages between team members through the WhatsApp AI: "text Sara that…", "call Omar
// and tell him…", "when Omar calls, tell him…". Kept ones wait in whatsapp_notes for the
// person's next call or chat, and the sender hears back once they are passed on.

type note struct {
	id         int64
	from, text string
	at         int64
}

func (d callDeps) leaveNote(from, to, text string) error {
	_, err := d.db.Exec(`INSERT INTO whatsapp_notes (from_person, to_person, text, created_at) VALUES (?, ?, ?, ?)`, from, to, text, time.Now().UnixMilli())
	return err
}

// notesFor are the messages waiting for someone, oldest first.
func (d callDeps) notesFor(person string) []note {
	rows, err := d.db.Query(`SELECT id, from_person, text, created_at FROM whatsapp_notes WHERE to_person = ? AND delivered_at = 0 ORDER BY id`, person)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []note
	for rows.Next() {
		var n note
		if rows.Scan(&n.id, &n.from, &n.text, &n.at) == nil {
			out = append(out, n)
		}
	}
	return out
}

// notesBrief tells the AI which messages to pass on ("" when none).
func (d callDeps) notesBrief(notes []note) string {
	if len(notes) == 0 {
		return ""
	}
	b := "\nMessages teammates left for this person: pass each on clearly (who it is from and what they said), once, early in the conversation:\n"
	for _, n := range notes {
		b += fmt.Sprintf("- from %s, %s ago: %q\n", d.nameOf(n.from), time.Since(time.UnixMilli(n.at)).Round(time.Minute), n.text)
	}
	return b
}

// delivered marks messages passed on and lets each sender know.
func (d callDeps) delivered(to string, notes []note) {
	for _, n := range notes {
		if res, err := d.db.Exec(`UPDATE whatsapp_notes SET delivered_at = ? WHERE id = ? AND delivered_at = 0`, time.Now().UnixMilli(), n.id); err != nil {
			continue
		} else if k, _ := res.RowsAffected(); k == 0 {
			continue // passed on elsewhere meanwhile
		}
		if wp, ok := d.wa.PersonOf(n.from); ok && wp.Phone != "" && n.from != to {
			go func(phone, text string) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := d.wa.Send(ctx, phone, text); err != nil {
					slog.Warn("whatsapp note receipt", "err", err)
				}
			}(wp.Phone, fmt.Sprintf("✅ I passed your message on to %s: %q", d.nameOf(to), n.text))
		}
	}
}

func (d callDeps) nameOf(person string) string {
	for _, p := range d.people() {
		if p.ID == person {
			return p.Name
		}
	}
	return person
}

// passMessage is the AI tool behind all three ways.
func (d callDeps) passMessage(from string) ai.Tool {
	return ai.Tool{Name: "pass_message", Description: "Pass a message on to a teammate. how: text (a WhatsApp message now), call (call them now with the AI, which tells them; if they do not answer it is kept for later), later (keep it and tell them the next time they call or chat with you, e.g. \"when Omar calls, tell him…\"). Confirm what you did in a few words.",
		Params: map[string]any{
			"name":    map[string]any{"type": "STRING", "description": "The teammate's name"},
			"message": map[string]any{"type": "STRING", "description": "What to tell them, as the sender meant it"},
			"how":     map[string]any{"type": "STRING", "enum": []string{"text", "call", "later"}},
		}, Required: []string{"name", "message", "how"},
		Run: func(_ context.Context, args map[string]any) (any, error) {
			name, _ := args["name"].(string)
			msg, _ := args["message"].(string)
			how, _ := args["how"].(string)
			name, msg = strings.TrimSpace(name), strings.TrimSpace(msg)
			if msg == "" {
				return nil, valid.Error("what should I tell them?")
			}
			to := ""
			for _, p := range d.people() {
				if strings.EqualFold(p.Name, name) || strings.EqualFold(p.Username, name) {
					to = p.ID
				}
			}
			wp, ok := d.wa.PersonOf(to)
			if to == "" || !ok || wp.Phone == "" {
				return nil, valid.Error("no teammate called " + name + " has a WhatsApp number saved")
			}
			if to == from {
				return nil, valid.Error("that is the person you are talking to")
			}
			sender := d.nameOf(from)
			switch how {
			case "text":
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := d.wa.Send(ctx, wp.Phone, fmt.Sprintf("📨 From %s: %s", sender, msg)); err != nil {
					return nil, err
				}
				return "sent", nil
			case "call":
				if err := d.leaveNote(from, to, msg); err != nil { // the call passes it on; unanswered, it waits for later
					return nil, err
				}
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
					defer cancel()
					opening := "The call just connected. You are calling " + d.nameOf(to) + " to pass on a message from " + sender + ". Greet them, say who it is from, and tell them the message. Then ask if they want to reply or need anything else."
					if answered, err := d.wa.Ring(ctx, wp.Phone, 45*time.Second, d.sessionOpening(to, "", false, opening)); err != nil || !answered {
						slog.Info("whatsapp note call not answered: kept for later", "to", to)
					}
				}()
				return "calling them now; if they do not answer, I will tell them the next time they call or chat", nil
			case "later":
				if err := d.leaveNote(from, to, msg); err != nil {
					return nil, err
				}
				return "kept: I will tell them the next time they call or chat with me, and let you know", nil
			}
			return nil, valid.Error("how: text, call or later")
		}}
}
