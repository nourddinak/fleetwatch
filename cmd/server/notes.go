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

// notesBrief tells the AI which messages to pass on ("" when none).
func (d callDeps) notesBrief(notes []whatsapp.Note) string {
	if len(notes) == 0 {
		return ""
	}
	b := "\nMessages teammates left for this person: pass each on clearly (who it is from and what they said), once, early in the conversation. Then ask if they want to answer: if so, pass_message their answer back to the sender (how: text, unless they ask for a call).\n"
	for _, n := range notes {
		b += fmt.Sprintf("- from %s, %s ago: %q\n", d.nameOf(n.From), time.Since(time.UnixMilli(n.At)).Round(time.Minute), n.Text)
	}
	return b
}

// delivered marks messages passed on and lets each sender know.
func (d callDeps) delivered(to string, notes []whatsapp.Note) {
	for _, n := range notes {
		if d.wa.Deliver(n.ID) && n.From != to { // false: passed on elsewhere meanwhile, or canceled
			d.tell(n.From, fmt.Sprintf("✅ I passed your message on to %s: %q", d.nameOf(to), n.Text))
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
	if sayOnCall(person, text) {
		return
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

// sayOnCall tells someone on their AI call now, if they are on one.
func sayOnCall(person, text string) bool {
	onCalls.Lock()
	ch := onCalls.m[person]
	onCalls.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- text:
		return true
	default: // a backlog
		return false
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
				d.wa.LeaveNote(from, to, msg, "text", true)
				sayOnCall(to, "message from "+sender+": "+msg) // on a call with the AI right now: they hear it too
				return "sent", nil
			case "call", "later":
				id, err := d.wa.LeaveNote(from, to, msg, how, false) // the call passes it on; unanswered, it waits for later
				if err != nil {
					return nil, err
				}
				if sayOnCall(to, "message from "+sender+": "+msg+" (if they answer it, pass_message it back to "+sender+")") {
					d.wa.Deliver(id) // already on a call with the AI (often the reply to this sender's own call): no second call
					return "they are on a call with me right now, so I told them there instead of calling", nil
				}
				if how == "later" {
					return "kept: I will tell them the next time they call or chat with me, and let you know", nil
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
					switch o, err := d.wa.Dial(ctx, wp.Phone, 45*time.Second, picked); {
					case err != nil:
						d.tell(from, "The call to "+them+" did not go out ("+err.Error()+"); I kept the message and will tell them when they call or write.")
					case o == whatsapp.Declined:
						d.tell(from, them+" declined the call; I kept the message and will tell them when they call or write.")
					case o == whatsapp.NoAnswer:
						d.tell(from, them+" did not answer; I kept the message and will tell them when they call or write.")
					case slices.ContainsFunc(d.wa.Waiting(to), func(n whatsapp.Note) bool { return n.ID == id }):
						d.tell(from, them+" picked up but hung up before hearing your message; I kept it and will tell them when they call or write.")
					}
				}()
				return "calling them now, while this call goes on; you will hear how it goes", nil
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
