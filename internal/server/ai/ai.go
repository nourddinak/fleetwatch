// Package ai talks to Gemini for the dashboard: the owner's API key, which live model
// and voice to use, a monthly cap on call minutes, and live voice conversations
// (see live.go) for on-call calls.
package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// API is Gemini's address; tests point it at a fake.
var (
	API    = "https://generativelanguage.googleapis.com"
	client = &http.Client{Timeout: 20 * time.Second}
)

// PreferredModel is used when the key can reach it.
const PreferredModel = "gemini-3.8-live"

// Voices are some of Gemini's prebuilt voices.
var Voices = []string{"Kore", "Puck", "Charon", "Aoede", "Fenrir", "Leda", "Orus", "Zephyr"}

const keyAD = "ai.gemini"

// Config is what the owner chooses.
type Config struct {
	Model          string `json:"model"`
	Voice          string `json:"voice"`
	MonthlyMinutes int    `json:"monthlyMinutes"` // call minutes a month; 0 = no calls with the AI
}

// Status is what the dashboard shows.
type Status struct {
	Configured  bool     `json:"configured"`
	Config      Config   `json:"config"`
	UsedSeconds int      `json:"usedSeconds"` // this month
	Voices      []string `json:"voices"`
}

type Service struct {
	db  *sql.DB
	box *secret.Box
	Now func() time.Time

	mu   sync.Mutex
	last *CallReport
}

func New(d *sql.DB, box *secret.Box) *Service {
	return &Service{db: d, box: box, Now: time.Now}
}

func (s *Service) load() (Config, string, bool) {
	var pub string
	var enc []byte
	var c Config
	if s.db.QueryRow(`SELECT public, secret_enc FROM integrations WHERE provider = 'gemini'`).Scan(&pub, &enc) != nil {
		return c, "", false
	}
	json.Unmarshal([]byte(pub), &c)
	key, err := s.box.Open(enc, keyAD)
	return c, string(key), err == nil && len(key) > 0
}

func (s *Service) save(c Config, key string) error {
	pub, _ := json.Marshal(c)
	_, err := s.db.Exec(`INSERT INTO integrations (provider, public, secret_enc, created_at) VALUES ('gemini', ?, ?, ?)
		ON CONFLICT (provider) DO UPDATE SET public = excluded.public, secret_enc = excluded.secret_enc`,
		string(pub), s.box.Seal([]byte(key), keyAD), s.Now().UnixMilli())
	return err
}

type usage struct {
	Month   string `json:"month"`
	Seconds int    `json:"seconds"`
}

func (s *Service) usage() usage {
	var u usage
	settings.Load(s.db, "ai.usage", &u)
	if month := s.Now().UTC().Format("2006-01"); u.Month != month {
		u = usage{Month: month}
	}
	return u
}

// AddUsage counts call time against the month's cap.
func (s *Service) AddUsage(d time.Duration) {
	u := s.usage()
	u.Seconds += int(d.Seconds() + 0.5)
	settings.Save(s.db, "ai.usage", u)
}

// Status reports the setup and this month's use.
func (s *Service) Status() Status {
	c, _, ok := s.load()
	return Status{Configured: ok, Config: c, UsedSeconds: s.usage().Seconds, Voices: Voices}
}

// Remaining is the call time left this month (0 when the AI is not set up).
func (s *Service) Remaining() time.Duration {
	c, _, ok := s.load()
	if !ok {
		return 0
	}
	return max(0, time.Duration(c.MonthlyMinutes)*time.Minute-time.Duration(s.usage().Seconds)*time.Second)
}

// liveModels lists the models the key can hold live conversations with.
func liveModels(ctx context.Context, key string) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, API+"/v1beta/models?pageSize=1000", nil)
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Gemini: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		msg := out.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		return nil, errors.New("Gemini: " + strings.TrimSuffix(msg, "."))
	}
	var live []string
	for _, m := range out.Models {
		if slices.Contains(m.Methods, "bidiGenerateContent") {
			live = append(live, strings.TrimPrefix(m.Name, "models/"))
		}
	}
	return live, nil
}

