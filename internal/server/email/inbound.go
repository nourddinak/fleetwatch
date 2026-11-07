package email

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fleetwatch/internal/server/notify"
)

// Inbound is one message the Worker handed over.
type Inbound struct {
	Rcpt     string // envelope recipient: the address it was sent to
	MailFrom string // envelope sender
}

// Result tells the Worker what to do next.
type Result struct {
	Stored    bool     `json:"stored"`
	Duplicate bool     `json:"duplicate,omitempty"`
	Reject    string   `json:"reject,omitempty"` // refuse the message with this reason (bounce)
	Forward   []string `json:"forward"`          // Gmail copies to make
}

// resolve finds the address a recipient means: itself (me+anything@ is me@), or the
// domain's catch-all.
func (s *Service) resolve(rcpt string) (Address, Domain, string) {
	rcpt = strings.ToLower(strings.TrimSpace(rcpt))
	at := strings.LastIndexByte(rcpt, '@')
	if at <= 0 {
		return Address{}, Domain{}, "No such address."
	}
	tries := []string{rcpt}
	if plus := strings.IndexByte(rcpt[:at], '+'); plus > 0 {
		tries = append(tries, rcpt[:plus]+rcpt[at:])
	}
	for _, t := range tries {
		if a, err := s.addressByEmail(t); err == nil {
			d, _ := s.Domain(a.DomainID)
			if a.Disabled || !a.Receive {
				return a, d, "This address does not accept mail."
			}
			return a, d, ""
		}
	}
	var id string
	if s.db.QueryRow(`SELECT id FROM email_domains WHERE name = ?`, rcpt[at+1:]).Scan(&id) != nil {
		return Address{}, Domain{}, "No such address."
	}
	d, _ := s.Domain(id)
	if !d.CatchAll {
		return Address{}, d, "No such address."
	}
	a, err := s.Address(d.CatchAllTo)
	if err != nil || a.Disabled {
		return Address{}, d, "No such address."
	}
	return a, d, ""
}

// Ingest files one raw message. It returns only after the message is on disk and in
// the database (or was already there).
func (s *Service) Ingest(in Inbound, raw []byte) (Result, error) {
	res := Result{Forward: []string{}}
	if len(raw) > MaxMessage {
		res.Reject = "Message too large (25 MiB at most)."
		return res, nil
	}
	a, d, reject := s.resolve(in.Rcpt)
	if reject != "" {
		res.Reject = reject
		return res, nil
	}
	res.Forward = forwardFor(a, d)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	box := a.Mailbox()
	var dup int64
	if s.db.QueryRow(`SELECT id FROM email_messages WHERE mailbox_id = ? AND sha256 = ?`, box, hash).Scan(&dup) == nil {
		res.Stored, res.Duplicate = true, true
		return res, nil
	}
	p := Parse(raw, false)
	if p.From.Address == "" {
		p.From.Address = strings.ToLower(in.MailFrom)
	}
	mine := s.domainNames()
	score, why := Score(p, mine)
	folder := "inbox"
	switch s.listed(p.From.Address, a) {
	case "allow":
		score, why = 0, []string{"sender is on your allow list"}
	case "block":
		score, why = 100, []string{"sender is on your block list"}
	}
	if score >= d.SpamThreshold {
		folder = "spam"
		res.Forward = []string{} // spam stays here, out of Gmail
	}
	now := s.Now()
	rel := fmt.Sprintf("raw/%s/%s-%s.eml", now.UTC().Format("2006/01"), hash[:32], box[len(box)-8:])
	if err := s.writeFile(rel, raw); err != nil {
		return res, fmt.Errorf("store message: %w", err)
	}
	id, err := s.file(fileInput{
		mailbox: box, domain: d.ID, direction: "in", folder: folder, rcpt: strings.ToLower(in.Rcpt), parsed: p,
		size: int64(len(raw)), hash: hash, rel: rel, score: score, why: why, unread: true, at: now,
	})
	if err != nil {
		var n int
		if s.db.QueryRow(`SELECT COUNT(*) FROM email_messages WHERE raw_path = ?`, rel).Scan(&n); n == 0 {
			s.recordFailure(in, int64(len(raw)), rel, err)
		}
		return res, err
	}
	res.Stored = true
	s.changed()
	if folder == "inbox" && a.Notify && s.Notify != nil {
		who := p.From.Name
		if who == "" {
			who = p.From.Address
		}
		link := ""
		if s.PublicURL != "" {
			link = strings.TrimRight(s.PublicURL, "/") + fmt.Sprintf("/#/emails?m=%d", id)
		}
		// No subject: notification history is visible to every team member.
		s.Notify.Notice("email.received", notify.Message{Title: "New email to " + strings.ToLower(in.Rcpt), Body: "From " + who, Severity: "info", Link: link}, "", "")
	}
	return res, nil
}

