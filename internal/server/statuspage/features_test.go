package statuspage

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/secret"
)

// fakeSMTP accepts mail without authentication and hands each message over.
func fakeSMTP(t *testing.T) (string, <-chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 20)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				io.WriteString(c, "220 fake\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						io.WriteString(c, "250 fake\r\n")
					case cmd == "DATA":
						io.WriteString(c, "354 go\r\n")
						var b strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							b.WriteString(l)
						}
						got <- b.String()
						io.WriteString(c, "250 queued\r\n")
					case cmd == "QUIT":
						io.WriteString(c, "221 bye\r\n")
						return
					default:
						io.WriteString(c, "250 ok\r\n")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), got
}

type world struct {
	s     *Service
	mu    sync.Mutex
	state map[string]string
	now   time.Time
	mail  <-chan string
}

func (w *world) set(id, st string) { w.mu.Lock(); w.state[id] = st; w.mu.Unlock() }

func newWorld(t *testing.T) *world {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	w := &world{state: map[string]string{"app1": "operational", "app2": "operational"}, now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	w.s = New(d, incidents.NewStore(d, nil))
	w.s.Now = func() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.now }
	w.s.AppState = func(id string) (string, string, int64, bool) {
		w.mu.Lock()
		defer w.mu.Unlock()
		st, ok := w.state[id]
		return st, "srv1", 1, ok
	}
	w.s.ServerState = func(id string) (string, int64, bool) { return "operational", 1, id == "srv1" }
	box, _ := secret.New(make([]byte, 32))
	w.s.Mail = NewMailer(d, box)
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	if _, err := w.s.Mail.Save(SMTPConfig{Host: "mail.test", Port: 25, Security: "none", From: "status@example.com", FromName: "Acme"}); err != nil {
		t.Fatal(err)
	}
	w.s.Mail.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	}
	w.mail = got
	return w
}

