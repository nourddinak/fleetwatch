package statuspage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

// Sending it yourself (free, no email company): the dashboard signs each email with
// its own DKIM key and delivers it straight to the recipients' mail servers (port 25).
// The domain's DNS says the key and the server's IP are allowed: a DKIM record and SPF
// on a subdomain (fwmail.<domain>) used as the bounce address, so existing mail and
// SPF records of the domain stay untouched.

const (
	DirectSelector = "fleetwatch"
	directBounce   = "fwmail" // envelope sender subdomain: bounce@fwmail.<domain>
)

// DirectPlan is what sending from a domain yourself needs in its DNS.
type DirectPlan struct {
	Records []ResendRecord `json:"records"` // same shape as Resend's: type, name, value
	KeyPEM  string         `json:"-"`
}

// PlanDirect makes a DKIM key for a domain, and the records that allow ip to send.
func PlanDirect(ip string) (DirectPlan, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return DirectPlan{}, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return DirectPlan{}, err
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: must(x509.MarshalPKCS8PrivateKey(key))})
	return DirectPlan{KeyPEM: string(priv), Records: []ResendRecord{
		{Record: "DKIM", Type: "TXT", Name: DirectSelector + "._domainkey", Value: "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pub)},
		{Record: "SPF", Type: "TXT", Name: directBounce, Value: "v=spf1 ip4:" + ip + " -all"},
	}}, nil
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err) // only for a key just made
	}
	return b
}

// Lookups and dialing of mail servers; tests replace them.
var (
	lookupMX = net.DefaultResolver.LookupMX
	dialMX   = func(ctx context.Context, host string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp4", net.JoinHostPort(host, "25")) // IPv4: the IP in SPF
	}
)

// sendDirect signs a message and hands it to each recipient domain's mail server.
func (m *Mailer) sendDirect(ctx context.Context, cfg storedSender, keyPEM []byte, msg Mail) error {
	body, err := buildMessage(cfg.SMTPConfig, msg)
	if err != nil {
		return err
	}
	_, domain, _ := strings.Cut(cfg.From, "@")
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return errors.New("the signing key cannot be read")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	var signed bytes.Buffer
	if err := dkim.Sign(&signed, bytes.NewReader(body), &dkim.SignOptions{Domain: domain, Selector: DirectSelector, Signer: key.(*rsa.PrivateKey),
		HeaderCanonicalization: dkim.CanonicalizationRelaxed, BodyCanonicalization: dkim.CanonicalizationRelaxed}); err != nil {
		return fmt.Errorf("signing: %w", err)
	}
	byDomain := map[string][]string{}
	for _, to := range msg.To {
		if _, d, ok := strings.Cut(to, "@"); ok {
			byDomain[strings.ToLower(d)] = append(byDomain[strings.ToLower(d)], to)
		}
	}
	helo := m.HeloName
	if helo == "" {
		helo = directBounce + "." + domain
	}
	for d, rcpts := range byDomain {
		if err := deliver(ctx, helo, "bounce@"+directBounce+"."+domain, d, rcpts, signed.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// deliver hands a message to a domain's mail servers, best first.
func deliver(ctx context.Context, helo, from, domain string, rcpts []string, data []byte) error {
	mxs, err := lookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		mxs = []*net.MX{{Host: domain, Pref: 0}} // no MX: the domain itself (RFC 5321)
	}
	sort.Slice(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
	var last error
	for _, mx := range mxs {
		host := strings.TrimSuffix(mx.Host, ".")
		if last = deliverTo(ctx, host, helo, from, rcpts, data); last == nil {
			return nil
		}
	}
	return fmt.Errorf("%s: %w", domain, last)
}

func deliverTo(ctx context.Context, host, helo, from string, rcpts []string, data []byte) error {
	conn, err := dialMX(ctx, host)
	if err != nil {
		return fmt.Errorf("cannot reach %s on port 25 (is outgoing port 25 blocked by your server's provider?)", host)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello(helo); err != nil {
		return err
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("%s: TLS: %w", host, err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("%s refused the sender: %w", host, err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("%s refused %s: %w", host, r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("%s did not take the message: %w", host, err)
	}
	return c.Quit()
}

// DirectReady is whether this server can send itself: its public IPv4, whether port 25
// goes out, and the name its IP points back to (mail servers like it to be set).
type DirectReady struct {
	IP     string `json:"ip"`
	Port25 bool   `json:"port25"`
	PTR    string `json:"ptr"`
}

var publicIPURLs = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com"}

func CheckDirect(ctx context.Context) DirectReady {
	var r DirectReady
	for _, u := range publicIPURLs {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req); err == nil {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 64))
			res.Body.Close()
			if ip := net.ParseIP(strings.TrimSpace(string(b))); ip != nil && ip.To4() != nil {
				r.IP = ip.String()
				break
			}
		}
	}
	if c, err := dialMX(ctx, "gmail-smtp-in.l.google.com"); err == nil {
		r.Port25 = true
		c.Close()
	}
	if r.IP != "" {
		if names, err := net.DefaultResolver.LookupAddr(ctx, r.IP); err == nil && len(names) > 0 {
			r.PTR = strings.TrimSuffix(names[0], ".")
		}
	}
	return r
}

var lookupTXT = net.DefaultResolver.LookupTXT

// DirectCheck looks up a direct sender's records in public DNS, in the shape of a
// Resend domain (verified when both are there).
func (m *Mailer) DirectCheck(ctx context.Context, id string) (ResendDomain, error) {
	m.mu.Lock()
	s, ok := m.pick(id)
	m.mu.Unlock()
	if !ok || s.ID != id || !s.Direct {
		return ResendDomain{}, errors.New("this sender does not send itself")
	}
	keyPEM, err := m.box.Open(s.Sealed, "smtp.password")
	if err != nil {
		return ResendDomain{}, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return ResendDomain{}, errors.New("the signing key cannot be read")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return ResendDomain{}, err
	}
	pub := base64.StdEncoding.EncodeToString(must(x509.MarshalPKIXPublicKey(&key.(*rsa.PrivateKey).PublicKey)))
	_, domain, _ := strings.Cut(s.From, "@")
	d := ResendDomain{Name: domain, Status: "verified"}
	check := func(record, name, want string, ok func(string) bool) {
		r := ResendRecord{Record: record, Type: "TXT", Name: name, Value: want, Status: "pending"}
		if txts, err := lookupTXT(ctx, name+"."+domain); err == nil {
			for _, t := range txts {
				if ok(t) {
					r.Status = "verified"
				}
			}
		}
		if r.Status != "verified" {
			d.Status = "pending"
		}
		d.Records = append(d.Records, r)
	}
	check("DKIM", DirectSelector+"._domainkey", "v=DKIM1; k=rsa; p="+pub, func(t string) bool { return strings.Contains(strings.ReplaceAll(t, " ", ""), "p="+pub) })
	check("SPF", directBounce, "v=spf1 ip4:<this server> -all", func(t string) bool { return strings.HasPrefix(t, "v=spf1") && strings.Contains(t, "ip4:") })
	return d, nil
}
