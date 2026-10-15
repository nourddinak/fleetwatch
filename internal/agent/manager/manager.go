// Package manager abstracts process managers (PM2, systemd; Docker later) so the rest
// of the agent never deals with their CLIs directly.
package manager

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"fleetwatch/internal/agent/procs"
	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// ProcessManager lists what a manager runs. Control actions (start/stop/restart) are
// added to this interface with the command system.
type ProcessManager interface {
	Name() string
	List(ctx context.Context, tab *procs.Table) ([]proto.Process, error)
}

// usage sums CPU and RSS over the process trees rooted at pids.
func usage(tab *procs.Table, pids ...int) (float64, uint64) {
	var all []int
	for _, pid := range pids {
		all = append(all, tab.Tree(pid)...)
	}
	return tab.Usage(all)
}

// Actions are the built-in commands every manager supports.
var Actions = []string{"start", "stop", "restart", "reload"}

var (
	pm2NameRe = regexp.MustCompile(`^[A-Za-z0-9@._:+-][A-Za-z0-9@._:+ -]{0,99}$`)
	unitRe    = regexp.MustCompile(`^[A-Za-z0-9@._:\-]{1,240}\.service$`)
)

// Act runs a built-in action. It returns what was executed and its output. Names are
// re-validated here: the agent never trusts the server with a raw argument.
func Act(ctx context.Context, managerName, key, pm2Home, action string) (preview string, out []byte, err error) {
	if !slices.Contains(Actions, action) {
		return "", nil, fmt.Errorf("unknown action %q", action)
	}
	switch managerName {
	case "pm2":
		if !pm2NameRe.MatchString(key) {
			return "", nil, fmt.Errorf("invalid PM2 name %q", key)
		}
		return pm2Act(ctx, key, pm2Home, action)
	case "systemd":
		if !unitRe.MatchString(key) {
			return "", nil, fmt.Errorf("invalid unit name %q", key)
		}
		verb := action
		if action == "reload" {
			verb = "reload-or-restart" // many services have no reload handler
		}
		preview = "systemctl " + verb + " " + key
		out, err = sysexec.Run(ctx, sysexec.Opts{Timeout: 90 * time.Second}, "systemctl", verb, "--", key)
		return preview, out, err
	}
	return "", nil, fmt.Errorf("manager %q has no built-in actions", managerName)
}

func pm2Act(ctx context.Context, name, home, action string) (string, []byte, error) {
	var tracker procs.Tracker
	var d *daemon
	for _, x := range findDaemons(tracker.Snapshot()) {
		if home == "" || x.home == home {
			d = &x
			break
		}
	}
	if d == nil {
		return "", nil, errors.New("no running PM2 daemon found for " + home)
	}
	preview := "pm2 " + action + " " + name
	out, err := pm2Run(ctx, *d, 90*time.Second, action, name)
	return preview, out, err
}
