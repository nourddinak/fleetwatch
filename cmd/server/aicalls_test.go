package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/domains"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/statuspage"
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
	if _, err := g.do(whatsapp.WithGroup(said), "4821"); err == nil || !strings.Contains(err.Error(), "team group") {
		t.Fatal("a PIN in the team group", err)
	}
	if out, err := g.do(said, "4 8 2 1"); err != nil || out != "ok" {
		t.Fatal("the right PIN did not run it", out, err)
	}
	g.pending = &voiceAction{what: "x", at: time.Now(), run: func(context.Context) (any, error) { return "ok", nil }}
	g.do(said, "0000")
	g.do(said, "0000")
	g.do(said, "0000")
	if _, err := g.do(said, "4821"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatal("three wrong PINs in a row lock actions", err)
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
	notes := wa.Waiting("mem_sara")
	if len(notes) != 1 || !strings.Contains(deps.notesBrief(notes), "from Nour") || !strings.Contains(deps.notesBrief(notes), "renew the domain today") {
		t.Fatal("kept for Sara", notes)
	}
	deps.delivered("mem_sara", notes)
	if len(wa.Waiting("mem_sara")) != 0 {
		t.Fatal("passed on twice")
	}
	if l := wa.Notes("mem_sara"); len(l) != 1 || l[0].Status != "delivered" || l[0].Via != "later" {
		t.Fatal("the list", l)
	}
	id, _ := wa.LeaveNote(oncall.Owner, "mem_sara", "second", "later", false)
	if wa.CancelNote(id, "mem_sara") == nil {
		t.Fatal("only the sender (or the owner) cancels")
	}
	if wa.CancelNote(id, oncall.Owner) != nil || len(wa.Waiting("mem_sara")) != 0 || wa.Deliver(id) {
		t.Fatal("a canceled message is not passed on")
	}
	old, _ := wa.LeaveNote(oncall.Owner, "mem_sara", "old", "later", false)
	d.Exec(`UPDATE whatsapp_notes SET created_at = ? WHERE id = ?`, time.Now().Add(-8*24*time.Hour).UnixMilli(), old)
	if len(wa.Waiting("mem_sara")) != 0 || wa.Notes("")[0].Status != "expired" {
		t.Fatal("a week-old message expires")
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

// Names as heard: first names work, close ones are asked back, unknown ones get the team.
func TestTeammate(t *testing.T) {
	deps := callDeps{people: func() []oncall.Person {
		return []oncall.Person{{ID: oncall.Owner, Name: "Nour"}, {ID: "m1", Name: "Sara Ali", Username: "sara"}, {ID: "m2", Name: "Omar Haddad", Username: "omar"},
			{ID: "m3", Name: "Sara Bennani", Username: "sbennani"}}
	}}
	for in, want := range map[string]string{"omar": "m2", "Omar Haddad": "m2", "haddad": "m2", "NOUR": oncall.Owner, "sbennani": "m3", "Sara Ali": "m1"} {
		if got, err := deps.teammate(in); got != want {
			t.Fatal(in, got, err)
		}
	}
	for in, says := range map[string]string{"Sara": "which one", "Omer": "Did they mean Omar Haddad", "Karim": "The team: Nour, Sara Ali", "": "which teammate"} {
		if _, err := deps.teammate(in); err == nil || !strings.Contains(err.Error(), says) {
			t.Fatal(in, err)
		}
	}
}

// A due reminder is told once; a watch past its time is closed and said so.
func TestReminders(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	wa := whatsapp.New(d, t.TempDir(), nil)
	wa.SetPerson("mem_sara", whatsapp.Person{Phone: "+212600000002", Language: "en", TimeZone: "Africa/Casablanca"})
	deps := &callDeps{db: d, wa: wa, people: func() []oncall.Person { return []oncall.Person{{ID: "mem_sara", Name: "Sara"}} }}

	due, err := deps.dueTime("mem_sara", "18:00", nil)
	loc, _ := time.LoadLocation("Africa/Casablanca")
	if err != nil || due.In(loc).Format("15:04") != "18:00" || !due.After(time.Now()) || time.Until(due) > 24*time.Hour {
		t.Fatal("at 18:00 their time, the next one", due, err)
	}
	if due, _ := deps.dueTime("mem_sara", "", float64(30)); time.Until(due).Round(time.Minute) != 30*time.Minute {
		t.Fatal("in 30 minutes", due)
	}

	news, done := onCall("mem_sara")
	defer done()
	past := time.Now().Add(-time.Minute).UnixMilli()
	wa.AddWatch(whatsapp.Watch{Person: "mem_sara", What: "check the backup", Due: past, How: "text", Expires: time.Now().Add(time.Hour).UnixMilli()})
	wa.AddWatch(whatsapp.Watch{Person: "mem_sara", What: "CPU below 50%", Cond: `{"kind":"app"}`, How: "text", Expires: past})
	wa.AddWatch(whatsapp.Watch{Person: "mem_sara", What: "later", Due: time.Now().Add(time.Hour).UnixMilli(), How: "text", Expires: time.Now().Add(2 * time.Hour).UnixMilli()})
	deps.checkWatches(context.Background())
	deps.checkWatches(context.Background()) // nothing twice
	var got []string
	for len(news) > 0 {
		got = append(got, <-news)
	}
	if len(got) != 2 || !strings.Contains(got[0], "check the backup") || !strings.Contains(got[1], "stopped watching") {
		t.Fatal("told", got)
	}
	status := map[string]string{}
	for _, w := range wa.Watches("mem_sara") {
		status[w.What] = w.Status
	}
	if status["check the backup"] != "done" || status["CPU below 50%"] != "expired" || status["later"] != "active" {
		t.Fatal(status)
	}
}

// Certificates and domains ending within 14 days, to tell the owner (and the site's).
func TestExpiring(t *testing.T) {
	now := time.Now()
	in := func(days float64) int64 { return now.Add(time.Duration(days * 24 * float64(time.Hour))).UnixMilli() }
	list := []domains.Domain{
		{Host: "shop.example.com", Public: domains.Cert{NotAfter: in(5)}, Registered: "example.com", Expires: in(10)},
		{Host: "api.example.com", Public: domains.Cert{NotAfter: in(1), AutoRenew: true}, Registered: "example.com", Expires: in(10)},
		{Host: "far.org", Public: domains.Cert{NotAfter: in(60)}, Registered: "far.org", Expires: in(300)},
	}
	got := (&callDeps{}).expiring(list, now)
	if len(got) != 2 || got[0].key != "ssl|shop.example.com" || got[1].key != "domain|example.com" || got[0].people[0] != oncall.Owner {
		t.Fatal(got)
	}
	for left, mark := range map[int]int{20: 0, 13: 14, 6: 7, 0: 1} {
		if markFor(left) != mark {
			t.Fatal(left, markFor(left))
		}
	}
}

// "Tell customers we're fixing it": ready first, posted only once they said yes.
func TestStatusPost(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st := statuspage.New(d, incidents.NewStore(d, live.NewBus()))
	cfg := statuspage.Defaults()
	cfg.Title = "Shop status"
	page, err := st.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	deps := callDeps{db: d, status: st}
	if len(deps.statusTools(&voiceActions{d: deps, person: "mem_1"})) != 0 {
		t.Fatal("status pages are the owner's")
	}
	g := &voiceActions{d: deps, person: oncall.Owner, name: "Nour"}
	var propose ai.Tool
	for _, tool := range deps.statusTools(g) {
		if tool.Name == "propose_status_update" {
			propose = tool
		}
	}
	if _, err := propose.Run(context.Background(), map[string]any{"title": "Checkout is slow", "message": "We are fixing it."}); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Page(page.ID); len(p.Notices) != 0 {
		t.Fatal("posted before a yes")
	}
	if _, err := g.do(ai.WithHeard(context.Background(), func(time.Time) string { return "yes" }), ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Page(page.ID); len(p.Notices) != 1 || p.Notices[0].Title != "Checkout is slow" || p.Notices[0].Updates[0].Body != "We are fixing it." {
		t.Fatalf("%+v", p.Notices)
	}
}

// The weekly summary read out: no emoji or bullets, whole sentences.
func TestSpoken(t *testing.T) {
	got := spoken("📊 Your week, Nour:\n• Incidents: 3 this week, 3 resolved\n• Lowest uptime: shop 99.50%\nReply here to ask me anything.")
	want := "Hi Nour! Here is your week. Incidents: 3 this week, 3 resolved. Lowest uptime: shop 99.50 percent. Reply here to ask me anything."
	if got != want {
		t.Fatalf("%q", got)
	}
}

// "Looks like you're busy, I'll call you back": a call back that only rings if the
// incident still waits for someone, and not endlessly.
func TestCallBack(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'web-1', 0)`)
	inc := incidents.NewStore(d, live.NewBus())
	inc.Ensure(incidents.Signal{Type: "server_offline", Key: "k1", Severity: "critical", ServerID: "s1", Title: "web-1 is offline"})
	list, _ := inc.List(incidents.Filter{Status: "open"})
	if len(list) != 1 {
		t.Fatal(list)
	}
	x := list[0]
	wa := whatsapp.New(d, t.TempDir(), nil)
	deps := callDeps{db: d, wa: wa, incidents: inc, people: func() []oncall.Person { return []oncall.Person{{ID: oncall.Owner, Name: "Nour"}} }}
	var callBack ai.Tool
	for _, tool := range deps.watchTools(oncall.Owner, &voiceActions{d: deps, person: oncall.Owner}, func(string) bool { return true }, x.ID) {
		if tool.Name == "call_back" {
			callBack = tool
		}
	}
	if _, err := callBack.Run(context.Background(), map[string]any{"minutes": float64(5), "reason": "web-1 is offline"}); err != nil {
		t.Fatal(err)
	}
	w := wa.Watches(oncall.Owner)[0]
	var c cond
	json.Unmarshal([]byte(w.Cond), &c)
	if w.How != "call" || time.Until(time.UnixMilli(w.Due)).Round(time.Minute) != 5*time.Minute || c.ID != x.ID || c.State != "active" {
		t.Fatalf("%+v %+v", w, c)
	}
	if ok, _ := deps.holds(c); !ok {
		t.Fatal("rings while nobody took it")
	}
	inc.Act(x.ID, "acknowledge", "", "Omar")
	if ok, _ := deps.holds(c); ok {
		t.Fatal("not once someone took it")
	}
	callBack.Run(context.Background(), map[string]any{})
	if _, err := callBack.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("three call backs in an hour")
	}
}

// A sign-in from a browser the account never used is flagged (not the first one, not a
// known one), and the AI can sign it out.
func TestNewBrowserSignIn(t *testing.T) {
	chromeWin := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36"
	firefoxLinux := "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
	for ua, want := range map[string]string{chromeWin: "Chrome on Windows", firefoxLinux: "Firefox on Linux",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36 Edg/141.0": "Edge on Windows", "curl/8": ""} {
		if got := browserOf(ua); got != want {
			t.Fatalf("%q: %q", ua, got)
		}
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	a := auth.NewService(d, box)
	m, err := a.CreateMember(auth.Client{IP: "203.0.113.5"}, auth.MemberInput{Username: "sara", Role: auth.RoleOperator, Password: "member-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	deps := &callDeps{db: d, auth: a, wa: whatsapp.New(d, t.TempDir(), nil), people: func() []oncall.Person { return nil }}
	signIn := func(ua string) string {
		c := auth.Client{IP: "198.51.100.7", UserAgent: ua}
		if _, _, err := a.LoginExternal(c, m.ID, "test"); err != nil {
			t.Fatal(err)
		}
		list := a.SessionsOf(m.ID)
		id := list[len(list)-1].ID
		deps.signedIn(m.ID, id, c)
		return id
	}
	flagged := func() (newLogin, bool) {
		newLogins.Lock()
		defer newLogins.Unlock()
		n, ok := newLogins.m[m.ID]
		return n, ok
	}
	signIn(chromeWin)
	signIn(chromeWin)
	if _, ok := flagged(); ok {
		t.Fatal("the first and a known browser are not flagged")
	}
	id := signIn(firefoxLinux)
	if n, ok := flagged(); !ok || n.browser != "Firefox on Linux" || n.session != id {
		t.Fatalf("a new browser: %+v %v", n, ok)
	}
	if _, err := deps.signInTools(m.ID)[0].Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, s := range a.SessionsOf(m.ID) {
		if s.ID == id {
			t.Fatal("not signed out")
		}
	}
}
