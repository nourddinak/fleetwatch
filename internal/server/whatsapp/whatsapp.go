// Package whatsapp links the owner's WhatsApp account (the way WhatsApp Web is linked)
// and reaches the team from it: on-call pages as messages for now.
package whatsapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

var (
	ErrNotLinked = valid.Error("WhatsApp is not linked: the owner links it in Integrations → WhatsApp")
	ErrSelf      = valid.Error("this is the number the dashboard sends from: WhatsApp cannot message or call itself")
)

// Status is how the link stands, for the dashboard.
type Status struct {
	Linked    bool   `json:"linked"`
	Connected bool   `json:"connected"`
	Number    string `json:"number,omitempty"` // the linked account
	Name      string `json:"name,omitempty"`
	QR        string `json:"qr,omitempty"`   // while linking: a data: URL of the QR code to scan
	Code      string `json:"code,omitempty"` // while linking by phone number: the code to type in WhatsApp
	Error     string `json:"error,omitempty"`
}

// Person is how one dashboard user is reached on WhatsApp.
type Person struct {
	Phone       string `json:"phone"`               // digits, with the country code
	Language    string `json:"language"`            // "" = whatever the conversation is in
	QuietFrom   string `json:"quietFrom,omitempty"` // "22:00": no WhatsApp from then…
	QuietTo     string `json:"quietTo,omitempty"`   // …until then, in TimeZone
	TimeZone    string `json:"timeZone,omitempty"`
	Briefing    string `json:"briefing,omitempty"`    // "08:00": a morning summary then, in TimeZone; "" = none
	Weekly      bool   `json:"weekly,omitempty"`      // a summary of the week on Mondays, at Briefing (or 09:00)
	WeeklyVoice bool   `json:"weeklyVoice,omitempty"` // …as a voice note in the AI's call voice
	Replies     string `json:"replies,omitempty"`     // "" text | "voice": a voice note to a voice note | "always": voice notes
}

// Languages people may pick ("" follows the conversation).
var Languages = []string{"", "en", "fr", "ar", "ary", "es", "de", "it", "pt", "tr", "nl"}

// LanguageNames names them, for the AI.
var LanguageNames = map[string]string{"en": "English", "fr": "French", "ar": "Arabic", "ary": "Moroccan Arabic (Darija)",
	"es": "Spanish", "de": "German", "it": "Italian", "pt": "Portuguese", "tr": "Turkish", "nl": "Dutch"}

type Service struct {
	db   *sql.DB
	Bus  *live.Bus
	Now  func() time.Time
	path string
	// Talker answers calls (person, incident ("" for a test call)); see TalkerFor.
	Talker func(person, incident string) Talker
	// Inbound answers a call a known person makes to the linked number; nil, or a nil
	// Talker, declines it. Calls from numbers nobody saved are always declined.
	Inbound func(person string) Talker
	// OnMessage answers a team member's message (text, or a voice note's audio and
	// type); "" sends nothing. Messages from numbers nobody saved never reach it.
	OnMessage func(ctx context.Context, person, text string, audio []byte, mime string) string
	// DataDir and HelperPath let it ask the root helper for ffmpeg (see ffmpeg.go).
	DataDir, HelperPath string
	// Speak turns a reply into speech (16-bit mono PCM at rate), for voice-note replies.
	Speak func(ctx context.Context, text string) (pcm []byte, rate int, err error)
	// Alert tells the owner something went wrong with the link (the watchdog).
	Alert func(title, body, severity string)

	lostAt  time.Time // when the connection dropped (zero: connected)
	alerted bool      // the owner was told it is down

	mu        sync.Mutex
	container *sqlstore.Container
	client    *whatsmeow.Client
	caller    *meowcaller.Client // calls through client
	qr, code  string
	lastErr   string
	stopLink  context.CancelFunc
}

func New(d *sql.DB, dataDir string, bus *live.Bus) *Service {
	return &Service{db: d, Bus: bus, Now: time.Now, path: filepath.Join(dataDir, "whatsapp.db")}
}

