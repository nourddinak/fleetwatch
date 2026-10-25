package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/whatsapp"
)

// "Call Nour and tell him…" while Nour is still on his own call with the AI: he is told
// on that call, the message counts as passed on, and nobody rings him a second time.
func TestPassMessageToSomeoneOnACall(t *testing.T) {
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
		return []oncall.Person{{ID: oncall.Owner, Name: "Nour"}, {ID: "mem_sara", Name: "Sara"}}
	}}
	news, done := onCall(oncall.Owner)
	defer done()
	out, err := deps.passMessage("mem_sara").Run(context.Background(), map[string]any{"name": "Nour", "message": "I am on it", "how": "call"})
	if err != nil || !strings.Contains(out.(string), "told them there") {
		t.Fatal(out, err)
	}
	select {
	case n := <-news:
		if !strings.Contains(n, "Sara") || !strings.Contains(n, "I am on it") {
			t.Fatal(n)
		}
	default:
		t.Fatal("not said on his call")
	}
	if len(wa.Waiting(oncall.Owner)) != 0 || wa.Notes(oncall.Owner)[0].Status != "delivered" {
		t.Fatal("the message is passed on")
	}
}

// A message counts as passed on only if they stayed to hear it.
func TestHeard(t *testing.T) {
	ai1 := []ai.Line{{Who: "ai", Text: "Hi Sara, a message from Nour…"}}
	if heard(ai1, 4*time.Second) {
		t.Fatal("hung up at once")
	}
	if !heard(ai1, 25*time.Second) || !heard(append(ai1, ai.Line{Who: "person", Text: "ok"}), 5*time.Second) {
		t.Fatal("listened, or answered")
	}
}
