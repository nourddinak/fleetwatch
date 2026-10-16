// Package host collects host information and resource metrics.
package host

import (
	"bufio"
	"net"
	"os"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	gohost "github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gonet "github.com/shirou/gopsutil/v4/net"

	"fleetwatch/internal/proto"
)

// Capabilities grows as agent features land.
var Capabilities = []string{"Metrics collection", "Process discovery", "PM2 integration", "systemd integration", "Listening-port scan", "Git inspection", "Caddy inspection", "Log streaming", "Safe command execution", "HTTP health checks", "Folder size scan"}

// Hello describes this host. Errors from individual probes leave fields zero.
func Hello(version string) proto.Hello {
	h := proto.Hello{Protocol: proto.Protocol, AgentVersion: version, IPs: addresses(), Capabilities: Capabilities}
	if smi() != "" {
		h.Capabilities = append(append([]string{}, h.Capabilities...), "GPU monitoring")
	}
	if docker() {
		h.Capabilities = append(append([]string{}, h.Capabilities...), "Docker")
	}
	if info, err := gohost.Info(); err == nil {
		h.Hostname, h.Kernel, h.Arch, h.BootTime = info.Hostname, info.KernelVersion, info.KernelArch, int64(info.BootTime)
		h.OS = strings.TrimSpace(info.Platform + " " + info.PlatformVersion)
	}
	if pretty := osRelease(); pretty != "" {
		h.OS = pretty
	}
	h.CPUCores, _ = cpu.Counts(true)
	if vm, err := mem.VirtualMemory(); err == nil {
		h.MemTotal = vm.Total
	}
	if sw, err := mem.SwapMemory(); err == nil {
		h.SwapTotal = sw.Total
	}
	if du, err := disk.Usage("/"); err == nil {
		h.DiskTotal = du.Total
	}
	return h
}

// osRelease returns PRETTY_NAME from /etc/os-release, e.g. "Ubuntu 24.04.1 LTS".
func osRelease() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// addresses lists the host's non-loopback, non-link-local IPs.
func addresses() []string {
	out := []string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
				continue
			}
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

// Sampler produces metrics; network rates are computed against the previous sample.
type Sampler struct {
	lastRx, lastTx uint64
	lastT          time.Time
}

func (s *Sampler) Sample() proto.Metrics {
	now := time.Now()
	m := proto.Metrics{TS: now.UnixMilli()}
	if p, err := cpu.Percent(0, false); err == nil && len(p) > 0 {
		m.CPU = p[0] // since the previous call
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		// Total minus available matches what "free" calls pressure; caches don't count as used.
		m.MemTotal, m.MemUsed = vm.Total, vm.Total-vm.Available
	}
	if sw, err := mem.SwapMemory(); err == nil {
		m.SwapTotal, m.SwapUsed = sw.Total, sw.Used
	}
	if du, err := disk.Usage("/"); err == nil {
		m.DiskTotal, m.DiskUsed = du.Total, du.Used
	}
	if l, err := load.Avg(); err == nil {
		m.Load1, m.Load5, m.Load15 = l.Load1, l.Load5, l.Load15
	}
	if up, err := gohost.Uptime(); err == nil {
		m.Uptime = up
	}
	m.GPUs = gpus()
	if io, err := gonet.IOCounters(true); err == nil {
		var rx, tx uint64
		for _, c := range io {
			if c.Name != "lo" {
				rx, tx = rx+c.BytesRecv, tx+c.BytesSent
			}
		}
		if dt := now.Sub(s.lastT).Seconds(); !s.lastT.IsZero() && dt > 0 && rx >= s.lastRx && tx >= s.lastTx {
			m.NetRxBps, m.NetTxBps = float64(rx-s.lastRx)/dt, float64(tx-s.lastTx)/dt
		}
		s.lastRx, s.lastTx, s.lastT = rx, tx, now
	}
	return m
}