// store opens WhatsApp's own keys, kept in a file of their own that only this user reads
// (lock held).
func (s *Service) store(ctx context.Context) (*sqlstore.Container, error) {
	if s.container != nil {
		return s.container, nil
	}
	db, err := sql.Open("sqlite", "file:"+s.path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	c := sqlstore.NewWithDB(db, "sqlite", nil)
	if err := c.Upgrade(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("WhatsApp storage: %w", err)
	}
	os.Chmod(s.path, 0o600)
	s.container = c
	return c, nil
}

// Start reconnects the account linked before, if any.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.store(ctx)
	if err != nil {
		return err
	}
	dev, err := c.GetFirstDevice(ctx)
	if err != nil || dev.ID == nil {
		return err
	}
	cl := whatsmeow.NewClient(dev, nil)
	cl.AddEventHandler(s.onEvent)
	s.client, s.caller = cl, s.newCaller(cl) // before connecting, as meowcaller needs
	return cl.Connect()
}

// publish tells browsers the link changed. Every signed-in browser gets it, so it never
// carries the QR code or the pairing code (whoever scans those links their account).
func (s *Service) publish() {
	if s.Bus != nil {
		st := s.Status()
		s.Bus.Publish("whatsapp", map[string]bool{"linked": st.Linked, "connected": st.Connected, "linking": st.QR != "" || st.Code != ""})
	}
}

// Status reports the link.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{QR: s.qr, Code: s.code, Error: s.lastErr}
	if cl := s.client; cl != nil && cl.Store.ID != nil {
		st.Linked, st.Connected = true, cl.IsConnected() && cl.IsLoggedIn()
		st.Number, st.Name = cl.Store.ID.User, cl.Store.PushName
	}
	return st
}

// Linked reports whether messages can be sent.
func (s *Service) Linked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client != nil && s.client.Store.ID != nil
}

var nonDigits = regexp.MustCompile(`[^0-9]`)

// CleanPhone keeps the digits of a phone number written with its country code.
func CleanPhone(phone string) (string, error) {
	phone = strings.TrimSpace(phone)
	if strings.HasPrefix(phone, "00") {
		phone = phone[2:]
	}
	d := nonDigits.ReplaceAllString(phone, "")
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return "", valid.Error("write the number with its country code, like +212 6 12 34 56 78")
	}
	return d, nil
}

