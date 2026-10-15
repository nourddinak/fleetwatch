// Package health runs the HTTP health checks configured for monitored apps. They run
// on the agent so that 127.0.0.1 URLs reach the app directly.
package health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

type Scheduler struct {
	mu      sync.Mutex
	running map[string]running // by app id
	results []proto.HealthResult
	ctx     context.Context
}

type running struct {
	check  proto.HealthCheck
	cancel context.CancelFunc
	now    chan struct{}
}

// NewScheduler runs checks until ctx ends.
func NewScheduler(ctx context.Context) *Scheduler {
	return &Scheduler{running: map[string]running{}, ctx: ctx}
}

// Set replaces the configured checks, restarting only the ones that changed.
func (s *Scheduler) Set(checks []proto.HealthCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]proto.HealthCheck{}
	for _, c := range checks {
		want[c.AppID] = c
	}
	for id, r := range s.running {
		if c, ok := want[id]; !ok || c != r.check {
			r.cancel()
			delete(s.running, id)
		}
	}
	for id, c := range want {
		if _, ok := s.running[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		r := running{check: c, cancel: cancel, now: make(chan struct{}, 1)}
		s.running[id] = r
		go s.loop(ctx, r)
	}
}

// Now runs an app's check immediately.
func (s *Scheduler) Now(appID string) {
	s.mu.Lock()
	r, ok := s.running[appID]
	s.mu.Unlock()
	if ok {
		select {
		case r.now <- struct{}{}:
		default:
		}
	}
}

// Take returns and clears the results collected so far.
func (s *Scheduler) Take() []proto.HealthResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.results
	s.results = nil
	return out
}

func (s *Scheduler) loop(ctx context.Context, r running) {
	every := time.Duration(max(10, r.check.IntervalSec)) * time.Second
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		res := Probe(ctx, r.check)
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.results = append(s.results, res)
		if len(s.results) > 1000 { // server unreachable for a long time
			s.results = s.results[len(s.results)-1000:]
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.now:
		}
	}
}

var client = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("more than 3 redirects")
		}
		return nil
	},
}

// Probe performs one GET and compares the status code.
func Probe(ctx context.Context, c proto.HealthCheck) proto.HealthResult {
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	if timeout <= 0 || timeout > time.Minute {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res := proto.HealthResult{AppID: c.AppID, TS: time.Now().UnixMilli()}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "ops-agent-healthcheck")
	resp, err := client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = shortErr(err)
		return res
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	res.Status = resp.StatusCode
	want := c.ExpectedStatus
	if want == 0 {
		want = 200
	}
	res.OK = resp.StatusCode == want
	if !res.OK {
		res.Error = "expected HTTP " + strconv.Itoa(want) + ", got " + strconv.Itoa(resp.StatusCode)
	}
	return res
}

func shortErr(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "Client.Timeout"):
		return "timed out"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	}
	if i := strings.LastIndex(msg, ": "); i > 0 && len(msg) > 120 {
		return msg[i+2:]
	}
	return msg
}
