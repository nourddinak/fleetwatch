// Package notify delivers alerts through pluggable providers (Telegram,
// Discord) and keeps a delivery history.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

// Message is provider-neutral.
type Message struct {
	Title    string
	Body     string
	Severity string // info | warning | critical | ok
	Link     string // deep link into the dashboard, when OPS_PUBLIC_URL is known
	Details  Details
}

// Details are the structured facts behind a message, for machine receivers (webhooks).
type Details struct {
	Event      string `json:"event"`          // incident.opened | incident.resolved | test
	Type       string `json:"type,omitempty"` // incident type key, e.g. app_offline
	TypeLabel  string `json:"typeLabel,omitempty"`
	IncidentID string `json:"incidentId,omitempty"`
	ServerID   string `json:"serverId,omitempty"`
	ServerName string `json:"serverName,omitempty"`
	AppID      string `json:"appId,omitempty"`
	AppName    string `json:"appName,omitempty"`
}

// Provider sends a message. Add a provider by implementing this and registering it in
// Providers.
type Provider interface {
	Send(ctx context.Context, m Message) error
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func post(ctx context.Context, rawURL, contentType string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return redact(err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}

// redact keeps tokens embedded in URLs (Telegram, Discord) out of stored errors.
func redact(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return fmt.Errorf("%s request failed: %v", ue.Op, ue.Err)
	}
	return err
}

// ---------- Telegram ----------

type Telegram struct {
	BotToken string `json:"botToken"`
	ChatID   string `json:"chatId"`
	apiBase  string // tests
}

func (t Telegram) validate() error {
	if !strings.Contains(t.BotToken, ":") || len(t.BotToken) > 100 || strings.ContainsAny(t.BotToken, "/?# ") {
		return valid.Error("Telegram bot token looks like 123456:ABC-DEF… (from @BotFather)")
	}
	if t.ChatID == "" || len(t.ChatID) > 40 {
		return valid.Error("Telegram chat ID is required")
	}
	return nil
}

func (t Telegram) Send(ctx context.Context, m Message) error {
	base := t.apiBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	icon := map[string]string{"critical": "🔴", "warning": "🟠", "ok": "🟢", "info": "🔵"}[m.Severity]
	text := fmt.Sprintf("%s <b>%s</b>\n%s", icon, html.EscapeString(m.Title), html.EscapeString(m.Body))
	if m.Link != "" {
		text += fmt.Sprintf("\n<a href=\"%s\">Open in Fleetwatch</a>", html.EscapeString(m.Link))
	}
	body, _ := json.Marshal(map[string]any{"chat_id": t.ChatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true})
	return post(ctx, base+"/bot"+t.BotToken+"/sendMessage", "application/json", body, nil)
}

// ---------- Discord ----------

type Discord struct {
	Webhook string `json:"webhook"`
}

func (d Discord) validate() error {
	u, err := url.Parse(d.Webhook)
	if err != nil || u.Scheme != "https" || (u.Host != "discord.com" && u.Host != "discordapp.com" && !strings.HasSuffix(u.Host, ".discord.com")) || !strings.HasPrefix(u.Path, "/api/webhooks/") {
		return valid.Error("Discord webhook must be https://discord.com/api/webhooks/…")
	}
	return nil
}

func (d Discord) Send(ctx context.Context, m Message) error {
	color := map[string]int{"critical": 0xE5484D, "warning": 0xF5A524, "ok": 0x30A46C, "info": 0x3E63DD}[m.Severity]
	embed := map[string]any{"title": trunc(m.Title, 250), "description": trunc(m.Body, 3500), "color": color}
	if m.Link != "" {
		embed["url"] = m.Link
	}
	body, _ := json.Marshal(map[string]any{"username": "Fleetwatch", "embeds": []any{embed}, "allowed_mentions": map[string]any{"parse": []string{}}})
	return post(ctx, d.Webhook, "application/json", body, nil)
}

// ---------- custom webhook ----------

// Webhook POSTs a JSON document to any URL. With a secret, the body is signed:
// X-Fleetwatch-Signature: sha256=<hex HMAC-SHA256 of the raw body, keyed by the secret>.
type Webhook struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

func (w Webhook) validate() error {
	u, err := url.Parse(w.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return valid.Error("webhook URL must look like https://example.com/hooks/fleetwatch")
	}
	if len(w.Secret) > 256 {
		return valid.Error("signing secret is too long")
	}
	return nil
}

// WebhookPayload is the documented body of a webhook request.
type WebhookPayload struct {
	Details
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Link     string `json:"link,omitempty"`
	TS       int64  `json:"ts"` // unix milliseconds
}

func (w Webhook) Send(ctx context.Context, m Message) error {
	body, _ := json.Marshal(WebhookPayload{Details: m.Details, Severity: m.Severity, Title: m.Title, Message: m.Body, Link: m.Link, TS: time.Now().UnixMilli()})
	h := map[string]string{"User-Agent": "Fleetwatch", "X-Fleetwatch-Event": m.Details.Event}
	if w.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.Secret))
		mac.Write(body)
		h["X-Fleetwatch-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	return post(ctx, w.URL, "application/json", body, h)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
