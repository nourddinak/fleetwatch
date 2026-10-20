package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/mail"
	"slices"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

// Personal channels: a team member's own Telegram, Discord, webhook or email for their
// own alerts. Which alerts reach them is decided by Allows (their servers and their
// "My phone alerts" rules), after the dashboard's rules let the alert through.

var personalProviders = []string{"telegram", "discord", "webhook", "email"}

// Personal is one person's channel, as their settings show it (secrets only as set or not).
type Personal struct {
	Provider   string            `json:"provider"`
	Enabled    bool              `json:"enabled"`
	Configured bool              `json:"configured"`
	Fields     map[string]string `json:"fields"`
	Secrets    map[string]bool   `json:"secrets"`
}

func personalKey(person, provider string) string { return "notify.me." + person + "." + provider }

func (s *Service) loadPersonal(person, provider string) (bool, map[string]string) {
	var enabled bool
	var enc []byte
	cfg := map[string]string{}
	if s.db.QueryRow(`SELECT enabled, config_enc FROM personal_channels WHERE person = ? AND provider = ?`, person, provider).Scan(&enabled, &enc) != nil {
		return false, cfg
	}
	if raw, err := s.box.Open(enc, personalKey(person, provider)); err == nil {
		json.Unmarshal(raw, &cfg)
	}
	return enabled, cfg
}

// PersonalChannels are someone's channels ("" = the owner).
func (s *Service) PersonalChannels(person string) []Personal {
	out := []Personal{}
	for _, p := range personalProviders {
		enabled, cfg := s.loadPersonal(person, p)
		v := Personal{Provider: p, Enabled: enabled, Configured: len(cfg) > 0, Fields: map[string]string{}, Secrets: map[string]bool{}}
		if p == "email" {
			v.Fields["to"] = cfg["to"]
		}
		for _, f := range plainFields[p] {
			v.Fields[f] = cfg[f]
		}
		for _, f := range secretFields[p] {
			v.Secrets[f] = cfg[f] != ""
		}
		out = append(out, v)
	}
	return out
}

// SavePersonal sets someone's channel. Empty secret fields keep their stored value.
func (s *Service) SavePersonal(person, provider string, enabled bool, fields map[string]string) error {
	allowed := map[string]bool{}
	for _, f := range append(append([]string{}, plainFields[provider]...), secretFields[provider]...) {
		allowed[f] = true
	}
	switch provider {
	case "email":
		allowed = map[string]bool{"to": true}
	case "telegram", "discord", "webhook":
	default:
		return ErrUnknownProvider
	}
	_, cfg := s.loadPersonal(person, provider)
	for k, v := range fields {
		if !allowed[k] {
			return valid.Error("unknown field " + k)
		}
		v = strings.TrimSpace(v)
		if v == "" && slices.Contains(secretFields[provider], k) {
			continue // keep the saved secret
		}
		cfg[k] = v
	}
	if enabled || len(fields) > 0 {
		if provider == "email" {
			if a, err := mail.ParseAddress(cfg["to"]); err != nil || a.Address != cfg["to"] {
				return valid.Error("an email address like you@example.com")
			}
		} else if _, err := build(provider, cfg); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(cfg)
	_, err := s.db.Exec(`INSERT INTO personal_channels (person, provider, enabled, config_enc, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (person, provider) DO UPDATE SET enabled = excluded.enabled, config_enc = excluded.config_enc, updated_at = excluded.updated_at`,
		person, provider, enabled, s.box.Seal(raw, personalKey(person, provider)), s.Now().UnixMilli())
	return err
}

// sendPersonal delivers one message through someone's channel.
func (s *Service) sendPersonal(ctx context.Context, provider string, cfg map[string]string, m Message) error {
	if provider == "email" {
		if s.Email == nil {
			return valid.Error("email is not set up (Integrations → Email)")
		}
		text := m.Body
		if m.Link != "" {
			text += "\n\n" + m.Link
		}
		return s.Email(ctx, cfg["to"], m.Title, text)
	}
	p, err := build(provider, cfg)
	if err != nil {
		return err
	}
	return p.Send(ctx, m)
}

// TestPersonal sends a test through someone's channel.
func (s *Service) TestPersonal(ctx context.Context, person, provider string) error {
	_, cfg := s.loadPersonal(person, provider)
	if len(cfg) == 0 {
		return valid.Error("save it first")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return s.sendPersonal(ctx, provider, cfg, Message{Title: "Fleetwatch test", Body: "Your own " + provider + " alerts work.", Severity: "info", Details: Details{Event: "test"}})
}

// Personal sends an alert to everyone's own channels that Allows lets it reach.
func (s *Service) Personal(m Message, allows func(person string) bool) {
	rows, err := s.db.Query(`SELECT DISTINCT person FROM personal_channels WHERE enabled = 1`)
	if err != nil {
		return
	}
	var people []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			people = append(people, p)
		}
	}
	rows.Close()
	for _, person := range people {
		if !allows(person) {
			continue
		}
		for _, provider := range personalProviders {
			enabled, cfg := s.loadPersonal(person, provider)
			if !enabled || len(cfg) == 0 {
				continue
			}
			go func(provider string, cfg map[string]string) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := s.sendPersonal(ctx, provider, cfg, m); err != nil {
					slog.Warn("personal alert", "provider", provider, "err", fmt.Sprint(err))
				}
			}(provider, cfg)
		}
	}
}
