package email

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

var systemFolders = map[string]bool{"inbox": true, "sent": true, "archive": true, "spam": true, "trash": true}

// Query is a list request.
type Query struct {
	Folder  string // inbox sent archive spam trash | all | starred | f:<id>
	Label   string
	Mailbox string // address id
	Domain  string
	Search  string
	Unread  bool
	Cursor  string // from the previous page
	Limit   int
}

// Page is one page of a list.
type Page struct {
	Messages []Message `json:"messages"`
	Next     string    `json:"next,omitempty"`
}

const msgCols = `m.id, m.mailbox_id, m.domain_id, m.thread_id, m.direction, m.folder, m.message_id, m.in_reply_to, m.refs, m.rcpt, m.from_addr, m.from_name,
	m.to_json, m.cc_json, m.bcc_json, m.reply_to, m.subject, m.snippet, m.size, m.attachments, m.unread, m.starred, m.spam_score, m.spam_why, m.auth, m.sent_at, m.received_at`

func scanMessage(sc interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var to, cc, bcc, why, auth string
	err := sc.Scan(&m.ID, &m.MailboxID, &m.DomainID, &m.ThreadID, &m.Direction, &m.Folder, &m.MessageID, &m.InReplyTo, &m.References, &m.Rcpt, &m.From.Address, &m.From.Name,
		&to, &cc, &bcc, &m.ReplyTo, &m.Subject, &m.Snippet, &m.Size, &m.Attachments, &m.Unread, &m.Starred, &m.SpamScore, &why, &auth, &m.SentAt, &m.ReceivedAt)
	m.To, m.Cc, m.Bcc, m.SpamWhy = jsonPeople(to), jsonPeople(cc), jsonPeople(bcc), listJSON(why)
	m.Labels = []string{}
	jsonInto(auth, &m.Auth)
	return m, err
}

// search turns what people type into SQL: words (all must match), "quoted phrases",
// and from: to: subject: filename: has:attachment is:unread is:starred before: after:
// (YYYY-MM-DD) label:name.
var reToken = regexp.MustCompile(`(\w+:)?("[^"]*"|\S+)`)

func (s *Service) searchSQL(q string) (string, []any) {
	var conds []string
	var args []any
	var fts []string
	ftsTerm := func(col, v string) {
		v = strings.ReplaceAll(strings.Trim(v, `"`), `"`, "")
		if strings.TrimSpace(v) == "" {
			return
		}
		t := `"` + v + `"`
		if !strings.Contains(v, " ") {
			t += "*"
		}
		if col != "" {
			t = col + ":" + t
		}
		fts = append(fts, t)
	}
	for _, m := range reToken.FindAllStringSubmatch(q, 30) {
		key, val := strings.ToLower(strings.TrimSuffix(m[1], ":")), m[2]
		uval := strings.ToLower(strings.Trim(val, `"`))
		switch key {
		case "from":
			conds = append(conds, "(m.from_addr LIKE ? OR m.from_name LIKE ?)")
			args = append(args, "%"+uval+"%", "%"+uval+"%")
		case "to":
			conds = append(conds, "(m.to_json LIKE ? OR m.cc_json LIKE ? OR m.rcpt LIKE ?)")
			args = append(args, "%"+uval+"%", "%"+uval+"%", "%"+uval+"%")
		case "subject":
			ftsTerm("subject", val)
		case "filename", "file":
			ftsTerm("files", val)
		case "has":
			if uval == "attachment" || uval == "attachments" {
				conds = append(conds, "m.attachments > 0")
			}
		case "is":
			switch uval {
			case "unread":
				conds = append(conds, "m.unread = 1")
			case "read":
				conds = append(conds, "m.unread = 0")
			case "starred":
				conds = append(conds, "m.starred = 1")
			}
		case "before", "after":
			t, err := time.Parse("2006-01-02", uval)
			if err != nil {
				continue
			}
			if key == "before" {
				conds = append(conds, "m.received_at < ?")
			} else {
				conds = append(conds, "m.received_at >= ?")
			}
			args = append(args, t.Unix())
		case "label":
			conds = append(conds, "m.id IN (SELECT ml.message_id FROM mail_message_labels ml JOIN mail_folders f ON f.id = ml.label_id WHERE f.kind = 'label' AND f.name = ? COLLATE NOCASE)")
			args = append(args, strings.Trim(val, `"`))
		default:
			ftsTerm("", m[0])
		}
	}
	if len(fts) > 0 {
		conds = append(conds, "m.id IN (SELECT rowid FROM mail_fts WHERE mail_fts MATCH ?)")
		args = append(args, strings.Join(fts, " AND "))
	}
	return strings.Join(conds, " AND "), args
}

