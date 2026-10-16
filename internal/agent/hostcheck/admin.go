package hostcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// ---------- failed SSH logins (for fleet-wide blocking) ----------

// Failures counts failed SSH logins per address since a time. newest is the time of the
// last line read, the next call's starting point.
func Failures(ctx context.Context, since time.Time) (list []proto.SSHFailure, newest time.Time) {
	byIP := map[string]*proto.SSHFailure{}
	newest = since
	lines := sshLines(ctx, since)
	for _, l := range lines {
		if l.ts.After(newest) {
			newest = l.ts
		}
	}
	failedAttempts(lines, func(l sshLine, user, ip string) {
		if net.ParseIP(ip) == nil {
			return
		}
		f := byIP[ip]
		if f == nil {
			f = &proto.SSHFailure{IP: ip}
			byIP[ip] = f
		}
		f.Count++
		f.Last = l.ts.UnixMilli()
		if user != "" && len(f.Users) < 3 && !contains(f.Users, user) {
			f.Users = append(f.Users, user)
		}
	})
	for _, f := range byIP {
		list = append(list, *f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Count > list[j].Count })
	if len(list) > 500 {
		list = list[:500]
	}
	return list, newest
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---------- blocked addresses ----------

// BlockChain is the iptables chain holding the dashboard's blocked addresses.
const BlockChain = "FLEETWATCH-BLOCK"

// ApplyBlocks makes BlockChain drop exactly these addresses (IPs or CIDR ranges), for
// IPv4 and IPv6. enabled false removes the chain.
func ApplyBlocks(ctx context.Context, enabled bool, addrs []string) proto.BlockStatus {
	st := proto.BlockStatus{At: time.Now().UnixMilli()}
	var v4, v6 []string
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			ip, err := netip.ParseAddr(a)
			if err != nil {
				continue
			}
			p = netip.PrefixFrom(ip, ip.BitLen())
		}
		if p.Addr().Unmap().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	var errs []string
	for _, fam := range []struct {
		bin  string
		list []string
	}{{"iptables", v4}, {"ip6tables", v6}} {
		if _, err := exec.LookPath(fam.bin); err != nil {
			if enabled && len(fam.list) > 0 {
				errs = append(errs, fam.bin+" is not installed")
			}
			continue
		}
		run := func(args ...string) error {
			_, err := sysexec.Run(ctx, sysexec.Opts{Timeout: 30 * time.Second}, fam.bin, append([]string{"-w", "10"}, args...)...)
			return err
		}
		if !enabled {
			run("-D", "INPUT", "-j", BlockChain)
			run("-F", BlockChain)
			run("-X", BlockChain)
			continue
		}
		run("-N", BlockChain) // fails when it exists, which is fine
		if run("-C", "INPUT", "-j", BlockChain) != nil {
			if err := run("-I", "INPUT", "1", "-j", BlockChain); err != nil {
				errs = append(errs, err.Error())
				continue
			}
		}
		if err := run("-F", BlockChain); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, a := range fam.list {
			if err := run("-A", BlockChain, "-s", a, "-j", "DROP"); err == nil {
				st.Applied++
			} else if len(errs) < 3 {
				errs = append(errs, err.Error())
			}
		}
	}
	st.Error = strings.Join(errs, "; ")
	return st
}

// ---------- firewall ----------

