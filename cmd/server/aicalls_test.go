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
	deps := callDeps{auth: a}
	gate := func(person string) *voiceActions { return &voiceActions{d: deps, person: person, name: person} }
	if gate(oncall.Owner).allowed("s2") != nil || gate(op.ID).allowed("s1") != nil {
		t.Fatal("the owner, and an operator on their server, may act")
	}
	if gate(op.ID).allowed("s2") == nil || gate(viewer.ID).allowed("s1") == nil {
		t.Fatal("an operator elsewhere, or a viewer, may not")
	}

	g := gate(oncall.Owner)
	ctx := context.Background()
	if _, err := g.do(ctx); err == nil {
		t.Fatal("ran with nothing proposed")
	}
	g.pending = &voiceAction{appID: "app_1", appName: "api", action: "restart", at: time.Now()}
	if _, err := g.do(ctx); err == nil || !strings.Contains(err.Error(), "not answered") {
		t.Fatal("ran before they answered", err)
	}
	silent := ai.WithHeard(ctx, func(time.Time) string { return " " })
	if _, err := g.do(silent); err == nil {
		t.Fatal("ran on silence")
	}
	g.pending.at = time.Now().Add(-2 * confirmWithin)
	said := ai.WithHeard(ctx, func(time.Time) string { return "yes go ahead" })
	if _, err := g.do(said); err == nil || !strings.Contains(err.Error(), "too long ago") {
		t.Fatal("ran a stale proposal", err)
	}
}
