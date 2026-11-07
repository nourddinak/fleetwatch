package email

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

// Contacts fill themselves from who writes in and who you write to. The owner sees
// and edits the address book; a team member's suggestions come from their own inboxes.

func (s *Service) Contacts(q string, limit int) ([]Contact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	like := "%" + strings.ToLower(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(`SELECT id, address, name, company, notes, uses, last_used FROM email_contacts
		WHERE address LIKE ? OR name LIKE ? OR company LIKE ? ORDER BY uses DESC, last_used DESC LIMIT ?`, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Contact{}
	for rows.Next() {
		var c Contact
		rows.Scan(&c.ID, &c.Address, &c.Name, &c.Company, &c.Notes, &c.Uses, &c.LastUsed)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Suggest is the composer's autocomplete for a person.
func (s *Service) Suggest(a Access, q string) ([]Contact, error) {
	if a.Owner {
		return s.Contacts(q, 8)
	}
	where, args := a.mailboxFilter("mailbox_id")
	like := "%" + strings.ToLower(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(`SELECT from_addr, MAX(from_name), COUNT(*) FROM email_messages WHERE direction = 'in' AND folder != 'spam' AND (from_addr LIKE ? OR from_name LIKE ?) AND `+where+`
		GROUP BY from_addr ORDER BY COUNT(*) DESC LIMIT 8`, append([]any{like, like}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Contact{}
	for rows.Next() {
		var c Contact
		rows.Scan(&c.Address, &c.Name, &c.Uses)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) SaveContact(c Contact) (Contact, error) {
	c.Address = strings.ToLower(strings.TrimSpace(c.Address))
	if a, err := mail.ParseAddress(c.Address); err != nil || a.Address != c.Address {
		return c, valid.Error("enter an email address")
	}
	c.Name, c.Company = strings.TrimSpace(c.Name), strings.TrimSpace(c.Company)
	if len(c.Name) > 100 || len(c.Company) > 100 || len(c.Notes) > 4000 {
		return c, valid.Error("name, company or notes are too long")
	}
	if c.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO email_contacts (address, name, company, notes, last_used) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (address) DO UPDATE SET name = excluded.name, company = excluded.company, notes = excluded.notes`, c.Address, c.Name, c.Company, c.Notes, s.Now().Unix())
		if err != nil {
			return c, err
		}
		c.ID, _ = res.LastInsertId()
		return c, nil
	}
	_, err := s.db.Exec(`UPDATE email_contacts SET address = ?, name = ?, company = ?, notes = ? WHERE id = ?`, c.Address, c.Name, c.Company, c.Notes, c.ID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return c, valid.Error("another contact has that address")
	}
	return c, err
}

func (s *Service) DeleteContact(id int64) error {
	_, err := s.db.Exec(`DELETE FROM email_contacts WHERE id = ?`, id)
	return err
}

// ---------- export ----------

// ExportMbox writes the person's messages (one inbox and/or folder, or all) as an
// MBOX file (mboxrd: lines starting with "From " get a ">"). It answers how many.
func (s *Service) ExportMbox(a Access, mailbox, folder string, w io.Writer) (int, error) {
	where, args := a.mailboxFilter("mailbox_id")
	if mailbox != "" {
		where += " AND mailbox_id = ?"
		args = append(args, mailbox)
	}
	if folder != "" {
		where += " AND folder = ?"
		args = append(args, folder)
	}
	rows, err := s.db.Query(`SELECT id, from_addr, received_at, raw_path FROM email_messages WHERE `+where+` ORDER BY received_at`, args...)
	if err != nil {
		return 0, err
	}
	type item struct {
		from string
		at   int64
		rel  string
	}
	var items []item
	for rows.Next() {
		var it item
		var id int64
		rows.Scan(&id, &it.from, &it.at, &it.rel)
		items = append(items, it)
	}
	rows.Close()
	bw := bufio.NewWriterSize(w, 64<<10)
	n := 0
	for _, it := range items {
		raw, err := s.readFile(it.rel)
		if err != nil {
			continue // a missing file is skipped, not the whole export
		}
		from := it.from
		if from == "" {
			from = "MAILER-DAEMON"
		}
		fmt.Fprintf(bw, "From %s %s\n", from, time.Unix(it.at, 0).UTC().Format(time.ANSIC))
		for _, line := range bytes.SplitAfter(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n")) {
			if bytes.HasPrefix(bytes.TrimLeft(line, ">"), []byte("From ")) {
				bw.WriteByte('>')
			}
			bw.Write(line)
		}
		if !bytes.HasSuffix(raw, []byte("\n")) {
			bw.WriteByte('\n')
		}
		bw.WriteByte('\n')
		n++
	}
	return n, bw.Flush()
}
