package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"fleetwatch/internal/agent/procs"
	"fleetwatch/internal/agent/projects"
	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// PM2 finds every running PM2 daemon (one per user) and lists its apps.
type PM2 struct{}

func (PM2) Name() string { return "pm2" }

// The daemon renames itself to "PM2 vX.Y.Z: God Daemon (<PM2_HOME>)".
var daemonRe = regexp.MustCompile(`God Daemon \((/[^)]+)\)`)

type daemon struct {
	pid  int
	home string
	node string // node binary running the daemon, so nvm installs are found too
}

func findDaemons(tab *procs.Table) []daemon {
	var out []daemon
	for pid, p := range tab.ByPID {
		if !strings.HasPrefix(p.Name, "PM2") {
			continue
		}
		if m := daemonRe.FindStringSubmatch(procs.Cmdline(pid)); m != nil {
			out = append(out, daemon{pid: pid, home: filepath.Clean(m[1]), node: procs.Exe(pid)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].home < out[j].home })
	return out
}

func (PM2) List(ctx context.Context, tab *procs.Table) ([]proto.Process, error) {
	var all []proto.Process
	var errs []error
	for _, d := range findDaemons(tab) {
		raw, err := jlist(ctx, d)
		if err != nil {
			errs = append(errs, fmt.Errorf("pm2 (%s): %w", d.home, err))
			continue
		}
		list, err := ParseJList(raw, d.home)
		if err != nil {
			errs = append(errs, fmt.Errorf("pm2 (%s): %w", d.home, err))
			continue
		}
		for i := range list {
			list[i].CPU, list[i].MemBytes = usage(tab, instancePIDs(raw, list[i].Key)...)
		}
		all = append(all, list...)
	}
	return all, errors.Join(errs...)
}

// jlist runs `pm2 jlist` as the owner of the PM2 home so it talks to that user's daemon.
// It is only called for homes with a running daemon, so it never starts a new one.
func jlist(ctx context.Context, d daemon) ([]byte, error) {
	return pm2Run(ctx, d, 15*time.Second, "jlist")
}

// pm2Run runs the pm2 CLI that belongs to a running daemon, as the daemon's user.
func pm2Run(ctx context.Context, d daemon, timeout time.Duration, args ...string) ([]byte, error) {
	nodeDir := filepath.Dir(d.node)
	bin := ""
	for _, c := range []string{filepath.Join(nodeDir, "pm2"), "/usr/local/bin/pm2", "/usr/bin/pm2"} {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			bin = c
			break
		}
	}
	if bin == "" {
		return nil, errors.New("pm2 binary not found next to " + d.node)
	}
	o := sysexec.Opts{
		Timeout: timeout,
		Env:     []string{"PATH=" + nodeDir + ":/usr/local/bin:/usr/bin:/bin", "PM2_HOME=" + d.home, "HOME=" + filepath.Dir(d.home), "LANG=C.UTF-8", "NO_COLOR=1", "FORCE_COLOR=0"},
	}
	if uid, gid, _, ok := sysexec.Owner(d.home); ok {
		o.UID, o.GID, o.AsUser = uid, gid, true
	}
	return sysexec.Run(ctx, o, bin, args...)
}

// NodeDir is the directory of the node binary running the first PM2 daemon (for nvm
// installs this holds node, npm and pm2), or "".
func NodeDir() string {
	var tracker procs.Tracker
	for _, d := range findDaemons(tracker.Snapshot()) {
		if d.node != "" {
			return filepath.Dir(d.node)
		}
	}
	return ""
}

// jlistEntry declares only what we use: the app environment (pm2_env.env and the
// copied variables) is deliberately never decoded, so secrets cannot leak.
type jlistEntry struct {
	PID    int    `json:"pid"`
	Name   string `json:"name"`
	PMID   int    `json:"pm_id"`
	PM2Env struct {
		Status      string `json:"status"`
		PMUptime    int64  `json:"pm_uptime"`
		RestartTime int    `json:"restart_time"`
		Unstable    int    `json:"unstable_restarts"`
		ExecPath    string `json:"pm_exec_path"`
		Cwd         string `json:"pm_cwd"`
		Interpreter string `json:"exec_interpreter"`
		OutLog      string `json:"pm_out_log_path"`
		ErrLog      string `json:"pm_err_log_path"`
		Username    string `json:"username"`
	} `json:"pm2_env"`
}

func decodeJList(raw []byte) ([]jlistEntry, error) {
	// pm2 may print warnings before the JSON array.
	i := bytes.IndexByte(raw, '[')
	if i < 0 {
		return nil, errors.New("no JSON in pm2 jlist output")
	}
	var entries []jlistEntry
	if err := json.Unmarshal(raw[i:], &entries); err != nil {
		return nil, fmt.Errorf("parse pm2 jlist: %w", err)
	}
	return entries, nil
}

func instancePIDs(raw []byte, name string) []int {
	entries, _ := decodeJList(raw)
	var pids []int
	for _, e := range entries {
		if e.Name == name && e.PID > 0 {
			pids = append(pids, e.PID)
		}
	}
	return pids
}

// statusRank orders PM2 states from healthy to broken; an app shows its worst instance.
var statusRank = map[string]int{"online": 0, "starting": 1, "stopped": 2, "errored": 3}

func pm2Status(s string) string {
	switch s {
	case "online":
		return "online"
	case "launching", "waiting restart", "one-launch-status":
		return "starting"
	case "stopping", "stopped":
		return "stopped"
	case "errored":
		return "errored"
	}
	return "unknown"
}

// ParseJList turns `pm2 jlist` output into one Process per app name (cluster
// instances are merged).
func ParseJList(raw []byte, home string) ([]proto.Process, error) {
	entries, err := decodeJList(raw)
	if err != nil {
		return nil, err
	}
	byName := map[string]*proto.Process{}
	var order []string
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		st := pm2Status(e.PM2Env.Status)
		p := byName[e.Name]
		if p == nil {
			p = &proto.Process{
				Manager: "pm2", Key: e.Name, PM2Home: home, User: e.PM2Env.Username,
				Status: st, RawStatus: e.PM2Env.Status, PID: e.PID,
				Script: e.PM2Env.ExecPath, Interpreter: e.PM2Env.Interpreter, Cwd: e.PM2Env.Cwd,
				OutLog: e.PM2Env.OutLog, ErrLog: e.PM2Env.ErrLog, RestartPolicy: "pm2 autorestart",
				Likely: true, Reason: "managed by PM2",
			}
			if st == "online" {
				p.StartedAt = e.PM2Env.PMUptime
			}
			byName[e.Name] = p
			order = append(order, e.Name)
		} else if statusRank[st] > statusRank[p.Status] {
			p.Status, p.RawStatus = st, e.PM2Env.Status
		}
		p.PM2IDs = append(p.PM2IDs, e.PMID)
		p.Instances++
		p.Restarts += e.PM2Env.RestartTime
		p.Unstable = p.Unstable || e.PM2Env.Unstable > 0 || e.PM2Env.Status == "waiting restart"
		if p.PID == 0 {
			p.PID = e.PID
		}
	}
	out := make([]proto.Process, 0, len(order))
	for _, n := range order {
		p := byName[n]
		p.Runtime = projects.RuntimeFromExec(p.Interpreter)
		if p.Runtime == "" && p.Interpreter == "none" {
			p.Runtime = projects.ExecRuntime(p.Script, p.PID)
		}
		out = append(out, *p)
	}
	return out, nil
}
