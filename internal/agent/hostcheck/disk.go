// Package hostcheck looks at the host itself: what fills its disk (and cleans the safe
// parts), and its security (SSH settings, updates, logins, exposed secrets).
package hostcheck

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// Root is the filesystem root; tests point it at a temporary folder.
var Root = "/"

func p(parts ...string) string { return filepath.Join(append([]string{Root}, parts...)...) }

// homes are root's home and every /home/<user>.
func homes() []string {
	out := []string{p("root")}
	if entries, err := os.ReadDir(p("home")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, p("home", e.Name()))
			}
		}
	}
	return out
}

// size totals regular files under path (not following symlinks), within limits.
func size(ctx context.Context, path string, match func(string, fs.DirEntry) bool) (int64, int) {
	var total int64
	files := 0
	deadline := time.Now().Add(30 * time.Second)
	filepath.WalkDir(path, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() && (match == nil || match(name, d)) {
			if info, err := d.Info(); err == nil {
				total += info.Size()
				files++
			}
		}
		return nil
	})
	return total, files
}

var rotated = regexp.MustCompile(`(\.gz|\.xz|\.bz2|\.zst|\.old|\.\d+|\.\d+\.log|-\d{8})$`)

// cleanup is one kind of space the agent knows how to give back safely.
type cleanup struct {
	id, label, note string
	measure         func(ctx context.Context) (int64, int)
	clean           func(ctx context.Context) error
}

func run(ctx context.Context, timeout time.Duration, name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return errors.New(name + " is not installed")
	}
	_, err := sysexec.Run(ctx, sysexec.Opts{Timeout: timeout}, name, args...)
	return err
}

func removeMatching(dirs []string, match func(string, fs.DirEntry) bool) error {
	var firstErr error
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && match(name, d) {
				if err := os.Remove(name); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			return nil
		})
	}
	return firstErr
}

func userDirs(rel ...string) []string {
	var out []string
	for _, h := range homes() {
		out = append(out, filepath.Join(append([]string{h}, rel...)...))
	}
	return out
}

func sizeAll(ctx context.Context, dirs []string, match func(string, fs.DirEntry) bool) (int64, int) {
	var total int64
	n := 0
	for _, d := range dirs {
		b, f := size(ctx, d, match)
		total, n = total+b, n+f
	}
	return total, n
}

func cleanups() []cleanup {
	isRotated := func(name string, d fs.DirEntry) bool {
		return !strings.Contains(name, string(filepath.Separator)+"journal"+string(filepath.Separator)) && rotated.MatchString(d.Name())
	}
	caches := func() []string {
		var dirs []string
		for _, rel := range [][]string{{".npm", "_cacache"}, {".cache", "yarn"}, {".yarn", "berry", "cache"}, {".cache", "pip"}, {".cache", "go-build"}, {".bun", "install", "cache"}} {
			dirs = append(dirs, userDirs(rel...)...)
		}
		return dirs
	}
	pm2Logs := func() []string { return userDirs(".pm2", "logs") }
	isLog := func(name string, _ fs.DirEntry) bool { return strings.HasSuffix(name, ".log") }
	return []cleanup{
		{id: "journal", label: "System journal", note: "Keeps the last 7 days (and at most 500 MB).",
			measure: func(ctx context.Context) (int64, int) { return size(ctx, p("var", "log", "journal"), nil) },
			clean: func(ctx context.Context) error {
				return run(ctx, 2*time.Minute, "journalctl", "--vacuum-time=7d", "--vacuum-size=500M")
			}},
		{id: "oldlogs", label: "Old rotated logs in /var/log", note: "Compressed and numbered copies (.gz, .1 …); current logs stay.",
			measure: func(ctx context.Context) (int64, int) { return size(ctx, p("var", "log"), isRotated) },
			clean:   func(ctx context.Context) error { return removeMatching([]string{p("var", "log")}, isRotated) }},
		{id: "packages", label: "Downloaded package files", note: "Installers apt/dnf already used; they download again if ever needed.",
			measure: func(ctx context.Context) (int64, int) {
				return sizeAll(ctx, []string{p("var", "cache", "apt", "archives"), p("var", "cache", "dnf"), p("var", "cache", "yum")}, nil)
			},
			clean: func(ctx context.Context) error {
				if _, err := exec.LookPath("apt-get"); err == nil {
					return run(ctx, 2*time.Minute, "apt-get", "clean")
				}
				if _, err := exec.LookPath("dnf"); err == nil {
					return run(ctx, 2*time.Minute, "dnf", "clean", "packages")
				}
				return run(ctx, 2*time.Minute, "yum", "clean", "packages")
			}},
		{id: "docker", label: "Unused Docker images and build cache", note: "Only images no container uses (running or stopped); volumes are never touched.",
			measure: dockerReclaimable,
			clean: func(ctx context.Context) error {
				if err := run(ctx, 10*time.Minute, "docker", "image", "prune", "-a", "-f"); err != nil {
					return err
				}
				return run(ctx, 10*time.Minute, "docker", "builder", "prune", "-a", "-f")
			}},
		{id: "caches", label: "Package manager caches (npm, yarn, pip, Go, Bun)", note: "Download caches in the users' home folders; your apps' node_modules stay.",
			measure: func(ctx context.Context) (int64, int) { return sizeAll(ctx, caches(), nil) },
			clean: func(ctx context.Context) error {
				var firstErr error
				for _, d := range caches() {
					if err := os.RemoveAll(d); err != nil && firstErr == nil {
						firstErr = err
					}
				}
				return firstErr
			}},
		{id: "pm2logs", label: "PM2 log files", note: "Emptied, not deleted, so running apps keep logging. Fleetwatch keeps its own copy of recent lines.",
			measure: func(ctx context.Context) (int64, int) { return sizeAll(ctx, pm2Logs(), isLog) },
			clean: func(ctx context.Context) error {
				var firstErr error
				for _, dir := range pm2Logs() {
					filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
						if err == nil && d.Type().IsRegular() && isLog(name, d) {
							if err := os.Truncate(name, 0); err != nil && firstErr == nil {
								firstErr = err
							}
						}
						return nil
					})
				}
				return firstErr
			}},
		{id: "coredumps", label: "Crash dumps", note: "Memory dumps of programs that crashed; only useful for debugging those crashes.",
			measure: func(ctx context.Context) (int64, int) {
				return sizeAll(ctx, []string{p("var", "lib", "systemd", "coredump"), p("var", "crash")}, nil)
			},
			clean: func(ctx context.Context) error {
				return removeMatching([]string{p("var", "lib", "systemd", "coredump"), p("var", "crash")}, func(string, fs.DirEntry) bool { return true })
			}},
	}
}

