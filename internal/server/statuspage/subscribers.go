package statuspage

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"syscall"
	"time"

	"fleetwatch/internal/server/valid"
)

const maxSubscribers = 10000

// Subscriber is one sign-up, as the admin sees it.
type Subscriber struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // email | webhook
	Target     string `json:"target"`
	Confirmed  bool   `json:"confirmed"`
	CreatedAt  int64  `json:"createdAt"`
	LastSentAt int64  `json:"lastSentAt"`
	LastError  string `json:"lastError"`
	token      string
	base       string
}

var ErrNoSubscriber = errors.New("subscription not found")

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// PageURL is where visitors find the page: its own domain, or base + /status/<slug>.
func PageURL(c Config, base string) string {
	if c.Domain != "" {
		return "https://" + c.Domain
	}
	u := strings.TrimRight(base, "/") + "/status"
	if c.Slug != "" {
		u += "/" + c.Slug
	}
	return u
}

func linkBase(c Config, base string) string {
	if c.Domain != "" {
		return "https://" + c.Domain
	}
	return strings.TrimRight(base, "/")
}

// Subscribe signs a visitor up. Email addresses get a confirmation link first;
// webhooks get a test call that must succeed. base is the site the visitor used
// (scheme://host), for the links in messages.
func (s *Service) Subscribe(ctx context.Context, c Config, kind, target, base string) error {
	target = strings.TrimSpace(target)
	switch kind {
	case "email":
		if !c.Subscribe || s.Mail == nil || !s.Mail.ReadyFor(c.Sender) {
			return valid.Error("email updates are not offered on this page")
		}
		a, err := mail.ParseAddress(target)
		if err != nil || a.Address != target || len(target) > 254 {
			return valid.Error("enter a valid email address")
		}
		target = strings.ToLower(target)
	case "webhook":
		if !c.SubscribeHooks {
			return valid.Error("webhooks are not offered on this page")
		}
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(target) > 500 {
			return valid.Error("the webhook must be an https:// URL")
		}
	default:
		return valid.Error("subscribe by email or webhook")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM status_subscribers WHERE page_id = ?`, c.ID).Scan(&n)
	if n >= maxSubscribers {
		return valid.Error("this page cannot take more subscribers")
	}
	var sub Subscriber
	err := s.db.QueryRow(`SELECT id, confirmed, token FROM status_subscribers WHERE page_id = ? AND kind = ? AND target = ?`, c.ID, kind, target).
		Scan(&sub.ID, &sub.Confirmed, &sub.token)
	switch {
	case err == nil && sub.Confirmed:
		return nil // already subscribed: nothing to tell either way
	case errors.Is(err, sql.ErrNoRows):
		sub = Subscriber{ID: "sub_" + token()[:16], token: token()}
	case err != nil:
		return err
	}
	sub.Kind, sub.Target, sub.base = kind, target, base
	if kind == "webhook" {
		if err := s.postHook(ctx, sub, map[string]any{"event": "subscription.created", "page": map[string]string{"title": c.Title, "url": PageURL(c, base)},
			"unsubscribe": linkBase(c, base) + "/api/public/subscribe/remove?t=" + sub.token}); err != nil {
			return valid.Error("the webhook did not answer with success: " + err.Error())
		}
		sub.Confirmed = true
	} else {
		link := linkBase(c, base) + "/api/public/subscribe/confirm?t=" + sub.token
		if err := s.Mail.SendFrom(ctx, c.Sender, Mail{To: []string{target}, Subject: "Confirm updates from " + c.Title,
			Text: "Someone (hopefully you) asked to get updates about " + c.Title + " at this address.\n\nConfirm: " + link +
				"\n\nIf it was not you, ignore this email: nothing will be sent without confirming."}); err != nil {
			slog.Warn("status page: confirmation email failed", "err", err)
			return valid.Error("the confirmation email could not be sent; try again later")
		}
	}
	_, err = s.db.Exec(`INSERT INTO status_subscribers (id, page_id, kind, target, token, base_url, confirmed, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (page_id, kind, target) DO UPDATE SET base_url = excluded.base_url, confirmed = excluded.confirmed`,
		sub.ID, c.ID, kind, target, sub.token, base, sub.Confirmed, s.Now().UnixMilli())
	return err
}

// Confirm activates an email sign-up; it returns the page.
func (s *Service) Confirm(tok string) (Config, error) {
	var pageID string
	if err := s.db.QueryRow(`SELECT page_id FROM status_subscribers WHERE token = ?`, tok).Scan(&pageID); err != nil || tok == "" {
		return Config{}, ErrNoSubscriber
	}
	s.db.Exec(`UPDATE status_subscribers SET confirmed = 1 WHERE token = ?`, tok)
	c, _ := s.Page(pageID)
	return c, nil
}

// Unsubscribe removes the sign-up behind a link.
func (s *Service) Unsubscribe(tok string) (Config, error) {
	var pageID string
	if err := s.db.QueryRow(`SELECT page_id FROM status_subscribers WHERE token = ?`, tok).Scan(&pageID); err != nil || tok == "" {
		return Config{}, ErrNoSubscriber
	}
	s.db.Exec(`DELETE FROM status_subscribers WHERE token = ?`, tok)
	c, _ := s.Page(pageID)
	return c, nil
}

func (s *Service) Subscribers(pageID string) ([]Subscriber, error) {
	rows, err := s.db.Query(`SELECT id, kind, target, confirmed, created_at, last_sent_at, last_error, token, base_url
		FROM status_subscribers WHERE page_id = ? ORDER BY created_at DESC`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscriber{}
	for rows.Next() {
		var x Subscriber
		if err := rows.Scan(&x.ID, &x.Kind, &x.Target, &x.Confirmed, &x.CreatedAt, &x.LastSentAt, &x.LastError, &x.token, &x.base); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Service) RemoveSubscriber(pageID, id string) error {
	res, err := s.db.Exec(`DELETE FROM status_subscribers WHERE page_id = ? AND id = ?`, pageID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSubscriber
	}
	return nil
}

func (s *Service) subscriberCount(pageID string) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM status_subscribers WHERE page_id = ? AND confirmed = 1`, pageID).Scan(&n)
	return n
}

// ---------- delivery ----------

var impactLabel = map[string]string{"info": "Information", "maintenance": "Maintenance", "degraded": "Degraded performance", "outage": "Outage", "resolved": "Resolved"}
var stageLabel = map[string]string{"investigating": "Investigating", "identified": "Identified", "monitoring": "Monitoring", "resolved": "Resolved", "update": "Update"}

// texts is the subject and body of an event, without the per-subscriber footer.
func (e event) texts() (string, string) {
	when := func(ms int64) string { return time.UnixMilli(ms).UTC().Format("2 Jan 2006 15:04 UTC") }
	var b strings.Builder
	switch e.kind {
	case "status.changed":
		subject := e.page.Title + ": "
		if len(e.down) > 0 {
			subject += strings.Join(e.down, ", ") + " down"
			fmt.Fprintf(&b, "Not available now: %s\n", strings.Join(e.down, ", "))
		}
		if len(e.up) > 0 {
			if len(e.down) > 0 {
				subject += "; "
			}
			subject += strings.Join(e.up, ", ") + " back up"
			fmt.Fprintf(&b, "Working again: %s\n", strings.Join(e.up, ", "))
		}
		return subject, b.String()
	case "test":
		return e.page.Title + ": test message", "This is a test message from the status page " + e.page.Title + ".\n"
	}
	n := e.notice
	subject := e.page.Title + ": " + n.Title
	if e.update != nil {
		subject = e.page.Title + ": " + stageLabel[e.update.Stage] + " – " + n.Title
		fmt.Fprintf(&b, "%s – %s\n\n%s\n", stageLabel[e.update.Stage], when(e.update.At), e.update.Body)
		return subject, b.String()
	}
	fmt.Fprintf(&b, "%s\n%s", impactLabel[n.Status], when(n.Start))
	if n.End != 0 {
		fmt.Fprintf(&b, " – %s", when(n.End))
	}
	b.WriteString("\n\n")
	if n.Body != "" {
		b.WriteString(n.Body + "\n\n")
	}
	if len(n.Updates) > 0 {
		b.WriteString(n.Updates[len(n.Updates)-1].Body + "\n")
	}
	return subject, b.String()
}

func (s *Service) deliver(ctx context.Context, e event) {
	subs, err := s.Subscribers(e.page.ID)
	if err != nil {
		return
	}
	subject, text := e.texts()
	for _, sub := range subs {
		if !sub.Confirmed || (e.only != nil && e.only.ID != sub.ID) {
			continue
		}
		unsub := linkBase(e.page, sub.base) + "/api/public/subscribe/remove?t=" + sub.token
		var err error
		if sub.Kind == "email" {
			if s.Mail == nil {
				continue
			}
			err = s.Mail.SendFrom(ctx, e.page.Sender, Mail{To: []string{sub.Target}, Subject: subject,
				Text:    text + "\nStatus page: " + PageURL(e.page, sub.base) + "\nStop these emails: " + unsub + "\n",
				Headers: map[string]string{"List-Unsubscribe": "<" + unsub + ">", "List-Unsubscribe-Post": "List-Unsubscribe=One-Click"}})
		} else {
			payload := map[string]any{"event": e.kind, "page": map[string]string{"title": e.page.Title, "url": PageURL(e.page, sub.base)},
				"subject": subject, "text": strings.TrimSpace(text), "at": s.Now().UnixMilli(), "unsubscribe": unsub}
			if e.notice != nil {
				payload["notice"] = map[string]any{"title": e.notice.Title, "status": e.notice.Status, "start": e.notice.Start, "end": e.notice.End}
			}
			if e.update != nil {
				payload["update"] = e.update
			}
			if e.kind == "status.changed" {
				payload["down"], payload["up"] = e.down, e.up
			}
			err = s.postHook(ctx, sub, payload)
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		s.db.Exec(`UPDATE status_subscribers SET last_sent_at = ?, last_error = ? WHERE id = ?`, s.Now().UnixMilli(), msg, sub.ID)
	}
}

// SendTest sends a test message to one subscriber.
func (s *Service) SendTest(pageID, id string) error {
	c, ok := s.Page(pageID)
	if !ok {
		return ErrNotFound
	}
	subs, _ := s.Subscribers(pageID)
	for i := range subs {
		if subs[i].ID == id {
			s.send(event{kind: "test", page: c, only: &subs[i]})
			return nil
		}
	}
	return ErrNoSubscriber
}

// ---------- webhooks ----------

// publicOnly keeps webhooks (URLs typed by visitors) away from this server's own
// networks; tests turn it off to reach their local server.
var publicOnly = true

var errPrivate = errors.New("that address is not on the public internet")

var sharedNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

var hookClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second, DialContext: (&net.Dialer{Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, _ := net.SplitHostPort(address)
			ip := net.ParseIP(host)
			if publicOnly && (ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || sharedNAT.Contains(ip)) {
				return errPrivate
			}
			return nil
		}}).DialContext},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (s *Service) postHook(ctx context.Context, sub Subscriber, payload any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Fleetwatch-Status/1")
	res, err := hookClient.Do(req)
	if err != nil {
		if errors.Is(err, errPrivate) {
			return errPrivate
		}
		return errors.New("could not reach it")
	}
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}