func (s *Service) domainNames() []string {
	rows, err := s.db.Query(`SELECT name FROM email_domains`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, strings.ToLower(n))
	}
	return out
}

type fileInput struct {
	mailbox, domain, direction, folder, rcpt string
	parsed                                   Parsed
	bcc                                      []Person
	size                                     int64
	hash, rel                                string
	score                                    float64
	why                                      []string
	unread                                   bool
	at                                       time.Time
}

// file writes a message's rows (message, thread, attachments, search, contacts) in one
// transaction.
func (s *Service) file(f fileInput) (int64, error) {
	p := f.parsed
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	thread, err := s.threadFor(tx, f.mailbox, p, f.at)
	if err != nil {
		return 0, err
	}
	msgID := p.MessageID
	if msgID == "" {
		msgID = fmt.Sprintf("<%s@fleetwatch.local>", f.hash[:24])
	}
	sent := p.Date
	if sent.IsZero() || sent.After(f.at.Add(24*time.Hour)) {
		sent = f.at
	}
	res, err := tx.Exec(`INSERT INTO email_messages (mailbox_id, domain_id, thread_id, direction, folder, message_id, in_reply_to, refs, rcpt, from_addr, from_name,
		to_json, cc_json, bcc_json, reply_to, subject, snippet, size, attachments, unread, spam_score, spam_why, auth, sha256, raw_path, sent_at, received_at, moved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.mailbox, f.domain, thread, f.direction, f.folder, msgID, p.InReplyTo, strings.Join(p.References, " "), f.rcpt, p.From.Address, p.From.Name,
		peopleJSON(p.To), peopleJSON(p.Cc), peopleJSON(f.bcc), p.ReplyTo, p.Subject, snippet(p), f.size, len(p.Attachments), f.unread, f.score, jsonList(f.why),
		jsonString(p.Auth), f.hash, f.rel, sent.Unix(), f.at.Unix(), f.at.Unix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	files := []string{}
	for i, at := range p.Attachments {
		if _, err := tx.Exec(`INSERT INTO email_attachments (message_id, part, filename, type, size, content_id) VALUES (?, ?, ?, ?, ?, ?)`, id, i, at.Filename, at.Type, at.Size, at.ContentID); err != nil {
			return 0, err
		}
		if !at.Inline {
			files = append(files, at.Filename)
		}
	}
	people := []string{p.From.Name, p.From.Address}
	for _, x := range append(append(append([]Person{}, p.To...), p.Cc...), f.bcc...) {
		people = append(people, x.Name, x.Address)
	}
	body := p.Text
	if strings.TrimSpace(body) == "" {
		body = HTMLToText(p.HTML)
	}
	if len(body) > 200000 {
		body = body[:200000]
	}
	if _, err := tx.Exec(`INSERT INTO email_fts (rowid, subject, people, body, files) VALUES (?, ?, ?, ?, ?)`, id, p.Subject, strings.Join(people, " "), body, strings.Join(files, " ")); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE email_threads SET last_at = MAX(last_at, ?) WHERE id = ?`, f.at.Unix(), thread); err != nil {
		return 0, err
	}
	// Contacts: who writes in (not spam), and whoever you write to.
	var who []Person
	if f.direction == "in" && f.folder != "spam" {
		who = []Person{p.From}
	} else if f.direction == "out" {
		who = append(append(append([]Person{}, p.To...), p.Cc...), f.bcc...)
	}
	for _, c := range who {
		if c.Address == "" || containsStr(s.domainNamesTx(tx), domainOf(c.Address)) {
			continue
		}
		tx.Exec(`INSERT INTO email_contacts (address, name, uses, last_used) VALUES (?, ?, 1, ?)
			ON CONFLICT (address) DO UPDATE SET uses = uses + 1, last_used = excluded.last_used, name = CASE WHEN name = '' THEN excluded.name ELSE name END`,
			c.Address, c.Name, f.at.Unix())
	}
	return id, tx.Commit()
}

