package apps

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"

	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

var ErrDiscoveryNotFound = errors.New("discovery not found")

// DiscoveryView matches web/src/mock/types.ts Discovery.
type DiscoveryView struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	ServerID        string     `json:"serverId"`
	Kind            string     `json:"kind"` // pm2 | systemd | folder
	Headline        string     `json:"headline"`
	Manager         string     `json:"manager"`
	Unit            string     `json:"unit"`
	Path            string     `json:"path"`
	Runtime         string     `json:"runtime"`
	Port            *int       `json:"port"`
	Ports           []PortView `json:"ports"`
	Public          bool       `json:"public"`
	Git             bool       `json:"git"`
	GitRemote       string     `json:"gitRemote"`
	DiscoveredAt    int64      `json:"discoveredAt"`
	LastSeen        int64      `json:"lastSeen"`
	PID             *int       `json:"pid"`
	Status          string     `json:"status"`
	Cmdline         string     `json:"cmdline"`
	User            string     `json:"user"`
	Files           []string   `json:"files"`
	Markers         []string   `json:"markers"`
	State           string     `json:"state"`
	Reason          string     `json:"reason"`
	SuggestedDomain string     `json:"suggestedDomain"`
}

var headlines = map[string]string{
	"pm2":     "PM2 process detected",
	"systemd": "systemd service detected",
	"folder":  "Project folder with no running process",
}

func (s *Service) discoveryView(id, serverID, kind, name, key, dir, state, raw string, first, last int64) DiscoveryView {
	v := DiscoveryView{ID: id, Name: name, ServerID: serverID, Kind: kind, Headline: headlines[kind], Manager: managerLabel[kind],
		Unit: key, Path: dir, Ports: []PortView{}, Files: []string{}, Markers: []string{}, State: state, DiscoveredAt: first, LastSeen: last}
	if kind == "folder" {
		v.Manager, v.Unit = "standalone", ""
	}
	var d discoveryData
	json.Unmarshal([]byte(raw), &d)
	v.SuggestedDomain = d.Domain
	if pr := d.Project; pr != nil {
		v.Runtime, v.Files, v.Markers, v.User = pr.Runtime, pr.Files, pr.Markers, pr.Owner
		if g := pr.Git; g != nil {
			v.Git, v.GitRemote = true, g.Remote
		}
	}
	if p := d.Process; p != nil {
		if p.Runtime != "" {
			v.Runtime = p.Runtime
		}
		v.User, v.Reason, v.Status = p.User, p.Reason, p.Status
		if p.PID > 0 {
			pid := p.PID
			v.PID = &pid
		}
		v.Cmdline = p.ExecStart
		if v.Cmdline == "" {
			v.Cmdline = strings.TrimSpace(p.Interpreter + " " + p.Script)
		}
		for _, x := range p.Ports {
			v.Ports = append(v.Ports, PortView{Port: x.Port, Bind: x.Addr, Proto: x.Proto})
			v.Public = v.Public || x.Addr == "0.0.0.0" || x.Addr == "::" || x.Addr == "*"
		}
		if len(v.Ports) > 0 {
			port := v.Ports[0].Port
			v.Port = &port
		}
	}
	return v
}

const discoveryCols = `id, server_id, kind, name, process_key, path, state, data, first_seen, last_seen`

func (s *Service) scanDiscovery(sc interface{ Scan(...any) error }) (DiscoveryView, error) {
	var id, serverID, kind, name, key, dir, state, data string
	var first, last int64
	if err := sc.Scan(&id, &serverID, &kind, &name, &key, &dir, &state, &data, &first, &last); err != nil {
		return DiscoveryView{}, err
	}
	return s.discoveryView(id, serverID, kind, name, key, dir, state, data, first, last), nil
}

