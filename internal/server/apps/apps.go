// Package apps owns monitored applications and discoveries. Agents report inventories;
// this package matches them to configured apps, records status changes and app
// metrics, and turns unknown processes and project folders into discoveries.
package apps

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

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/valid"
)

var ErrNotFound = errors.New("application not found")

type Flags struct {
	Monitoring    bool `json:"monitoring"`
	Logs          bool `json:"logs"`
	Metrics       bool `json:"metrics"`
	Incidents     bool `json:"incidents"`
	Notifications bool `json:"notifications"`
}

func DefaultFlags() Flags { return Flags{true, true, true, true, true} }

type App struct {
	ID, ServerID, Name, Manager, ProcessKey string
	Path, Domain, Notes, Source             string
	Flags                                   Flags
	CreatedAt, LastSeenAt                   int64
}

type Service struct {
	db        *sql.DB
	Bus       *live.Bus
	Metrics   *metrics.Store // AppKind
	Connected func(serverID string) bool
	Now       func() time.Time

	// Optional hooks, set before use.
	OnChange   func(serverID string)               // apps of a server were added, edited or removed
	ErrorCount func(appID string) int              // error/critical log lines in the last 24 hours
	OnViews    func(serverID string, views []View) // fresh views of a server's apps (incident detection)
	IsSite     func(serverID, path string) bool    // a folder Fleetwatch Sites publishes: listed under Sites, never discovered

	mu      sync.Mutex
	inv     map[string]*proto.Inventory // latest inventory per server
	status  map[string]string           // last status per app, for change events
	commits map[string]string           // last git commit seen per app (deploy detection)
	health  map[string]healthState      // recent health check summary per app
}

func New(d *sql.DB, bus *live.Bus, m *metrics.Store, connected func(string) bool) *Service {
	return &Service{db: d, Bus: bus, Metrics: m, Connected: connected, Now: time.Now, inv: map[string]*proto.Inventory{}, status: map[string]string{}, health: map[string]healthState{}, commits: map[string]string{}}
}

func newID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

// inventory returns the latest inventory for a server, loading the persisted copy
// after a restart.
// Routes are the domains a server's web server (Caddy or Nginx) sends somewhere, from
// its last inventory.
func (s *Service) Routes(serverID string) []proto.Route {
	if inv := s.inventory(serverID); inv != nil && inv.Routes != nil {
		return inv.Routes
	}
	return []proto.Route{}
}

func (s *Service) inventory(serverID string) *proto.Inventory {
	s.mu.Lock()
	inv, ok := s.inv[serverID]
	s.mu.Unlock()
	if ok {
		return inv
	}
	var raw string
	if s.db.QueryRow(`SELECT last_inventory FROM servers WHERE id = ?`, serverID).Scan(&raw) == nil && raw != "" {
		var x proto.Inventory
		if json.Unmarshal([]byte(raw), &x) == nil {
			inv = &x
		}
	}
	s.mu.Lock()
	s.inv[serverID] = inv
	s.mu.Unlock()
	return inv
}

const appCols = `id, server_id, name, manager, process_key, path, domain, notes, source, flags, created_at, COALESCE(last_seen_at, 0)`

func scanApp(sc interface{ Scan(...any) error }) (App, error) {
	var a App
	var flags string
	err := sc.Scan(&a.ID, &a.ServerID, &a.Name, &a.Manager, &a.ProcessKey, &a.Path, &a.Domain, &a.Notes, &a.Source, &flags, &a.CreatedAt, &a.LastSeenAt)
	a.Flags = DefaultFlags()
	json.Unmarshal([]byte(flags), &a.Flags)
	return a, err
}