// Link starts linking an account: with phone "", by a QR code to scan; otherwise by a
// code to type in WhatsApp on that phone (Linked devices → Link with phone number).
func (s *Service) Link(phone string) error {
	digits := ""
	if phone != "" {
		var err error
		if digits, err = CleanPhone(phone); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if s.client != nil && s.client.Store.ID != nil {
		s.mu.Unlock()
		return valid.Error("a WhatsApp account is already linked: unlink it first")
	}
	if s.stopLink != nil {
		s.stopLink()
	}
	if s.client != nil {
		s.client.Disconnect()
	}
	c, err := s.store(context.Background())
	if err != nil {
		s.mu.Unlock()
		return err
	}
	cl := whatsmeow.NewClient(c.NewDevice(), nil)
	cl.AddEventHandler(s.onEvent)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	s.client, s.caller, s.stopLink, s.qr, s.code, s.lastErr = cl, s.newCaller(cl), cancel, "", "", ""
	s.mu.Unlock()

	if digits == "" {
		ch, err := cl.GetQRChannel(ctx)
		if err != nil {
			cancel()
			return err
		}
		go s.watchQR(ch)
	}
	if err := cl.Connect(); err != nil {
		cancel()
		return fmt.Errorf("cannot reach WhatsApp: %w", err)
	}
	if digits != "" {
		code, err := cl.PairPhone(ctx, digits, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
		if err != nil {
			cancel()
			cl.Disconnect()
			return valid.Error("WhatsApp did not give a code: " + err.Error())
		}
		s.mu.Lock()
		s.code = code
		s.mu.Unlock()
		go func() { // the code is good for a few minutes
			<-ctx.Done()
			s.mu.Lock()
			if s.code != "" {
				s.code, s.lastErr = "", "the code ran out: start again"
				if s.client == cl && cl.Store.ID == nil {
					cl.Disconnect()
				}
			}
			s.mu.Unlock()
			s.publish()
		}()
	}
	s.publish()
	return nil
}

func (s *Service) watchQR(ch <-chan whatsmeow.QRChannelItem) {
	for item := range ch {
		s.mu.Lock()
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			if png, err := qrcode.Encode(item.Code, qrcode.Medium, 280); err == nil {
				s.qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
			}
		case whatsmeow.QRChannelSuccess.Event:
			s.qr = ""
		default: // timeout or an error
			s.qr, s.lastErr = "", "linking stopped before the code was scanned: start again"
		}
		s.mu.Unlock()
		s.publish()
	}
}

func (s *Service) onEvent(evt any) {
	if m, ok := evt.(*events.Message); ok {
		s.mu.Lock()
		cl := s.client
		s.mu.Unlock()
		go s.onMessage(cl, m)
		return
	}
	switch evt.(type) {
	case *events.PairSuccess:
		s.mu.Lock()
		s.qr, s.code, s.lastErr = "", "", ""
		if s.stopLink != nil {
			s.stopLink()
			s.stopLink = nil
		}
		s.mu.Unlock()
	case *events.LoggedOut: // unlinked from the phone
		s.mu.Lock()
		if s.client != nil {
			s.client.Disconnect()
			s.client, s.caller = nil, nil
		}
		s.lastErr = "WhatsApp was unlinked from the phone"
		s.mu.Unlock()
		s.alert("WhatsApp was unlinked", "The dashboard's WhatsApp was signed out (from the phone, or by WhatsApp). On-call calls and messages stop until it is linked again in Integrations → WhatsApp.", "critical")
	case *events.Connected:
		s.mu.Lock()
		back := s.alerted
		s.lostAt, s.alerted = time.Time{}, false
		s.mu.Unlock()
		if back {
			s.alert("WhatsApp is back", "The dashboard's WhatsApp is connected again: on-call calls and messages work.", "info")
		}
	case *events.Disconnected:
		s.mu.Lock()
		if s.lostAt.IsZero() {
			s.lostAt = s.Now()
		}
		s.mu.Unlock()
	default:
		return
	}
	s.publish()
}

// Unlink signs the dashboard out of WhatsApp.
func (s *Service) Unlink(ctx context.Context) error {
	s.mu.Lock()
	cl := s.client
	s.client, s.caller, s.qr, s.code, s.lastErr = nil, nil, "", "", ""
	if s.stopLink != nil {
		s.stopLink()
		s.stopLink = nil
	}
	s.mu.Unlock()
	if cl != nil {
		if cl.Store.ID != nil {
			if err := cl.Logout(ctx); err != nil { // offline: forget it here anyway
				cl.Store.Delete(ctx)
			}
		}
		cl.Disconnect()
	}
	s.publish()
	return nil
}

// Send writes a message to a phone number.
func (s *Service) Send(ctx context.Context, phone, text string) error {
	digits, err := CleanPhone(phone)
	if err != nil {
		return err
	}
	s.mu.Lock()
	cl := s.client
	s.mu.Unlock()
	if cl == nil || cl.Store.ID == nil {
		return ErrNotLinked
	}
	if digits == cl.Store.ID.User {
		return ErrSelf
	}
	if !cl.IsConnected() {
		return errors.New("WhatsApp is not connected right now")
	}
	if on, err := cl.IsOnWhatsApp(ctx, []string{"+" + digits}); err == nil && len(on) > 0 && !on[0].IsIn {
		return valid.Error("+" + digits + " is not on WhatsApp")
	}
	_, err = cl.SendMessage(ctx, types.NewJID(digits, types.DefaultUserServer), &waE2E.Message{Conversation: proto.String(text)})
	return err
}

// ---------- people ----------

// PersonOf is how someone is reached ("owner" or a member ID).
func (s *Service) PersonOf(id string) (Person, bool) {
	var raw string
	var p Person
	if s.db.QueryRow(`SELECT data FROM whatsapp_people WHERE person = ?`, id).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &p) != nil {
		return p, false
	}
	return p, p.Phone != ""
}

// People is everyone's settings, by person.
func (s *Service) People() map[string]Person {
	out := map[string]Person{}
	rows, err := s.db.Query(`SELECT person, data FROM whatsapp_people`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		var p Person
		if rows.Scan(&id, &raw) == nil && json.Unmarshal([]byte(raw), &p) == nil {
			out[id] = p
		}
	}
	return out
}

