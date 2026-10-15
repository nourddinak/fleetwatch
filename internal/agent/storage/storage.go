// Package storage measures application folders. Scans are throttled, bounded in
// entries and time, run one at a time, and repeat at most every few hours.
package storage

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

const (
	Every       = 6 * time.Hour
	maxEntries  = 2_000_000
	maxDuration = 10 * time.Minute
	topItems    = 12
	pauseEvery  = 2000 // entries between short sleeps, to keep disk IO polite
)

// pseudoFS are kernel filesystems that are never part of an app's disk use.
var pseudoFS = map[string]bool{"/proc": true, "/sys": true, "/dev": true, "/run": true}

// Measure walks root without following symlinks and sizes its top-level entries.
func Measure(ctx context.Context, appID, root string) proto.StorageResult {
	start := time.Now()
	res := proto.StorageResult{AppID: appID, Path: root, ScannedAt: start.UnixMilli(), Items: []proto.StorageItem{}}
	if clean := path.Clean(filepath.ToSlash(root)); clean == "/" || pseudoFS[clean] {
		res.Error = "refusing to measure " + root + ": not an application folder"
		return res
	}
	fi, err := os.Lstat(root)
	if err != nil || !fi.IsDir() {
		res.Error = "not a directory: " + root
		return res
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var seen int64
	deadline := start.Add(maxDuration)
	var items []proto.StorageItem
	for _, e := range entries {
		item := proto.StorageItem{Name: e.Name(), Dir: e.IsDir()}
		if e.IsDir() {
			filepath.WalkDir(filepath.Join(root, e.Name()), func(name string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // unreadable subtree: skip, keep going
				}
				if d.IsDir() && pseudoFS[name] {
					return filepath.SkipDir // virtual files such as /proc/kcore report huge sizes
				}
				seen++
				if seen%pauseEvery == 0 {
					time.Sleep(2 * time.Millisecond)
					if ctx.Err() != nil || seen > maxEntries || time.Now().After(deadline) {
						res.Truncated = true
						return filepath.SkipAll
					}
				}
				if d.Type().IsRegular() {
					if info, err := d.Info(); err == nil {
						item.Bytes += info.Size()
						res.Files++
					}
				}
				return nil
			})
		} else if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			item.Bytes = info.Size()
			res.Files++
		}
		res.TotalBytes += item.Bytes
		items = append(items, item)
		if res.Truncated {
			break
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Bytes > items[j].Bytes })
	if len(items) > topItems {
		other := proto.StorageItem{Name: "(other)"}
		for _, it := range items[topItems:] {
			other.Bytes += it.Bytes
		}
		items = append(items[:topItems], other)
	}
	res.Items = items
	res.DurationMs = time.Since(start).Milliseconds()
	return res
}

// Scanner measures configured targets periodically and on request, one at a time.
type Scanner struct {
	mu      sync.Mutex
	targets map[string]proto.StorageTarget
	last    map[string]time.Time
	queue   []proto.StorageTarget
	results []proto.StorageResult
	wake    chan struct{}
}

func NewScanner() *Scanner {
	return &Scanner{targets: map[string]proto.StorageTarget{}, last: map[string]time.Time{}, wake: make(chan struct{}, 1)}
}

func (s *Scanner) Set(targets []proto.StorageTarget) {
	s.mu.Lock()
	s.targets = map[string]proto.StorageTarget{}
	for _, t := range targets {
		s.targets[t.AppID] = t
	}
	s.mu.Unlock()
	s.poke()
}

// Request queues an immediate scan.
func (s *Scanner) Request(t proto.StorageTarget) {
	s.mu.Lock()
	s.queue = append(s.queue, t)
	s.mu.Unlock()
	s.poke()
}

func (s *Scanner) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Take returns and clears finished results.
func (s *Scanner) Take() []proto.StorageResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.results
	s.results = nil
	return out
}

func (s *Scanner) next() (proto.StorageTarget, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) > 0 {
		t := s.queue[0]
		s.queue = s.queue[1:]
		return t, true
	}
	for id, t := range s.targets {
		if time.Since(s.last[id]) > Every {
			return t, true
		}
	}
	return proto.StorageTarget{}, false
}

func (s *Scanner) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		for {
			target, ok := s.next()
			if !ok || ctx.Err() != nil {
				break
			}
			res := Measure(ctx, target.AppID, target.Path)
			s.mu.Lock()
			s.last[target.AppID] = time.Now()
			s.results = append(s.results, res)
			s.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}