var ufwRule = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)(?: (IN|OUT|FWD))?\s{2,}(.+?)\s*$`)

// sshPort is the port the SSH server listens on (22 unless configured otherwise).
func sshPort(ctx context.Context) int {
	if out, ok := output(ctx, 10*time.Second, "sshd", "-T"); ok {
		for _, line := range strings.Split(out, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "port "); ok {
				if n, err := strconv.Atoi(v); err == nil {
					return n
				}
			}
		}
	}
	return 22
}

// Firewall describes the host firewall: rules from ufw, or the raw configuration of
// firewalld or iptables (shown, not edited).
func Firewall(ctx context.Context) proto.FirewallReport {
	rep := proto.FirewallReport{At: time.Now().UnixMilli(), Tool: "none", Rules: []proto.FirewallRule{}, SSHPort: sshPort(ctx)}
	if out, ok := output(ctx, 15*time.Second, "ufw", "status", "numbered"); ok {
		rep.Tool = "ufw"
		rep.Active = strings.Contains(out, "Status: active")
		for _, line := range strings.Split(out, "\n") {
			m := ufwRule.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			r := proto.FirewallRule{Num: n, To: m[2], Action: m[3], Direction: m[4], From: m[5], V6: strings.Contains(line, "(v6)")}
			r.Raw = strings.TrimSpace(line[strings.Index(line, "]")+1:])
			rep.Rules = append(rep.Rules, r)
		}
		if v, ok := output(ctx, 15*time.Second, "ufw", "status", "verbose"); ok {
			if _, rest, ok := strings.Cut(v, "Default: "); ok {
				rep.DefaultIncoming, _, _ = strings.Cut(rest, " (incoming)")
			}
		}
		return rep
	}
	if out, ok := output(ctx, 15*time.Second, "firewall-cmd", "--list-all"); ok {
		rep.Tool, rep.Active, rep.Raw = "firewalld", true, out
		return rep
	}
	if out, ok := output(ctx, 15*time.Second, "iptables", "-S", "INPUT"); ok {
		rep.Tool, rep.Raw = "iptables", out
		rep.Active = strings.Contains(out, "-P INPUT DROP") || strings.Count(out, "\n") > 2
		return rep
	}
	return rep
}

var (
	portRe   = regexp.MustCompile(`^\d{1,5}(:\d{1,5})?$`)
	errNoUFW = errors.New("rules can only be changed with ufw; this server uses another firewall")
)

// ChangeFirewall adds or deletes a ufw rule, or turns ufw on or off. Turning it on
// first allows the SSH port, so the change cannot lock anyone out.
func ChangeFirewall(ctx context.Context, c proto.FirewallChange) proto.HostActionResult {
	res := proto.HostActionResult{RunID: c.RunID}
	out, err := changeFirewall(ctx, c)
	res.Output, res.OK = out, err == nil
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func changeFirewall(ctx context.Context, c proto.FirewallChange) (string, error) {
	if _, err := exec.LookPath("ufw"); err != nil {
		return "", errNoUFW
	}
	ufw := func(args ...string) (string, error) {
		out, err := sysexec.Run(ctx, sysexec.Opts{Timeout: 60 * time.Second}, "ufw", args...)
		return string(out), err
	}
	switch c.Action {
	case "allow", "deny", "limit":
		if !portRe.MatchString(c.Port) {
			return "", errors.New("the port must be a number (1-65535) or a range like 6000:6100")
		}
		for _, p := range strings.Split(c.Port, ":") {
			if n, _ := strconv.Atoi(p); n < 1 || n > 65535 {
				return "", errors.New("ports are 1-65535")
			}
		}
		if c.Proto != "" && c.Proto != "tcp" && c.Proto != "udp" {
			return "", errors.New("the protocol is tcp, udp or both")
		}
		if strings.Contains(c.Port, ":") && c.Proto == "" {
			return "", errors.New("a port range needs tcp or udp")
		}
		if c.From == "" {
			spec := c.Port
			if c.Proto != "" {
				spec += "/" + c.Proto
			}
			return ufw(c.Action, spec)
		}
		if _, err := netip.ParsePrefix(c.From); err != nil {
			if _, err := netip.ParseAddr(c.From); err != nil {
				return "", errors.New("“from” must be an IP address or a range like 10.0.0.0/8")
			}
		}
		args := []string{c.Action, "from", c.From, "to", "any", "port", c.Port}
		if c.Proto != "" {
			args = append(args, "proto", c.Proto)
		}
		return ufw(args...)
	case "delete":
		// Rule numbers shift as rules change: find the rule by its text right now.
		rep := Firewall(ctx)
		for _, r := range rep.Rules {
			if r.Raw == c.Rule {
				return ufw("--force", "delete", strconv.Itoa(r.Num))
			}
		}
		return "", errors.New("that rule is gone already")
	case "enable":
		out, err := ufw("allow", fmt.Sprintf("%d/tcp", sshPort(ctx)))
		if err != nil {
			return out, err
		}
		more, err := ufw("--force", "enable")
		return out + more, err
	case "disable":
		return ufw("disable")
	}
	return "", errors.New("unknown firewall change")
}

// ---------- package updates ----------

var aptInst = regexp.MustCompile(`^Inst (\S+) (?:\[([^\]]+)\] )?\((\S+) ([^)]*)\)`)

// Updates lists waiting package updates (apt or dnf). refresh first downloads fresh
// package lists, which takes a while.
func Updates(ctx context.Context, refresh bool) proto.UpdatesReport {
	rep := proto.UpdatesReport{At: time.Now().UnixMilli(), Packages: []proto.Package{}}
	if _, err := os.Stat(p("var", "run", "reboot-required")); err == nil {
		rep.RebootRequired = true
	}
	if _, err := exec.LookPath("apt-get"); err == nil {
		rep.Manager = "apt"
		if refresh {
			if _, err := sysexec.Run(ctx, sysexec.Opts{Timeout: 5 * time.Minute}, "apt-get", "update", "-q"); err != nil {
				rep.Error = "apt-get update: " + err.Error()
			}
		}
		out, err := sysexec.Run(ctx, sysexec.Opts{Timeout: 2 * time.Minute}, "apt-get", "-s", "-o", "Debug::NoLocking=1", "dist-upgrade")
		if err != nil {
			rep.Error = err.Error()
			return rep
		}
		for _, line := range strings.Split(string(out), "\n") {
			if m := aptInst.FindStringSubmatch(line); m != nil {
				rep.Packages = append(rep.Packages, proto.Package{Name: m[1], Current: m[2], New: m[3], Security: secRepo.MatchString(m[4])})
			}
		}
		if fi, err := os.Stat(p("var", "lib", "apt", "lists")); err == nil {
			rep.ListsAt = fi.ModTime().UnixMilli()
		}
		if st, ok := output(ctx, 10*time.Second, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"); ok && strings.Contains(st, "install ok installed") {
			rep.AutoSecurity = true
		}
		return rep
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		rep.Manager = "dnf"
		args := []string{"-q", "check-update"}
		if !refresh {
			args = append(args, "--cacheonly")
		}
		// check-update exits 100 when there are updates: read the output either way.
		out, _ := sysexec.Run(ctx, sysexec.Opts{Timeout: 5 * time.Minute}, "dnf", args...)
		sec := map[string]bool{}
		if s, err := sysexec.Run(ctx, sysexec.Opts{Timeout: 5 * time.Minute}, "dnf", "-q", "updateinfo", "list", "--security"); err == nil {
			for _, line := range strings.Split(string(s), "\n") {
				if f := strings.Fields(line); len(f) >= 3 {
					sec[f[2]] = true // name-version-release.arch
				}
			}
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) != 3 || strings.HasPrefix(line, " ") || !strings.Contains(f[0], ".") {
				continue
			}
			name, arch, _ := strings.Cut(f[0], ".")
			_, isSec := sec[name+"-"+f[1]+"."+arch]
			rep.Packages = append(rep.Packages, proto.Package{Name: name, New: f[1], Security: isSec})
		}
		return rep
	}
	rep.Error = "no supported package manager (apt or dnf)"
	return rep
}

// InstallUpdates installs waiting updates: only security fixes, or everything.
func InstallUpdates(ctx context.Context, req proto.UpdatesInstall) proto.HostActionResult {
	res := proto.HostActionResult{RunID: req.RunID}
	var out []byte
	var err error
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive"}
	o := sysexec.Opts{Timeout: 45 * time.Minute, Env: env, MaxOut: 1 << 20}
	keep := []string{"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	switch {
	case func() bool { _, e := exec.LookPath("apt-get"); return e == nil }():
		if req.SecurityOnly {
			var names []string
			for _, pkg := range Updates(ctx, false).Packages {
				if pkg.Security {
					names = append(names, pkg.Name)
				}
			}
			if len(names) == 0 {
				res.OK, res.Output = true, "No security updates are waiting."
				return res
			}
			out, err = sysexec.Run(ctx, o, "apt-get", append(append([]string{"install", "-y", "--only-upgrade"}, keep...), names...)...)
		} else {
			out, err = sysexec.Run(ctx, o, "apt-get", append(append([]string{"-y"}, keep...), "--with-new-pkgs", "upgrade")...)
		}
	case func() bool { _, e := exec.LookPath("dnf"); return e == nil }():
		args := []string{"-y", "upgrade"}
		if req.SecurityOnly {
			args = append(args, "--security")
		}
		out, err = sysexec.Run(ctx, o, "dnf", args...)
	default:
		err = errors.New("no supported package manager (apt or dnf)")
	}
	if len(out) > 64<<10 {
		out = out[len(out)-64<<10:]
	}
	res.Output, res.OK = string(out), err == nil
	if err != nil {
		res.Error = err.Error()
	}
	return res
}