var clock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// SetPerson checks and keeps someone's settings; an empty phone removes them.
func (s *Service) SetPerson(id string, p Person) (Person, error) {
	if strings.TrimSpace(p.Phone) == "" {
		_, err := s.db.Exec(`DELETE FROM whatsapp_people WHERE person = ?`, id)
		return Person{}, err
	}
	var err error
	if p.Phone, err = CleanPhone(p.Phone); err != nil {
		return p, err
	}
	if !slices.Contains(Languages, p.Language) {
		return p, valid.Error("unknown language")
	}
	if (p.QuietFrom == "") != (p.QuietTo == "") || p.QuietFrom != "" && (!clock.MatchString(p.QuietFrom) || !clock.MatchString(p.QuietTo)) {
		return p, valid.Error("quiet hours need a start and an end, like 22:00 and 07:00")
	}
	if p.Replies != "" && p.Replies != "voice" && p.Replies != "always" {
		return p, valid.Error("replies are text, voice or always")
	}
	if p.Briefing != "" && !clock.MatchString(p.Briefing) {
		return p, valid.Error("the briefing time is like 08:00")
	}
	if p.TimeZone != "" {
		if _, err := time.LoadLocation(p.TimeZone); err != nil {
			return p, valid.Error("unknown time zone")
		}
	}
	raw, _ := json.Marshal(p)
	_, err = s.db.Exec(`INSERT INTO whatsapp_people (person, data) VALUES (?, ?) ON CONFLICT (person) DO UPDATE SET data = excluded.data`, id, string(raw))
	if err == nil && (p.Replies != "" || p.WeeklyVoice) {
		s.EnsureVoice() // voice notes need ffmpeg: the helper installs it
	}
	return p, err
}

// Quiet reports whether t falls in the person's quiet hours.
func (p Person) Quiet(t time.Time) bool {
	if p.QuietFrom == "" {
		return false
	}
	if loc, err := time.LoadLocation(p.TimeZone); err == nil {
		t = t.In(loc)
	}
	now := t.Format("15:04")
	if p.QuietFrom <= p.QuietTo {
		return now >= p.QuietFrom && now < p.QuietTo
	}
	return now >= p.QuietFrom || now < p.QuietTo // over midnight
}

// watchdogAfter is how long the link may be down before the owner is told.
const watchdogAfter = 3 * time.Minute

func (s *Service) alert(title, body, severity string) {
	if s.Alert != nil {
		go s.Alert(title, body, severity)
	}
}

// Watch tells the owner when the link has been down for a while (whatsmeow keeps
// reconnecting by itself); call it every minute or so.
func (s *Service) Watch() {
	s.mu.Lock()
	down := !s.lostAt.IsZero() && s.Now().Sub(s.lostAt) > watchdogAfter && !s.alerted && s.client != nil
	if down {
		s.alerted = true
	}
	s.mu.Unlock()
	if down {
		s.alert("WhatsApp is disconnected", "The dashboard has not reached WhatsApp for "+watchdogAfter.String()+" (the server's network, or WhatsApp itself). On-call calls and messages wait until it is back; push and email still go out.", "warning")
	}
}

// Options are the owner's choices for what follows a page.
type Options struct {
	ResolvedMessages bool `json:"resolvedMessages"` // "resolved" messages to whoever was paged
	FollowUpMinutes  int  `json:"followUpMinutes"`  // call back the acknowledger if still open; 0 = never
	RequirePIN       bool `json:"requirePin"`       // actions by voice or chat need the person's PIN
}

// Options reads them (resolved messages on, follow-up after 20 minutes by default).
func (s *Service) Options() Options {
	o := Options{ResolvedMessages: true, FollowUpMinutes: 20}
	settings.Load(s.db, "whatsapp.options", &o)
	return o
}

// SetOptions saves them.
func (s *Service) SetOptions(o Options) (Options, error) {
	if o.FollowUpMinutes < 0 || o.FollowUpMinutes > 24*60 {
		return o, valid.Error("the follow-up must be between 0 (never) and 1440 minutes")
	}
	return o, settings.Save(s.db, "whatsapp.options", o)
}

var pinRe = regexp.MustCompile(`^[0-9]{4}$`)

type pinHash struct {
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}

func hashPIN(salt, pin string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + pin))
	return hex.EncodeToString(sum[:])
}

