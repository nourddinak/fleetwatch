package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
)

type got struct {
	path, auth, title, prio string
	body                    []byte
}

func recorder(t *testing.T, status int) (*httptest.Server, chan got) {
	ch := make(chan got, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Title"), r.Header.Get("Priority"), b}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func TestProviders(t *testing.T) {
	srv, ch := recorder(t, 200)
	ctx := context.Background()
	m := Message{Title: "App <down>", Body: "API on VPS-1", Severity: "critical", Link: "https://ops.example/#/incidents"}

	if err := (Telegram{BotToken: "1:abc", ChatID: "42", apiBase: srv.URL}).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	g := <-ch
	var tg map[string]any
	json.Unmarshal(g.body, &tg)
	if g.path != "/bot1:abc/sendMessage" || tg["chat_id"] != "42" || !strings.Contains(tg["text"].(string), "App &lt;down&gt;") {
		t.Fatalf("telegram: %s %s", g.path, g.body)
	}

	// Discord only accepts discord.com URLs; exercise Send directly against the recorder.
	if err := (Discord{Webhook: srv.URL + "/api/webhooks/1/x"}).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	if g = <-ch; !bytes.Contains(g.body, []byte(`"embeds"`)) {
		t.Fatalf("discord: %s", g.body)
	}
	if (Discord{Webhook: "https://evil.example/api/webhooks/1/x"}).validate() == nil {
		t.Fatal("non-discord webhook accepted")
	}

	bad, _ := recorder(t, 401)
	err := (Telegram{BotToken: "1:secret", ChatID: "42", apiBase: bad.URL}).Send(ctx, m)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error status: %v", err)
	}
	err = (Telegram{BotToken: "1:secret", ChatID: "42", apiBase: "http://127.0.0.1:1"}).Send(ctx, m)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("token leaked into error: %v", err)
	}
}

func setup(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(bytes.Repeat([]byte{7}, 32))
	return New(d, box, live.NewBus())
}

