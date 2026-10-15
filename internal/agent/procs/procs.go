// Package procs snapshots every process once per inventory and answers tree queries
// (all descendants of an app's main PID), so CPU/RAM include an app's workers.
package procs

import (
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

type Proc struct {
	PID, PPID int
	Name      string
	RSS       uint64
	CPU       float64 // percent of one core since the previous snapshot
	Created   int64   // unix ms
	cpuTotal  float64 // seconds of CPU time
}

type Table struct {
	ByPID    map[int]*Proc
	children map[int][]int
}

// Tracker keeps the previous snapshot to turn CPU time into a percentage.
type Tracker struct {
	prev   map[int]float64
	prevAt time.Time
}

// Snapshot reads all processes. Processes that exit mid-read are skipped.
func (t *Tracker) Snapshot() *Table {
	now := time.Now()
	tab := &Table{ByPID: map[int]*Proc{}, children: map[int][]int{}}
	list, _ := process.Processes()
	cur := make(map[int]float64, len(list))
	dt := now.Sub(t.prevAt).Seconds()
	for _, p := range list {
		ppid, err := p.Ppid()
		if err != nil {
			continue
		}
		x := &Proc{PID: int(p.Pid), PPID: int(ppid)}
		x.Name, _ = p.Name()
		if mi, err := p.MemoryInfo(); err == nil {
			x.RSS = mi.RSS
		}
		if ct, err := p.CreateTime(); err == nil {
			x.Created = ct
		}
		if times, err := p.Times(); err == nil {
			x.cpuTotal = times.User + times.System
			cur[x.PID] = x.cpuTotal
			if before, ok := t.prev[x.PID]; ok && dt > 0 && x.cpuTotal >= before {
				x.CPU = (x.cpuTotal - before) / dt * 100
			}
		}
		tab.ByPID[x.PID] = x
		tab.children[x.PPID] = append(tab.children[x.PPID], x.PID)
	}
	t.prev, t.prevAt = cur, now
	return tab
}

// Tree returns pid and all its descendants that exist in the table.
func (t *Table) Tree(pid int) []int {
	if _, ok := t.ByPID[pid]; !ok || pid <= 1 {
		return nil
	}
	out := []int{pid}
	for i := 0; i < len(out) && len(out) < 10000; i++ {
		out = append(out, t.children[out[i]]...)
	}
	return out
}

// Usage sums CPU and RSS over the given PIDs.
func (t *Table) Usage(pids []int) (cpu float64, rss uint64) {
	for _, pid := range pids {
		if p := t.ByPID[pid]; p != nil {
			cpu += p.CPU
			rss += p.RSS
		}
	}
	return cpu, rss
}

// Cwd returns the working directory of pid ("" if unreadable).
func Cwd(pid int) string {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return ""
	}
	cwd, _ := p.Cwd()
	return cwd
}

// Exe returns the executable path of pid ("" if unreadable).
func Exe(pid int) string {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return ""
	}
	exe, _ := p.Exe()
	return strings.TrimSuffix(exe, " (deleted)")
}

// Cmdline returns the command line of pid.
func Cmdline(pid int) string {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return ""
	}
	c, _ := p.Cmdline()
	return c
}
