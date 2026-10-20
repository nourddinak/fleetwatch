package statuspage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

const smtpKey = "smtp"

// SMTPConfig is how emails leave this server: any provider's SMTP (Gmail, Brevo,
// Resend, Mailgun, your own). The password is stored encrypted.
type SMTPConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"` // starttls | tls | none
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"` // write-only
	HasPassword bool   `json:"hasPassword"`        // read-only
	From        string `json:"from"`
	FromName    string `json:"fromName"`
}

// Sender is one way out for emails (Integrations → Email): status pages pick one, and
// the default sends everything else (on-call emails, pages without their own).
type Sender struct {
	ID      string `json:"id"`
	Label   string `json:"label"` // how people know it: "Acme support", "Gmail"
	Default bool   `json:"default"`
	SMTPConfig
}

type storedSender struct {
	Sender
	Sealed []byte `json:"sealed,omitempty"`
}

const sendersKey = "smtp.senders"

// Mailer sends emails through the saved senders.
type Mailer struct {
	db      *sql.DB
	box     *secret.Box
	mu      sync.Mutex
	senders []storedSender
	// dial is replaced in tests.
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

func NewMailer(d *sql.DB, box *secret.Box) *Mailer {
	m := &Mailer{db: d, box: box}
	if settings.Load(d, sendersKey, &m.senders) != nil || m.senders == nil {
		var old struct { // before several senders: the one SMTP setting becomes the default
			SMTPConfig
			Sealed []byte `json:"sealed,omitempty"`
		}
		if settings.Load(d, smtpKey, &old) == nil && old.Host != "" {
			m.senders = []storedSender{{Sender: Sender{ID: "default", Label: "Main", Default: true, SMTPConfig: old.SMTPConfig}, Sealed: old.Sealed}}
			settings.Save(d, sendersKey, m.senders)
		}
	}
	m.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	return m
}

// Ready reports whether email can be sent (through the default sender).
func (m *Mailer) Ready() bool { return m.ReadyFor("") }

// ReadyFor reports whether a sender ("" = the default) can send.
func (m *Mailer) ReadyFor(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.pick(id)
	return ok && s.Host != "" && s.From != ""
}

// pick is a sender by id, or the default ("" or a sender that is gone). Lock held.
func (m *Mailer) pick(id string) (storedSender, bool) {
	var def *storedSender
	for i := range m.senders {
		if id != "" && m.senders[i].ID == id {
			return m.senders[i], true
		}
		if m.senders[i].Default && def == nil {
			def = &m.senders[i]
		}
	}
	if def == nil && len(m.senders) > 0 {
		def = &m.senders[0]
	}
	if def == nil {
		return storedSender{}, false
	}
	return *def, true
}

// Senders are all of them, without their passwords.
func (m *Mailer) Senders() []Sender {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Sender{}
	for _, s := range m.senders {
		x := s.Sender
		x.Password, x.HasPassword = "", len(s.Sealed) > 0
		out = append(out, x)
	}
	return out
}

// Config is the default sender's settings (the dashboard's email before several senders).
func (m *Mailer) Config() SMTPConfig {
	for _, s := range m.Senders() {
		if s.Default {
			return s.SMTPConfig
		}
	}
	return SMTPConfig{}
}

// Save sets the default sender's settings (made if there is none).
func (m *Mailer) Save(c SMTPConfig) (SMTPConfig, error) {
	m.mu.Lock()
	def, ok := m.pick("")
	m.mu.Unlock()
	in := Sender{Label: "Main", Default: true, SMTPConfig: c}
	if ok {
		in.ID, in.Label = def.ID, def.Label
	}
	out, err := m.SaveSender(in)
	return out.SMTPConfig, err
}

// SaveSender adds (no id) or changes a sender. An empty password keeps the saved one.
func (m *Mailer) SaveSender(in Sender) (Sender, error) {
	c := in.SMTPConfig
	c.Host, c.Username, c.From, c.FromName = strings.TrimSpace(c.Host), strings.TrimSpace(c.Username), strings.TrimSpace(c.From), strings.TrimSpace(c.FromName)
	in.Label = strings.TrimSpace(in.Label)
	switch {
	case c.Host == "" || strings.ContainsAny(c.Host, " /:") || len(c.Host) > 253:
		return in, valid.Error("SMTP server must be a host name like smtp.example.com")
	case c.Port <= 0 || c.Port > 65535:
		return in, valid.Error("SMTP port must be 1-65535 (usually 587 or 465)")
	case c.Security != "starttls" && c.Security != "tls" && c.Security != "none":
		return in, valid.Error("security must be starttls, tls or none")
	case len(in.Label) > 60:
		return in, valid.Error("the name is too long")
	}
	if a, err := mail.ParseAddress(c.From); err != nil || a.Address != c.From {
		return in, valid.Error("the sender must be a plain email address, like status@example.com")
	}
	if len(c.FromName) > 80 || strings.ContainsAny(c.FromName, "\r\n") || len(c.Username) > 200 || len(c.Password) > 500 {
		return in, valid.Error("sender name, username or password is too long")
	}
	if in.Label == "" {
		in.Label = c.From
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i := -1
	for k := range m.senders {
		if in.ID != "" && m.senders[k].ID == in.ID {
			i = k
		}
	}
	if i < 0 {
		if in.ID != "" {
			return in, valid.Error("no such sender")
		}
		if len(m.senders) >= 20 {
			return in, valid.Error("20 senders at most")
		}
		in.ID = "snd_" + newID()[3:]
		m.senders = append(m.senders, storedSender{})
		i = len(m.senders) - 1
	}
	st := storedSender{Sender: Sender{ID: in.ID, Label: in.Label, Default: in.Default || len(m.senders) == 1, SMTPConfig: c}, Sealed: m.senders[i].Sealed}
	if c.Password != "" {
		st.Sealed = m.box.Seal([]byte(c.Password), "smtp.password")
	}
	if c.Username == "" {
		st.Sealed = nil
	}
	st.Password, st.HasPassword = "", false
	m.senders[i] = st
	if st.Default {
		for k := range m.senders {
			m.senders[k].Default = k == i
		}
	}
	if err := settings.Save(m.db, sendersKey, m.senders); err != nil {
		return in, err
	}
	out := st.Sender
	out.HasPassword = len(st.Sealed) > 0
	return out, nil
}

// DeleteSender removes one; another becomes the default if it was.
func (m *Mailer) DeleteSender(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.senders {
		if m.senders[k].ID == id {
			was := m.senders[k].Default
			m.senders = append(m.senders[:k], m.senders[k+1:]...)
			if was && len(m.senders) > 0 {
				m.senders[0].Default = true
			}
			return settings.Save(m.db, sendersKey, m.senders)
		}
	}
	return valid.Error("no such sender")
}

// Attachment is a file sent with an email.
type Attachment struct {
	Name, Type string
	Data       []byte
}

// Mail is one message.
type Mail struct {
	To       []string
	Subject  string
	Text     string
	Headers  map[string]string // e.g. List-Unsubscribe
	Attached *Attachment
}

// Send sends through the default sender.
func (m *Mailer) Send(ctx context.Context, msg Mail) error { return m.SendFrom(ctx, "", msg) }

// SendFrom sends through a sender ("" or one that is gone = the default).
func (m *Mailer) SendFrom(ctx context.Context, sender string, msg Mail) error {
	m.mu.Lock()
	cfg, ok := m.pick(sender)
	m.mu.Unlock()
	if !ok || cfg.Host == "" || cfg.From == "" {
		return valid.Error("email is not set up: add a sender in Integrations → Email")
	}
	pass := ""
	if len(cfg.Sealed) > 0 {
		p, err := m.box.Open(cfg.Sealed, "smtp.password")
		if err != nil {
			return fmt.Errorf("SMTP password cannot be read: %w", err)
		}
		pass = string(p)
	}
	body, err := buildMessage(cfg.SMTPConfig, msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	conn, err := m.dial(ctx, addr)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.Security == "tls" {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS with %s failed: %w", addr, err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP: %w", err)
	}
	defer c.Close()
	if cfg.Security == "starttls" {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, pass, cfg.Host)); err != nil {
			return fmt.Errorf("SMTP sign-in failed: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("SMTP refused the sender: %w", err)
	}
	for _, to := range msg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("SMTP refused %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP did not accept the message: %w", err)
	}
	return c.Quit()
}

func clean(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

func buildMessage(cfg SMTPConfig, msg Mail) ([]byte, error) {
	var b bytes.Buffer
	from := (&mail.Address{Name: cfg.FromName, Address: cfg.From}).String()
	id := make([]byte, 12)
	rand.Read(id)
	domain := cfg.From[strings.LastIndexByte(cfg.From, '@')+1:]
	head := textproto.MIMEHeader{}
	head.Set("From", from)
	head.Set("To", strings.Join(msg.To, ", "))
	head.Set("Subject", mime.QEncoding.Encode("utf-8", clean(msg.Subject)))
	head.Set("Date", time.Now().Format(time.RFC1123Z))
	head.Set("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	head.Set("MIME-Version", "1.0")
	head.Set("Auto-Submitted", "auto-generated")
	for k, v := range msg.Headers {
		head.Set(k, clean(v))
	}
	text := func(w *bytes.Buffer) {
		q := quotedprintable.NewWriter(w)
		q.Write([]byte(strings.ReplaceAll(msg.Text, "\n", "\r\n")))
		q.Close()
	}
	if msg.Attached == nil {
		head.Set("Content-Type", "text/plain; charset=utf-8")
		head.Set("Content-Transfer-Encoding", "quoted-printable")
		writeHead(&b, head)
		text(&b)
		return b.Bytes(), nil
	}
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	tw, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
	var t bytes.Buffer
	text(&t)
	tw.Write(t.Bytes())
	aw, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {msg.Attached.Type},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": msg.Attached.Name})},
	})
	enc := base64.StdEncoding.EncodeToString(msg.Attached.Data)
	for len(enc) > 76 {
		aw.Write([]byte(enc[:76] + "\r\n"))
		enc = enc[76:]
	}
	aw.Write([]byte(enc + "\r\n"))
	mw.Close()
	head.Set("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	writeHead(&b, head)
	b.Write(parts.Bytes())
	return b.Bytes(), nil
}

func writeHead(b *bytes.Buffer, h textproto.MIMEHeader) {
	for _, k := range []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Auto-Submitted", "List-Unsubscribe", "List-Unsubscribe-Post", "Content-Type", "Content-Transfer-Encoding"} {
		if v := h.Get(k); v != "" {
			fmt.Fprintf(b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
}

// parseAddresses reads a comma-separated list of email addresses.
func parseAddresses(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		a, err := mail.ParseAddress(part)
		if err != nil || len(a.Address) > 254 {
			return nil, valid.Error(fmt.Sprintf("%q is not an email address", part))
		}
		out = append(out, a.Address)
	}
	if len(out) == 0 || len(out) > 20 {
		return nil, valid.Error("enter 1 to 20 email addresses, separated by commas")
	}
	return out, nil
}
