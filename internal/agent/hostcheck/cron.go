package hostcheck

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

// Cron finds the scheduled jobs someone set up on this server: users' crontabs, files in
// /etc/cron.d that no package installed, and systemd timers in /etc/systemd/system. The
// system log says when each crontab job last ran; systemd says how each timer's last
// run went.
func Cron(ctx context.Context) proto.CronReport {
	var jobs []proto.CronJob
	jobs = append(jobs, crontab(p("etc", "crontab"), "cron.d", "")...)
	if files, _ := filepath.Glob(p("etc", "cron.d", "*")); files != nil {
		for _, f := range files {
			if !packaged(ctx, onHost(f)) {
				jobs = append(jobs, crontab(f, "cron.d", "")...)
			}
		}
	}
	for _, dir := range []string{p("var", "spool", "cron", "crontabs"), p("var", "spool", "cron")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() {
				jobs = append(jobs, crontab(filepath.Join(dir, e.Name()), "crontab", e.Name())...)
			}
		}
	}
	runs, hasLog := cronRuns(ctx)
	for i := range jobs {
		j := &jobs[i]
		j.LastRun = runs.last(j.User, j.Command)
	}
	jobs = append(jobs, timers(ctx)...)
	return proto.CronReport{Jobs: jobs, HasLog: hasLog, TimeZone: timeZone()}
}

// timeZone names the server's time zone from /etc/localtime ("UTC" if unknown).
func timeZone() string {
	if l, err := os.Readlink(p("etc", "localtime")); err == nil {
		if _, name, ok := strings.Cut(filepath.ToSlash(l), "zoneinfo/"); ok {
			return name
		}
	}
	if b, err := os.ReadFile(p("etc", "timezone")); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	return "UTC"
}

var (
	envLine   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)
	shortcuts = map[string]bool{"@yearly": true, "@annually": true, "@monthly": true, "@weekly": true, "@daily": true, "@midnight": true, "@hourly": true}
)

// crontab reads one crontab. user is its owner for users' crontabs; system crontabs
// ("" here) name the user on each line.
func crontab(path, source, user string) []proto.CronJob {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 1<<20 {
		return nil
	}
	var out []proto.CronJob
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || envLine.MatchString(line) {
			continue
		}
		f := strings.Fields(line)
		var sched string
		switch {
		case shortcuts[f[0]]:
			sched, f = f[0], f[1:]
		case strings.HasPrefix(f[0], "@") || len(f) < 6: // @reboot, or not a schedule
			continue
		default:
			sched, f = strings.Join(f[:5], " "), f[5:]
		}
		u := user
		if u == "" {
			if len(f) < 2 {
				continue
			}
			u, f = f[0], f[1:]
		}
		if len(f) == 0 {
			continue
		}
		// The command is the rest of the line as written (spacing kept).
		cmd := strings.TrimSpace(line[strings.Index(line, f[0]):])
		if strings.Contains(cmd, "run-parts") || strings.Contains(cmd, "anacron") {
			continue // the system's own cron.daily/weekly/monthly
		}
		file := onHost(path)
		h := sha1.Sum([]byte(file + "\n" + u + "\n" + sched + "\n" + cmd))
		out = append(out, proto.CronJob{ID: hex.EncodeToString(h[:6]), Source: source, File: file, User: u, Schedule: sched, Command: cmd})
		if len(out) >= 200 {
			break
		}
	}
	return out
}

// packaged reports whether a package installed the file (its jobs are the system's).
func packaged(ctx context.Context, file string) bool {
	if _, ok := output(ctx, 5*time.Second, "dpkg", "-S", file); ok {
		return true
	}
	_, ok := output(ctx, 5*time.Second, "rpm", "-qf", file)
	return ok
}

// ---------- when crontab jobs ran (system log) ----------

type runLog map[string]int64 // "user\x00command" → last run, unix ms

func (r runLog) last(user, cmd string) int64 {
	if t, ok := r[user+"\x00"+cmd]; ok {
		return t
	}
	var best int64
	for k, t := range r { // cron may shorten long commands in the log
		u, logged, _ := strings.Cut(k, "\x00")
		if u == user && len(logged) >= 40 && strings.HasPrefix(cmd, logged) && t > best {
			best = t
		}
	}
	return best
}

var (
	cronCmd = regexp.MustCompile(`^\((\S+)\) CMD \((.*)\)$`)
	cronMu  sync.Mutex
	seen    = runLog{}
	seenAt  time.Time // read up to here
	logSeen bool
)

