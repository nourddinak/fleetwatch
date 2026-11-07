package email

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

const draftCols = `id, from_id, to_json, cc_json, bcc_json, subject, html, COALESCE(reply_to_id, 0), COALESCE(forward_id, 0), files, updated_at`

func scanDraft(sc interface{ Scan(...any) error }) (Draft, error) {
	var d Draft
	var to, cc, bcc, files string
	err := sc.Scan(&d.ID, &d.FromID, &to, &cc, &bcc, &d.Subject, &d.HTML, &d.ReplyToID, &d.ForwardID, &files, &d.UpdatedAt)
	d.To, d.Cc, d.Bcc = listJSON(to), listJSON(cc), listJSON(bcc)
	d.Files = []File{}
	jsonInto(files, &d.Files)
	return d, err
}

func (s *Service) Drafts(a Access) ([]Draft, error) {
	where, args := a.mailboxFilter("from_id")
	if !a.Owner { // drafts are per sending address, which may be an alias
		where, args = "0", nil
		for id := range a.addrs {
			if a.AddressCan(id, "send") {
				where += " OR from_id = ?"
				args = append(args, id)
			}
		}
	}
	rows, err := s.db.Query(`SELECT `+draftCols+` FROM email_drafts WHERE queued = 0 AND (`+where+`) ORDER BY updated_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) Draft(a Access, id string) (Draft, error) {
	d, err := scanDraft(s.db.QueryRow(`SELECT `+draftCols+` FROM email_drafts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || err == nil && !a.AddressCan(d.FromID, "send") {
		return Draft{}, ErrNotFound
	}
	return d, err
}

func cleanRecipients(list []string) ([]string, error) {
	out := []string{}
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		a, err := mail.ParseAddress(v)
		if err != nil || len(a.Address) > 254 {
			return nil, valid.Error(fmt.Sprintf("%q is not an email address", v))
		}
		out = append(out, strings.ToLower(a.Address))
	}
	return out, nil
}

