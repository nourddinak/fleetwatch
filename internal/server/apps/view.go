package apps

import (
	"math"
	"strings"

	"fleetwatch/internal/proto"
)

// View is an application as the UI sees it (field names match web/src/mock/types.ts App).
type View struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Slug            string     `json:"slug"`
	ServerID        string     `json:"serverId"`
	Manager         string     `json:"manager"` // PM2 | systemd | standalone
	ProcessKey      string     `json:"processKey"`
	PM2ID           *int       `json:"pm2Id"`
	PM2Name         string     `json:"pm2Name"`
	Unit            string     `json:"unit"`
	Path            string     `json:"path"`
	Runtime         string     `json:"runtime"`
	Status          string     `json:"status"`
	RawStatus       string     `json:"rawStatus"`
	PID             *int       `json:"pid"`
	CPU             float64    `json:"cpu"`
	RAM             float64    `json:"ram"` // MB
	UptimeSec       int64      `json:"uptimeSec"`
	Restarts        int        `json:"restarts"`
	Instances       int        `json:"instances"`
	Ports           []PortView `json:"ports"`
	FolderMB        float64    `json:"folderMB"`
	Domain          *string    `json:"domain"`
	SuggestedDomain string     `json:"suggestedDomain"`
	Health          HealthView `json:"health"`
	HealthStatus    string     `json:"healthStatus"`
	HealthError     string     `json:"healthError"`
	HealthFailures  int        `json:"healthFailures"` // consecutive
	HealthLastOK    int64      `json:"healthLastOk"`
	HealthLastFail  int64      `json:"healthLastFail"`
	StorageScanned  int64      `json:"storageScannedAt"`
	Latency         *int       `json:"latency"`
	Git             *GitView   `json:"git"`
	LogRate         float64    `json:"logRate"`
	Errors24h       int        `json:"errors24h"`
	DiscoveredAt    int64      `json:"discoveredAt"`
	LastSeen        int64      `json:"lastSeen"`
	Notes           string     `json:"notes"`
	Docker          any        `json:"docker"`
	Flags           Flags      `json:"flags"`
	Source          string     `json:"source"`
	User            string     `json:"user"`
	Script          string     `json:"script"`
	Interpreter     string     `json:"interpreter"`
	ExecStart       string     `json:"execStart"`
	OutLog          string     `json:"outLog"`
	ErrLog          string     `json:"errLog"`
	ProcessFound    bool       `json:"processFound"`
	Markers         []string   `json:"markers"`
}

type PortView struct {
	Port  int    `json:"port"`
	Bind  string `json:"bind"`
	Proto string `json:"proto"`
}

// HealthView is an app's health check configuration.
type HealthView struct {
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url"`
	IntervalSec    int    `json:"intervalSec"`
	TimeoutMs      int    `json:"timeoutMs"`
	ExpectedStatus int    `json:"expectedStatus"`
}

type GitView struct {
	Repo    string            `json:"repo"`
	Remote  string            `json:"remote"`
	Branch  string            `json:"branch"`
	Commit  string            `json:"commit"`
	Message string            `json:"message"`
	Author  string            `json:"author"`
	Date    int64             `json:"date"`
	Dirty   bool              `json:"dirty"`
	Ahead   int               `json:"ahead"`
	Behind  int               `json:"behind"`
	Changes []proto.GitChange `json:"changes"`
}

var managerLabel = map[string]string{"pm2": "PM2", "systemd": "systemd", "standalone": "standalone"}