func (s *Service) domainNamesTx(tx *sql.Tx) []string {
	rows, err := tx.Query(`SELECT name FROM email_domains`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, strings.ToLower(n))
	}
	return out
}

// threadFor finds the conversation a message belongs to: by the messages it answers
// (In-Reply-To, References), else the same subject recently in the same inbox.
func (s *Service) threadFor(tx *sql.Tx, mailbox string, p Parsed, at time.Time) (int64, error) {
	ids := append([]string{}, p.References...)
	if p.InReplyTo != "" {
		ids = append(ids, p.InReplyTo)
	}
	if p.MessageID != "" {
		ids = append(ids, p.MessageID) // a copy of one already here (sent to two of your addresses)
	}
	for i := len(ids) - 1; i >= 0 && i >= len(ids)-20; i-- {
		var t int64
		if tx.QueryRow(`SELECT thread_id FROM email_messages WHERE mailbox_id = ? AND message_id = ? LIMIT 1`, mailbox, ids[i]).Scan(&t) == nil {
			return t, nil
		}
	}
	subj := normSubject(p.Subject)
	if subj != "" && (p.InReplyTo != "" || len(p.References) > 0 || reSubjectPrefix.MatchString(p.Subject)) {
		var t int64
		if tx.QueryRow(`SELECT id FROM email_threads WHERE mailbox_id = ? AND subject = ? AND last_at > ? ORDER BY last_at DESC LIMIT 1`, mailbox, subj, at.Add(-30*24*time.Hour).Unix()).Scan(&t) == nil {
			return t, nil
		}
	}
	res, err := tx.Exec(`INSERT INTO email_threads (mailbox_id, subject, last_at) VALUES (?, ?, ?)`, mailbox, subj, at.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---------- messages that could not be filed ----------

func (s *Service) recordFailure(in Inbound, size int64, rel string, cause error) {
	slog.Error("email: could not file an inbound message", "rcpt", in.Rcpt, "err", cause)
	s.db.Exec(`INSERT INTO email_inbound_failures (rcpt, mail_from, size, raw_path, error, received_at) VALUES (?, ?, ?, ?, ?, ?)`,
		strings.ToLower(in.Rcpt), strings.ToLower(in.MailFrom), size, rel, cause.Error(), s.Now().Unix())
}

func (s *Service) Failures() ([]Failure, error) {
	rows, err := s.db.Query(`SELECT id, rcpt, mail_from, size, raw_path, error, received_at FROM email_inbound_failures ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Failure{}
	for rows.Next() {
		var f Failure
		var rel string
		rows.Scan(&f.ID, &f.Rcpt, &f.MailFrom, &f.Size, &rel, &f.Error, &f.ReceivedAt)
		if p, err := s.path(rel); err == nil && rel != "" {
			f.Kept = fileExists(p)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RetryFailure files a kept message again.
func (s *Service) RetryFailure(id int64) (Result, error) {
	var in Inbound
	var rel string
	if err := s.db.QueryRow(`SELECT rcpt, mail_from, raw_path FROM email_inbound_failures WHERE id = ?`, id).Scan(&in.Rcpt, &in.MailFrom, &rel); err != nil {
		return Result{}, ErrNotFound
	}
	raw, err := s.readFile(rel)
	if err != nil {
		return Result{}, fmt.Errorf("the saved copy cannot be read: %w", err)
	}
	s.db.Exec(`DELETE FROM email_inbound_failures WHERE id = ?`, id)
	res, err := s.Ingest(in, raw)
	if err != nil {
		return res, err
	}
	if res.Reject != "" {
		s.recordFailure(in, int64(len(raw)), rel, errors.New("refused: "+res.Reject))
	}
	return res, nil
}

func (s *Service) DismissFailure(id int64) error {
	var rel string
	s.db.QueryRow(`SELECT raw_path FROM email_inbound_failures WHERE id = ?`, id).Scan(&rel)
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM email_messages WHERE raw_path = ?`, rel).Scan(&n)
	if rel != "" && n == 0 {
		s.removeFile(rel)
	}
	_, err := s.db.Exec(`DELETE FROM email_inbound_failures WHERE id = ?`, id)
	return err
}
