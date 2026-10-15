package logs

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

const (
	pollEvery    = 500 * time.Millisecond
	saveEvery    = 5 * time.Second
	defaultMax   = 16 << 10
	ringLines    = 50_000
	ringBytes    = 32 << 20
	journalRetry = 5 * time.Second
)

// Resolver maps a PM2 app name to its stdout and stderr files.
type Resolver func(manager, key string) (outLog, errLog string)

// state is persisted so an agent restart neither re-sends nor skips lines.
type state struct {
	Files   map[string]int64 `json:"files"`
	Journal string           `json:"journalCursor"`
}

type Manager struct {
	Buf       *Ring
	statePath string
	resolve   Resolver

	mu      sync.Mutex
	targets []proto.LogTarget
	maxLine int
	tails   map[string]*fileTail // by path
	st      state
	dirty   bool
}

func NewManager(statePath string, resolve Resolver) *Manager {
	m := &Manager{Buf: NewRing(ringLines, ringBytes), statePath: statePath, resolve: resolve, maxLine: defaultMax,
		tails: map[string]*fileTail{}, st: state{Files: map[string]int64{}}}
	if raw, err := os.ReadFile(statePath); err == nil {
		json.Unmarshal(raw, &m.st)
		if m.st.Files == nil {
			m.st.Files = map[string]int64{}
		}
	}
	return m
}

// SetTargets replaces the set of apps to collect (from the server's config).
func (m *Manager) SetTargets(targets []proto.LogTarget, maxLine int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets = targets
	if maxLine >= 1024 && maxLine <= 1<<20 {
		m.maxLine = maxLine
	}
}

func (m *Manager) snapshot() ([]proto.LogTarget, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]proto.LogTarget(nil), m.targets...), m.maxLine
}

// Run tails files and follows the journal until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	go m.runJournal(ctx)
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	lastSave := time.Now()
	for {
		select {
		case <-ctx.Done():
			m.save()
			return
		case <-tick.C:
		}
		m.pollFiles()
		if time.Since(lastSave) > saveEvery {
			m.save()
			lastSave = time.Now()
		}
	}
}

func (m *Manager) pollFiles() {
	targets, maxLine := m.snapshot()
	want := map[string]*fileTail{}
	for _, t := range targets {
		if t.Manager != "pm2" {
			continue
		}
		out, errLog := m.resolve(t.Manager, t.Key)
		for _, f := range []struct{ path, stream string }{{out, "stdout"}, {errLog, "stderr"}} {
			if f.path == "" || f.path == "/dev/null" {
				continue
			}
			want[f.path] = &fileTail{appID: t.AppID, stream: f.stream, path: f.path}
		}
	}
	m.mu.Lock()
	for path, t := range m.tails {
		if want[path] == nil {
			t.close()
			delete(m.tails, path)
		}
	}
	for path, t := range want {
		if cur := m.tails[path]; cur == nil {
			m.tails[path] = t
		} else {
			cur.appID, cur.stream = t.appID, t.stream
		}
	}
	tails := maps.Clone(m.tails)
	m.mu.Unlock()

	for path, t := range tails {
		t.maxLine = maxLine
		m.mu.Lock()
		saved, ok := m.st.Files[path]
		m.mu.Unlock()
		lines := t.poll(saved, ok)
		for _, l := range lines {
			m.Buf.Push(l)
		}
		m.mu.Lock()
		if t.f != nil && (!ok || saved != t.off) {
			m.st.Files[path], m.dirty = t.off, true
		}
		m.mu.Unlock()
	}
}

// runJournal keeps one journalctl process following all monitored units, restarting
// it when the set of units changes or the process exits.
func (m *Manager) runJournal(ctx context.Context) {
	stop := func() {}
	var running map[string]string
	done := make(chan struct{}, 1)
	check := time.NewTicker(time.Second)
	defer check.Stop()
	defer func() { stop() }()
	restartAt := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			running = nil // exited; restart below after a pause
			restartAt = time.Now().Add(journalRetry)
		case <-check.C:
		}
		targets, maxLine := m.snapshot()
		units := map[string]string{}
		for _, t := range targets {
			if t.Manager == "systemd" {
				units[t.Key] = t.AppID
			}
		}
		if maps.Equal(units, running) || time.Now().Before(restartAt) {
			continue
		}
		stop()
		running = units
		if len(units) == 0 {
			stop = func() {}
			continue
		}
		jctx, cancelJournal := context.WithCancel(ctx)
		stop = cancelJournal
		m.mu.Lock()
		cursor := m.st.Journal
		m.mu.Unlock()
		go func(units map[string]string) {
			err := follow(jctx, units, cursor, maxLine, func(l proto.LogLine, cur string) {
				m.Buf.Push(l)
				m.mu.Lock()
				m.st.Journal, m.dirty = cur, true
				m.mu.Unlock()
			})
			if jctx.Err() == nil {
				slog.Warn("journalctl exited; restarting", "err", err)
				select {
				case done <- struct{}{}:
				default:
				}
			}
		}(units)
	}
}

func (m *Manager) save() {
	m.mu.Lock()
	if !m.dirty {
		m.mu.Unlock()
		return
	}
	raw, _ := json.Marshal(m.st)
	m.dirty = false
	m.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return
	}
	tmp := m.statePath + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		os.Rename(tmp, m.statePath)
	}
}
