// Package exec runs commands requested by the server: built-in manager actions and
// admin-defined custom commands. Custom commands are confined to the app directory,
// run as the app's own user, and can be refused entirely by the local config.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/agent/manager"
	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

const (
	maxStreamBytes = 1 << 20 // per stream; the rest is discarded with a note
	chunkEvery     = 250 * time.Millisecond
	maxConcurrent  = 4
	maxTimeout     = time.Hour
)

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Sender delivers frames to the server; it fails while disconnected.
type Sender func(typ string, v any) error

type Runner struct {
	AllowCustom bool
	// Lookup returns the process the manager reports for an app (for PM2 home and user).
	Lookup  func(manager, key string) (proto.Process, bool)
	Send    Sender
	Rescan  func() // ask for a fresh inventory after an action
	sem     chan struct{}
	initSem sync.Once
}

// Handle runs one request in the background and reports its result.
func (r *Runner) Handle(ctx context.Context, req proto.Exec) {
	r.initSem.Do(func() { r.sem = make(chan struct{}, maxConcurrent) })
	go func() {
		select {
		case r.sem <- struct{}{}:
			defer func() { <-r.sem }()
		case <-ctx.Done():
			return
		}
		var res proto.ExecResult
		if req.Kind == "builtin" {
			res = r.builtin(ctx, req)
		} else {
			res = r.custom(ctx, req)
		}
		res.RunID = req.RunID
		r.Send(proto.TypeExecResult, res)
		if req.Kind == "builtin" && r.Rescan != nil {
			r.Rescan()
		}
	}()
}

func (r *Runner) builtin(ctx context.Context, req proto.Exec) proto.ExecResult {
	home := ""
	if p, ok := r.Lookup(req.Manager, req.Key); ok {
		home = p.PM2Home
	}
	preview, out, err := manager.Act(ctx, req.Manager, req.Key, home, req.Action)
	if len(out) > 0 {
		r.Send(proto.TypeExecOutput, proto.ExecOutput{RunID: req.RunID, Stream: "stdout", Data: string(out)})
	}
	if err != nil {
		return proto.ExecResult{Status: "failed", ExitCode: -1, Error: err.Error(), Preview: preview, RunAs: "root"}
	}
	return proto.ExecResult{Status: "succeeded", Preview: preview, RunAs: "root"}
}

// ConfineDir resolves sub inside base and refuses anything that escapes it,
// including through symlinks.
func ConfineDir(base, sub string) (string, error) {
	if base == "" || !filepath.IsAbs(base) {
		return "", errors.New("the app has no absolute working directory")
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("app directory: %w", err)
	}
	if filepath.IsAbs(sub) {
		return "", errors.New("command directory must be relative to the app directory")
	}
	target, err := filepath.EvalSymlinks(filepath.Join(realBase, sub))
	if err != nil {
		return "", fmt.Errorf("command directory: %w", err)
	}
	if target != realBase && !strings.HasPrefix(target, realBase+string(filepath.Separator)) {
		return "", errors.New("command directory escapes the app directory")
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return "", errors.New("command directory is not a directory")
	}
	return target, nil
}

// runAs picks the user for a custom command: the app process's user, else the owner
// of the app directory.
func (r *Runner) runAs(req proto.Exec, dir string) (*user.User, error) {
	name := ""
	if p, ok := r.Lookup(req.Manager, req.Key); ok && p.User != "" {
		name = p.User
	}
	if name == "" {
		_, _, owner, ok := sysexec.Owner(dir)
		if !ok {
			return nil, errors.New("cannot determine the owner of " + dir)
		}
		name = owner
	}
	if u, err := user.Lookup(name); err == nil {
		return u, nil
	}
	return user.LookupId(name)
}

func (r *Runner) custom(ctx context.Context, req proto.Exec) proto.ExecResult {
	if !r.AllowCustom {
		return proto.ExecResult{Status: "refused", ExitCode: -1, Error: "custom commands are disabled on this server (allowCustomCommands=false in /etc/ops-agent/config.json)"}
	}
	if strings.TrimSpace(req.Command) == "" || len(req.Command) > 4096 {
		return proto.ExecResult{Status: "refused", ExitCode: -1, Error: "empty or oversized command"}
	}
	dir, err := ConfineDir(req.AppPath, req.Cwd)
	if err != nil {
		return proto.ExecResult{Status: "refused", ExitCode: -1, Error: err.Error()}
	}
	u, err := r.runAs(req, dir)
	if err != nil {
		return proto.ExecResult{Status: "error", ExitCode: -1, Error: err.Error()}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	path := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if nd := manager.NodeDir(); nd != "" {
		path = nd + ":" + path // node/npm from the same install PM2 uses (nvm)
	}
	env := []string{"PATH=" + path, "HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username, "LANG=C.UTF-8", "CI=1"}
	for _, kv := range req.Env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && envKeyRe.MatchString(k) {
			env = append(env, kv)
		}
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 || timeout > maxTimeout {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := osexec.CommandContext(cctx, "/bin/sh", "-c", req.Command)
	cmd.Dir, cmd.Env = dir, env
	prepare(cmd, uid, gid) // own process group, run as uid/gid, kill the group on timeout
	res := proto.ExecResult{RunAs: u.Username, Preview: req.Command}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		res.Status, res.ExitCode, res.Error = "error", -1, err.Error()
		return res
	}
	var wg sync.WaitGroup
	for _, s := range []struct {
		r    io.Reader
		name string
	}{{stdout, "stdout"}, {stderr, "stderr"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.stream(req.RunID, s.name, s.r)
		}()
	}
	wg.Wait()
	err = cmd.Wait()
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		res.Status, res.ExitCode, res.Error = "timeout", -1, fmt.Sprintf("killed after %s", timeout)
	case err != nil:
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			res.Status, res.ExitCode = "failed", ee.ExitCode()
		} else {
			res.Status, res.ExitCode, res.Error = "error", -1, err.Error()
		}
	default:
		res.Status = "succeeded"
	}
	return res
}

// stream forwards output in chunks, capping the total per stream.
func (r *Runner) stream(runID, name string, rd io.Reader) {
	var mu sync.Mutex
	var buf bytes.Buffer
	sent := 0
	truncated := false
	flush := func() {
		mu.Lock()
		defer mu.Unlock()
		if buf.Len() == 0 {
			return
		}
		r.Send(proto.TypeExecOutput, proto.ExecOutput{RunID: runID, Stream: name, Data: buf.String()})
		buf.Reset()
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(chunkEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				flush()
			}
		}
	}()
	p := make([]byte, 16<<10)
	for {
		n, err := rd.Read(p)
		if n > 0 {
			mu.Lock()
			if room := maxStreamBytes - sent; room > 0 {
				w := min(n, room)
				buf.Write(p[:w])
				sent += w
			} else if !truncated {
				truncated = true
				buf.WriteString("\n[output truncated: more than 1 MiB]\n")
			}
			full := buf.Len() >= 16<<10
			mu.Unlock()
			if full {
				flush()
			}
		}
		if err != nil {
			break
		}
	}
	close(done)
	flush()
}