func (w *world) page(t *testing.T, mod func(c *Config)) Config {
	c := Defaults()
	c.Enabled, c.Title = true, "Acme"
	c.Groups = []Group{{Name: "Core", Components: []Component{{Kind: "app", ID: "app1", Name: "API"}, {Kind: "app", ID: "app2", Name: "Web"}}}}
	if mod != nil {
		mod(&c)
	}
	c, err := w.s.Save(c)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (w *world) mailOrFail(t *testing.T) string {
	select {
	case m := <-w.mail:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no email sent")
		return ""
	}
}

func TestNoticesAndDrafts(t *testing.T) {
	w := newWorld(t)
	c := w.page(t, nil)
	n, err := w.s.AddNotice(c.ID, Notice{Title: "Slow API", Status: "degraded", Body: "Looking into it"}, false)
	if err != nil || len(n.Updates) != 1 || n.Updates[0].Stage != "investigating" {
		t.Fatalf("add: %v %+v", err, n)
	}
	draft, _ := w.s.AddNotice(c.ID, Notice{Title: "Secret", Status: "outage", Draft: true}, false)
	p := w.s.build(mustPage(t, w.s, c.ID))
	if len(p.Notices) != 1 || p.Notices[0].Title != "Slow API" || p.Overall != "degraded" {
		t.Fatalf("public: %+v", p)
	}
	// Saving the page from an editor that loaded before must not drop notices.
	old := c
	old.Notices = nil
	if _, err := w.s.Save(old); err != nil {
		t.Fatal(err)
	}
	if got := mustPage(t, w.s, c.ID).Notices; len(got) != 2 {
		t.Fatalf("notices lost on save: %d", len(got))
	}
	if n, err = w.s.PostUpdate(c.ID, n.ID, NoticeUpdate{Stage: "resolved", Body: "Fixed"}, false); err != nil || n.Status != "resolved" || n.End == 0 {
		t.Fatalf("resolve: %v %+v", err, n)
	}
	draft.Draft = false
	if _, err := w.s.EditNotice(c.ID, draft, false); err != nil {
		t.Fatal(err)
	}
	p = w.s.build(mustPage(t, w.s, c.ID))
	if len(p.Notices) != 2 || p.Overall != "partial" || p.Notices[1].Updates[0].Body != "Fixed" {
		t.Fatalf("after publish: %+v", p)
	}
}

func mustPage(t *testing.T, s *Service, id string) Config {
	c, ok := s.Page(id)
	if !ok {
		t.Fatal("page gone")
	}
	return c
}

func TestMaintenanceNotices(t *testing.T) {
	w := newWorld(t)
	c := w.page(t, nil)
	skip := w.page(t, func(c *Config) { c.Slug, c.SkipMaintNotices = "quiet", true })
	until := w.now.Add(time.Hour).UnixMilli()
	serverOf := func(string) string { return "srv1" }
	w.s.MaintenanceChanged("server", "srv1", until, serverOf)
	got := mustPage(t, w.s, c.ID).Notices
	if len(got) != 1 || got[0].Status != "maintenance" || got[0].End != until || !strings.Contains(got[0].Title, "API, Web") {
		t.Fatalf("maintenance notice: %+v", got)
	}
	if len(mustPage(t, w.s, skip.ID).Notices) != 0 {
		t.Fatal("a page that skips maintenance notices got one")
	}
	w.s.MaintenanceChanged("server", "srv1", 1<<62, serverOf) // extended until turned off
	if got := mustPage(t, w.s, c.ID).Notices; len(got) != 1 || got[0].End != 0 {
		t.Fatalf("extended: %+v", got)
	}
	w.s.MaintenanceChanged("server", "srv1", 0, serverOf)
	if got := mustPage(t, w.s, c.ID).Notices; got[0].Status != "resolved" || got[0].Updates[len(got[0].Updates)-1].Body != "Maintenance is complete." {
		t.Fatalf("ended: %+v", got)
	}
	w.s.MaintenanceChanged("app", "other", until, serverOf) // not on any page
	if got := mustPage(t, w.s, c.ID).Notices; len(got) != 1 {
		t.Fatalf("unrelated app opened a notice: %+v", got)
	}
}

func TestAutoNoticesAndSubscribers(t *testing.T) {
	w := newWorld(t)
	c := w.page(t, func(c *Config) { c.AutoNotices, c.Subscribe = "publish", true })
	ctx := context.Background()
	if err := w.s.Subscribe(ctx, c, "email", "Visitor@Example.com", "https://dash.example.com"); err != nil {
		t.Fatal(err)
	}
	confirm := w.mailOrFail(t)
	if !strings.Contains(confirm, "To: visitor@example.com") || !strings.Contains(confirm, "/api/public/subscribe/confirm?t=") {
		t.Fatalf("confirmation: %s", confirm)
	}
	subs, _ := w.s.Subscribers(c.ID)
	if len(subs) != 1 || subs[0].Confirmed {
		t.Fatalf("subscriber: %+v", subs)
	}
	if _, err := w.s.Confirm(subs[0].token); err != nil {
		t.Fatal(err)
	}
	go func() { // the delivery loop
		for e := range w.s.queue {
			w.s.deliver(ctx, e)
		}
	}()

	w.s.tick() // first look
	w.set("app1", "outage")
	w.s.tick() // seen once: not yet
	if len(mustPage(t, w.s, c.ID).Notices) != 0 {
		t.Fatal("a single bad check opened a notice")
	}
	w.s.tick() // confirmed
	got := mustPage(t, w.s, c.ID).Notices
	if len(got) != 1 || got[0].Auto != "app:app1" || got[0].Draft || got[0].Title != "API is down" {
		t.Fatalf("auto notice: %+v", got)
	}
	down := w.mailOrFail(t)
	if !strings.Contains(down, "Subject: Acme: API is down") || !strings.Contains(down, "List-Unsubscribe: <https://dash.example.com/api/public/subscribe/remove?t=") {
		t.Fatalf("down email: %s", down)
	}
	w.set("app1", "operational")
	w.s.tick()
	w.s.tick()
	if got := mustPage(t, w.s, c.ID).Notices; got[0].Status != "resolved" {
		t.Fatalf("not resolved: %+v", got)
	}
	if up := w.mailOrFail(t); !strings.Contains(up, "Resolved") {
		t.Fatalf("resolved email: %s", up)
	}
	if _, err := w.s.Unsubscribe(subs[0].token); err != nil || w.s.subscriberCount(c.ID) != 0 {
		t.Fatal("unsubscribe failed")
	}
}

func TestDraftAutoNotice(t *testing.T) {
	w := newWorld(t)
	c := w.page(t, func(c *Config) { c.AutoNotices = "draft" })
	drafts := 0
	w.s.OnDraft = func(Config, Notice) { drafts++ }
	w.s.tick()
	w.set("app2", "outage")
	w.s.tick()
	w.s.tick()
	got := mustPage(t, w.s, c.ID).Notices
	if len(got) != 1 || !got[0].Draft || drafts != 1 || len(w.s.build(mustPage(t, w.s, c.ID)).Notices) != 0 {
		t.Fatalf("draft: %+v, told %d", got, drafts)
	}
	w.set("app2", "operational")
	w.s.tick()
	w.s.tick()
	if got := mustPage(t, w.s, c.ID).Notices; len(got) != 0 {
		t.Fatalf("an unpublished draft should go away on recovery: %+v", got)
	}
}

func TestWebhookSubscriber(t *testing.T) {
	w := newWorld(t)
	c := w.page(t, func(c *Config) { c.SubscribeHooks = true })
	hits := make(chan string, 5)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits <- string(b)
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := w.s.Subscribe(ctx, c, "webhook", srv.URL+"/hook", "https://dash"); err == nil {
		t.Fatal("a webhook on this machine was accepted")
	}
	publicOnly = false
	defer func() { publicOnly = true }()
	old := hookClient.Transport
	hookClient.Transport = srv.Client().Transport
	defer func() { hookClient.Transport = old }()
	if err := w.s.Subscribe(ctx, c, "webhook", srv.URL+"/hook", "https://dash"); err != nil {
		t.Fatal(err)
	}
	if h := <-hits; !strings.Contains(h, `"event":"subscription.created"`) {
		t.Fatalf("test call: %s", h)
	}
	if err := w.s.Subscribe(ctx, c, "webhook", "http://example.com/x", "https://dash"); err == nil {
		t.Fatal("plain http webhook accepted")
	}
	if err := w.s.Subscribe(ctx, c, "email", "a@b.com", "https://dash"); err == nil {
		t.Fatal("email accepted on a page that only offers webhooks")
	}
}

func TestReportAndBadge(t *testing.T) {
	w := newWorld(t)
	c := w.page(t, func(c *Config) { c.Report = ReportConfig{Monthly: true, To: "boss@example.com"} })
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	list := []incidents.Incident{{TypeKey: "app_offline", FirstAt: sep.Add(24 * time.Hour).UnixMilli(), ResolvedAt: sep.Add(24*time.Hour + 72*time.Minute).UnixMilli()}}
	pct, mins, n := rangeUptime(list, 1, sep.UnixMilli(), sep.AddDate(0, 1, 0).UnixMilli(), w.now)
	if pct == nil || *pct != 99.833 || mins != 72 || n != 1 {
		t.Fatalf("range uptime: %v %d %d", pct, mins, n)
	}
	r, err := w.s.Report(c.ID, "2026-09")
	if err != nil || len(r.Rows) != 2 || !strings.Contains(string(r.CSV()), "Core,API,100.000,0,0") {
		t.Fatalf("report: %v %s", err, r.CSV())
	}
	if _, err := w.s.Report(c.ID, "2027-01"); err == nil {
		t.Fatal("future month accepted")
	}
	w.s.housekeeping(context.Background())
	m := w.mailOrFail(t)
	if !strings.Contains(m, "Subject: Uptime report 2026-09: Acme") || !strings.Contains(m, "filename=uptime-2026-09.csv") {
		t.Fatalf("report email: %s", m)
	}
	if mustPage(t, w.s, c.ID).Report.LastSent != "2026-09" {
		t.Fatal("report not marked as sent")
	}
	w.s.housekeeping(context.Background()) // not twice
	select {
	case m := <-w.mail:
		t.Fatalf("sent again: %s", m)
	case <-time.After(300 * time.Millisecond):
	}
	b := string(Badge("partial", `<script>`))
	if !strings.Contains(b, "partial outage") || strings.Contains(b, "<script>") || !strings.Contains(b, "&lt;script&gt;") {
		t.Fatalf("badge: %s", b)
	}
}

func TestPasswordPage(t *testing.T) {
	w := newWorld(t)
	c := Defaults()
	c.Access = "password"
	if _, err := w.s.Save(c); err == nil {
		t.Fatal("a password page without a password was saved")
	}
	c.Password = "letmein1"
	c, err := w.s.Save(c)
	if err != nil || c.PasswordHash == "" || c.Password != "" || c.Redacted().PasswordHash != "" || !c.Redacted().HasPassword {
		t.Fatalf("save: %v %+v", err, c)
	}
	if !w.s.CheckPassword(c, "letmein1") || w.s.CheckPassword(c, "nope") {
		t.Fatal("password check")
	}
	tok := w.s.UnlockToken(c)
	c.Password = "" // saving again keeps the password
	c, _ = w.s.Save(c)
	if !w.s.Unlocked(c, tok) {
		t.Fatal("unchanged password locked people out")
	}
	c.Password = "another1"
	c, _ = w.s.Save(c)
	if w.s.Unlocked(c, tok) {
		t.Fatal("a new password must lock old cookies out")
	}
}

func TestLatency(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 10, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	pts := latency([]proto.HealthResult{
		{TS: at(5 * time.Minute), OK: true, LatencyMs: 100},
		{TS: at(6 * time.Minute), OK: true, LatencyMs: 300},
		{TS: at(7 * time.Minute), OK: false, LatencyMs: 9000}, // failed checks do not count
		{TS: at(3 * time.Hour), OK: true, LatencyMs: 50},
		{TS: at(30 * time.Hour), OK: true, LatencyMs: 70}, // older than a day
	}, now)
	if len(pts) != 2 || pts[0].Ms != 50 || pts[1].Ms != 200 || pts[1].T != time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("latency: %+v", pts)
	}
	if latency(nil, now) != nil {
		t.Fatal("no data should give no chart")
	}
}