// List is one page of messages the person may see, newest first.
func (s *Service) List(a Access, q Query) (Page, error) {
	where, args := a.mailboxFilter("m.mailbox_id")
	add := func(cond string, v ...any) {
		where += " AND " + cond
		args = append(args, v...)
	}
	switch {
	case q.Folder == "" || q.Folder == "all":
		if q.Search == "" || q.Folder == "" {
			add("m.folder NOT IN ('spam', 'trash')")
		}
	case q.Folder == "starred":
		add("m.starred = 1 AND m.folder != 'trash'")
	case systemFolders[q.Folder] || strings.HasPrefix(q.Folder, "f:"):
		add("m.folder = ?", q.Folder)
	default:
		return Page{}, valid.Error("unknown folder")
	}
	if q.Label != "" {
		add("m.id IN (SELECT message_id FROM mail_message_labels WHERE label_id = ?)", q.Label)
	}
	if q.Mailbox != "" {
		add("(m.mailbox_id = ? OR m.rcpt = (SELECT address FROM mail_addresses WHERE id = ?))", q.Mailbox, q.Mailbox)
	}
	if q.Domain != "" {
		add("m.domain_id = ?", q.Domain)
	}
	if q.Unread {
		add("m.unread = 1")
	}
	if q.Search != "" {
		if c, v := s.searchSQL(q.Search); c != "" {
			add(c, v...)
		}
	}
	if q.Cursor != "" {
		ts, id, ok := strings.Cut(q.Cursor, ":")
		t, err1 := strconv.ParseInt(ts, 10, 64)
		n, err2 := strconv.ParseInt(id, 10, 64)
		if !ok || err1 != nil || err2 != nil {
			return Page{}, valid.Error("bad page cursor")
		}
		add("(m.received_at < ? OR (m.received_at = ? AND m.id < ?))", t, t, n)
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 50
	}
	rows, err := s.db.Query(`SELECT `+msgCols+`, (SELECT COUNT(*) FROM mail_messages t WHERE t.thread_id = m.thread_id AND t.folder != 'trash')
		FROM mail_messages m WHERE `+where+` ORDER BY m.received_at DESC, m.id DESC LIMIT ?`, append(args, q.Limit+1)...)
	if err != nil {
		if strings.Contains(err.Error(), "fts5") {
			return Page{}, valid.Error("that search could not be understood")
		}
		return Page{}, err
	}
	defer rows.Close()
	out := Page{Messages: []Message{}}
	for rows.Next() {
		var count int
		m, err := scanMessage(&withExtra{rows, []any{&count}})
		if err != nil {
			return Page{}, err
		}
		m.ThreadCount = count
		out.Messages = append(out.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(out.Messages) > q.Limit {
		out.Messages = out.Messages[:q.Limit]
		last := out.Messages[q.Limit-1]
		out.Next = fmt.Sprintf("%d:%d", last.ReceivedAt, last.ID)
	}
	s.addLabels(out.Messages)
	s.addJobs(out.Messages)
	return out, nil
}

// withExtra scans extra trailing columns after a message's.
type withExtra struct {
	sc    interface{ Scan(...any) error }
	extra []any
}

func (w *withExtra) Scan(dest ...any) error { return w.sc.Scan(append(dest, w.extra...)...) }

func (s *Service) addLabels(ms []Message) {
	if len(ms) == 0 {
		return
	}
	idx := map[int64]int{}
	ph := make([]string, len(ms))
	args := make([]any, len(ms))
	for i, m := range ms {
		idx[m.ID] = i
		ph[i], args[i] = "?", m.ID
	}
	rows, err := s.db.Query(`SELECT message_id, label_id FROM mail_message_labels WHERE message_id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var l string
		rows.Scan(&id, &l)
		ms[idx[id]].Labels = append(ms[idx[id]].Labels, l)
	}
}

func (s *Service) addJobs(ms []Message) {
	for i := range ms {
		if ms[i].Direction != "out" {
			continue
		}
		if j, err := s.jobFor(ms[i].ID); err == nil {
			ms[i].Outbox = &j
		}
	}
}

// Get is one message the person may read.
func (s *Service) Get(a Access, id int64) (Message, error) {
	m, err := scanMessage(s.db.QueryRow(`SELECT `+msgCols+` FROM mail_messages m WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || err == nil && !a.Mailbox(m.MailboxID, "read") {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	ms := []Message{m}
	s.addLabels(ms)
	s.addJobs(ms)
	return ms[0], nil
}

// Body is a message's content, ready to show.
type Body struct {
	HTML         string       `json:"html"` // sanitized; empty when the message is text only
	Text         string       `json:"text"`
	Attachments  []Attachment `json:"attachments"`
	RemoteImages bool         `json:"remoteImages"` // some images were held back
	Headers      string       `json:"headers,omitempty"`
}

func (s *Service) rawOf(id int64) ([]byte, error) {
	var rel string
	if err := s.db.QueryRow(`SELECT raw_path FROM mail_messages WHERE id = ?`, id).Scan(&rel); err != nil {
		return nil, ErrNotFound
	}
	return s.readFile(rel)
}

// Body reads and cleans a message (access already checked with Get).
func (s *Service) Body(id int64, images, headers bool) (Body, error) {
	raw, err := s.rawOf(id)
	if err != nil {
		return Body{}, err
	}
	p := Parse(raw, true)
	b := Body{Text: p.Text, Attachments: []Attachment{}}
	cid := map[string]string{}
	for i, at := range p.Attachments {
		if at.ContentID != "" && strings.HasPrefix(at.Type, "image/") && at.Type != "image/svg+xml" && at.Size <= 2<<20 {
			cid[at.ContentID] = "data:" + at.Type + ";base64," + base64.StdEncoding.EncodeToString(at.Data)
		}
		if at.Inline && cid[at.ContentID] != "" {
			continue // shown in the message itself
		}
		b.Attachments = append(b.Attachments, Attachment{Part: i, Filename: at.Filename, Type: at.Type, Size: at.Size, ContentID: at.ContentID, Scan: scanState(at.Filename)})
	}
	if p.HTML != "" {
		b.HTML = Sanitize(p.HTML, SanitizeOptions{Images: images, CID: cid})
		b.RemoteImages = !images && strings.Contains(b.HTML, "data-fw-src=")
	}
	if headers {
		if i := strings.Index(string(raw[:min(len(raw), 256<<10)]), "\r\n\r\n"); i > 0 {
			b.Headers = string(raw[:i])
		} else if i := strings.Index(string(raw[:min(len(raw), 256<<10)]), "\n\n"); i > 0 {
			b.Headers = string(raw[:i])
		}
	}
	return b, nil
}

// Attachment is one attached file's bytes.
func (s *Service) Attachment(id int64, part int) (Part, error) {
	raw, err := s.rawOf(id)
	if err != nil {
		return Part{}, err
	}
	p := Parse(raw, true)
	if part < 0 || part >= len(p.Attachments) {
		return Part{}, ErrNotFound
	}
	return p.Attachments[part], nil
}

// Raw is the original message as received (or sent).
func (s *Service) Raw(id int64) ([]byte, error) { return s.rawOf(id) }

// Thread is every message of a conversation the person may read, oldest first
// (without those in Trash, unless trash).
func (s *Service) Thread(a Access, threadID int64, trash bool) ([]Message, error) {
	where, args := a.mailboxFilter("m.mailbox_id")
	if !trash {
		where += " AND m.folder != 'trash'"
	}
	rows, err := s.db.Query(`SELECT `+msgCols+` FROM mail_messages m WHERE m.thread_id = ? AND `+where+` ORDER BY m.sent_at, m.id`, append([]any{threadID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	s.addLabels(out)
	s.addJobs(out)
	return out, rows.Err()
}

// Counts are the unread (inbox, custom folders, spam) and draft numbers for the sidebar.
type Counts struct {
	Inbox   int            `json:"inbox"`
	Spam    int            `json:"spam"`
	Drafts  int            `json:"drafts"`
	Folders map[string]int `json:"folders"` // f:<id> → unread
	Boxes   map[string]int `json:"boxes"`   // address id → unread in inbox
}

func (s *Service) Counts(a Access) (Counts, error) {
	c := Counts{Folders: map[string]int{}, Boxes: map[string]int{}}
	where, args := a.mailboxFilter("mailbox_id")
	rows, err := s.db.Query(`SELECT folder, mailbox_id, COUNT(*) FROM mail_messages WHERE unread = 1 AND `+where+` GROUP BY folder, mailbox_id`, args...)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var f, box string
		var n int
		rows.Scan(&f, &box, &n)
		switch {
		case f == "inbox":
			c.Inbox += n
			c.Boxes[box] += n
		case f == "spam":
			c.Spam += n
		case strings.HasPrefix(f, "f:"):
			c.Folders[f] += n
		}
	}
	rows.Close()
	dw, dargs := a.mailboxFilter("from_id")
	s.db.QueryRow(`SELECT COUNT(*) FROM mail_drafts WHERE queued = 0 AND `+dw, dargs...).Scan(&c.Drafts)
	return c, nil
}

// ---------- actions ----------

// Action changes messages: read, unread, star, unstar, move (Folder), label/unlabel
// (Label), trash, restore, spam, notspam, purge (delete for good: trash and spam only).
type Action struct {
	IDs    []int64 `json:"ids"`
	Action string  `json:"action"`
	Folder string  `json:"folder,omitempty"`
	Label  string  `json:"label,omitempty"`
}

func (s *Service) Act(a Access, in Action) (int, error) {
	if len(in.IDs) == 0 || len(in.IDs) > 500 {
		return 0, valid.Error("pick 1 to 500 messages")
	}
	if in.Action == "move" {
		if in.Folder == "sent" || !systemFolders[in.Folder] && !strings.HasPrefix(in.Folder, "f:") {
			return 0, valid.Error("cannot move there")
		}
		if strings.HasPrefix(in.Folder, "f:") {
			var n int
			s.db.QueryRow(`SELECT COUNT(*) FROM mail_folders WHERE id = ? AND kind = 'folder'`, in.Folder[2:]).Scan(&n)
			if n == 0 {
				return 0, valid.Error("no such folder")
			}
		}
	}
	if in.Action == "label" || in.Action == "unlabel" {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM mail_folders WHERE id = ? AND kind = 'label'`, in.Label).Scan(&n)
		if n == 0 {
			return 0, valid.Error("no such label")
		}
	}
	need := "read"
	if in.Action == "purge" {
		need = "manage"
	}
	now := s.Now().Unix()
	done := 0
	for _, id := range in.IDs {
		var box, folder, prev, from string
		if s.db.QueryRow(`SELECT mailbox_id, folder, prev_folder, from_addr FROM mail_messages WHERE id = ?`, id).Scan(&box, &folder, &prev, &from) != nil || !a.Mailbox(box, need) {
			continue // not found and not allowed look the same
		}
		var err error
		move := func(to string) {
			if to == folder {
				return
			}
			_, err = s.db.Exec(`UPDATE mail_messages SET folder = ?, prev_folder = ?, moved_at = ? WHERE id = ?`, to, folder, now, id)
		}
		back := func() string {
			if prev == "" || prev == "trash" || prev == "spam" {
				return "inbox"
			}
			return prev
		}
		switch in.Action {
		case "read", "unread":
			_, err = s.db.Exec(`UPDATE mail_messages SET unread = ? WHERE id = ?`, in.Action == "unread", id)
		case "star", "unstar":
			_, err = s.db.Exec(`UPDATE mail_messages SET starred = ? WHERE id = ?`, in.Action == "star", id)
		case "move":
			move(in.Folder)
		case "trash":
			move("trash")
		case "spam":
			move("spam")
		case "restore":
			if folder == "trash" || folder == "spam" {
				move(back())
			}
		case "notspam":
			if folder == "spam" {
				move(back())
				s.AddSpamRule(SpamRule{Scope: box, Pattern: from, Action: "allow"})
			}
		case "label":
			_, err = s.db.Exec(`INSERT OR IGNORE INTO mail_message_labels (message_id, label_id) VALUES (?, ?)`, id, in.Label)
		case "unlabel":
			_, err = s.db.Exec(`DELETE FROM mail_message_labels WHERE message_id = ? AND label_id = ?`, id, in.Label)
		case "purge":
			if folder != "trash" && folder != "spam" {
				return done, valid.Error("only mail in Trash or Spam can be deleted for good")
			}
			err = s.purge(id)
		default:
			return done, valid.Error("unknown action")
		}
		if err != nil {
			return done, err
		}
		done++
	}
	if done > 0 {
		s.changed()
	}
	return done, nil
}

// purge deletes a message, its rows and (when no other message shares it) its file.
func (s *Service) purge(id int64) error {
	var rel string
	var thread int64
	if err := s.db.QueryRow(`SELECT raw_path, thread_id FROM mail_messages WHERE id = ?`, id).Scan(&rel, &thread); err != nil {
		return nil
	}
	var state string
	if s.db.QueryRow(`SELECT state FROM mail_outbox WHERE message_id = ?`, id).Scan(&state) == nil && (state == "scheduled" || state == "sending") {
		return valid.Error("this message is still being sent")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM mail_outbox WHERE message_id = ?`, `DELETE FROM mail_fts WHERE rowid = ?`, `DELETE FROM mail_messages WHERE id = ?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	tx.Exec(`DELETE FROM mail_threads WHERE id = ? AND NOT EXISTS (SELECT 1 FROM mail_messages WHERE thread_id = ?)`, thread, thread)
	if err := tx.Commit(); err != nil {
		return err
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE raw_path = ?`, rel).Scan(&n)
	if n == 0 {
		s.removeFile(rel)
	}
	return nil
}

// ---------- folders and labels ----------

func (s *Service) Folders(a Access) ([]Folder, error) {
	rows, err := s.db.Query(`SELECT id, kind, name, color, created_at FROM mail_folders ORDER BY kind, name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	out := []Folder{}
	for rows.Next() {
		var f Folder
		rows.Scan(&f.ID, &f.Kind, &f.Name, &f.Color, &f.CreatedAt)
		out = append(out, f)
	}
	rows.Close()
	where, args := a.mailboxFilter("m.mailbox_id")
	for i, f := range out {
		if f.Kind == "folder" {
			s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(unread), 0) FROM mail_messages m WHERE m.folder = ? AND `+where, append([]any{"f:" + f.ID}, args...)...).Scan(&out[i].Count, &out[i].Unread)
		} else {
			s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(m.unread), 0) FROM mail_message_labels l JOIN mail_messages m ON m.id = l.message_id WHERE l.label_id = ? AND `+where, append([]any{f.ID}, args...)...).Scan(&out[i].Count, &out[i].Unread)
		}
	}
	return out, nil
}

var reColor = regexp.MustCompile(`^(#[0-9a-fA-F]{6})?$`)

func (s *Service) SaveFolder(f Folder) (Folder, error) {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || len(f.Name) > 60 {
		return f, valid.Error("give it a name (60 characters at most)")
	}
	if !reColor.MatchString(f.Color) {
		return f, valid.Error("color must look like #3b82f6")
	}
	if f.ID == "" {
		if f.Kind != "folder" && f.Kind != "label" {
			return f, valid.Error("kind must be folder or label")
		}
		f.ID, f.CreatedAt = newID("emf"), s.Now().Unix()
		_, err := s.db.Exec(`INSERT INTO mail_folders (id, kind, name, color, created_at) VALUES (?, ?, ?, ?, ?)`, f.ID, f.Kind, f.Name, f.Color, f.CreatedAt)
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			return f, valid.Error("that name is taken")
		}
		return f, err
	}
	res, err := s.db.Exec(`UPDATE mail_folders SET name = ?, color = ? WHERE id = ?`, f.Name, f.Color, f.ID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return f, valid.Error("that name is taken")
	}
	if n, _ := res.RowsAffected(); err == nil && n == 0 {
		return f, ErrNotFound
	}
	return f, err
}

// DeleteFolder removes a folder (its mail goes back to the inbox) or a label.
func (s *Service) DeleteFolder(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.Exec(`UPDATE mail_messages SET folder = 'inbox', moved_at = ? WHERE folder = ?`, s.Now().Unix(), "f:"+id)
	if _, err := tx.Exec(`DELETE FROM mail_folders WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- storage and retention ----------

type Usage struct {
	Bytes    int64              `json:"bytes"`
	Messages int                `json:"messages"`
	Domains  map[string]int64   `json:"domains"` // domain id → bytes
	Largest  []Message          `json:"largest"`
	Folders  map[string]int64   `json:"folders"`
	Retain   map[string]Pending `json:"retain"` // trash / spam: what the keep rules would delete now
	Dir      string             `json:"dir"`
}

type Pending struct {
	Messages int   `json:"messages"`
	Bytes    int64 `json:"bytes"`
}

func (s *Service) Usage() (Usage, error) {
	u := Usage{Domains: map[string]int64{}, Folders: map[string]int64{}, Retain: map[string]Pending{}, Largest: []Message{}, Dir: s.dir}
	s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM mail_messages`).Scan(&u.Messages, &u.Bytes)
	rows, err := s.db.Query(`SELECT domain_id, folder, SUM(size) FROM mail_messages GROUP BY domain_id, folder`)
	if err != nil {
		return u, err
	}
	for rows.Next() {
		var d, f string
		var n int64
		rows.Scan(&d, &f, &n)
		u.Domains[d] += n
		if strings.HasPrefix(f, "f:") {
			f = "folders"
		}
		u.Folders[f] += n
	}
	rows.Close()
	lr, err := s.db.Query(`SELECT ` + msgCols + ` FROM mail_messages m ORDER BY m.size DESC LIMIT 10`)
	if err != nil {
		return u, err
	}
	for lr.Next() {
		if m, err := scanMessage(lr); err == nil {
			u.Largest = append(u.Largest, m)
		}
	}
	lr.Close()
	o := s.Options()
	for folder, days := range map[string]int{"trash": o.TrashDays, "spam": o.SpamDays} {
		if days <= 0 {
			continue
		}
		var p Pending
		s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM mail_messages WHERE folder = ? AND moved_at < ?`, folder, s.Now().AddDate(0, 0, -days).Unix()).Scan(&p.Messages, &p.Bytes)
		u.Retain[folder] = p
	}
	return u, nil
}

// Sweep applies the keep rules (Trash and Spam only; nothing else is ever deleted by
// itself), and drops uploads no draft uses anymore.
func (s *Service) Sweep() {
	o := s.Options()
	for folder, days := range map[string]int{"trash": o.TrashDays, "spam": o.SpamDays} {
		if days <= 0 {
			continue
		}
		rows, err := s.db.Query(`SELECT id FROM mail_messages WHERE folder = ? AND moved_at < ? LIMIT 500`, folder, s.Now().AddDate(0, 0, -days).Unix())
		if err != nil {
			continue
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			s.purge(id)
		}
	}
	s.sweepUploads()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