// cronRuns reads cron's "(user) CMD (command)" lines: the last 8 days at first, then
// only what is new.
func cronRuns(ctx context.Context) (runLog, bool) {
	cronMu.Lock()
	defer cronMu.Unlock()
	since := seenAt
	if since.IsZero() {
		since = time.Now().Add(-8 * 24 * time.Hour)
	}
	now := time.Now()
	raw, ok := output(ctx, 60*time.Second, "journalctl", "-q", "--no-pager", "-o", "json", "--output-fields=MESSAGE,__REALTIME_TIMESTAMP",
		"--since", "@"+strconv.FormatInt(since.Unix(), 10), "-t", "CRON", "-t", "CROND", "-t", "crond", "-t", "cron")
	if !ok {
		return seen, logSeen
	}
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e struct {
			Message  any    `json:"MESSAGE"`
			Realtime string `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		msg, _ := e.Message.(string)
		m := cronCmd.FindStringSubmatch(strings.TrimSpace(msg))
		if m == nil {
			continue
		}
		us, _ := strconv.ParseInt(e.Realtime, 10, 64)
		k := m[1] + "\x00" + m[2]
		if t := us / 1000; t > seen[k] {
			seen[k] = t
		}
		logSeen = true
	}
	seenAt = now.Add(-time.Minute) // overlap a little: lines written while reading
	if len(seen) > 2000 {          // commands that change every time (dates in them): forget
		seen, seenAt = runLog{}, time.Time{}
	}
	return seen, logSeen
}

// ---------- systemd timers ----------

var (
	argvRe     = regexp.MustCompile(`argv\[\]=([^;]*)`)
	calendarRe = regexp.MustCompile(`OnCalendar=([^;]+)`)
)

// onHost is a path as the server sees it ("/etc/cron.d/x"), also when Root is a test folder.
func onHost(path string) string {
	rel, err := filepath.Rel(Root, path)
	if err != nil {
		return path
	}
	return "/" + filepath.ToSlash(rel)
}

func timers(ctx context.Context) []proto.CronJob {
	raw, ok := output(ctx, 15*time.Second, "systemctl", "list-timers", "--all", "--output=json", "--no-pager")
	if !ok {
		return nil
	}
	var list []struct {
		Next      int64  `json:"next"` // µs
		Last      int64  `json:"last"`
		Unit      string `json:"unit"`
		Activates string `json:"activates"`
	}
	if json.Unmarshal([]byte(raw), &list) != nil {
		return nil
	}
	var out []proto.CronJob
	for _, t := range list {
		props := show(ctx, t.Unit, "FragmentPath", "TimersCalendar", "TimersMonotonic")
		if !strings.HasPrefix(props["FragmentPath"], "/etc/systemd/system/") || t.Activates == "" {
			continue // the system's own timers
		}
		svc := show(ctx, t.Activates, "Result", "ExecMainStatus", "ExecStart", "User")
		sched := props["TimersCalendar"]
		if m := calendarRe.FindStringSubmatch(sched); m != nil {
			sched = strings.TrimSpace(m[1])
		} else if props["TimersMonotonic"] != "" {
			sched = strings.TrimSpace(strings.Trim(props["TimersMonotonic"], "{}"))
		}
		cmd := ""
		if m := argvRe.FindStringSubmatch(svc["ExecStart"]); m != nil {
			cmd = strings.TrimSpace(m[1])
		}
		j := proto.CronJob{ID: "timer:" + t.Unit, Source: "timer", File: t.Unit, User: svc["User"], Schedule: sched, Command: cmd,
			LastRun: t.Last / 1000, NextRun: t.Next / 1000}
		if j.User == "" {
			j.User = "root"
		}
		if r := svc["Result"]; r != "" && r != "success" {
			j.Failed, j.LastError = true, r
			if st := svc["ExecMainStatus"]; st != "" && st != "0" {
				j.LastError += " (exit status " + st + ")"
			}
		}
		out = append(out, j)
		if len(out) >= 100 {
			break
		}
	}
	return out
}

func show(ctx context.Context, unit string, props ...string) map[string]string {
	out := map[string]string{}
	raw, ok := output(ctx, 10*time.Second, "systemctl", "show", unit, "--no-pager", "-p", strings.Join(props, ","))
	if !ok {
		return out
	}
	for _, line := range strings.Split(raw, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}