// Several senders: each sends as itself, one is the default, and deleting it passes
// the default on.
func TestSenders(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	m := NewMailer(d, box)
	addr, got := fakeSMTP(t)
	m.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if m.Ready() {
		t.Fatal("no sender yet")
	}
	a, err := m.SaveSender(Sender{Label: "Status", SMTPConfig: SMTPConfig{Host: "mail.test", Port: 25, Security: "none", From: "status@acme.test"}})
	if err != nil || !a.Default {
		t.Fatal(a, err)
	}
	b, err := m.SaveSender(Sender{Label: "Support", SMTPConfig: SMTPConfig{Host: "mail.test", Port: 25, Security: "none", From: "support@acme.test"}})
	if err != nil || b.Default {
		t.Fatal(b, err)
	}
	if err := m.SendFrom(context.Background(), b.ID, Mail{To: []string{"x@y.test"}, Subject: "hi", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if msg := <-got; !strings.Contains(msg, "support@acme.test") {
		t.Fatal("sent as the other sender:", msg)
	}
	if err := m.Send(context.Background(), Mail{To: []string{"x@y.test"}, Subject: "hi", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if msg := <-got; !strings.Contains(msg, "status@acme.test") {
		t.Fatal("the default sends the rest:", msg)
	}
	c, _ := m.SaveSender(Sender{Label: "Gmail", SMTPConfig: SMTPConfig{Host: "smtp.gmail.com", Port: 587, Security: "starttls", From: "me@gmail.test", Username: "me", Password: "p1"}})
	c.Password = "" // kept
	if c2, err := m.SaveSender(c); err != nil || !c2.HasPassword {
		t.Fatal("password kept", c2, err)
	}
	if err := m.DeleteSender(a.ID); err != nil || len(m.Senders()) != 2 || !m.Senders()[0].Default || m.Senders()[0].ID != b.ID {
		t.Fatal("the default passes on", m.Senders(), err)
	}
	if raw, _ := json.Marshal(NewMailer(d, box).Senders()); strings.Contains(string(raw), "p1") {
		t.Fatal("password leaked")
	}
}

// A sender from your own domain: added at Resend (or found there), then checked.
func TestResendDomain(t *testing.T) {
	verified := false
	var created int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer re_key" {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"API key is invalid"}`))
			return
		}
		status := "pending"
		if verified {
			status = "verified"
		}
		dom := `{"id":"d1","name":"acme.test","status":"` + status + `","records":[{"record":"DKIM","name":"resend._domainkey","type":"TXT","value":"p=abc","status":"` + status + `"},{"record":"SPF","name":"send","type":"MX","value":"feedback-smtp.eu-west-1.amazonses.com","priority":10,"status":"` + status + `"}]}`
		switch {
		case r.Method == "GET" && r.URL.Path == "/domains":
			if created > 0 {
				w.Write([]byte(`{"data":[{"id":"d1","name":"acme.test"}]}`))
			} else {
				w.Write([]byte(`{"data":[]}`))
			}
		case r.Method == "POST" && r.URL.Path == "/domains":
			created++
			w.Write([]byte(dom))
		case r.URL.Path == "/domains/d1/verify":
			w.Write([]byte(`{}`))
		case r.URL.Path == "/domains/d1":
			w.Write([]byte(dom))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ResendAPI = srv.URL
	ctx := context.Background()
	if _, err := ResendAddDomain(ctx, "re_bad", "acme.test"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal("a bad key", err)
	}
	d, err := ResendAddDomain(ctx, "re_key", "acme.test")
	if err != nil || d.ID != "d1" || len(d.Records) != 2 || d.Records[1].Priority != 10 {
		t.Fatal(d, err)
	}
	if again, _ := ResendAddDomain(ctx, "re_key", "ACME.test"); again.ID != "d1" || created != 1 {
		t.Fatal("added twice", created)
	}

	db1, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db1.Close()
	box, _ := secret.New(make([]byte, 32))
	m := NewMailer(db1, box)
	s, err := m.SaveSender(Sender{Label: "Acme", DomainID: "d1", SMTPConfig: SMTPConfig{Host: "smtp.resend.com", Port: 465, Security: "tls", Username: "resend", Password: "re_key", From: "noreply@acme.test"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Label, s.DomainID, s.Password = "Acme status", "", ""
	if s2, _ := m.SaveSender(s); s2.DomainID != "d1" || !s2.HasPassword {
		t.Fatal("an edit keeps the domain and key", s2)
	}
	verified = true
	if got, err := m.DomainCheck(ctx, s.ID); err != nil || got.Status != "verified" {
		t.Fatal(got, err)
	}
}