// SetPIN sets someone's action PIN (4 to 8 digits); "" removes it.
func (s *Service) SetPIN(person, pin string) error {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, "whatsapp.pin."+person)
		return err
	}
	if !pinRe.MatchString(pin) {
		return valid.Error("a PIN is 4 digits")
	}
	if strings.Count(pin, pin[:1]) == len(pin) {
		return valid.Error("a PIN can't be the same digit four times, like 1111")
	}
	if run(pin) {
		return valid.Error("a PIN can't be a run of digits, like 1234 or 4321")
	}
	b := make([]byte, 16)
	rand.Read(b)
	salt := hex.EncodeToString(b)
	return settings.Save(s.db, "whatsapp.pin."+person, pinHash{salt, hashPIN(salt, pin)})
}

// HasPIN reports whether someone set a PIN.
func (s *Service) HasPIN(person string) bool {
	var h pinHash
	return settings.Load(s.db, "whatsapp.pin."+person, &h) == nil && h.Hash != ""
}

// CheckPIN reports whether a PIN is someone's (spoken digits may come with spaces).
// run reports digits that go up or down one at a time (1234, 4321).
func run(pin string) bool {
	step := int(pin[1]) - int(pin[0])
	if step != 1 && step != -1 {
		return false
	}
	for i := 2; i < len(pin); i++ {
		if int(pin[i])-int(pin[i-1]) != step {
			return false
		}
	}
	return true
}

const (
	pinTries = 3
	pinLock  = 15 * time.Minute
)

type pinFails struct {
	Count int   `json:"count"`
	Until int64 `json:"until"` // locked until then
}

// TryPIN checks someone's PIN: three wrong in a row lock their actions for 15 minutes,
// on every call and chat.
func (s *Service) TryPIN(person, pin string) error {
	var f pinFails
	settings.Load(s.db, "whatsapp.pinfails."+person, &f)
	if left := time.Until(time.UnixMilli(f.Until)); left > 0 {
		return valid.Error(fmt.Sprintf("too many wrong PINs: actions are locked for %d more minutes", int(left.Minutes())+1))
	}
	if s.CheckPIN(person, pin) {
		settings.Save(s.db, "whatsapp.pinfails."+person, pinFails{})
		return nil
	}
	f.Count++
	if f.Count >= pinTries {
		f = pinFails{Until: s.Now().Add(pinLock).UnixMilli()}
		settings.Save(s.db, "whatsapp.pinfails."+person, f)
		return valid.Error("that PIN is wrong, three times: actions are locked for 15 minutes")
	}
	settings.Save(s.db, "whatsapp.pinfails."+person, f)
	return valid.Error(fmt.Sprintf("that PIN is wrong (%d tries left)", pinTries-f.Count))
}

func (s *Service) CheckPIN(person, pin string) bool {
	var h pinHash
	if settings.Load(s.db, "whatsapp.pin."+person, &h) != nil || h.Hash == "" {
		return false
	}
	pin = nonDigits.ReplaceAllString(pin, "")
	return subtle.ConstantTimeCompare([]byte(hashPIN(h.Salt, pin)), []byte(h.Hash)) == 1
}

// BriefingDue reports whether someone's morning briefing is due at now, given the day
// it was last sent ("2006-01-02", their time); it answers today's day to remember.
func (p Person) BriefingDue(now time.Time, lastSent string) (string, bool) {
	if p.Briefing == "" {
		return "", false
	}
	if loc, err := time.LoadLocation(p.TimeZone); err == nil {
		now = now.In(loc)
	}
	day := now.Format("2006-01-02")
	return day, day != lastSent && now.Format("15:04") >= p.Briefing && now.Format("15:04") < addMinutes(p.Briefing, 30)
}

// addMinutes moves a "15:04" clock forward (it stops at 23:59: no briefing wraps to tomorrow).
func addMinutes(clock string, m int) string {
	t, err := time.Parse("15:04", clock)
	if err != nil {
		return clock
	}
	if end := t.Add(time.Duration(m) * time.Minute); end.Day() == t.Day() {
		return end.Format("15:04")
	}
	return "23:59"
}

// WeeklyDue is BriefingDue for the Monday summary of the week.
func (p Person) WeeklyDue(now time.Time, lastSent string) (string, bool) {
	if !p.Weekly {
		return "", false
	}
	if p.Briefing == "" {
		p.Briefing = "09:00"
	}
	if loc, err := time.LoadLocation(p.TimeZone); err == nil {
		now = now.In(loc)
	}
	if now.Weekday() != time.Monday {
		return "", false
	}
	return p.BriefingDue(now, lastSent)
}
