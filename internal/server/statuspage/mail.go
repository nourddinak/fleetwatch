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

type storedSMTP struct {
	SMTPConfig
	Sealed []byte `json:"sealed,omitempty"`
}

// Mailer sends emails with the saved SMTP settings.
type Mailer struct {
	db  *sql.DB
	box *secret.Box
	mu  sync.Mutex
	cfg storedSMTP
	// dial is replaced in tests.
	dial func(ctx context.Context, addr string) (net.Conn, error)
}

func NewMailer(d *sql.DB, box *secret.Box) *Mailer {
	m := &Mailer{db: d, box: box}
	settings.Load(d, smtpKey, &m.cfg)
	m.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	return m
}

// Ready reports whether email can be sent.
func (m *Mailer) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Host != "" && m.cfg.From != ""
}

func (m *Mailer) Config() SMTPConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cfg.SMTPConfig
	c.Password, c.HasPassword = "", len(m.cfg.Sealed) > 0
	return c
}

// Save validates and stores the settings; an empty password keeps the saved one, and
// an empty host turns email off.
func (m *Mailer) Save(c SMTPConfig) (SMTPConfig, error) {
	c.Host, c.Username, c.From, c.FromName = strings.TrimSpace(c.Host), strings.TrimSpace(c.Username), strings.TrimSpace(c.From), strings.TrimSpace(c.FromName)
	if c.Host != "" {
		if strings.ContainsAny(c.Host, " /:") || len(c.Host) > 253 {
			return c, valid.Error("SMTP server must be a host name like smtp.example.com")
		}
		if c.Port <= 0 || c.Port > 65535 {
			return c, valid.Error("SMTP port must be 1-65535 (usually 587 or 465)")
		}
		if c.Security != "starttls" && c.Security != "tls" && c.Security != "none" {
			return c, valid.Error("security must be starttls, tls or none")
		}
		if a, err := mail.ParseAddress(c.From); err != nil || a.Address != c.From {
			return c, valid.Error("the sender must be a plain email address, like status@example.com")
		}
		if len(c.FromName) > 80 || strings.ContainsAny(c.FromName, "\r\n") || len(c.Username) > 200 || len(c.Password) > 500 {
			return c, valid.Error("sender name, username or password is too long")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := storedSMTP{SMTPConfig: c, Sealed: m.cfg.Sealed}
	if c.Password != "" {
		st.Sealed = m.box.Seal([]byte(c.Password), "smtp.password")
	}
	if c.Username == "" {
		st.Sealed = nil
	}
	st.Password, st.HasPassword = "", false
	if err := settings.Save(m.db, smtpKey, st); err != nil {
		return c, err
	}
	m.cfg = st
	out := st.SMTPConfig
	out.HasPassword = len(st.Sealed) > 0
	return out, nil
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

func (m *Mailer) Send(ctx context.Context, msg Mail) error {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	if cfg.Host == "" || cfg.From == "" {
		return valid.Error("email is not set up: add your SMTP settings under Status pages → Email sending")
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
