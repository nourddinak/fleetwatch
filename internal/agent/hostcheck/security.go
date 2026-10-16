package hostcheck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// SecurityScan checks SSH settings, waiting updates, failed logins, the firewall and
// readable .env files in the given app folders.
func SecurityScan(ctx context.Context, req proto.SecurityScan) proto.SecurityReport {
	rep := proto.SecurityReport{At: time.Now().UnixMilli(), Findings: []proto.Finding{}}
	add := func(f ...proto.Finding) { rep.Findings = append(rep.Findings, f...) }
	password := false
	if f, pw := sshdFindings(ctx); f != nil {
		add(f...)
		password = pw
	}
	add(updateFindings(ctx)...)
	add(failedLoginFindings(ctx, password)...)
	add(firewallFindings(ctx)...)
	add(envFindings(req.AppPaths)...)
	return rep
}

func output(ctx context.Context, timeout time.Duration, name string, args ...string) (string, bool) {
	if _, err := exec.LookPath(name); err != nil {
		return "", false
	}
	out, err := sysexec.Run(ctx, sysexec.Opts{Timeout: timeout}, name, args...)
	return string(out), err == nil
}

// sshdFindings reads the effective SSH server settings (`sshd -T`).
func sshdFindings(ctx context.Context) ([]proto.Finding, bool) {
	out, ok := output(ctx, 10*time.Second, "sshd", "-T")
	if !ok {
		return nil, false
	}
	conf := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			conf[strings.ToLower(k)] = strings.ToLower(v)
		}
	}
	var fs []proto.Finding
	password := conf["passwordauthentication"] == "yes" || conf["kbdinteractiveauthentication"] == "yes" && conf["usepam"] == "yes"
	if password {
		sev, title := "warning", "SSH accepts passwords"
		if conf["permitrootlogin"] == "yes" {
			sev, title = "critical", "SSH accepts passwords, also for root"
		}
		fs = append(fs, proto.Finding{ID: "ssh_password", Severity: sev, Title: title,
			Detail: "Anyone on the internet can keep guessing passwords. SSH keys cannot be guessed.",
			Fix: "First make sure you can log in with an SSH key. Then in /etc/ssh/sshd_config (and any file in /etc/ssh/sshd_config.d/) set:\n" +
				"PasswordAuthentication no\nKbdInteractiveAuthentication no\nthen run: sudo systemctl reload ssh"})
	} else if conf["permitrootlogin"] == "yes" {
		fs = append(fs, proto.Finding{ID: "ssh_root", Severity: "info", Title: "root can log in over SSH",
			Detail: "Only with a key (passwords are off), which is safe, but a personal user with sudo leaves a better trail.",
			Fix:    "Set PermitRootLogin prohibit-password (or no) in /etc/ssh/sshd_config, then: sudo systemctl reload ssh"})
	}
	return fs, password
}

var secRepo = regexp.MustCompile(`(?i)-security|security\.`)