func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// repoName turns a remote URL into "owner/repo".
func repoName(remote string) string {
	r := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if i := strings.LastIndex(r, ":"); i >= 0 && !strings.Contains(r, "://") {
		r = r[i+1:] // git@host:owner/repo
	}
	parts := strings.Split(strings.Trim(r, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return r
}

// match finds the app's process and project in an inventory.
func match(a App, inv *proto.Inventory) (*proto.Process, *proto.Project) {
	if inv == nil {
		return nil, nil
	}
	var p *proto.Process
	if a.Manager != "standalone" {
		for i := range inv.Processes {
			x := &inv.Processes[i]
			if x.Manager == a.Manager && x.Key == a.ProcessKey {
				p = x
				break
			}
		}
	}
	dir := a.Path
	if dir == "" && p != nil {
		dir = p.Project
	}
	for i := range inv.Projects {
		if inv.Projects[i].Path == dir {
			return p, &inv.Projects[i]
		}
	}
	return p, nil
}

// servingRoute finds the Caddy route that serves files from dir (its root is dir or
// a folder inside it, such as dir/dist).
func servingRoute(routes []proto.Route, dir string) *proto.Route {
	if dir == "" || dir == "/" {
		return nil
	}
	for i := range routes {
		if r := routes[i].Root; r == dir || strings.HasPrefix(r, dir+"/") {
			return &routes[i]
		}
	}
	return nil
}

// staticSite fills in a process-less app that Caddy serves as a static site: its state
// comes from Caddy and from its health check (a failing check shows as "warning" and
// raises a Health Check Failed incident, not a second "offline" one).
func staticSite(v *View, a App, routes []proto.Route) {
	r := servingRoute(routes, v.Path)
	if r == nil {
		v.RawStatus = "not served by Caddy"
		return
	}
	v.Runtime = "Static"
	if a.Domain == "" {
		v.SuggestedDomain = r.Host
	}
	v.RawStatus = "served by Caddy from " + r.Root
	switch {
	case v.HealthStatus == "failed":
		v.Status = "warning"
	case v.HealthStatus == "healthy" || r.Live:
		v.Status = "online"
	default:
		v.RawStatus += " (Caddy admin API unreachable: enable a health check to confirm the site is up)"
	}
}

// appStatus maps the manager's state to the UI status vocabulary.
func appStatus(p *proto.Process) string {
	switch p.Status {
	case "online":
		return "online"
	case "starting":
		if p.Unstable {
			return "crash-loop"
		}
		return "starting"
	case "stopped":
		return "stopped"
	case "errored", "failed":
		if p.Unstable {
			return "crash-loop"
		}
		return "offline"
	}
	return "unknown"
}

func (s *Service) view(a App, inv *proto.Inventory) View {
	v := View{
		ID: a.ID, Name: a.Name, Slug: slug(a.Name), ServerID: a.ServerID, Manager: managerLabel[a.Manager], ProcessKey: a.ProcessKey,
		Path: a.Path, Status: "unknown", Ports: []PortView{}, HealthStatus: "disabled", Notes: a.Notes, Flags: a.Flags,
		Source: a.Source, DiscoveredAt: a.CreatedAt, LastSeen: a.LastSeenAt, Markers: []string{},
		Health: HealthView{IntervalSec: 30, TimeoutMs: 5000, ExpectedStatus: 200},
	}
	switch a.Manager {
	case "pm2":
		v.PM2Name = a.ProcessKey
	case "systemd":
		v.Unit = a.ProcessKey
	}
	if a.Domain != "" {
		d := a.Domain
		v.Domain = &d
	}
	if s.ErrorCount != nil {
		v.Errors24h = s.ErrorCount(a.ID)
	}
	s.applyHealth(&v)
	var scanned int64
	var bytes float64
	if s.db.QueryRow(`SELECT scanned_at, json_extract(data, '$.totalBytes') FROM app_storage WHERE app_id = ?`, a.ID).Scan(&scanned, &bytes) == nil {
		v.StorageScanned, v.FolderMB = scanned, math.Round(bytes/(1<<20)*10)/10
	}
	p, pr := match(a, inv)
	if pr != nil {
		v.Runtime, v.Markers = pr.Runtime, pr.Markers
		if v.Path == "" {
			v.Path = pr.Path
		}
		if g := pr.Git; g != nil {
			v.Git = &GitView{Repo: repoName(g.Remote), Remote: g.Remote, Branch: g.Branch, Commit: g.Commit, Message: g.Message,
				Author: g.Author, Date: g.Date, Dirty: g.Dirty, Ahead: g.Ahead, Behind: g.Behind, Changes: g.Changes}
		}
	}
	connected := s.Connected == nil || s.Connected(a.ServerID)
	if p == nil {
		switch {
		case a.Manager == "standalone" && inv != nil && connected:
			staticSite(&v, a, inv.Routes)
		case inv != nil && connected:
			v.Status = "offline" // the manager no longer knows this process
		}
		return v
	}
	v.ProcessFound = true
	if v.Path == "" {
		v.Path = p.Cwd
	}
	if p.Runtime != "" {
		v.Runtime = p.Runtime
	}
	v.RawStatus, v.Restarts, v.Instances, v.User = p.RawStatus, p.Restarts, p.Instances, p.User
	v.Script, v.Interpreter, v.ExecStart, v.OutLog, v.ErrLog = p.Script, p.Interpreter, p.ExecStart, p.OutLog, p.ErrLog
	if len(p.PM2IDs) > 0 {
		id := p.PM2IDs[0]
		v.PM2ID = &id
	}
	if connected {
		v.Status = appStatus(p)
	}
	if p.PID > 0 && v.Status != "stopped" && v.Status != "offline" {
		pid := p.PID
		v.PID = &pid
		v.CPU = math.Round(p.CPU*10) / 10
		v.RAM = math.Round(float64(p.MemBytes) / (1 << 20))
		if p.StartedAt > 0 {
			v.UptimeSec = max(0, (s.Now().UnixMilli()-p.StartedAt)/1000)
		}
	}
	ports := map[int]bool{}
	for _, x := range p.Ports {
		v.Ports = append(v.Ports, PortView{Port: x.Port, Bind: x.Addr, Proto: x.Proto})
		ports[x.Port] = true
	}
	if a.Domain == "" && inv != nil {
		for _, r := range inv.Routes {
			if ports[r.Port] {
				v.SuggestedDomain = r.Host
				break
			}
		}
	}
	return v
}