// ModelInfo is a live model as the dashboard offers it for calls.
type ModelInfo struct {
	ID          string `json:"id"`
	Note        string `json:"note"`
	Recommended bool   `json:"recommended,omitempty"`
}

// describe says what a live model is like for on-call calls, going by its name and the
// free-tier limits Google lists in AI Studio; false: it cannot hold a conversation.
func describe(id string) (ModelInfo, bool) {
	m := ModelInfo{ID: id}
	switch {
	case strings.Contains(id, "translate") || strings.Contains(id, "transcribe"):
		return m, false // translation and transcription only
	case strings.Contains(id, "extended-thinking"):
		m.Note = "Thinks before it answers: slower to reply on a call."
	case strings.Contains(id, "3.8-live"):
		m.Note, m.Recommended = "Best voice and tool use. Free tier: 65K tokens a minute, plenty for one call at a time (a 5-minute call uses about 20K).", true
	case strings.Contains(id, "native-audio"):
		m.Note = "Free tier: 1M tokens a minute, the most room when several calls happen at once."
	default:
		m.Note = "Free tier: about 65K tokens a minute, enough for one call at a time."
	}
	return m, true
}

// usable keeps the models that can hold a call, recommended first.
func usable(ids []string) []ModelInfo {
	var out []ModelInfo
	for _, id := range ids {
		if m, ok := describe(id); ok {
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b ModelInfo) int {
		switch {
		case a.Recommended == b.Recommended:
			return 0
		case a.Recommended:
			return -1
		}
		return 1
	})
	return out
}

// Models lists the live models of the saved key that can hold a call.
func (s *Service) Models(ctx context.Context) ([]ModelInfo, error) {
	_, key, ok := s.load()
	if !ok {
		return nil, valid.Error("add a Gemini API key first")
	}
	ids, err := liveModels(ctx, key)
	return usable(ids), err
}

// SetKey checks a key by listing its live models, then keeps it (encrypted), picking
// the preferred model when there is none yet.
func (s *Service) SetKey(ctx context.Context, key string) (Status, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return Status{}, valid.Error("paste the Gemini API key")
	}
	ids, err := liveModels(ctx, key)
	if err != nil {
		return Status{}, valid.Error("this key does not work: " + err.Error())
	}
	models := usable(ids)
	if len(models) == 0 {
		return Status{}, valid.Error("this key can use no live voice model")
	}
	c, _, _ := s.load()
	if !slices.ContainsFunc(models, func(m ModelInfo) bool { return m.ID == c.Model }) {
		c.Model = models[0].ID // the recommended one when the key has it
		if slices.ContainsFunc(models, func(m ModelInfo) bool { return m.ID == PreferredModel }) {
			c.Model = PreferredModel
		}
	}
	if c.Voice == "" {
		c.Voice = Voices[0]
	}
	if c.MonthlyMinutes == 0 {
		c.MonthlyMinutes = 60
	}
	if err := s.save(c, key); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

// SetConfig changes the model, voice and cap.
func (s *Service) SetConfig(ctx context.Context, c Config) (Status, error) {
	_, key, ok := s.load()
	if !ok {
		return Status{}, valid.Error("add a Gemini API key first")
	}
	if !slices.Contains(Voices, c.Voice) {
		return Status{}, valid.Error("unknown voice")
	}
	if c.MonthlyMinutes < 0 || c.MonthlyMinutes > 100000 {
		return Status{}, valid.Error("the monthly minutes must be between 0 and 100000")
	}
	if ids, err := liveModels(ctx, key); err == nil && !slices.ContainsFunc(usable(ids), func(m ModelInfo) bool { return m.ID == c.Model }) {
		return Status{}, valid.Error("this key cannot hold calls with " + c.Model)
	}
	if err := s.save(c, key); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

// Clear forgets the key.
func (s *Service) Clear() error {
	_, err := s.db.Exec(`DELETE FROM integrations WHERE provider = 'gemini'`)
	return err
}

// liveURL is the WebSocket of a live session.
func liveURL(key string) string {
	u := strings.Replace(API, "http", "ws", 1) + "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	return u + "?key=" + url.QueryEscape(key)
}