// dockerReclaimable reads `docker system df` (images and build cache).
func dockerReclaimable(ctx context.Context) (int64, int) {
	if _, err := exec.LookPath("docker"); err != nil {
		return 0, 0
	}
	out, err := sysexec.Run(ctx, sysexec.Opts{Timeout: 30 * time.Second}, "docker", "system", "df", "--format", "{{json .}}")
	if err != nil {
		return 0, 0
	}
	var total int64
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		var row struct{ Type, Reclaimable, TotalCount string }
		if json.Unmarshal([]byte(line), &row) != nil || (row.Type != "Images" && row.Type != "Build Cache") {
			continue
		}
		total += parseSize(strings.Fields(row.Reclaimable + " ")[0])
		c, _ := strconv.Atoi(row.TotalCount)
		n += c
	}
	return total, n
}

var sizeRe = regexp.MustCompile(`^([\d.]+)\s*([kKMGT]?i?B)$`)

// parseSize reads Docker's sizes ("1.2GB", "512kB", "0B").
func parseSize(s string) int64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[m[2]]
	return int64(v * mult)
}

var diskBusy sync.Mutex

// DiskScan measures what can be cleaned and which folders are biggest.
func DiskScan(ctx context.Context) proto.DiskReport {
	diskBusy.Lock()
	defer diskBusy.Unlock()
	start := time.Now()
	rep := proto.DiskReport{At: start.UnixMilli(), Items: []proto.DiskItem{}, Top: []proto.DiskDir{}}
	for _, c := range cleanups() {
		b, n := c.measure(ctx)
		rep.Items = append(rep.Items, proto.DiskItem{ID: c.id, Label: c.label, Note: c.note, Bytes: b, Files: n})
	}
	rep.Top = biggest(ctx, 15)
	rep.DurationMs = time.Since(start).Milliseconds()
	return rep
}

// DiskClean runs one cleanup and reports the space it gave back.
func DiskClean(ctx context.Context, req proto.DiskClean) proto.DiskCleaned {
	diskBusy.Lock()
	defer diskBusy.Unlock()
	res := proto.DiskCleaned{RunID: req.RunID, Action: req.Action}
	for _, c := range cleanups() {
		if c.id != req.Action {
			continue
		}
		before, _ := c.measure(ctx)
		if err := c.clean(ctx); err != nil {
			res.Error = err.Error()
		}
		after, _ := c.measure(ctx)
		res.Freed = max(before-after, 0)
		return res
	}
	res.Error = "unknown cleanup " + req.Action
	return res
}

var skipTop = map[string]bool{"proc": true, "sys": true, "dev": true, "run": true}

// biggest sizes folders two levels below the root (/var/lib, /home/alice …) on the
// root filesystem, within a time budget.
func biggest(ctx context.Context, n int) []proto.DiskDir {
	sizes := map[string]int64{}
	deadline := time.Now().Add(45 * time.Second)
	rootDev := deviceOf(Root)
	seen := 0
	filepath.WalkDir(Root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(Root, name)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if d.IsDir() && rel != "." {
			if len(parts) == 1 && skipTop[parts[0]] {
				return filepath.SkipDir
			}
			if dev := deviceOf(name); dev != rootDev { // other filesystems (mounted volumes) are not this disk
				return filepath.SkipDir
			}
		}
		if seen++; seen%5000 == 0 && (ctx.Err() != nil || time.Now().After(deadline)) {
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() || len(parts) < 2 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		key := "/" + parts[0]
		if len(parts) > 2 {
			key += "/" + parts[1]
		}
		sizes[key] += info.Size()
		return nil
	})
	out := make([]proto.DiskDir, 0, len(sizes))
	for k, v := range sizes {
		out = append(out, proto.DiskDir{Path: k, Bytes: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
