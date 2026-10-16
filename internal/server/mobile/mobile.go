// Package mobile connects the Fleetwatch Notifications phone app. A person makes a
// short pairing code in the dashboard, the app trades it for its own device token, and
// then keeps a connection open: every alert meant for that person is queued per phone
// and sent as soon as the phone is connected (so nothing is lost while it is offline).
package mobile

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/valid"
)

const (
	TokenPrefix  = "fwm_"
	codeTTL      = 10 * time.Minute
	keepMessages = 7 * 24 * time.Hour
	maxDevices   = 20 // per person
)

var (
	ErrNoDevice = errors.New("phone not found")
	ErrBadCode  = errors.New("that pairing code is wrong or has expired; make a new one in the dashboard")
)

// Message is one alert as the app shows it.
type Message struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"` // incident | resolved | oncall | notice | test
	Title      string `json:"title"`
	Body       string `json:"body"`
	Severity   string `json:"severity"` // info | warning | critical | ok
	IncidentID string `json:"incidentId,omitempty"`
	ServerID   string `json:"serverId,omitempty"`
	URL        string `json:"url,omitempty"`
	TS         int64  `json:"ts"`
}

// Device is one paired phone.
type Device struct {
	ID         string `json:"id"`
	MemberID   string `json:"-"` // "" = the owner
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"appVersion"`
	Alerts     string `json:"alerts"` // all | oncall
	CreatedAt  int64  `json:"createdAt"`
	LastSeenAt int64  `json:"lastSeenAt"`
	Connected  bool   `json:"connected"`
}

type Service struct {
	db *sql.DB
	// Scope says which servers a person may see (nil = all) and whether they still
	// exist; "" is the owner.
	Scope func(memberID string) (servers []string, ok bool)
	Now   func() time.Time

	mu    sync.Mutex
	conns map[string]map[chan struct{}]bool // device → open connections
}

func New(d *sql.DB) *Service {
	return &Service{db: d, Now: time.Now, conns: map[string]map[chan struct{}]bool{},
		Scope: func(string) ([]string, bool) { return nil, true }}
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// codeAlphabet has no look-alike characters (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func normalizeCode(c string) string {
	c = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(c))
	return c
}

// NewPairingCode makes a single-use code (valid 10 minutes) for one person.
func (s *Service) NewPairingCode(memberID string) (string, int64, error) {
	b := make([]byte, 8)
	rand.Read(b)
	code := make([]byte, 8)
	for i, x := range b {
		code[i] = codeAlphabet[int(x)%len(codeAlphabet)]
	}
	exp := s.Now().Add(codeTTL).UnixMilli()
	s.db.Exec(`DELETE FROM mobile_pairing WHERE expires_at < ?`, s.Now().UnixMilli())
	if _, err := s.db.Exec(`INSERT INTO mobile_pairing (code_hash, member_id, expires_at) VALUES (?, ?, ?)`, hash(string(code)), memberID, exp); err != nil {
		return "", 0, err
	}
	return string(code[:4]) + "-" + string(code[4:]), exp, nil
}

// Pair trades a pairing code for a device token (shown once to the app).
func (s *Service) Pair(code, name, platform, appVersion string) (string, Device, error) {
	h := hash(normalizeCode(code))
	var memberID string
	var exp int64
	if err := s.db.QueryRow(`SELECT member_id, expires_at FROM mobile_pairing WHERE code_hash = ?`, h).Scan(&memberID, &exp); err != nil || exp < s.Now().UnixMilli() {
		return "", Device{}, ErrBadCode
	}
	s.db.Exec(`DELETE FROM mobile_pairing WHERE code_hash = ?`, h) // single use
	if _, ok := s.Scope(memberID); !ok {
		return "", Device{}, ErrBadCode
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM mobile_devices WHERE member_id = ?`, memberID).Scan(&n)
	if n >= maxDevices {
		return "", Device{}, valid.Error("too many phones for this account; remove one in the dashboard first")
	}
	name, platform, appVersion = clip(strings.TrimSpace(name), 60), clip(platform, 20), clip(appVersion, 20)
	if name == "" {
		name = "Phone"
	}
	token := TokenPrefix + random(24)
	d := Device{ID: "mob_" + random(6), MemberID: memberID, Name: name, Platform: platform, AppVersion: appVersion, Alerts: "all", CreatedAt: s.Now().UnixMilli()}
	_, err := s.db.Exec(`INSERT INTO mobile_devices (id, member_id, name, platform, app_version, token_hash, alerts, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.MemberID, d.Name, d.Platform, d.AppVersion, hash(token), d.Alerts, d.CreatedAt)
	return token, d, err
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

const deviceCols = `id, member_id, name, platform, app_version, alerts, created_at, last_seen_at`

func (s *Service) scan(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.MemberID, &d.Name, &d.Platform, &d.AppVersion, &d.Alerts, &d.CreatedAt, &d.LastSeenAt)
	if err == nil {
		s.mu.Lock()
		d.Connected = len(s.conns[d.ID]) > 0
		s.mu.Unlock()
	}
	return d, err
}

// Authenticate finds the phone behind a device token.
func (s *Service) Authenticate(token string) (Device, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return Device{}, ErrNoDevice
	}
	d, err := s.scan(s.db.QueryRow(`SELECT `+deviceCols+` FROM mobile_devices WHERE token_hash = ?`, hash(token)))
	if err != nil {
		return Device{}, ErrNoDevice
	}
	if _, ok := s.Scope(d.MemberID); !ok { // the member was removed or disabled
		return Device{}, ErrNoDevice
	}
	return d, nil
}

// Devices lists one person's phones.
func (s *Service) Devices(memberID string) ([]Device, error) {
	rows, err := s.db.Query(`SELECT `+deviceCols+` FROM mobile_devices WHERE member_id = ? ORDER BY created_at`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Update changes a phone's name or which alerts it gets.
func (s *Service) Update(memberID, id, name, alerts string) (Device, error) {
	if alerts != "" && alerts != "all" && alerts != "oncall" {
		return Device{}, valid.Error("alerts must be all or oncall")
	}
	res, err := s.db.Exec(`UPDATE mobile_devices SET name = COALESCE(NULLIF(?, ''), name), alerts = COALESCE(NULLIF(?, ''), alerts) WHERE id = ? AND member_id = ?`,
		clip(strings.TrimSpace(name), 60), alerts, id, memberID)
	if err != nil {
		return Device{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Device{}, ErrNoDevice
	}
	return s.scan(s.db.QueryRow(`SELECT `+deviceCols+` FROM mobile_devices WHERE id = ?`, id))
}

// Remove unpairs a phone; its open connection closes.
func (s *Service) Remove(memberID, id string) error {
	res, err := s.db.Exec(`DELETE FROM mobile_devices WHERE id = ? AND member_id = ?`, id, memberID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoDevice
	}
	s.db.Exec(`DELETE FROM mobile_messages WHERE device_id = ?`, id)
	s.wake(id)
	return nil
}

// RemoveMember unpairs every phone of a removed team member.
func (s *Service) RemoveMember(memberID string) {
	list, _ := s.Devices(memberID)
	for _, d := range list {
		s.Remove(memberID, d.ID)
	}
}

func (s *Service) Seen(id, appVersion string) {
	s.db.Exec(`UPDATE mobile_devices SET last_seen_at = ?, app_version = COALESCE(NULLIF(?, ''), app_version) WHERE id = ?`, s.Now().UnixMilli(), clip(appVersion, 20), id)
}

// ---------- delivery ----------

func (s *Service) queue(deviceID string, m Message) {
	m.TS = s.Now().UnixMilli()
	raw, _ := json.Marshal(m)
	if _, err := s.db.Exec(`INSERT INTO mobile_messages (device_id, created_at, payload) VALUES (?, ?, ?)`, deviceID, m.TS, string(raw)); err == nil {
		s.wake(deviceID)
	}
}

func (s *Service) wake(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.conns[deviceID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type target struct {
	id, member, alerts string
}

func (s *Service) targets(where string, args ...any) []target {
	rows, err := s.db.Query(`SELECT id, member_id, alerts FROM mobile_devices`+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []target
	for rows.Next() {
		var t target
		if rows.Scan(&t.id, &t.member, &t.alerts) == nil {
			out = append(out, t)
		}
	}
	return out
}

// Alert sends a dashboard alert to every phone set to "all" whose person can see the
// server ("" = a notice for the owner only).
func (s *Service) Alert(m Message) {
	scopes := map[string][]string{}
	for _, t := range s.targets(` WHERE alerts = 'all'`) {
		if m.ServerID == "" && t.member != "" {
			continue
		}
		servers, seen := scopes[t.member]
		if !seen {
			var ok bool
			if servers, ok = s.Scope(t.member); !ok {
				continue
			}
			scopes[t.member] = servers
		}
		if servers != nil && !slices.Contains(servers, m.ServerID) {
			continue
		}
		s.queue(t.id, m)
	}
}

// ToPerson sends to all of one person's phones, whatever their alert setting
// (on-call pages, tests).
func (s *Service) ToPerson(memberID string, m Message) int {
	list := s.targets(` WHERE member_id = ?`, memberID)
	for _, t := range list {
		s.queue(t.id, m)
	}
	return len(list)
}

// ToDevice sends to one phone.
func (s *Service) ToDevice(deviceID string, m Message) { s.queue(deviceID, m) }

// Pending returns the phone's messages after id (at most 200, oldest first).
func (s *Service) Pending(deviceID string, after int64) []Message {
	rows, err := s.db.Query(`SELECT id, payload FROM mobile_messages WHERE device_id = ? AND id > ? ORDER BY id LIMIT 200`, deviceID, after)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var id int64
		var raw string
		if rows.Scan(&id, &raw) != nil {
			continue
		}
		var m Message
		if json.Unmarshal([]byte(raw), &m) == nil {
			m.ID = id
			out = append(out, m)
		}
	}
	return out
}

// Listen registers an open connection; the channel fires when new messages wait.
func (s *Service) Listen(deviceID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.conns[deviceID] == nil {
		s.conns[deviceID] = map[chan struct{}]bool{}
	}
	s.conns[deviceID][ch] = true
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.conns[deviceID], ch)
		s.mu.Unlock()
	}
}

// Exists tells whether a phone is still paired (checked by open connections).
func (s *Service) Exists(deviceID string) bool {
	var one int
	return s.db.QueryRow(`SELECT 1 FROM mobile_devices WHERE id = ?`, deviceID).Scan(&one) == nil
}

// Prune forgets messages older than a week.
func (s *Service) Prune() {
	s.db.Exec(`DELETE FROM mobile_messages WHERE created_at < ?`, s.Now().Add(-keepMessages).UnixMilli())
	s.db.Exec(`DELETE FROM mobile_pairing WHERE expires_at < ?`, s.Now().UnixMilli())
}