func (s *Service) get(id string) (App, error) {
	a, err := scanApp(s.db.QueryRow(`SELECT `+appCols+` FROM apps WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// list returns apps of one server, or all apps when serverID is "".
func (s *Service) list(serverID string) ([]App, error) {
	rows, err := s.db.Query(`SELECT `+appCols+` FROM apps WHERE (? = '' OR server_id = ?) ORDER BY name COLLATE NOCASE`, serverID, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Input is what the admin provides when adding or editing an application.
type Input struct {
	ServerID   string `json:"serverId"`
	Name       string `json:"name"`
	Manager    string `json:"manager"` // pm2 | systemd | standalone
	ProcessKey string `json:"processKey"`
	Path       string `json:"path"`
	Domain     string `json:"domain"`
	Notes      string `json:"notes"`
	Flags      *Flags `json:"flags"`
}

var (
	pm2NameRe = regexp.MustCompile(`^[A-Za-z0-9@._:+-][A-Za-z0-9@._:+ -]{0,99}$`)
	unitRe    = regexp.MustCompile(`^[A-Za-z0-9@._:\\-]{1,240}\.service$`)
	domainRe  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// CleanPath validates an absolute directory path: no relative parts, no "..".
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if len(p) > 512 || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n\r") {
		return "", valid.Error("path must be an absolute directory path such as /var/www/app")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return "", valid.Error("path must not contain . or .. segments")
		}
	}
	return path.Clean(p), nil
}

func (in *Input) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 || strings.ContainsFunc(in.Name, func(r rune) bool { return r < 32 || r == 127 }) {
		return valid.Error("name must be 1-80 characters")
	}
	var err error
	if in.Path, err = CleanPath(in.Path); err != nil {
		return err
	}
	in.ProcessKey = strings.TrimSpace(in.ProcessKey)
	switch in.Manager {
	case "pm2":
		if !pm2NameRe.MatchString(in.ProcessKey) {
			return valid.Error("enter the PM2 process name (letters, digits, . _ - @ :)")
		}
	case "systemd":
		if in.ProcessKey != "" && !strings.HasSuffix(in.ProcessKey, ".service") {
			in.ProcessKey += ".service"
		}
		if !unitRe.MatchString(in.ProcessKey) {
			return valid.Error("enter the systemd unit name, e.g. billing-api.service")
		}
	case "standalone":
		if in.Path == "" {
			return valid.Error("a standalone application needs its directory path")
		}
		in.ProcessKey = in.Path
	default:
		return valid.Error("manager must be pm2, systemd or standalone")
	}
	in.Domain = strings.ToLower(strings.TrimSpace(in.Domain))
	if in.Domain != "" && (len(in.Domain) > 253 || !domainRe.MatchString(in.Domain)) {
		return valid.Error("domain must be a hostname such as api.example.com")
	}
	if len(in.Notes) > 4000 {
		return valid.Error("notes are too long")
	}
	return nil
}

func (s *Service) event(serverID, appID, kind, text string) {
	s.db.Exec(`INSERT INTO events (ts, server_id, app_id, kind, text) VALUES (?, ?, ?, ?, ?)`, s.Now().UnixMilli(), serverID, appID, kind, text)
}

// Create adds an application. source is "manual" or the discovery kind.
func (s *Service) Create(in Input, source string) (View, error) {
	if err := in.normalize(); err != nil {
		return View{}, err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM servers WHERE id = ?`, in.ServerID).Scan(&n); err != nil || n == 0 {
		return View{}, valid.Error("choose an existing server")
	}
	flags := DefaultFlags()
	if in.Flags != nil {
		flags = *in.Flags
	}
	fj, _ := json.Marshal(flags)
	id := newID("app_")
	_, err := s.db.Exec(`INSERT INTO apps (id, server_id, name, manager, process_key, path, domain, notes, source, flags, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.ServerID, in.Name, in.Manager, in.ProcessKey, in.Path, in.Domain, in.Notes, source, string(fj), s.Now().UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return View{}, valid.Error("this process is already monitored on that server")
		}
		return View{}, err
	}
	s.event(in.ServerID, id, "added", "Application added to monitoring: "+in.Name)
	s.changed(in.ServerID)
	return s.Get(id)
}

// Patch holds editable fields; nil means unchanged.
type Patch struct {
	Name       *string `json:"name"`
	Manager    *string `json:"manager"`
	ProcessKey *string `json:"processKey"`
	Path       *string `json:"path"`
	Domain     *string `json:"domain"`
	Notes      *string `json:"notes"`
	Flags      *Flags  `json:"flags"`
}

func (s *Service) Update(id string, p Patch) (View, error) {
	a, err := s.get(id)
	if err != nil {
		return View{}, err
	}
	in := Input{ServerID: a.ServerID, Name: a.Name, Manager: a.Manager, ProcessKey: a.ProcessKey, Path: a.Path, Domain: a.Domain, Notes: a.Notes}
	for _, f := range []struct {
		src *string
		dst *string
	}{{p.Name, &in.Name}, {p.Manager, &in.Manager}, {p.ProcessKey, &in.ProcessKey}, {p.Path, &in.Path}, {p.Domain, &in.Domain}, {p.Notes, &in.Notes}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	if err := in.normalize(); err != nil {
		return View{}, err
	}
	flags := a.Flags
	if p.Flags != nil {
		flags = *p.Flags
	}
	fj, _ := json.Marshal(flags)
	_, err = s.db.Exec(`UPDATE apps SET name = ?, manager = ?, process_key = ?, path = ?, domain = ?, notes = ?, flags = ? WHERE id = ?`,
		in.Name, in.Manager, in.ProcessKey, in.Path, in.Domain, in.Notes, string(fj), id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return View{}, valid.Error("this process is already monitored on that server")
		}
		return View{}, err
	}
	s.changed(a.ServerID)
	return s.Get(id)
}

// Delete stops monitoring an app. Nothing on the server is touched; if the process
// still runs it shows up under Discoveries again.
func (s *Service) Delete(id string) (App, error) {
	a, err := s.get(id)
	if err != nil {
		return a, err
	}
	if _, err := s.db.Exec(`DELETE FROM apps WHERE id = ?`, id); err != nil {
		return a, err
	}
	s.db.Exec(`DELETE FROM events WHERE app_id = ?`, id)
	s.Metrics.Forget(id)
	s.mu.Lock()
	delete(s.status, id)
	s.mu.Unlock()
	s.changed(a.ServerID)
	return a, nil
}

func (s *Service) Get(id string) (View, error) {
	a, err := s.get(id)
	if err != nil {
		return View{}, err
	}
	return s.view(a, s.inventory(a.ServerID)), nil
}

// List returns every app (serverID "") or one server's apps, as the UI sees them.
func (s *Service) List(serverID string) ([]View, error) {
	list, err := s.list(serverID)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(list))
	for _, a := range list {
		out = append(out, s.view(a, s.inventory(a.ServerID)))
	}
	return out, nil
}

// Events returns an app's timeline, newest first.
func (s *Service) Events(appID string, limit int) ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT id, ts, kind, text FROM events WHERE app_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, ts int64
		var kind, text string
		if err := rows.Scan(&id, &ts, &kind, &text); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "ts": ts, "kind": kind, "text": text, "appId": appID})
	}
	return out, rows.Err()
}

func (s *Service) changed(serverID string) {
	s.PublishServer(serverID)
	if s.OnChange != nil {
		s.OnChange(serverID)
	}
}

// LogTargets lists the apps on a server whose logs the agent should collect.
func (s *Service) LogTargets(serverID string) []proto.LogTarget {
	out := []proto.LogTarget{}
	list, err := s.list(serverID)
	if err != nil {
		return out
	}
	for _, a := range list {
		if a.Manager != "standalone" && a.Flags.Monitoring && a.Flags.Logs {
			out = append(out, proto.LogTarget{AppID: a.ID, Manager: a.Manager, Key: a.ProcessKey})
		}
	}
	return out
}

// PublishServer pushes the current views of a server's apps to browsers.
func (s *Service) PublishServer(serverID string) {
	if views, err := s.List(serverID); err == nil {
		s.Bus.Publish("apps", map[string]any{"serverId": serverID, "apps": views})
		if s.OnViews != nil {
			s.OnViews(serverID, views)
		}
	}
}

// ServerOffline is called when a server's agent disconnects.
func (s *Service) ServerOffline(serverID string) {
	if list, err := s.list(serverID); err == nil {
		for _, a := range list {
			s.Metrics.Flush(a.ID)
		}
	}
	s.PublishServer(serverID)
}
