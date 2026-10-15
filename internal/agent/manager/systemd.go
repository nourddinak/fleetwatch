package manager

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	gohost "github.com/shirou/gopsutil/v4/host"

	"fleetwatch/internal/agent/procs"
	"fleetwatch/internal/agent/projects"
	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// Systemd lists loaded service units and flags the ones that look like applications.
type Systemd struct{}

func (Systemd) Name() string { return "systemd" }

var showProps = "Id,Description,LoadState,ActiveState,SubState,MainPID,FragmentPath,WorkingDirectory,User,ExecStart,Restart,NRestarts,ActiveEnterTimestampMonotonic,UnitFileState"

func (Systemd) List(ctx context.Context, tab *procs.Table) ([]proto.Process, error) {
	o := sysexec.Opts{Timeout: 15 * time.Second}
	out, err := sysexec.Run(ctx, o, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		return nil, err
	}
	var units []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasSuffix(f[0], ".service") {
			units = append(units, f[0])
		}
	}
	if len(units) == 0 {
		return nil, nil
	}
	shown, err := sysexec.Run(ctx, o, "systemctl", append([]string{"show", "--no-pager", "-p", showProps, "--"}, units...)...)
	if err != nil {
		return nil, err
	}
	boot, _ := gohost.BootTime()
	list := ParseShow(string(shown), int64(boot)*1000)
	for i := range list {
		p := &list[i]
		if p.PID > 0 {
			p.CPU, p.MemBytes = usage(tab, p.PID)
			if p.Cwd == "" && p.Likely {
				p.Cwd = procs.Cwd(p.PID)
			}
		}
	}
	return list, nil
}

// ParseShow parses `systemctl show` output (blank-line separated blocks of Key=Value).
// bootMs converts the monotonic start timestamp into wall-clock time.
func ParseShow(out string, bootMs int64) []proto.Process {
	var list []proto.Process
	for _, block := range strings.Split(out, "\n\n") {
		props := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				props[k] = v
			}
		}
		id := props["Id"]
		if id == "" || props["LoadState"] != "loaded" {
			continue
		}
		p := proto.Process{
			Manager: "systemd", Key: id, Description: props["Description"],
			User: props["User"], RawStatus: props["ActiveState"] + "/" + props["SubState"],
			Cwd:           strings.TrimLeft(props["WorkingDirectory"], "-!"),
			ExecStart:     execArgv(props["ExecStart"]),
			RestartPolicy: props["Restart"], Instances: 1,
		}
		if p.User == "" {
			p.User = "root"
		}
		if p.Cwd == "~" {
			p.Cwd = ""
		}
		p.PID, _ = strconv.Atoi(props["MainPID"])
		p.Restarts, _ = strconv.Atoi(props["NRestarts"])
		p.Status, p.Unstable = systemdStatus(props["ActiveState"], props["SubState"])
		if mono, err := strconv.ParseInt(props["ActiveEnterTimestampMonotonic"], 10, 64); err == nil && mono > 0 && p.Status == "online" {
			p.StartedAt = bootMs + mono/1000
		}
		p.Likely, p.Reason = Classify(props)
		p.Runtime = projects.ExecRuntime(p.ExecStart, p.PID)
		list = append(list, p)
	}
	return list
}

func systemdStatus(active, sub string) (status string, unstable bool) {
	switch active {
	case "active", "reloading":
		return "online", false
	case "activating":
		return "starting", sub == "auto-restart"
	case "inactive", "deactivating":
		return "stopped", false
	case "failed":
		return "failed", false
	}
	return "unknown", false
}

// execArgv extracts the command line from "{ path=... ; argv[]=/usr/bin/node app.js ; ... }".
func execArgv(v string) string {
	_, rest, ok := strings.Cut(v, "argv[]=")
	if !ok {
		return ""
	}
	argv, _, _ := strings.Cut(rest, " ;")
	return strings.TrimSpace(argv)
}

// denyPrefixes are OS, infrastructure and agent services that are never applications.
var denyPrefixes = []string{
	"ops-agent", "ops-server", "pm2-", "snap.", "systemd-", "getty@", "serial-getty@", "user@", "user-runtime-dir@",
	"cloud-", "dbus", "ssh", "cron", "rsyslog", "syslog", "docker", "containerd", "ufw", "apparmor",
	"multipathd", "polkit", "networkd-dispatcher", "unattended-upgrades", "wsl", "caddy", "nginx",
	"apache2", "httpd", "mysql", "mariadb", "postgresql", "redis", "memcached", "fail2ban", "chrony",
	"ntp", "atd", "plymouth", "udisks2", "modemmanager", "networkmanager", "packagekit",
	"accounts-daemon", "irqbalance", "lvm2", "console-setup", "keyboard-setup", "kmod", "setvtrgb",
	"rc-local", "sysstat", "e2scrub", "fstrim", "grub", "motd", "landscape", "ubuntu-", "apport",
	"secureboot", "open-vm", "qemu-guest", "lxd", "tailscale", "netplan", "finalrd", "emergency",
	"rescue", "console-getty", "logrotate", "man-db", "dpkg", "apt-", "fwupd", "thermald", "rtkit",
}

var systemBinDirs = []string{"/usr/sbin/", "/usr/lib/", "/sbin/", "/lib/", "/usr/libexec/", "/lib64/"}

// Classify decides whether a loaded unit looks like a user application.
func Classify(props map[string]string) (bool, string) {
	id := strings.ToLower(props["Id"])
	for _, p := range denyPrefixes {
		if strings.HasPrefix(id, p) {
			return false, "operating system or infrastructure service"
		}
	}
	switch props["UnitFileState"] {
	case "generated", "transient", "alias":
		return false, props["UnitFileState"] + " unit"
	}
	frag := props["FragmentPath"]
	if !strings.HasPrefix(frag, "/etc/systemd/system/") {
		if frag == "" {
			return false, "no unit file"
		}
		return false, "unit file installed by a package (" + filepath.Dir(frag) + ")"
	}
	exe := strings.Fields(execArgv(props["ExecStart"]) + " ")
	wd := props["WorkingDirectory"]
	if len(exe) > 0 && wd == "" {
		for _, d := range systemBinDirs {
			if strings.HasPrefix(exe[0], d) {
				return false, "runs a system binary (" + exe[0] + ")"
			}
		}
	}
	reason := "custom unit in /etc/systemd/system"
	if wd != "" {
		reason += " with a working directory"
	}
	return true, reason
}
