package email

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/notify"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

var ErrNotFound = errors.New("not found")

// MaxMessage is Cloudflare Email Routing's limit for a whole message (MIME included).
const MaxMessage = 25 << 20

// Sender sends a finished MIME message. statuspage.Mailer is the real one.
type Sender interface {
	SendRaw(ctx context.Context, senderID, from string, rcpts []string, raw []byte) error
}

// Options are the page's settings (email.options).
type Options struct {
	UndoSeconds int `json:"undoSeconds"` // how long a sent message waits, so it can be undone
	TrashDays   int `json:"trashDays"`   // 0 = keep forever
	SpamDays    int `json:"spamDays"`
}

type Service struct {
	db  *sql.DB
	box *secret.Box
	dir string // OPS_DATA_DIR/mail
	Bus *live.Bus
	Now func() time.Time

	Mail   Sender
	Notify *notify.Service
	// SenderFor picks the Integrations → Email sender for a domain ("" = default).
	SenderFor func(domain, chosen string) string
	PublicURL string
	// Cloudflare API base; tests point it at a fake.
	CloudflareAPI string
	// Resolver answers the DNS checks; tests replace it.
	Resolver Resolver
	DKIM     DKIMCheck

	mu       sync.Mutex
	wake     chan struct{}
	edgeSoon chan struct{}
	edgeErr  string
}

func New(d *sql.DB, box *secret.Box, bus *live.Bus, dataDir string) (*Service, error) {
	dir := filepath.Join(dataDir, "mail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Service{db: d, box: box, dir: dir, Bus: bus, Now: time.Now, CloudflareAPI: "https://api.cloudflare.com/client/v4",
		Resolver: netResolver{}, wake: make(chan struct{}, 1), edgeSoon: make(chan struct{}, 1)}, nil
}

// Dir is where raw messages live (for backups).
func (s *Service) Dir() string { return s.dir }

func (s *Service) Options() Options {
	o := Options{UndoSeconds: 10}
	settings.Load(s.db, "email.options", &o)
	return o
}

func (s *Service) SaveOptions(o Options) error {
	if o.UndoSeconds < 0 || o.UndoSeconds > 60 {
		return valid.Error("undo time must be 0 to 60 seconds")
	}
	if o.TrashDays < 0 || o.SpamDays < 0 || o.TrashDays > 3650 || o.SpamDays > 3650 {
		return valid.Error("keep days must be 0 (forever) to 3650")
	}
	return settings.Save(s.db, "email.options", o)
}