func TestChannelsKeepSecrets(t *testing.T) {
	s := setup(t)
	srv, ch := recorder(t, 200)
	if err := s.SaveChannel("webhook", true, map[string]string{"url": srv.URL + "/hooks/s3cret", "secret": "sign"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannel("webhook", true, map[string]string{"url": "", "secret": ""}); err != nil { // empty: kept
		t.Fatal(err)
	}
	views, _ := s.Channels()
	raw, _ := json.Marshal(views)
	var hook ChannelView
	for _, v := range views {
		if v.Provider == "webhook" {
			hook = v
		}
		if v.Provider == "ntfy" {
			t.Fatal("ntfy is gone")
		}
	}
	if strings.Contains(string(raw), "s3cret") || !hook.Secrets["url"] || !hook.Secrets["secret"] || !strings.HasSuffix(hook.Fields["urlHint"], "/…") {
		t.Fatalf("view: %s", raw)
	}
	var enc []byte
	s.db.QueryRow(`SELECT config_enc FROM notification_channels WHERE provider = 'webhook'`).Scan(&enc)
	if bytes.Contains(enc, []byte("s3cret")) {
		t.Fatal("secret stored in plaintext")
	}
	if err := s.SaveChannel("webhook", true, map[string]string{"evil": "x"}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := s.SaveChannel("ntfy", true, map[string]string{"url": srv.URL, "topic": "ops"}); err == nil {
		t.Fatal("ntfy accepted")
	}
	if err := s.SaveChannel("telegram", true, map[string]string{"botToken": "nocolon", "chatId": "1"}); err == nil {
		t.Fatal("invalid telegram token accepted")
	}

	h, err := s.Test(context.Background(), "webhook")
	if err != nil || h.Result != "delivered" {
		t.Fatalf("test: %+v %v", h, err)
	}
	if g := <-ch; g.path != "/hooks/s3cret" {
		t.Fatalf("kept secret not used: %+v", g)
	}
	if list, _ := s.History(0, 0); len(list) != 1 || list[0].Read {
		t.Fatalf("history: %+v", list)
	}
	s.MarkRead(0)
	if list, _ := s.History(0, 0); !list[0].Read {
		t.Fatal("not marked read")
	}
}

func TestIncidentFiltering(t *testing.T) {
	s := setup(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	app := "a1"
	ev := func(kind, typ, sev string) incidents.Event {
		return incidents.Event{Kind: kind, Incident: incidents.Incident{ID: "i1", Key: typ + "|a1", TypeKey: typ, Type: typ, Severity: sev, ServerID: "s1", AppID: &app}}
	}
	queued := func() int {
		n := len(s.queue)
		for len(s.queue) > 0 {
			<-s.queue
		}
		return n
	}

	override, allows := "default", true
	s.Overrides = func(string, string) (string, bool) { return override, allows }

	s.Incident(ev("opened", "app_offline", "critical"))
	s.Incident(ev("opened", "app_offline", "critical")) // cooldown
	if n := queued(); n != 1 {
		t.Fatalf("cooldown: %d", n)
	}
	s.Incident(ev("resolved", "port_unavailable", "warning")) // OtherResolved is off by default
	if n := queued(); n != 0 {
		t.Fatalf("rules: %d", n)
	}
	override = "all"
	s.Incident(ev("resolved", "port_unavailable", "warning"))
	if n := queued(); n != 1 {
		t.Fatalf("override all: %d", n)
	}
	override = "critical-only"
	s.Incident(ev("opened", "high_cpu", "warning"))
	override, allows = "default", false
	s.Incident(ev("opened", "disk_full", "critical"))
	muted := ev("opened", "high_ram", "warning")
	until := now.Add(time.Hour).UnixMilli()
	muted.Incident.MutedUntil, muted.Incident.Status = until, "muted"
	allows = true
	s.Incident(muted)
	if n := queued(); n != 0 {
		t.Fatalf("critical-only / app off / muted: %d", n)
	}
	now = now.Add(11 * time.Minute)
	s.Incident(ev("opened", "app_offline", "critical"))
	if n := queued(); n != 1 {
		t.Fatalf("after cooldown: %d", n)
	}
}

func TestWebhook(t *testing.T) {
	type req struct {
		event, sig, ua string
		body           []byte
	}
	got := make(chan req, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- req{r.Header.Get("X-Fleetwatch-Event"), r.Header.Get("X-Fleetwatch-Signature"), r.Header.Get("User-Agent"), b}
	}))
	defer srv.Close()

	m := Message{Title: "API is offline", Body: "App Offline · CRITICAL · API on VPS-1", Severity: "critical", Link: "https://ops.example/#/incidents",
		Details: Details{Event: "incident.opened", Type: "app_offline", TypeLabel: "App Offline", IncidentID: "inc_1", ServerID: "srv_1", ServerName: "VPS-1", AppID: "app_1", AppName: "API"}}
	if err := (Webhook{URL: srv.URL + "/hook?token=t0k", Secret: "s3cret"}).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	r := <-got
	var p WebhookPayload
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != "incident.opened" || p.Type != "app_offline" || p.AppName != "API" || p.ServerName != "VPS-1" || p.Severity != "critical" || p.Title != "API is offline" || p.TS == 0 || r.event != "incident.opened" || r.ua != "Fleetwatch" {
		t.Fatalf("payload: %+v headers %+v", p, r)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(r.body)
	if r.sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature %q does not verify", r.sig)
	}
	if err := (Webhook{URL: srv.URL}).Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.sig != "" {
		t.Fatal("unsigned webhook sent a signature")
	}
	for _, bad := range []string{"", "ftp://x/y", "not a url", "https://"} {
		if (Webhook{URL: bad}).validate() == nil {
			t.Errorf("accepted %q", bad)
		}
	}

	// Through the service: secrets stay hidden, a hint shows which endpoint it is.
	s := setup(t)
	if err := s.SaveChannel("webhook", true, map[string]string{"url": srv.URL + "/hook?token=t0k", "secret": "s3cret"}); err != nil {
		t.Fatal(err)
	}
	views, _ := s.Channels()
	raw, _ := json.Marshal(views)
	if strings.Contains(string(raw), "t0k") || strings.Contains(string(raw), "s3cret") || !strings.Contains(string(raw), `"urlHint"`) {
		t.Fatalf("channel view leaks or lacks the hint: %s", raw)
	}
	h, err := s.Test(context.Background(), "webhook")
	if err != nil || h.Result != "delivered" {
		t.Fatalf("test send: %+v %v", h, err)
	}
	if r := <-got; r.event != "test" || r.sig == "" {
		t.Fatalf("test delivery: %+v", r)
	}
}

// A member's own channels get what their rules allow, and keep their secrets.
func TestPersonalChannels(t *testing.T) {
	s := setup(t)
	srv, ch := recorder(t, 200)
	var emailed []string
	s.Email = func(_ context.Context, to, subject, _ string) error {
		emailed = append(emailed, to+"|"+subject)
		return nil
	}
	if err := s.SavePersonal("mem_1", "webhook", true, map[string]string{"url": srv.URL + "/sara/s3cret"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePersonal("mem_1", "email", true, map[string]string{"to": "sara@example.com"}); err != nil {
		t.Fatal(err)
	}
	if s.SavePersonal("mem_1", "email", true, map[string]string{"to": "not an address"}) == nil || s.SavePersonal("mem_1", "ntfy", true, nil) == nil {
		t.Fatal("bad settings accepted")
	}
	raw, _ := json.Marshal(s.PersonalChannels("mem_1"))
	if strings.Contains(string(raw), "s3cret") || !strings.Contains(string(raw), "sara@example.com") {
		t.Fatalf("view: %s", raw)
	}
	m := Message{Title: "API down", Body: "on web-1", Severity: "critical"}
	s.Personal(m, func(p string) bool { return false })
	s.Personal(m, func(p string) bool { return p == "mem_1" })
	select {
	case g := <-ch:
		if g.path != "/sara/s3cret" {
			t.Fatal(g.path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("not sent to the webhook")
	}
	time.Sleep(100 * time.Millisecond)
	if len(emailed) != 1 || emailed[0] != "sara@example.com|API down" {
		t.Fatal("email", emailed)
	}
	select {
	case g := <-ch:
		t.Fatal("sent when not allowed", g.path)
	default:
	}
}
