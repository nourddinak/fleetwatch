// Package gitinfo reads repository state with read-only git commands.
package gitinfo

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

const ttl = 2 * time.Minute

type entry struct {
	at  time.Time
	git *proto.Git
}

// Cache avoids re-running git for every inventory.
type Cache struct {
	mu sync.Mutex
	m  map[string]entry
}

func NewCache() *Cache { return &Cache{m: map[string]entry{}} }

// Get returns git info for dir, or nil if dir is not a repository root.
func (c *Cache) Get(ctx context.Context, dir string) *proto.Git {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return nil
	}
	c.mu.Lock()
	e, ok := c.m[dir]
	c.mu.Unlock()
	if ok && time.Since(e.at) < ttl {
		return e.git
	}
	g := Read(ctx, dir)
	c.mu.Lock()
	c.m[dir] = entry{time.Now(), g}
	c.mu.Unlock()
	return g
}

// Read runs git as the directory owner (so nothing in the repo becomes root-owned)
// with --no-optional-locks (so status never rewrites the index).
func Read(ctx context.Context, dir string) *proto.Git {
	o := sysexec.Opts{Dir: dir, Timeout: 5 * time.Second, MaxOut: 256 << 10}
	if uid, gid, _, ok := sysexec.Owner(dir); ok {
		o.UID, o.GID, o.AsUser = uid, gid, true
		o.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "GIT_TERMINAL_PROMPT=0", "LANG=C.UTF-8"}
	}
	git := func(args ...string) string {
		out, _ := sysexec.Run(ctx, o, "git", append([]string{"-c", "safe.directory=*", "--no-optional-locks"}, args...)...)
		return strings.TrimSpace(string(out))
	}
	g := &proto.Git{Changes: []proto.GitChange{}}
	g.Remote = StripCredentials(git("config", "--get", "remote.origin.url"))
	g.Branch = git("rev-parse", "--abbrev-ref", "HEAD")
	if parts := strings.Split(git("log", "-1", "--format=%h%x1f%s%x1f%an%x1f%ct"), "\x1f"); len(parts) == 4 {
		g.Commit, g.Message, g.Author = parts[0], parts[1], parts[2]
		if ts, err := strconv.ParseInt(parts[3], 10, 64); err == nil {
			g.Date = ts * 1000
		}
	}
	ParseStatus(git("status", "--porcelain=v1", "--branch"), g)
	return g
}

var aheadRe = regexp.MustCompile(`ahead (\d+)`)
var behindRe = regexp.MustCompile(`behind (\d+)`)

// ParseStatus fills branch tracking and changed files from `git status --porcelain=v1 --branch`.
func ParseStatus(out string, g *proto.Git) {
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			if m := aheadRe.FindStringSubmatch(line); m != nil {
				g.Ahead, _ = strconv.Atoi(m[1])
			}
			if m := behindRe.FindStringSubmatch(line); m != nil {
				g.Behind, _ = strconv.Atoi(m[1])
			}
			continue
		}
		if len(line) < 4 {
			continue
		}
		g.Dirty = true
		if len(g.Changes) >= 20 {
			continue
		}
		code, file := line[:2], strings.TrimSpace(line[3:])
		state := "Modified"
		switch {
		case code == "??":
			state = "Untracked"
		case strings.ContainsRune(code, 'A'):
			state = "Added"
		case strings.ContainsRune(code, 'D'):
			state = "Deleted"
		case strings.ContainsRune(code, 'R'):
			state = "Renamed"
		}
		g.Changes = append(g.Changes, proto.GitChange{State: state, File: file})
	}
}

// StripCredentials removes user:password@ from http(s) remotes. scp-style remotes
// (git@host:repo) carry no secret and are returned unchanged.
func StripCredentials(remote string) string {
	u, err := url.Parse(remote)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return remote
	}
	if u.Scheme == "ssh" && u.User != nil {
		u.User = url.User(u.User.Username()) // keep "git@", drop any password
		return u.String()
	}
	u.User = nil
	return u.String()
}
