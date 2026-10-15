// Package commands manages per-app commands and their runs. The browser can only ask
// to run a command by id; the command text comes from definitions saved earlier
// (behind password confirmation) or from the fixed built-in actions.
package commands

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/valid"
)

var (
	ErrNotFound = errors.New("command not found")
	ErrOffline  = errors.New("the agent on this server is not connected")
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

const maxStoredOutput = 256 << 10 // per stream, per run

// Command is a custom command definition.
type Command struct {
	ID         string   `json:"id"`
	AppID      string   `json:"appId"`
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Cwd        string   `json:"cwd"`
	TimeoutSec int      `json:"timeoutSec"`
	Confirm    bool     `json:"confirm"`
	Env        []string `json:"env"`
	CreatedAt  int64    `json:"createdAt"`
	UpdatedAt  int64    `json:"updatedAt"`
}

// View is a command as listed in the UI (built-in or custom). Env values are hidden.
type View struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"` // builtin | custom
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Preview     string   `json:"preview"`
	Cwd         string   `json:"cwd"`
	TimeoutSec  int      `json:"timeoutSec"`
	Confirm     bool     `json:"requiresConfirm"`
	Danger      bool     `json:"danger"`
	EnvKeys     []string `json:"envKeys"`
	LastRun     *Run     `json:"lastRun"`
}