// SaveDraft creates (no id) or updates a draft. Saving is lenient (half-typed
// addresses are fine), sending checks everything.
func (s *Service) SaveDraft(a Access, d Draft, by string) (Draft, error) {
	if !a.AddressCan(d.FromID, "send") {
		return d, valid.Error("pick an address you may send from")
	}
	if d.ReplyToID != 0 {
		if _, err := s.Get(a, d.ReplyToID); err != nil {
			d.ReplyToID = 0
		}
	}
	if d.ForwardID != 0 {
		if _, err := s.Get(a, d.ForwardID); err != nil {
			d.ForwardID = 0
		}
	}
	if len(d.HTML) > 2<<20 || len(d.Subject) > 998 || len(d.To)+len(d.Cc)+len(d.Bcc) > 100 {
		return d, valid.Error("the message is too long or has too many recipients")
	}
	// Drafts open in someone's editor (maybe the owner's): never store markup that runs.
	d.HTML = Sanitize(d.HTML, SanitizeOptions{Images: true})
	if d.Files == nil {
		d.Files = []File{}
	}
	for _, f := range d.Files {
		if !reUploadID.MatchString(f.ID) {
			return d, valid.Error("bad attachment")
		}
	}
	d.UpdatedAt = s.Now().Unix()
	null := func(n int64) any {
		if n == 0 {
			return nil
		}
		return n
	}
	if d.ID == "" {
		d.ID = newID("emdr")
		_, err := s.db.Exec(`INSERT INTO email_drafts (id, from_id, to_json, cc_json, bcc_json, subject, html, reply_to_id, forward_id, files, created_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.FromID, jsonList(d.To), jsonList(d.Cc), jsonList(d.Bcc), d.Subject, d.HTML, null(d.ReplyToID), null(d.ForwardID), jsonString(d.Files), by, d.UpdatedAt)
		return d, err
	}
	if _, err := s.Draft(a, d.ID); err != nil {
		return d, err
	}
	res, err := s.db.Exec(`UPDATE email_drafts SET from_id = ?, to_json = ?, cc_json = ?, bcc_json = ?, subject = ?, html = ?, reply_to_id = ?, forward_id = ?, files = ?, updated_at = ? WHERE id = ? AND queued = 0`,
		d.FromID, jsonList(d.To), jsonList(d.Cc), jsonList(d.Bcc), d.Subject, d.HTML, null(d.ReplyToID), null(d.ForwardID), jsonString(d.Files), d.UpdatedAt, d.ID)
	if n, _ := res.RowsAffected(); err == nil && n == 0 {
		return d, valid.Error("this draft is being sent")
	}
	return d, err
}

func (s *Service) DeleteDraft(a Access, id string) error {
	d, err := s.Draft(a, id)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM email_drafts WHERE id = ? AND queued = 0`, id); err != nil {
		return err
	}
	for _, f := range d.Files {
		s.removeFile("uploads/" + f.ID)
	}
	return nil
}

// ---------- uploads ----------

var reUploadID = regexp.MustCompile(`^up_[0-9a-f]{18}$`)

// Upload keeps a file for a draft (encrypted, like everything else).
func (s *Service) Upload(name, typ string, data []byte) (File, error) {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "" || name == "." || len(name) > 200 || strings.ContainsAny(name, "\r\n\"") {
		return File{}, valid.Error("bad file name")
	}
	if len(data) > MaxMessage*3/4 {
		return File{}, valid.Error("the file is too large: a whole message can be 25 MiB at most")
	}
	if _, _, err := mime.ParseMediaType(typ); err != nil || typ == "" {
		typ = "application/octet-stream"
	}
	f := File{ID: newID("up"), Name: name, Type: typ, Size: int64(len(data))}
	return f, s.writeFile("uploads/"+f.ID, data)
}

func (s *Service) sweepUploads() {
	dir := filepath.Join(s.dir, "uploads")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || s.Now().Sub(info.ModTime()) < 48*time.Hour {
			continue
		}
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM email_drafts WHERE files LIKE ?`, "%"+strings.TrimSuffix(e.Name(), ".tmp")+"%").Scan(&n)
		if n == 0 {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ---------- building the message ----------

type outgoing struct {
	From       Person
	To, Cc     []string
	Subject    string
	HTML       string
	InReplyTo  string
	References []string
	Files      []Part
	MessageID  string
	Date       time.Time
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func encodeHeader(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return mime.QEncoding.Encode("utf-8", s)
}

func addrList(list []string) string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = (&mail.Address{Address: a}).String()
	}
	return strings.Join(out, ", ")
}

func qp(text string) []byte {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")))
	w.Close()
	return b.Bytes()
}

func b64lines(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var b bytes.Buffer
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes()
}

// build writes the MIME message: text + HTML alternatives, then attachments.
func build(o outgoing) []byte {
	var b bytes.Buffer
	head := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	head("From", (&mail.Address{Name: o.From.Name, Address: o.From.Address}).String())
	head("To", addrList(o.To))
	head("Cc", addrList(o.Cc))
	head("Subject", encodeHeader(o.Subject))
	head("Date", o.Date.Format(time.RFC1123Z))
	head("Message-ID", o.MessageID)
	head("In-Reply-To", o.InReplyTo)
	head("References", strings.Join(o.References, " "))
	head("MIME-Version", "1.0")

	var alt bytes.Buffer
	aw := multipart.NewWriter(&alt)
	tw, _ := aw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
	tw.Write(qp(HTMLToText(o.HTML)))
	hw, _ := aw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
	hw.Write(qp(`<!doctype html><html><body>` + o.HTML + `</body></html>`))
	aw.Close()

	if len(o.Files) == 0 {
		head("Content-Type", "multipart/alternative; boundary="+aw.Boundary())
		b.WriteString("\r\n")
		b.Write(alt.Bytes())
		return b.Bytes()
	}
	var mixed bytes.Buffer
	mw := multipart.NewWriter(&mixed)
	pw, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"multipart/alternative; boundary=" + aw.Boundary()}})
	pw.Write(alt.Bytes())
	for _, f := range o.Files {
		fw, _ := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(f.Type, map[string]string{"name": f.Filename})},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": f.Filename})},
		})
		fw.Write(b64lines(f.Data))
	}
	mw.Close()
	head("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	b.WriteString("\r\n")
	b.Write(mixed.Bytes())
	return b.Bytes()
}

// Send puts a draft in the outbox. It leaves after the undo delay, or at sendAt
// (unix seconds) when later. It answers the outbox job.
func (s *Service) Send(a Access, draftID string, sendAt int64) (Job, error) {
	d, err := s.Draft(a, draftID)
	if err != nil {
		return Job{}, err
	}
	from, err := s.Address(d.FromID)
	if err != nil {
		return Job{}, valid.Error("the sending address is gone")
	}
	if from.Disabled || !from.Send {
		return Job{}, valid.Error(from.Address + " is not allowed to send")
	}
	dom, err := s.Domain(from.DomainID)
	if err != nil {
		return Job{}, err
	}
	to, err := cleanRecipients(d.To)
	if err != nil {
		return Job{}, err
	}
	cc, err := cleanRecipients(d.Cc)
	if err != nil {
		return Job{}, err
	}
	bcc, err := cleanRecipients(d.Bcc)
	if err != nil {
		return Job{}, err
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		return Job{}, valid.Error("add at least one recipient")
	}
	if len(to)+len(cc)+len(bcc) > 100 {
		return Job{}, valid.Error("100 recipients at most")
	}
	now := s.Now()
	o := outgoing{From: Person{Name: from.DisplayName, Address: from.Address}, To: to, Cc: cc, Subject: d.Subject,
		HTML: Sanitize(d.HTML, SanitizeOptions{Images: true}), Date: now, MessageID: "<" + randomHex(16) + "@" + dom.Name + ">"}
	if d.ReplyToID != 0 {
		if orig, err := s.Get(a, d.ReplyToID); err == nil {
			o.InReplyTo = orig.MessageID
			o.References = append(msgIDs(orig.References), orig.MessageID)
			if len(o.References) > 30 {
				o.References = append(o.References[:1], o.References[len(o.References)-29:]...)
			}
		}
	}
	var total int64
	if d.ForwardID != 0 {
		if _, err := s.Get(a, d.ForwardID); err == nil {
			if raw, err := s.rawOf(d.ForwardID); err == nil {
				for _, p := range Parse(raw, true).Attachments {
					if !p.Inline {
						o.Files = append(o.Files, p)
						total += p.Size
					}
				}
			}
		}
	}
	for _, f := range d.Files {
		data, err := s.readFile("uploads/" + f.ID)
		if err != nil {
			return Job{}, valid.Error("the attachment " + f.Name + " is gone: attach it again")
		}
		o.Files = append(o.Files, Part{Filename: f.Name, Type: f.Type, Data: data, Size: int64(len(data))})
		total += int64(len(data))
	}
	if total*4/3+int64(len(o.HTML))*3 > MaxMessage {
		return Job{}, valid.Error("with its attachments the message is over 25 MiB, the most providers accept")
	}
	raw := build(o)
	p := Parse(raw, false)
	sum := randomHex(32) // sent mail is never a duplicate of anything
	box := from.Mailbox()
	rel := fmt.Sprintf("sent/%s/%s.eml", now.UTC().Format("2006/01"), sum[:32])
	if err := s.writeFile(rel, raw); err != nil {
		return Job{}, err
	}
	bccP := make([]Person, len(bcc))
	for i, x := range bcc {
		bccP[i] = Person{Address: x}
	}
	msgID, err := s.file(fileInput{mailbox: box, domain: dom.ID, direction: "out", folder: "sent", rcpt: from.Address, parsed: p, bcc: bccP,
		size: int64(len(raw)), hash: sum, rel: rel, at: now})
	if err != nil {
		s.removeFile(rel)
		return Job{}, err
	}
	when := now.Add(time.Duration(s.Options().UndoSeconds) * time.Second).Unix()
	if sendAt > when {
		if sendAt > now.AddDate(1, 0, 0).Unix() {
			return Job{}, valid.Error("schedule within a year")
		}
		when = sendAt
	}
	sender := dom.SenderID
	if s.SenderFor != nil {
		sender = s.SenderFor(dom.Name, dom.SenderID)
	}
	j := Job{ID: newID("job"), MessageID: msgID, State: "scheduled", SendAt: when, NextAt: when, From: from.Address}
	rcpts := append(append(append([]string{}, to...), cc...), bcc...)
	tx, err := s.db.Begin()
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO email_outbox (id, message_id, draft_id, sender_id, from_addr, rcpts, state, send_at, next_at, created_at) VALUES (?, ?, ?, ?, ?, ?, 'scheduled', ?, ?, ?)`,
		j.ID, msgID, d.ID, sender, from.Address, jsonList(rcpts), when, when, now.Unix()); err != nil {
		return j, err
	}
	if _, err := tx.Exec(`UPDATE email_drafts SET queued = 1 WHERE id = ?`, d.ID); err != nil {
		return j, err
	}
	if err := tx.Commit(); err != nil {
		return j, err
	}
	s.poke()
	s.changed()
	return j, nil
}
