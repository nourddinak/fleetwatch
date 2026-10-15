// Package logs collects application log lines on the agent: PM2 output files are
// tailed by polling, systemd units are followed through journalctl. Lines wait in a
// bounded buffer until the connection sends them, so short outages lose nothing and
// log storms cannot exhaust memory.
package logs

import (
	"fmt"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

// Ring is a bounded FIFO of log lines. When full, the oldest lines are dropped and
// counted; the next Peek reports the loss as an agent notice.
type Ring struct {
	mu       sync.Mutex
	lines    []proto.LogLine
	bytes    int
	maxLines int
	maxBytes int
	dropped  int
}

func NewRing(maxLines, maxBytes int) *Ring { return &Ring{maxLines: maxLines, maxBytes: maxBytes} }

func (r *Ring) Push(l proto.LogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, l)
	r.bytes += len(l.Message)
	for len(r.lines) > r.maxLines || r.bytes > r.maxBytes {
		r.bytes -= len(r.lines[0].Message)
		r.lines[0] = proto.LogLine{}
		r.lines = r.lines[1:]
		r.dropped++
	}
}

// Peek returns up to n lines without removing them, preceded by a notice if lines
// were dropped. Call Commit after the batch was sent.
func (r *Ring) Peek(n int) (lines []proto.LogLine, notice bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]proto.LogLine, 0, n+1)
	if r.dropped > 0 {
		out = append(out, proto.LogLine{TS: time.Now().UnixMilli(), Source: "agent", Stream: "stderr", Priority: 4,
			Message: fmt.Sprintf("agent dropped %d log lines: buffer full (server unreachable or log storm)", r.dropped)})
	}
	return append(out, r.lines[:n]...), r.dropped > 0
}

// Commit removes the first n buffered lines and clears the drop notice that Peek
// included (pass hadNotice = true when the sent batch started with it).
func (r *Ring) Commit(n int, hadNotice bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hadNotice {
		r.dropped = 0
	}
	if n > len(r.lines) {
		n = len(r.lines)
	}
	for i := 0; i < n; i++ {
		r.bytes -= len(r.lines[i].Message)
	}
	r.lines = append(r.lines[:0:0], r.lines[n:]...)
}

func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}
