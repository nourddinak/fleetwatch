package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/valid"
	"fleetwatch/internal/server/whatsapp"
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
		if n.from != to {
			d.tell(n.from, fmt.Sprintf("✅ I passed your message on to %s: %q", d.nameOf(to), n.text))
		}
	}
}

// onCalls holds the AI calls going on now, by person, to tell them news as it happens.
var onCalls = struct {
	sync.Mutex
	m map[string]chan string
}{m: map[string]chan string{}}

// onCall registers a call with someone until done.
func onCall(person string) (<-chan string, func()) {
	ch := make(chan string, 8)
	onCalls.Lock()
	onCalls.m[person] = ch
	onCalls.Unlock()
	return ch, func() {
		onCalls.Lock()
		if onCalls.m[person] == ch {
			delete(onCalls.m, person)
		}
		onCalls.Unlock()
	}
}

// tell lets someone know: said on their call when they are on one, else a WhatsApp text.
func (d callDeps) tell(person, text string) {
	onCalls.Lock()
	ch := onCalls.m[person]
	onCalls.Unlock()
	if ch != nil {
		select {
		case ch <- text:
			return
		default: // a backlog: text it instead
		}
	}
	wp, ok := d.wa.PersonOf(person)
	if !ok || wp.Phone == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.wa.Send(ctx, wp.Phone, text); err != nil {
			slog.Warn("whatsapp tell", "err", err)
		}
	}()
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
			to, err := d.teammate(name)
			if err != nil {
				return nil, err
			}
			if to == from {
				return nil, valid.Error("that is the person you are talking to")
			}
			wp, err := d.reachable(to)
			if err != nil {
				return nil, err
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
				go func() { // runs alongside this call: how it goes is told to the sender as it happens
					ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
					defer cancel()
					them := d.nameOf(to)
					opening := "The call just connected. You are calling " + them + " to pass on a message from " + sender + ". Greet them, say who it is from, and tell them the message. Then ask if they want to reply or need anything else."
					talker := d.sessionOpening(to, "", false, opening)
					if talker == nil {
						d.tell(from, "I cannot call "+them+" now (no AI call minutes left); I kept the message and will tell them when they call or write.")
						return
					}
					picked := func(ctx context.Context, m whatsapp.Media) {
						d.tell(from, them+" picked up; I am telling them your message now.")
						talker(ctx, m)
					}
					switch answered, err := d.wa.Ring(ctx, wp.Phone, 45*time.Second, picked); {
					case err != nil:
						d.tell(from, "The call to "+them+" did not go out ("+err.Error()+"); I kept the message and will tell them when they call or write.")
					case !answered:
						d.tell(from, them+" did not answer; I kept the message and will tell them when they call or write.")
					}
				}()
				return "calling them now, while this call goes on; you will hear how it goes", nil
			case "later":
				if err := d.leaveNote(from, to, msg); err != nil {
					return nil, err
				}
				return "kept: I will tell them the next time they call or chat with me, and let you know", nil
			}
			return nil, valid.Error("how: text, call or later")
		}}
}

// teammate finds who a name means, as heard on a call or written in a chat. A first
// name or username is enough; a name that only sounds close is asked back, never guessed.
func (d callDeps) teammate(name string) (string, error) {
	norm := func(s string) string {
		return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
	}
	q := norm(name)
	if q == "" {
		return "", valid.Error("which teammate?")
	}
	people := d.people()
	var names []string
	for _, p := range people {
		names = append(names, p.Name)
	}
	team := "The team: " + strings.Join(names, ", ") + "."
	pick := func(match func(p oncall.Person) bool) []oncall.Person {
		var out []oncall.Person
		for _, p := range people {
			if match(p) {
				out = append(out, p)
			}
		}
		return out
	}
	found := pick(func(p oncall.Person) bool { return norm(p.Name) == q })
	if len(found) == 0 { // a username, a first or last name
		found = pick(func(p oncall.Person) bool {
			return norm(p.Username) == q || slices.Contains(strings.Fields(norm(p.Name)), q)
		})
	}
	switch {
	case len(found) == 1:
		return found[0].ID, nil
	case len(found) > 1:
		var which []string
		for _, p := range found {
			which = append(which, p.Name)
		}
		return "", valid.Error("more than one teammate is called " + name + ": " + strings.Join(which, ", ") + ". Ask which one they mean.")
	}
	var near []string // misheard or misspelled: ask back
	for _, p := range people {
		for _, w := range append(strings.Fields(norm(p.Name)), norm(p.Username)) {
			if w != "" && editDistance(w, q) <= max(1, len(q)/4) {
				near = append(near, p.Name)
				break
			}
		}
	}
	if len(near) > 0 {
		return "", valid.Error("nobody is called exactly " + name + ". Did they mean " + strings.Join(near, " or ") + "? Ask them, then try again with that name.")
	}
	return "", valid.Error("nobody on the team is called " + name + ". " + team + " Ask who they meant.")
}

// reachable is a teammate's WhatsApp number, or why there is none.
func (d callDeps) reachable(id string) (whatsapp.Person, error) {
	wp, ok := d.wa.PersonOf(id)
	if !ok || wp.Phone == "" {
		return wp, valid.Error(d.nameOf(id) + " is on the team but has no WhatsApp number saved, so I cannot text or call them. They can add one in Integrations, WhatsApp.")
	}
	return wp, nil
}

// editDistance counts the letters to change, add or remove to turn a into b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
