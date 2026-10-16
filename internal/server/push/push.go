// Package push sends Web Push notifications to browsers and installed copies of the
// dashboard (Android, desktop, iOS 16.4+ home-screen apps), without a third-party app.
package push

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/valid"
)

const keyAD = "push.vapid"

// Message is what the service worker shows.
type Message struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	URL      string `json:"url,omitempty"`      // opened on tap
	Tag      string `json:"tag,omitempty"`      // a newer message with the same tag replaces the older
	Severity string `json:"severity,omitempty"` // critical stays on screen until dismissed
}

// Device is a subscribed browser as the UI lists it.
type Device struct {
	ID        string `json:"id"`
	Device    string `json:"device"`
	CreatedAt int64  `json:"createdAt"`
	LastOKAt  int64  `json:"lastOkAt"`
	LastError string `json:"lastError"`
	Endpoint  string `json:"-"`
}

type Service struct {
	db  *sql.DB
	box *secret.Box
	// Subscriber identifies the sender to push services (https URL or mailto:).
	Subscriber string
	Now        func() time.Time
	client     webpush.HTTPClient

	mu         sync.Mutex
	pub, priv  string
	keysLoaded bool
}

func New(d *sql.DB, box *secret.Box) *Service {
	return &Service{db: d, box: box, Subscriber: "mailto:fleetwatch@localhost", Now: time.Now, client: &http.Client{Timeout: 20 * time.Second}}
}

// keys returns the server's VAPID key pair, created on first use. The private key is
// stored encrypted; changing it would invalidate every subscription.
func (s *Service) keys() (pub, priv string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keysLoaded {
		return s.pub, s.priv, nil
	}
	var raw string
	err = s.db.QueryRow(`SELECT value FROM settings WHERE key = 'pushKeys'`).Scan(&raw)
	switch {
	case err == nil:
		var stored struct{ Public, Private string }
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return "", "", err
		}
		enc, err := base64.StdEncoding.DecodeString(stored.Private)
		if err != nil {
			return "", "", err
		}
		plain, err := s.box.Open(enc, keyAD)
		if err != nil {
			return "", "", fmt.Errorf("decrypt push key: %w", err)
		}
		s.pub, s.priv = stored.Public, string(plain)
	case errors.Is(err, sql.ErrNoRows):
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return "", "", err
		}
		stored, _ := json.Marshal(map[string]string{"Public": pub, "Private": base64.StdEncoding.EncodeToString(s.box.Seal([]byte(priv), keyAD))})
		if _, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES ('pushKeys', ?)`, string(stored)); err != nil {
			return "", "", err
		}
		s.pub, s.priv = pub, priv
	default:
		return "", "", err
	}
	s.keysLoaded = true
	return s.pub, s.priv, nil
}

// PublicKey is the applicationServerKey browsers subscribe with.
func (s *Service) PublicKey() (string, error) {
	pub, _, err := s.keys()
	return pub, err
}

func decodedLen(v string) int {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
	if err != nil {
		return -1
	}
	return len(b)
}

// Subscribe stores (or refreshes) a browser subscription.
func (s *Service) Subscribe(endpoint, p256dh, auth, device, memberID string) (Device, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(endpoint) > 1024 {
		return Device{}, valid.Error("push endpoint must be an https URL")
	}
	if decodedLen(p256dh) != 65 || decodedLen(auth) != 16 {
		return Device{}, valid.Error("invalid push subscription keys")
	}
	if len(device) > 300 {
		device = device[:300]
	}
	b := make([]byte, 6)
	rand.Read(b)
	d := Device{ID: "dev_" + base64.RawURLEncoding.EncodeToString(b), Device: device, CreatedAt: s.Now().UnixMilli(), Endpoint: endpoint}
	_, err = s.db.Exec(`INSERT INTO push_subscriptions (id, endpoint, p256dh, auth, device, created_at, member_id) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, device = excluded.device, member_id = excluded.member_id`,
		d.ID, endpoint, p256dh, auth, device, d.CreatedAt, memberID)
	return d, err
}

// Unsubscribe forgets one of a person's devices by id or by endpoint.
func (s *Service) Unsubscribe(idOrEndpoint, memberID string) error {
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE (id = ? OR endpoint = ?) AND member_id = ?`, idOrEndpoint, idOrEndpoint, memberID)
	return err
}

// Devices lists one person's devices (empty ID = the owner's).
func (s *Service) Devices(memberID string) ([]Device, error) {
	rows, err := s.db.Query(`SELECT id, device, created_at, last_ok_at, last_error, endpoint FROM push_subscriptions WHERE member_id = ? ORDER BY created_at`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Device, &d.CreatedAt, &d.LastOKAt, &d.LastError, &d.Endpoint); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Count is the number of a person's devices (empty ID = the owner's).
func (s *Service) Count(memberID string) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE member_id = ?`, memberID).Scan(&n)
	return n
}

// Send delivers m to the owner's devices: the "push" notification channel.
func (s *Service) Send(ctx context.Context, m Message) error { return s.SendTo(ctx, "", m) }

// SendTo delivers m to one person's devices. Devices the push service reports as
// gone (404/410) are removed. It fails only when no device received the message.
func (s *Service) SendTo(ctx context.Context, memberID string, m Message) error {
	pub, priv, err := s.keys()
	if err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT id, endpoint, p256dh, auth FROM push_subscriptions WHERE member_id = ?`, memberID)
	if err != nil {
		return err
	}
	type sub struct{ id, endpoint, p256dh, auth string }
	var subs []sub
	for rows.Next() {
		var x sub
		if rows.Scan(&x.id, &x.endpoint, &x.p256dh, &x.auth) == nil {
			subs = append(subs, x)
		}
	}
	rows.Close()
	if len(subs) == 0 {
		return errors.New("no device has push notifications turned on")
	}
	payload, _ := json.Marshal(m)
	urgency := webpush.UrgencyNormal
	if m.Severity == "critical" {
		urgency = webpush.UrgencyHigh
	}
	ok, lastErr := 0, ""
	for _, x := range subs {
		res, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{Endpoint: x.endpoint, Keys: webpush.Keys{P256dh: x.p256dh, Auth: x.auth}},
			&webpush.Options{HTTPClient: s.client, Subscriber: s.Subscriber, VAPIDPublicKey: pub, VAPIDPrivateKey: priv, TTL: 24 * 3600, Urgency: urgency})
		status := 0
		if res != nil {
			status = res.StatusCode
			io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			res.Body.Close()
		}
		switch {
		case err == nil && status >= 200 && status < 300:
			ok++
			s.db.Exec(`UPDATE push_subscriptions SET last_ok_at = ?, last_error = '' WHERE id = ?`, s.Now().UnixMilli(), x.id)
		case status == http.StatusNotFound || status == http.StatusGone:
			s.db.Exec(`DELETE FROM push_subscriptions WHERE id = ?`, x.id) // unsubscribed or expired on the device
		default:
			msg := fmt.Sprintf("HTTP %d", status)
			if err != nil {
				msg = "could not reach the push service"
			}
			lastErr = msg
			s.db.Exec(`UPDATE push_subscriptions SET last_error = ? WHERE id = ?`, msg, x.id)
		}
	}
	if ok == 0 {
		if lastErr == "" {
			lastErr = "every device has unsubscribed"
		}
		return errors.New("push failed: " + lastErr)
	}
	return nil
}