// updateFindings counts waiting (security) updates from the package lists the server
// already has; it does not refresh them.
func updateFindings(ctx context.Context) []proto.Finding {
	var fs []proto.Finding
	if out, ok := output(ctx, 60*time.Second, "apt-get", "-s", "-o", "Debug::NoLocking=1", "dist-upgrade"); ok {
		total, sec := 0, 0
		var names []string
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "Inst ") {
				continue
			}
			total++
			if secRepo.MatchString(line) {
				sec++
				if f := strings.Fields(line); len(f) > 1 && len(names) < 8 {
					names = append(names, f[1])
				}
			}
		}
		age := ""
		if fi, err := os.Stat(p("var", "lib", "apt", "lists")); err == nil {
			if d := time.Since(fi.ModTime()); d > 48*time.Hour {
				age = fmt.Sprintf(" The package lists are %d days old, so there may be more.", int(d.Hours()/24))
			}
		}
		if sec > 0 {
			fs = append(fs, proto.Finding{ID: "updates_security", Severity: "warning", Title: fmt.Sprintf("%d security update(s) waiting", sec),
				Detail: "Including " + strings.Join(names, ", ") + "." + age, Fix: "sudo apt update && sudo apt upgrade"})
		} else if total > 0 {
			fs = append(fs, proto.Finding{ID: "updates", Severity: "info", Title: fmt.Sprintf("%d update(s) waiting", total), Detail: "None of them is marked as a security fix." + age, Fix: "sudo apt update && sudo apt upgrade"})
		}
		if st, ok := output(ctx, 10*time.Second, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"); !ok || !strings.Contains(st, "install ok installed") {
			fs = append(fs, proto.Finding{ID: "updates_auto", Severity: "info", Title: "Security updates are not installed automatically",
				Fix: "sudo apt install unattended-upgrades && sudo dpkg-reconfigure -plow unattended-upgrades"})
		}
	} else if out, ok := output(ctx, 90*time.Second, "dnf", "-q", "updateinfo", "list", "--security"); ok {
		if n := len(strings.Fields(strings.TrimSpace(out))) / 3; n > 0 {
			fs = append(fs, proto.Finding{ID: "updates_security", Severity: "warning", Title: fmt.Sprintf("%d security update(s) waiting", n), Fix: "sudo dnf upgrade --security"})
		}
	}
	if _, err := os.Stat(p("var", "run", "reboot-required")); err == nil {
		fs = append(fs, proto.Finding{ID: "reboot", Severity: "info", Title: "A restart is needed to finish installed updates",
			Detail: "Updated parts (often the kernel) only take effect after a restart.", Fix: "Restart the server when it suits you: sudo reboot"})
	}
	return fs
}

var failedRe = regexp.MustCompile(`(?:Failed (?:password|publickey|none) for (?:invalid user )?(\S*)|Invalid user (\S*)) from (\S+)`)

func failedLoginFindings(ctx context.Context, password bool) []proto.Finding {
	lines := sshLines(ctx, time.Now().Add(-24*time.Hour))
	perIP := map[string]int{}
	total := 0
	for _, l := range lines {
		if m := failedRe.FindStringSubmatch(l.msg); m != nil {
			perIP[m[3]]++
			total++
		}
	}
	if total < 30 {
		return nil
	}
	type kv struct {
		ip string
		n  int
	}
	var top []kv
	for ip, n := range perIP {
		top = append(top, kv{ip, n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n })
	var most []string
	for i := 0; i < len(top) && i < 3; i++ {
		most = append(most, fmt.Sprintf("%s (%d)", top[i].ip, top[i].n))
	}
	fix := "This is normal background noise on the internet; with passwords off it is harmless."
	if password {
		fix = "Turn off SSH passwords (see above) so guessing cannot succeed."
	}
	if st, _ := output(ctx, 5*time.Second, "systemctl", "is-active", "fail2ban"); strings.TrimSpace(st) != "active" {
		fix += "\nTo block repeat guessers automatically: sudo apt install fail2ban"
	}
	sev := "info"
	if password {
		sev = "warning"
	}
	return []proto.Finding{{ID: "ssh_bruteforce", Severity: sev, Title: fmt.Sprintf("%d failed SSH logins in 24 hours from %d address(es)", total, len(perIP)),
		Detail: "Most from " + strings.Join(most, ", ") + ".", Fix: fix}}
}

func firewallFindings(ctx context.Context) []proto.Finding {
	if out, ok := output(ctx, 10*time.Second, "ufw", "status"); ok && strings.Contains(out, "Status: inactive") {
		return []proto.Finding{{ID: "firewall", Severity: "info", Title: "The firewall (ufw) is off",
			Detail: "Every program that listens on a public address can be reached from the internet.",
			Fix:    "sudo ufw allow OpenSSH && sudo ufw allow 80,443/tcp && sudo ufw enable"}}
	}
	if out, ok := output(ctx, 10*time.Second, "firewall-cmd", "--state"); ok && !strings.Contains(out, "running") {
		return []proto.Finding{{ID: "firewall", Severity: "info", Title: "The firewall (firewalld) is not running", Fix: "sudo systemctl enable --now firewalld"}}
	}
	return nil
}

// envFindings flags .env files in app folders that every user may read.
func envFindings(paths []string) []proto.Finding {
	var fs []proto.Finding
	for _, dir := range paths {
		if !filepath.IsAbs(dir) || len(fs) >= 20 {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(dir, ".env*"))
		for _, m := range matches {
			fi, err := os.Lstat(m)
			if err != nil || !fi.Mode().IsRegular() || strings.HasSuffix(m, ".example") || strings.HasSuffix(m, ".sample") {
				continue
			}
			if fi.Mode().Perm()&0o004 != 0 {
				fs = append(fs, proto.Finding{ID: "env_readable:" + m, Severity: "warning", Title: m + " can be read by every user on the server",
					Detail: "Secrets in it (passwords, API keys) are visible to any account or compromised service on this machine.",
					Fix:    "sudo chmod 600 " + m + "   (or 640 if a group needs it)"})
			}
		}
	}
	return fs
}

// ---------- SSH logins ----------

type sshLine struct {
	ts  time.Time
	msg string
}

// sshLines reads the SSH server's messages since a time: from the journal, else from
// /var/log/auth.log or /var/log/secure.
func sshLines(ctx context.Context, since time.Time) []sshLine {
	var out []sshLine
	if raw, ok := output(ctx, 60*time.Second, "journalctl", "-q", "--no-pager", "-o", "json", "--output-fields=MESSAGE,__REALTIME_TIMESTAMP",
		"--since", "@"+strconv.FormatInt(since.Unix(), 10), "_COMM=sshd", "_COMM=sshd-session"); ok && strings.TrimSpace(raw) != "" {
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
			us, _ := strconv.ParseInt(e.Realtime, 10, 64)
			out = append(out, sshLine{time.UnixMicro(us), msg})
		}
		return out
	}
	for _, f := range []string{p("var", "log", "auth.log"), p("var", "log", "secure")} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if len(b) > 32<<20 {
			b = b[len(b)-32<<20:]
		}
		for _, line := range bytes.Split(b, []byte("\n")) {
			if l, ok := parseSyslog(string(line), since); ok {
				out = append(out, l)
			}
		}
		break
	}
	return out
}