// Discoveries lists items in a state ("pending" or "ignored").
func (s *Service) Discoveries(state string) ([]DiscoveryView, error) {
	rows, err := s.db.Query(`SELECT `+discoveryCols+` FROM discoveries WHERE state = ? ORDER BY first_seen DESC`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoveryView{}
	for rows.Next() {
		v, err := s.scanDiscovery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) discovery(id string) (DiscoveryView, error) {
	v, err := s.scanDiscovery(s.db.QueryRow(`SELECT `+discoveryCols+` FROM discoveries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrDiscoveryNotFound
	}
	return v, err
}

// AddDiscovery turns a discovery into a monitored app. name overrides the detected name.
func (s *Service) AddDiscovery(id, name string) (View, error) {
	d, err := s.discovery(id)
	if err != nil {
		return View{}, err
	}
	if d.State == "added" {
		return View{}, valid.Error("this discovery was already added")
	}
	in := Input{ServerID: d.ServerID, Name: d.Name, Manager: d.Kind, ProcessKey: d.Unit, Path: d.Path}
	if strings.TrimSpace(name) != "" {
		in.Name = name
	}
	if d.Kind == "folder" {
		in.Manager = "standalone"
	}
	v, err := s.Create(in, d.Kind)
	if err != nil {
		return v, err
	}
	s.db.Exec(`UPDATE discoveries SET state = 'added' WHERE id = ?`, id)
	s.Bus.Publish("discoveries", map[string]string{"serverId": d.ServerID})
	return v, nil
}

func (s *Service) setDiscoveryState(id, from, to string) error {
	res, err := s.db.Exec(`UPDATE discoveries SET state = ? WHERE id = ? AND state = ?`, to, id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDiscoveryNotFound
	}
	s.Bus.Publish("discoveries", map[string]string{})
	return nil
}

func (s *Service) IgnoreDiscovery(id string) error {
	return s.setDiscoveryState(id, "pending", "ignored")
}
func (s *Service) RestoreDiscovery(id string) error {
	return s.setDiscoveryState(id, "ignored", "pending")
}

// ResetIgnored moves every ignored discovery back to pending.
func (s *Service) ResetIgnored() (int64, error) {
	res, err := s.db.Exec(`UPDATE discoveries SET state = 'pending' WHERE state = 'ignored'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	s.Bus.Publish("discoveries", map[string]string{})
	return n, nil
}

// PortRow is a listening socket on a server, attributed to an app when possible.
type PortRow struct {
	Proto   string `json:"proto"`
	Bind    string `json:"bind"`
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Process string `json:"process"`
	AppID   string `json:"appId,omitempty"`
	AppName string `json:"appName,omitempty"`
}

// ServerPorts lists every listening socket from the server's latest inventory.
func (s *Service) ServerPorts(serverID string) ([]PortRow, error) {
	out := []PortRow{}
	inv := s.inventory(serverID)
	if inv == nil {
		return out, nil
	}
	list, err := s.list(serverID)
	if err != nil {
		return nil, err
	}
	type sock struct {
		proto, addr string
		port        int
	}
	owner := map[sock]App{}
	for _, a := range list {
		if p, _ := match(a, inv); p != nil {
			for _, x := range p.Ports {
				owner[sock{x.Proto, x.Addr, x.Port}] = a
			}
		}
	}
	for _, x := range inv.Ports {
		r := PortRow{Proto: x.Proto, Bind: x.Addr, Port: x.Port, PID: x.PID, Process: x.Process}
		if a, ok := owner[sock{x.Proto, x.Addr, x.Port}]; ok {
			r.AppID, r.AppName = a.ID, a.Name
		}
		out = append(out, r)
	}
	return out, nil
}

// DiscoverySettings is the "discovery" settings section.
type DiscoverySettings struct {
	Roots []string `json:"roots"`
}

func DefaultDiscoverySettings() DiscoverySettings {
	return DiscoverySettings{Roots: []string{"/var/www", "/srv", "/opt"}}
}

func (d *DiscoverySettings) Validate() error {
	if len(d.Roots) > 10 {
		return valid.Error("at most 10 discovery roots")
	}
	clean := []string{}
	for _, r := range d.Roots {
		c, err := CleanPath(r)
		if err != nil {
			return err
		}
		if c == "" {
			continue
		}
		if c == "/" || len(c) > 256 {
			return valid.Error("discovery roots must be specific directories, not /")
		}
		if !slices.Contains(clean, c) {
			clean = append(clean, path.Clean(c))
		}
	}
	d.Roots = clean
	return nil
}

func (s *Service) DiscoverySettings() DiscoverySettings {
	d := DefaultDiscoverySettings()
	if settings.Load(s.db, "discovery", &d) != nil || d.Validate() != nil {
		return DefaultDiscoverySettings()
	}
	return d
}

func (s *Service) SaveDiscoverySettings(d DiscoverySettings) error {
	if err := d.Validate(); err != nil {
		return err
	}
	return settings.Save(s.db, "discovery", d)
}