func newID(prefix string) string {
	b := make([]byte, 9)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// changed tells browsers to refetch (no content: every member gets live events).
func (s *Service) changed() {
	if s.Bus != nil {
		s.Bus.Publish("emails", map[string]any{})
	}
}

// ---------- domains ----------

var reDomain = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

const domainCols = `id, name, zone_id, catch_all, catch_all_to, sender_id, forward_to, spam_threshold, checks, checked_at, created_at`

func scanDomain(sc interface{ Scan(...any) error }) (Domain, error) {
	var d Domain
	var fwd, checks string
	err := sc.Scan(&d.ID, &d.Name, &d.ZoneID, &d.CatchAll, &d.CatchAllTo, &d.SenderID, &fwd, &d.SpamThreshold, &checks, &d.CheckedAt, &d.CreatedAt)
	d.ForwardTo = listJSON(fwd)
	jsonInto(checks, &d.Checks)
	return d, err
}

func (s *Service) Domains() ([]Domain, error) {
	rows, err := s.db.Query(`SELECT ` + domainCols + ` FROM mail_domains ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) Domain(id string) (Domain, error) {
	d, err := scanDomain(s.db.QueryRow(`SELECT `+domainCols+` FROM mail_domains WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// AddDomain turns a domain on for email (zoneID from Cloudflare, or "" when set up by hand).
func (s *Service) AddDomain(name, zoneID string) (Domain, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if !reDomain.MatchString(name) || len(name) > 253 {
		return Domain{}, valid.Error("enter a domain like example.com")
	}
	d := Domain{ID: newID("emd"), Name: name, ZoneID: zoneID, ForwardTo: []string{}, SpamThreshold: 5, CreatedAt: s.Now().Unix()}
	_, err := s.db.Exec(`INSERT INTO mail_domains (id, name, zone_id, created_at) VALUES (?, ?, ?, ?)`, d.ID, d.Name, d.ZoneID, d.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return d, valid.Error(name + " is already on")
	}
	s.edgeChanged()
	return d, err
}

// DomainUpdate is what may change on a domain.
type DomainUpdate struct {
	CatchAll      bool     `json:"catchAll"`
	CatchAllTo    string   `json:"catchAllTo"`
	SenderID      string   `json:"senderId"`
	ForwardTo     []string `json:"forwardTo"`
	SpamThreshold float64  `json:"spamThreshold"`
}

func (s *Service) UpdateDomain(id string, in DomainUpdate) (Domain, error) {
	d, err := s.Domain(id)
	if err != nil {
		return d, err
	}
	fwd, err := cleanForward(in.ForwardTo)
	if err != nil {
		return d, err
	}
	if in.CatchAll {
		a, err := s.Address(in.CatchAllTo)
		if err != nil || a.DomainID != id {
			return d, valid.Error("pick the address whose inbox gets mail sent to unknown addresses")
		}
	}
	if in.SpamThreshold <= 0 || in.SpamThreshold > 100 {
		return d, valid.Error("spam threshold must be between 0 and 100")
	}
	if len(in.SenderID) > 64 {
		return d, valid.Error("unknown sender")
	}
	_, err = s.db.Exec(`UPDATE mail_domains SET catch_all = ?, catch_all_to = ?, sender_id = ?, forward_to = ?, spam_threshold = ? WHERE id = ?`,
		in.CatchAll, in.CatchAllTo, in.SenderID, jsonList(fwd), in.SpamThreshold, id)
	if err != nil {
		return d, err
	}
	s.edgeChanged()
	return s.Domain(id)
}

// RemoveDomain turns email off for a domain. Its mail must be gone first: nothing is
// deleted as a side effect.
func (s *Service) RemoveDomain(id string) error {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE domain_id = ?`, id).Scan(&n)
	if n > 0 {
		return valid.Error(fmt.Sprintf("this domain still has %d messages: delete them (or export them) first", n))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.Exec(`DELETE FROM mail_drafts WHERE from_id IN (SELECT id FROM mail_addresses WHERE domain_id = ?)`, id)
	tx.Exec(`UPDATE mail_addresses SET inbox_id = NULL WHERE domain_id = ?`, id)
	tx.Exec(`DELETE FROM mail_grants WHERE (scope_kind = 'domain' AND scope_id = ?) OR (scope_kind = 'address' AND scope_id IN (SELECT id FROM mail_addresses WHERE domain_id = ?))`, id, id)
	if _, err := tx.Exec(`DELETE FROM mail_addresses WHERE domain_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM mail_domains WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.edgeChanged()
	return nil
}

func (s *Service) saveChecks(id string, c Checks) {
	s.db.Exec(`UPDATE mail_domains SET checks = ?, checked_at = ? WHERE id = ?`, jsonString(c), s.Now().Unix(), id)
}

// ---------- addresses ----------

var reLocal = regexp.MustCompile(`^[a-z0-9]([a-z0-9._+-]{0,62}[a-z0-9])?$`)

const addrCols = `a.id, a.domain_id, a.local, a.address, a.display_name, COALESCE(a.inbox_id, ''), a.receive, a.send, a.disabled, a.forward_mode, a.forward_to, a.notify, a.signature, a.created_at`

func scanAddress(sc interface{ Scan(...any) error }, extra ...any) (Address, error) {
	var a Address
	var fwd string
	err := sc.Scan(append([]any{&a.ID, &a.DomainID, &a.Local, &a.Address, &a.DisplayName, &a.InboxID, &a.Receive, &a.Send, &a.Disabled, &a.ForwardMode, &fwd, &a.Notify, &a.Signature, &a.CreatedAt}, extra...)...)
	a.ForwardTo = listJSON(fwd)
	return a, err
}

// Addresses lists every address with its inbox's counts.
func (s *Service) Addresses() ([]Address, error) {
	rows, err := s.db.Query(`SELECT ` + addrCols + `,
		(SELECT COUNT(*) FROM mail_messages m WHERE m.mailbox_id = a.id AND m.unread = 1 AND m.folder = 'inbox'),
		(SELECT COUNT(*) FROM mail_messages m WHERE m.mailbox_id = a.id),
		(SELECT COALESCE(SUM(size), 0) FROM mail_messages m WHERE m.mailbox_id = a.id)
		FROM mail_addresses a ORDER BY a.address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Address{}
	for rows.Next() {
		var unread, total int
		var size int64
		a, err := scanAddress(rows, &unread, &total, &size)
		if err != nil {
			return nil, err
		}
		a.Unread, a.Total, a.Bytes = unread, total, size
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) Address(id string) (Address, error) {
	a, err := scanAddress(s.db.QueryRow(`SELECT `+addrCols+` FROM mail_addresses a WHERE a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Service) addressByEmail(addr string) (Address, error) {
	a, err := scanAddress(s.db.QueryRow(`SELECT `+addrCols+` FROM mail_addresses a WHERE a.address = ?`, addr))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// AddressInput creates or changes an address.
type AddressInput struct {
	DomainID    string   `json:"domainId"`
	Local       string   `json:"local"`
	DisplayName string   `json:"displayName"`
	InboxID     string   `json:"inboxId"`
	Receive     bool     `json:"receive"`
	Send        bool     `json:"send"`
	Disabled    bool     `json:"disabled"`
	ForwardMode string   `json:"forwardMode"`
	ForwardTo   []string `json:"forwardTo"`
	Notify      bool     `json:"notify"`
	Signature   string   `json:"signature"`
}

func (s *Service) checkAddress(in *AddressInput, self string) error {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if len(in.DisplayName) > 80 || strings.ContainsAny(in.DisplayName, "\r\n\"<>") {
		return valid.Error("the display name is too long or has < > \" in it")
	}
	if len(in.Signature) > 20000 {
		return valid.Error("the signature is too long")
	}
	in.Signature = Sanitize(in.Signature, SanitizeOptions{Images: true})
	switch in.ForwardMode {
	case "":
		in.ForwardMode = "domain"
	case "domain", "custom", "off":
	default:
		return valid.Error("forwarding must be domain, custom or off")
	}
	fwd, err := cleanForward(in.ForwardTo)
	if err != nil {
		return err
	}
	in.ForwardTo = fwd
	if in.InboxID != "" {
		if in.InboxID == self {
			in.InboxID = ""
		} else {
			to, err := s.Address(in.InboxID)
			if err != nil || to.InboxID != "" {
				return valid.Error("an alias must deliver into an address that has its own inbox")
			}
		}
	}
	return nil
}

func (s *Service) CreateAddress(in AddressInput) (Address, error) {
	d, err := s.Domain(in.DomainID)
	if err != nil {
		return Address{}, valid.Error("pick a domain")
	}
	in.Local = strings.ToLower(strings.TrimSpace(in.Local))
	if !reLocal.MatchString(in.Local) || strings.Contains(in.Local, "..") {
		return Address{}, valid.Error("the part before @ may have letters, digits, dots, dashes, + and _")
	}
	if err := s.checkAddress(&in, ""); err != nil {
		return Address{}, err
	}
	a := Address{ID: newID("ema"), DomainID: d.ID, Local: in.Local, Address: in.Local + "@" + d.Name}
	_, err = s.db.Exec(`INSERT INTO mail_addresses (id, domain_id, local, address, display_name, inbox_id, receive, send, disabled, forward_mode, forward_to, notify, signature, created_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?)`, a.ID, a.DomainID, a.Local, a.Address, in.DisplayName, in.InboxID, in.Receive, in.Send, in.Disabled,
		in.ForwardMode, jsonList(in.ForwardTo), in.Notify, in.Signature, s.Now().Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return a, valid.Error(a.Address + " already exists")
	}
	if err != nil {
		return a, err
	}
	s.edgeChanged()
	return s.Address(a.ID)
}

func (s *Service) UpdateAddress(id string, in AddressInput) (Address, error) {
	a, err := s.Address(id)
	if err != nil {
		return a, err
	}
	if err := s.checkAddress(&in, id); err != nil {
		return a, err
	}
	if in.InboxID != "" {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM mail_addresses WHERE inbox_id = ?`, id).Scan(&n)
		if n > 0 {
			return a, valid.Error("other addresses deliver into this inbox, so it cannot become an alias")
		}
		s.db.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE mailbox_id = ?`, id).Scan(&n)
		if n > 0 {
			return a, valid.Error("this address already has mail in its own inbox, so it cannot become an alias")
		}
	}
	_, err = s.db.Exec(`UPDATE mail_addresses SET display_name = ?, inbox_id = NULLIF(?, ''), receive = ?, send = ?, disabled = ?, forward_mode = ?, forward_to = ?, notify = ?, signature = ? WHERE id = ?`,
		in.DisplayName, in.InboxID, in.Receive, in.Send, in.Disabled, in.ForwardMode, jsonList(in.ForwardTo), in.Notify, in.Signature, id)
	if err != nil {
		return a, err
	}
	s.edgeChanged()
	return s.Address(id)
}

// DeleteAddress removes an address whose inbox is empty (disable it to keep its mail).
func (s *Service) DeleteAddress(id string) error {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE mailbox_id = ?`, id).Scan(&n)
	if n > 0 {
		return valid.Error(fmt.Sprintf("this inbox still has %d messages: turn the address off instead, or delete its mail first", n))
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM mail_addresses WHERE inbox_id = ?`, id).Scan(&n)
	if n > 0 {
		return valid.Error("aliases deliver into this inbox: remove or move them first")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.Exec(`DELETE FROM mail_drafts WHERE from_id = ?`, id)
	tx.Exec(`DELETE FROM mail_grants WHERE scope_kind = 'address' AND scope_id = ?`, id)
	tx.Exec(`UPDATE mail_domains SET catch_all = 0, catch_all_to = '' WHERE catch_all_to = ?`, id)
	res, err := tx.Exec(`DELETE FROM mail_addresses WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.edgeChanged()
	return nil
}

// forwardFor is where Gmail copies of mail to a go.
func forwardFor(a Address, d Domain) []string {
	switch a.ForwardMode {
	case "off":
		return []string{}
	case "custom":
		return a.ForwardTo
	}
	return d.ForwardTo
}

func cleanForward(in []string) ([]string, error) {
	out := []string{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if a, err := mail.ParseAddress(v); err != nil || a.Address != v || len(v) > 254 {
			return nil, valid.Error(fmt.Sprintf("%q is not an email address", v))
		}
		if !containsStr(out, v) {
			out = append(out, v)
		}
	}
	if len(out) > 10 {
		return nil, valid.Error("10 forwarding addresses at most")
	}
	return out, nil
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
