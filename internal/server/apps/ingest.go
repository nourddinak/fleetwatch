package apps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"fleetwatch/internal/proto"
)

// Ingest processes one inventory from a server's agent.
func (s *Service) Ingest(serverID string, inv proto.Inventory) {
	raw, _ := json.Marshal(inv)
	if _, err := s.db.Exec(`UPDATE servers SET last_inventory = ? WHERE id = ?`, string(raw), serverID); err != nil {
		slog.Error("store inventory", "server", serverID, "err", err)
	}
	s.mu.Lock()
	s.inv[serverID] = &inv
	s.mu.Unlock()

	list, err := s.list(serverID)
	if err != nil {
		slog.Error("list apps", "err", err)
		return
	}
	now := s.Now().UnixMilli()
	views := make([]View, 0, len(list))
	for _, a := range list {
		v := s.view(a, &inv)
		views = append(views, v)
		s.mu.Lock()
		prev, seen := s.status[a.ID]
		s.status[a.ID] = v.Status
		s.mu.Unlock()
		// "unknown" only means we cannot see the app; it is not a change worth logging.
		if seen && prev != v.Status && prev != "unknown" && v.Status != "unknown" {
			s.event(serverID, a.ID, "status", fmt.Sprintf("%s: %s → %s", a.Name, prev, v.Status))
		}
		// A new commit checked out in the app's folder is a deploy (shown on charts and in postmortems).
		if g := v.Git; g != nil && g.Commit != "" {
			s.mu.Lock()
			old, known := s.commits[a.ID]
			s.commits[a.ID] = g.Commit
			s.mu.Unlock()
			if known && old != g.Commit {
				s.event(serverID, a.ID, "deploy", fmt.Sprintf("%s deployed %.7s: %s", a.Name, g.Commit, g.Message))
			}
		}
		if v.ProcessFound {
			s.db.Exec(`UPDATE apps SET last_seen_at = ? WHERE id = ?`, now, a.ID)
			if a.Flags.Metrics {
				s.Metrics.Add(a.ID, now, []float64{v.CPU, v.RAM, float64(v.Restarts)})
			}
		}
	}
	s.Bus.Publish("apps", map[string]any{"serverId": serverID, "apps": views})
	if s.OnViews != nil {
		s.OnViews(serverID, views)
	}
	if changed, err := s.reconcile(serverID, &inv, list); err != nil {
		slog.Error("reconcile discoveries", "server", serverID, "err", err)
	} else if changed {
		s.Bus.Publish("discoveries", map[string]string{"serverId": serverID})
	}
}

// candidate is something that looks like an app but is not monitored yet.
type candidate struct {
	fingerprint, kind, name, key, path string
	data                               discoveryData
}

type discoveryData struct {
	Process *proto.Process `json:"process,omitempty"`
	Project *proto.Project `json:"project,omitempty"`
	Domain  string         `json:"suggestedDomain,omitempty"`
	Web     string         `json:"web,omitempty"` // "nginx" when Nginx serves it, else Caddy
}

// Fingerprint identifies a discovery. It changes when the manager, process name or
// project directory changes, which is when an ignored item should come back.
func Fingerprint(kind, key, path string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + key + "\x00" + path))
	return hex.EncodeToString(sum[:16])
}

