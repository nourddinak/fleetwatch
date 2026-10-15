// Package sysexec runs helper programs (pm2, systemctl, git) with a timeout, a capped
// output size and, optionally, as another user.
package sysexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Opts struct {
	Dir     string
	Env     []string // full environment; nil = minimal PATH-only environment
	UID     int      // run as this user when AsUser is true
	GID     int
	AsUser  bool
	Timeout time.Duration
	MaxOut  int // bytes of stdout kept; default 4 MiB
}

const defaultPath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Run executes name with args (never through a shell) and returns stdout.
func Run(ctx context.Context, o Opts, name string, args ...string) ([]byte, error) {
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Second
	}
	if o.MaxOut == 0 {
		o.MaxOut = 4 << 20
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	if cmd.Env == nil {
		cmd.Env = []string{defaultPath, "LANG=C.UTF-8"}
	}
	setUser(cmd, o)
	out := &capped{max: o.MaxOut}
	var stderr capped
	stderr.max = 8 << 10
	cmd.Stdout, cmd.Stderr = out, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.Bytes(), fmt.Errorf("%s: timed out after %s", name, o.Timeout)
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.Bytes(), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(stderr.String()))
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// capped is a buffer that silently drops bytes past max.
type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}