var syslogSSH = regexp.MustCompile(`^(\S+(?: +\d+ \d\d:\d\d:\d\d)?) \S+ sshd(?:-session)?\[\d+\]: (.*)$`)

// parseSyslog reads "2026-10-16T10:00:00.123+00:00 host sshd[1]: msg" or the classic
// "Oct 16 10:00:00 host sshd[1]: msg".
func parseSyslog(line string, since time.Time) (sshLine, bool) {
	m := syslogSSH.FindStringSubmatch(line)
	if m == nil {
		return sshLine{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, m[1])
	if err != nil {
		ts, err = time.ParseInLocation("Jan 2 15:04:05", strings.Join(strings.Fields(m[1]), " "), time.Local)
		if err != nil {
			return sshLine{}, false
		}
		now := time.Now()
		ts = ts.AddDate(now.Year(), 0, 0)
		if ts.After(now.Add(24 * time.Hour)) { // December lines read in January
			ts = ts.AddDate(-1, 0, 0)
		}
	}
	if ts.Before(since) {
		return sshLine{}, false
	}
	return sshLine{ts, m[2]}, true
}

var acceptedRe = regexp.MustCompile(`^Accepted (\S+) for (\S+) from (\S+) port \d+(?: ssh2)?(?:: (\S+) (\S+))?`)

// Logins are the successful SSH logins since a time.
func Logins(ctx context.Context, since time.Time) []proto.SSHLogin {
	var out []proto.SSHLogin
	for _, l := range sshLines(ctx, since) {
		m := acceptedRe.FindStringSubmatch(l.msg)
		if m == nil {
			continue
		}
		key := ""
		if m[5] != "" {
			key = m[4] + " " + m[5]
		}
		out = append(out, proto.SSHLogin{TS: l.ts.UnixMilli(), Method: m[1], User: m[2], IP: m[3], Key: key})
		if len(out) >= 2000 {
			break
		}
	}
	return out
}