func candidates(inv *proto.Inventory, apps []App) []candidate {
	monitored := map[string]bool{}
	paths := map[string]bool{}
	for _, a := range apps {
		monitored[a.Manager+"\x00"+a.ProcessKey] = true
		if a.Path != "" {
			paths[a.Path] = true
		}
	}
	projects := map[string]*proto.Project{}
	for i := range inv.Projects {
		projects[inv.Projects[i].Path] = &inv.Projects[i]
	}
	routeFor := func(p *proto.Process) string {
		for _, port := range p.Ports {
			for _, r := range inv.Routes {
				if r.Port == port.Port {
					return r.Host
				}
			}
		}
		return ""
	}
	var out []candidate
	claimed := map[string]bool{} // project dirs that belong to a process
	for i := range inv.Processes {
		p := &inv.Processes[i]
		if p.Project != "" {
			claimed[p.Project] = true
		}
		if !p.Likely || monitored[p.Manager+"\x00"+p.Key] || IsFleetwatchService(p.Key) {
			continue
		}
		pc := *p
		out = append(out, candidate{
			fingerprint: Fingerprint(p.Manager, p.Key, p.Project), kind: p.Manager,
			name: strings.TrimSuffix(p.Key, ".service"), key: p.Key, path: p.Project,
			data: discoveryData{Process: &pc, Project: projects[p.Project], Domain: routeFor(p)},
		})
	}
	for path, pr := range projects {
		if claimed[path] || paths[path] {
			continue
		}
		data := discoveryData{Project: pr}
		if r := servingRoute(inv.Routes, path); r != nil {
			data.Domain, data.Web = r.Host, r.Server // a static site served by Caddy or Nginx
		}
		out = append(out, candidate{
			fingerprint: Fingerprint("folder", path, path), kind: "folder",
			name: path[strings.LastIndex(path, "/")+1:], key: path, path: path,
			data: data,
		})
	}
	// Static sites Caddy or Nginx serves from a folder outside the discovery roots.
	covered := func(dir string) bool {
		for p := range projects {
			if dir == p || strings.HasPrefix(dir, p+"/") {
				return true
			}
		}
		for p := range paths {
			if dir == p || strings.HasPrefix(dir, p+"/") {
				return true
			}
		}
		return false
	}
	for _, r := range inv.Routes {
		if r.Root == "" || covered(r.Root) {
			continue
		}
		paths[r.Root] = true // one candidate per folder, even with several hosts
		out = append(out, candidate{
			fingerprint: Fingerprint("folder", r.Root, r.Root), kind: "folder",
			name: r.Host, key: r.Root, path: r.Root,
			data: discoveryData{Domain: r.Host, Web: r.Server},
		})
	}
	return out
}

// reconcile syncs the server's discoveries with the latest inventory. It reports
// whether anything visible changed.
func (s *Service) reconcile(serverID string, inv *proto.Inventory, apps []App) (bool, error) {
	type row struct{ id, state string }
	existing := map[string]row{}
	rows, err := s.db.Query(`SELECT id, fingerprint, state FROM discoveries WHERE server_id = ?`, serverID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id, fp, state string
		if err := rows.Scan(&id, &fp, &state); err != nil {
			rows.Close()
			return false, err
		}
		existing[fp] = row{id, state}
	}
	rows.Close()

	now := s.Now().UnixMilli()
	changed := false
	current := map[string]bool{}
	for _, c := range candidates(inv, apps) {
		if s.IsSite != nil && s.IsSite(serverID, c.path) {
			continue
		}
		current[c.fingerprint] = true
		data, _ := json.Marshal(c.data)
		if e, ok := existing[c.fingerprint]; ok {
			state := e.state
			if state == "added" { // its app was deleted while the process still runs
				state, changed = "pending", true
			}
			s.db.Exec(`UPDATE discoveries SET name = ?, data = ?, last_seen = ?, state = ? WHERE id = ?`, c.name, string(data), now, state, e.id)
			continue
		}
		_, err := s.db.Exec(`INSERT INTO discoveries (id, server_id, fingerprint, kind, name, process_key, path, state, data, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`, newID("dsc_"), serverID, c.fingerprint, c.kind, c.name, c.key, c.path, string(data), now, now)
		if err != nil {
			return changed, err
		}
		changed = true
		s.event(serverID, "", "discovery", fmt.Sprintf("New application detected: %s (%s)", c.name, kindLabel[c.kind]))
	}
	// Pending items that vanished are dropped; ignored ones are remembered.
	for fp, e := range existing {
		if !current[fp] && e.state == "pending" {
			s.db.Exec(`DELETE FROM discoveries WHERE id = ?`, e.id)
			changed = true
		}
	}
	return changed, nil
}

// IsFleetwatchService reports Fleetwatch's own units (dashboard, update helper, agent),
// which are never offered as applications, whatever an older agent reports.
func IsFleetwatchService(key string) bool {
	return strings.HasPrefix(key, "ops-server") || strings.HasPrefix(key, "ops-agent")
}

var kindLabel = map[string]string{"pm2": "PM2", "systemd": "systemd", "folder": "project folder"}
