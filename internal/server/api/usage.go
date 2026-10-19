package api

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/auth"
)

// The dashboard's own footprint: what it stores and what it uses to run.

var started = time.Now()

// cpuSample is the last reading of the process's CPU time, for the usage since then.
var cpuSample struct {
	sync.Mutex
	at  time.Time
	cpu time.Duration
}

type usageFile struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type usage struct {
	Storage struct {
		Total  int64       `json:"total"`
		Files  []usageFile `json:"files"`  // the biggest in the data folder
		Tables []usageFile `json:"tables"` // the biggest tables in the database, indexes included
		Free   int64       `json:"free"`   // database pages a compaction would give back
	} `json:"storage"`
	Memory struct {
		RSS  int64  `json:"rss"`  // what the OS sees (Linux only, else 0)
		Heap uint64 `json:"heap"` // live Go objects
		Sys  uint64 `json:"sys"`  // reserved by the Go runtime
	} `json:"memory"`
	CPU        float64 `json:"cpu"` // percent of one core since the last look (or since start)
	Cores      int     `json:"cores"`
	Goroutines int     `json:"goroutines"`
	Started    int64   `json:"started"`
	Go         string  `json:"go"`
}

func (s *server) usage(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var u usage
	u.Cores, u.Goroutines, u.Started, u.Go = runtime.NumCPU(), runtime.NumGoroutine(), started.UnixMilli(), runtime.Version()

	files := map[string]int64{} // top-level entry → size, folders summed
	filepath.WalkDir(s.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			rel, _ := filepath.Rel(s.DataDir, p)
			files[strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]] += info.Size()
			u.Storage.Total += info.Size()
		}
		return nil
	})
	u.Storage.Files = top(files, 8)

	// shortcut: dbstat reads every page, a second or so on a GB database; cache it if that bites
	tables := map[string]int64{}
	if rows, err := s.DB.QueryContext(r.Context(), `SELECT m.tbl_name, SUM(d.pgsize) FROM dbstat d JOIN sqlite_master m ON m.name = d.name GROUP BY m.tbl_name`); err == nil {
		for rows.Next() {
			var name string
			var n int64
			if rows.Scan(&name, &n) == nil {
				tables[name] = n
			}
		}
		rows.Close()
	}
	u.Storage.Tables = top(tables, 8)
	var free, size int64
	s.DB.QueryRow(`PRAGMA freelist_count`).Scan(&free)
	s.DB.QueryRow(`PRAGMA page_size`).Scan(&size)
	u.Storage.Free = free * size

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	u.Memory.Heap, u.Memory.Sys, u.Memory.RSS = m.HeapAlloc, m.Sys, rss()

	if cpu, ok := cpuTime(); ok {
		cpuSample.Lock()
		at, before := cpuSample.at, cpuSample.cpu
		if at.IsZero() {
			at, before = started, 0
		}
		if wall := time.Since(at); wall > 0 {
			u.CPU = float64(cpu-before) / float64(wall) * 100
		}
		cpuSample.at, cpuSample.cpu = time.Now(), cpu
		cpuSample.Unlock()
	}
	writeJSON(w, http.StatusOK, u)
}

// top returns the n biggest entries, biggest first.
func top(m map[string]int64, n int) []usageFile {
	out := make([]usageFile, 0, len(m))
	for k, v := range m {
		out = append(out, usageFile{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out[:min(n, len(out))]
}

// rss is the process's resident memory from /proc (0 where there is none).
func rss() int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb * 1024
		}
	}
	return 0
}

// cpuTime is the user+system CPU the process used so far, from /proc.
func cpuTime() (time.Duration, bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// fields after the ")" that closes the command name: utime and stime are the 12th and 13th
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	if len(f) < 13 {
		return 0, false
	}
	ut, _ := strconv.ParseInt(f[11], 10, 64)
	st, _ := strconv.ParseInt(f[12], 10, 64)
	return time.Duration(ut+st) * time.Second / 100, true // shortcut: assumes 100 ticks/s, Linux's usual USER_HZ
}

// cleanupTables are the histories a cleanup trims, with their time column. The
// audit log, incidents and call history are kept: they are the record.
var cleanupTables = [][2]string{
	{"server_metrics_1m", "ts"}, {"server_metrics_5m", "ts"}, {"server_metrics_1h", "ts"},
	{"app_metrics_1m", "ts"}, {"app_metrics_5m", "ts"}, {"app_metrics_1h", "ts"},
	{"logs", "ts"}, {"uptime_results", "ts"}, {"health_results", "ts"}, {"events", "ts"},
	{"notification_history", "ts"}, {"command_runs", "started_at"}, {"ssh_logins", "ts"}, {"mobile_messages", "created_at"},
}

// cleanup deletes history older than some days, then gives the space back to the disk.
func (s *server) cleanup(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ Days int }
	if !decode(w, r, &in) {
		return
	}
	if in.Days != 7 && in.Days != 30 && in.Days != 90 {
		writeError(w, http.StatusBadRequest, "invalid", "Keep 7, 30 or 90 days.")
		return
	}
	before := fileSize(s.DataDir)
	cutoff := time.Now().AddDate(0, 0, -in.Days).UnixMilli()
	deleted := map[string]int64{}
	for _, t := range cleanupTables {
		for { // in small chunks, so the dashboard keeps writing meanwhile
			res, err := s.DB.ExecContext(r.Context(), `DELETE FROM `+t[0]+` WHERE rowid IN (SELECT rowid FROM `+t[0]+` WHERE `+t[1]+` < ? LIMIT 5000)`, cutoff)
			if err != nil {
				break // a table this version does not have
			}
			n, _ := res.RowsAffected()
			deleted[t[0]] += n
			if n < 5000 {
				break
			}
		}
	}
	s.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if _, err := s.DB.Exec(`VACUUM`); err != nil {
		fail(w, err)
		return
	}
	s.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	var rows int64
	for _, n := range deleted {
		rows += n
	}
	s.audit(r, "system.cleanup", "", fmt.Sprintf("kept %d days: %d rows deleted", in.Days, rows))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": rows, "before": before, "after": fileSize(s.DataDir)})
}

// fileSize is the data folder's size.
func fileSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
