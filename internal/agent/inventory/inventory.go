// Package inventory assembles one snapshot of what runs on the host.
package inventory

import (
	"context"
	"slices"
	"sync"
	"time"

	"fleetwatch/internal/agent/caddy"
	"fleetwatch/internal/agent/gitinfo"
	"fleetwatch/internal/agent/manager"
	"fleetwatch/internal/agent/nginx"
	"fleetwatch/internal/agent/ports"
	"fleetwatch/internal/agent/procs"
	"fleetwatch/internal/agent/projects"
	"fleetwatch/internal/proto"
)

const maxProjects = 300

type Collector struct {
	managers []manager.ProcessManager
	tracker  procs.Tracker
	git      *gitinfo.Cache
	nginx    nginx.Cache
	caddy    caddy.Cache
	certs    caddy.CertCache

	mu   sync.Mutex
	last []proto.Process
}

func NewCollector() *Collector {
	return &Collector{managers: []manager.ProcessManager{manager.PM2{}, manager.Systemd{}}, git: gitinfo.NewCache()}
}

// Collect gathers processes, ports, projects (from process directories and the
// discovery roots) and Caddy routes. Probe failures are reported in Errors.
func (c *Collector) Collect(ctx context.Context, roots []string) proto.Inventory {
	tab := c.tracker.Snapshot()
	inv := proto.Inventory{TS: time.Now().UnixMilli(), Processes: []proto.Process{}, Errors: []string{}}

	var err error
	if inv.Ports, err = ports.List(tab); err != nil {
		inv.Errors = append(inv.Errors, "ports: "+err.Error())
		inv.Ports = []proto.Port{}
	}
	for _, m := range c.managers {
		list, err := m.List(ctx, tab)
		if err != nil {
			inv.Errors = append(inv.Errors, err.Error())
		}
		inv.Processes = append(inv.Processes, list...)
	}

	projectDirs := map[string]bool{}
	for i := range inv.Processes {
		p := &inv.Processes[i]
		if p.PID > 0 {
			tree := tab.Tree(p.PID)
			for _, port := range inv.Ports {
				if slices.Contains(tree, port.PID) {
					p.Ports = append(p.Ports, port)
				}
			}
		}
		if !p.Likely || p.Cwd == "" || p.Cwd == "/" {
			continue
		}
		p.Project = projects.FindRoot(p.Cwd)
		projectDirs[p.Project] = true
	}

	for dir := range projectDirs {
		if pr := projects.Detect(dir); pr != nil {
			inv.Projects = append(inv.Projects, *pr)
		}
	}
	for _, pr := range projects.Scan(roots, maxProjects) {
		if !projectDirs[pr.Path] {
			projectDirs[pr.Path] = true
			inv.Projects = append(inv.Projects, pr)
		}
	}
	runtimes := map[string]string{}
	for i := range inv.Projects {
		pr := &inv.Projects[i]
		pr.Git = c.git.Get(ctx, pr.Path)
		runtimes[pr.Path] = pr.Runtime
	}
	// What the process runs wins; the project only refines it (Node.js → Next.js, a Go
	// binary in a "Go + React" project) or fills in when the process says nothing.
	for i := range inv.Processes {
		p := &inv.Processes[i]
		p.Runtime = projects.Combine(p.Runtime, runtimes[p.Project])
		if p.Runtime == "" && p.Likely {
			p.Runtime = "Binary"
		}
	}
	if inv.Projects == nil {
		inv.Projects = []proto.Project{}
	}
	inv.Routes = append(append([]proto.Route{}, c.caddy.Routes(ctx)...), c.nginx.Routes()...)
	inv.Certs = c.certs.Certs(ctx, inv.Routes)
	c.mu.Lock()
	c.last = inv.Processes
	c.mu.Unlock()
	return inv
}

// LogFiles returns a PM2 app's output files from the latest inventory.
func (c *Collector) LogFiles(manager, key string) (outLog, errLog string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.last {
		if p.Manager == manager && p.Key == key {
			return p.OutLog, p.ErrLog
		}
	}
	return "", ""
}

// Process returns a copy of a managed process from the latest inventory.
func (c *Collector) Process(manager, key string) (proto.Process, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.last {
		if p.Manager == manager && p.Key == key {
			return p, true
		}
	}
	return proto.Process{}, false
}