type Run struct {
	ID          string `json:"id"`
	AppID       string `json:"appId"`
	CommandID   string `json:"commandId"`
	Name        string `json:"name"`
	Preview     string `json:"preview"`
	Cwd         string `json:"cwd"`
	RunAs       string `json:"runAs"`
	StartedAt   int64  `json:"startedAt"`
	FinishedAt  int64  `json:"finishedAt,omitempty"`
	Status      string `json:"status"`
	ExitCode    *int   `json:"exitCode"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Error       string `json:"error"`
	InitiatedBy string `json:"initiatedBy"`
	serverID    string
}

type Service struct {
	db   *sql.DB
	Apps *apps.Service
	Bus  *live.Bus
	Send func(serverID, typ string, v any) error
	Now  func() time.Time

	mu      sync.Mutex
	running map[string]*Run // by run id
}

func New(d *sql.DB, a *apps.Service, bus *live.Bus, send func(string, string, any) error) *Service {
	s := &Service{db: d, Apps: a, Bus: bus, Send: send, Now: time.Now, running: map[string]*Run{}}
	// Runs cannot survive a server restart: their results would never be recorded.
	d.Exec(`UPDATE command_runs SET status = 'error', error = 'central server restarted while running', finished_at = ? WHERE status = 'running'`, time.Now().UnixMilli())
	return s
}

func newID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

var builtins = []View{
	{ID: "builtin:start", Name: "Start", Description: "Start the process.", TimeoutSec: 90},
	{ID: "builtin:stop", Name: "Stop", Description: "Stop the process gracefully.", Confirm: true, Danger: true, TimeoutSec: 90},
	{ID: "builtin:restart", Name: "Restart", Description: "Stop and start the process.", Confirm: true, TimeoutSec: 90},
	{ID: "builtin:reload", Name: "Reload", Description: "Zero-downtime reload where supported (systemd falls back to restart).", TimeoutSec: 90},
}

func builtinPreview(manager, key, action string) string {
	if manager == "pm2" {
		return "pm2 " + action + " " + key
	}
	if action == "reload" {
		action = "reload-or-restart"
	}
	return "systemctl " + action + " " + key
}

// managerKey maps the UI manager label back to the stored manager name.
func managerKey(label string) string {
	switch label {
	case "PM2":
		return "pm2"
	case "systemd":
		return "systemd"
	}
	return "standalone"
}

// Input is a custom command definition from the admin.
type Input struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Cwd        string   `json:"cwd"`
	TimeoutSec int      `json:"timeoutSec"`
	Confirm    bool     `json:"confirm"`
	Env        []string `json:"env"`
}

func (in *Input) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 60 {
		return valid.Error("command name must be 1-60 characters")
	}
	in.Command = strings.TrimSpace(in.Command)
	if in.Command == "" || len(in.Command) > 4096 || strings.ContainsRune(in.Command, 0) {
		return valid.Error("command must be 1-4096 characters")
	}
	in.Cwd = strings.Trim(strings.TrimSpace(in.Cwd), "/")
	if in.Cwd != "" {
		for _, seg := range strings.Split(in.Cwd, "/") {
			if seg == ".." || seg == "." || seg == "" {
				return valid.Error("working directory must be a plain path inside the app directory (no . or ..)")
			}
		}
		in.Cwd = path.Clean(in.Cwd)
	}
	if in.TimeoutSec == 0 {
		in.TimeoutSec = 300
	}
	if in.TimeoutSec < 5 || in.TimeoutSec > 3600 {
		return valid.Error("timeout must be 5-3600 seconds")
	}
	if len(in.Env) > 20 {
		return valid.Error("at most 20 environment variables")
	}
	for _, kv := range in.Env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || !envKeyRe.MatchString(k) {
			return valid.Error("environment variables must look like NAME=value")
		}
	}
	if in.Env == nil {
		in.Env = []string{}
	}
	return nil
}

func scanCommand(sc interface{ Scan(...any) error }) (Command, error) {
	var c Command
	var env string
	err := sc.Scan(&c.ID, &c.AppID, &c.Name, &c.Command, &c.Cwd, &c.TimeoutSec, &c.Confirm, &env, &c.CreatedAt, &c.UpdatedAt)
	json.Unmarshal([]byte(env), &c.Env)
	return c, err
}

const commandCols = `id, app_id, name, command, cwd, timeout_sec, confirm, env, created_at, updated_at`

func (s *Service) get(id string) (Command, error) {
	c, err := scanCommand(s.db.QueryRow(`SELECT `+commandCols+` FROM app_commands WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Service) Create(appID string, in Input) (Command, error) {
	if _, err := s.Apps.Get(appID); err != nil {
		return Command{}, err
	}
	if err := in.normalize(); err != nil {
		return Command{}, err
	}
	env, _ := json.Marshal(in.Env)
	now := s.Now().UnixMilli()
	id := newID("cmd_")
	_, err := s.db.Exec(`INSERT INTO app_commands (id, app_id, name, command, cwd, timeout_sec, confirm, env, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, appID, in.Name, in.Command, in.Cwd, in.TimeoutSec, in.Confirm, string(env), now, now)
	if err != nil {
		return Command{}, err
	}
	return s.get(id)
}

func (s *Service) Update(id string, in Input) (Command, error) {
	if _, err := s.get(id); err != nil {
		return Command{}, err
	}
	if err := in.normalize(); err != nil {
		return Command{}, err
	}
	env, _ := json.Marshal(in.Env)
	_, err := s.db.Exec(`UPDATE app_commands SET name = ?, command = ?, cwd = ?, timeout_sec = ?, confirm = ?, env = ?, updated_at = ? WHERE id = ?`,
		in.Name, in.Command, in.Cwd, in.TimeoutSec, in.Confirm, string(env), s.Now().UnixMilli(), id)
	if err != nil {
		return Command{}, err
	}
	return s.get(id)
}

func (s *Service) Delete(id string) (Command, error) {
	c, err := s.get(id)
	if err != nil {
		return c, err
	}
	_, err = s.db.Exec(`DELETE FROM app_commands WHERE id = ?`, id)
	return c, err
}

// Hit is a custom command matching a search.
type Hit struct {
	ID      string `json:"id"`
	AppID   string `json:"appId"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

// Search finds custom commands by name or text (global search).
func (s *Service) Search(q string, limit int) ([]Hit, error) {
	q = strings.ToLower(q) // instr() needs no escaping, unlike LIKE
	rows, err := s.db.Query(`SELECT id, app_id, name, command FROM app_commands WHERE instr(lower(name), ?) > 0 OR instr(lower(command), ?) > 0 ORDER BY name LIMIT ?`, q, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.AppID, &h.Name, &h.Command); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Get returns a custom command (the full definition, for editing).
func (s *Service) Get(id string) (Command, error) { return s.get(id) }

// List returns the built-ins valid for the app's manager, then its custom commands.
func (s *Service) List(appID string) ([]View, error) {
	app, err := s.Apps.Get(appID)
	if err != nil {
		return nil, err
	}
	last := s.lastRuns(appID)
	out := []View{}
	if m := managerKey(app.Manager); m != "standalone" {
		for _, b := range builtins {
			v := b
			v.Kind = "builtin"
			v.Preview = builtinPreview(m, app.ProcessKey, strings.TrimPrefix(b.ID, "builtin:"))
			v.EnvKeys = []string{}
			v.LastRun = last[v.ID]
			out = append(out, v)
		}
	}
	rows, err := s.db.Query(`SELECT `+commandCols+` FROM app_commands WHERE app_id = ? ORDER BY name COLLATE NOCASE`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		keys := []string{}
		for _, kv := range c.Env {
			k, _, _ := strings.Cut(kv, "=")
			keys = append(keys, k)
		}
		out = append(out, View{ID: c.ID, Kind: "custom", Name: c.Name, Preview: c.Command, Cwd: c.Cwd, TimeoutSec: c.TimeoutSec,
			Confirm: c.Confirm, EnvKeys: keys, LastRun: last[c.ID]})
	}
	return out, rows.Err()
}
