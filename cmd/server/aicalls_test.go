package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/whatsapp"
)

// Voice actions follow the person's role and run only after they answered, in time.
func TestVoiceActionGate(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	a := auth.NewService(d, box)
	cl := auth.Client{IP: "203.0.113.5", UserAgent: "test"}
	op, err := a.CreateMember(cl, auth.MemberInput{Username: "op", Role: auth.RoleOperator, Servers: []string{"s1"}, Password: "member-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := a.CreateMember(cl, auth.MemberInput{Username: "viewer", Role: auth.RoleViewer, Password: "member-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	deps := callDeps{auth: a, db: d}
	if !deps.sees(oncall.Owner)("any") || !deps.sees(op.ID)("s1") || deps.sees(op.ID)("s2") || !deps.sees(viewer.ID)("s9") {
		t.Fatal("who sees which servers")
	}
	if deps.sees("mem_gone")("s1") {
		t.Fatal("an unknown member sees servers")
	}
	gate := func(person string) *voiceActions { return &voiceActions{d: deps, person: person, name: person} }
	if gate(oncall.Owner).allowed("s2", true) != nil || gate(op.ID).allowed("s1", false) != nil {
		t.Fatal("the owner, and an operator on their server, may act")
	}
	if gate(op.ID).allowed("s2", false) == nil || gate(viewer.ID).allowed("s1", false) == nil || gate(op.ID).allowed("s1", true) == nil {
		t.Fatal("an operator elsewhere, a viewer, or an operator on an owner-only action may not")
	}

	g := gate(oncall.Owner)
	ctx := context.Background()
	if _, err := g.do(ctx, ""); err == nil {
		t.Fatal("ran with nothing proposed")
	}
	ran := 0
	g.pending = &voiceAction{what: "restart api", at: time.Now(), run: func(context.Context) (any, error) { ran++; return "ok", nil }}
	if _, err := g.do(ctx, ""); err == nil || !strings.Contains(err.Error(), "not answered") {
		t.Fatal("ran before they answered", err)
	}
	silent := ai.WithHeard(ctx, func(time.Time) string { return " " })
	if _, err := g.do(silent, ""); err == nil {
		t.Fatal("ran on silence")
	}
	g.pending.at = time.Now().Add(-2 * confirmWithin)
	said := ai.WithHeard(ctx, func(time.Time) string { return "yes go ahead" })
	if _, err := g.do(said, ""); err == nil || !strings.Contains(err.Error(), "too long ago") {
		t.Fatal("ran a stale proposal", err)
	}
	g.pending.at = time.Now()
	if out, err := g.do(said, ""); err != nil || out != "ok" || ran != 1 {
		t.Fatal("did not run once confirmed", out, err)
	}

	// With PINs required: the right one runs it, three wrong ones end actions.
	wa := whatsapp.New(d, t.TempDir(), nil)
	wa.SetOptions(whatsapp.Options{RequirePIN: true})
	deps.wa = wa
	g = &voiceActions{d: deps, person: oncall.Owner, name: "Nour"}
	g.pending = &voiceAction{what: "restart api", at: time.Now(), run: func(context.Context) (any, error) { ran++; return "ok", nil }}
	if _, err := g.do(said, "1234"); err == nil || !strings.Contains(err.Error(), "not set one") {
		t.Fatal("ran without a PIN set", err)
	}
	wa.SetPIN(oncall.Owner, "4821")
	if _, err := g.do(said, "1234"); err == nil {
		t.Fatal("a wrong PIN ran it")
	}
	if out, err := g.do(said, "4 8 2 1"); err != nil || out != "ok" {
		t.Fatal("the right PIN did not run it", out, err)
	}
	g.pending = &voiceAction{what: "x", at: time.Now(), run: func(context.Context) (any, error) { return "ok", nil }}
	g.do(said, "0000")
	g.do(said, "0000")
	if _, err := g.do(said, "4821"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatal("three wrong PINs must end actions", err)
	}
}

// "When Sara calls, tell her…": kept, passed on once on her next call or chat.
func TestPassMessageLater(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	wa := whatsapp.New(d, t.TempDir(), nil)
	for id, phone := range map[string]string{oncall.Owner: "+212600000001", "mem_sara": "+212600000002"} {
		if _, err := wa.SetPerson(id, whatsapp.Person{Phone: phone, Language: "en"}); err != nil {
			t.Fatal(err)
		}
	}
	deps := callDeps{db: d, wa: wa, people: func() []oncall.Person {
		return []oncall.Person{{ID: oncall.Owner, Name: "Nour"}, {ID: "mem_sara", Name: "Sara", Username: "sara"}, {ID: "mem_omar", Name: "Omar"}}
	}}
	tool := deps.passMessage(oncall.Owner)
	run := func(args map[string]any) error { _, err := tool.Run(context.Background(), args); return err }
	if run(map[string]any{"name": "Omar", "message": "hi", "how": "later"}) == nil {
		t.Fatal("Omar has no WhatsApp number")
	}
	if run(map[string]any{"name": "Nour", "message": "hi", "how": "later"}) == nil {
		t.Fatal("a message to oneself")
	}
	if err := run(map[string]any{"name": "sara", "message": "renew the domain today", "how": "later"}); err != nil {
		t.Fatal(err)
	}
	notes := deps.notesFor("mem_sara")
	if len(notes) != 1 || !strings.Contains(deps.notesBrief(notes), "from Nour") || !strings.Contains(deps.notesBrief(notes), "renew the domain today") {
		t.Fatal("kept for Sara", notes)
	}
	deps.delivered("mem_sara", notes)
	if len(deps.notesFor("mem_sara")) != 0 {
		t.Fatal("passed on twice")
	}
}

// News reaches someone's call while they are on it, and not once it ended.
func TestTellOnCall(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	deps := callDeps{db: d, wa: whatsapp.New(d, t.TempDir(), nil)}
	news, done := onCall("mem_sara")
	deps.tell("mem_sara", "Omar picked up")
	select {
	case n := <-news:
		if n != "Omar picked up" {
			t.Fatal(n)
		}
	default:
		t.Fatal("not told on the call")
	}
	done()
	deps.tell("mem_sara", "later news") // no call now: a text (no number here, so nothing)
	select {
	case n := <-news:
		t.Fatal("told on an ended call", n)
	default:
	}
}
