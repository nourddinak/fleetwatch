// Package whatsapp links the owner's WhatsApp account (the way WhatsApp Web is linked)
// and reaches the team from it: on-call pages as messages for now.
package whatsapp

import (
	"context"
	"database/sql"
	"encoding/base64"
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

	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"fleetwatch/internal/server/live"
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
	Phone     string `json:"phone"`               // digits, with the country code
	Language  string `json:"language"`            // "" = whatever the conversation is in
	QuietFrom string `json:"quietFrom,omitempty"` // "22:00": no WhatsApp from then…
	QuietTo   string `json:"quietTo,omitempty"`   // …until then, in TimeZone
	TimeZone  string `json:"timeZone,omitempty"`
}

// Languages people may pick ("" follows the conversation).
var Languages = []string{"", "en", "fr", "ar", "ary", "es", "de", "it", "pt", "tr", "nl"}

type Service struct {
	db   *sql.DB
	Bus  *live.Bus
	Now  func() time.Time
	path string

	mu        sync.Mutex
	container *sqlstore.Container
	client    *whatsmeow.Client
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
	s.client = cl
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
	s.client, s.stopLink, s.qr, s.code, s.lastErr = cl, cancel, "", "", ""
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
			s.client = nil
		}
		s.lastErr = "WhatsApp was unlinked from the phone"
		s.mu.Unlock()
	case *events.Connected, *events.Disconnected:
	default:
		return
	}
	s.publish()
}

// Unlink signs the dashboard out of WhatsApp.
func (s *Service) Unlink(ctx context.Context) error {
	s.mu.Lock()
	cl := s.client
	s.client, s.qr, s.code, s.lastErr = nil, "", "", ""
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
	if p.TimeZone != "" {
		if _, err := time.LoadLocation(p.TimeZone); err != nil {
			return p, valid.Error("unknown time zone")
		}
	}
	raw, _ := json.Marshal(p)
	_, err = s.db.Exec(`INSERT INTO whatsapp_people (person, data) VALUES (?, ?) ON CONFLICT (person) DO UPDATE SET data = excluded.data`, id, string(raw))
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
