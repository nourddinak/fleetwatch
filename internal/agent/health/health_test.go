package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
)

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Write([]byte("ok"))
		case "/broken":
			w.WriteHeader(500)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	if r := Probe(ctx, proto.HealthCheck{AppID: "a", URL: srv.URL + "/health", TimeoutMs: 2000, ExpectedStatus: 200}); !r.OK || r.Status != 200 || r.AppID != "a" {
		t.Fatalf("healthy: %+v", r)
	}
	if r := Probe(ctx, proto.HealthCheck{URL: srv.URL + "/broken", TimeoutMs: 2000, ExpectedStatus: 200}); r.OK || r.Status != 500 || !strings.Contains(r.Error, "500") {
		t.Fatalf("500: %+v", r)
	}
	if r := Probe(ctx, proto.HealthCheck{URL: srv.URL + "/broken", TimeoutMs: 2000, ExpectedStatus: 500}); !r.OK {
		t.Fatalf("expected 500: %+v", r)
	}
	if r := Probe(ctx, proto.HealthCheck{URL: srv.URL + "/slow", TimeoutMs: 100}); r.OK || r.Error != "timed out" {
		t.Fatalf("timeout: %+v", r)
	}
	if r := Probe(ctx, proto.HealthCheck{URL: srv.URL + "/loop", TimeoutMs: 2000}); r.OK || !strings.Contains(r.Error, "redirect") {
		t.Fatalf("redirect loop: %+v", r)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	if r := Probe(ctx, proto.HealthCheck{URL: "http://" + addr + "/", TimeoutMs: 2000}); r.OK || r.Status != 0 || r.Error == "" {
		t.Fatalf("refused: %+v", r)
	}
}

func TestSchedulerRunsAndReconfigures(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx)
	s.Set([]proto.HealthCheck{{AppID: "a", URL: srv.URL, IntervalSec: 3600, TimeoutMs: 1000}})
	deadline := time.Now().Add(3 * time.Second)
	pending := func() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.results) }
	for pending() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	s.Now("a")
	time.Sleep(200 * time.Millisecond)
	res := s.Take()
	if len(res) != 2 || !res[0].OK {
		t.Fatalf("results: %+v", res)
	}
	s.Set(nil)
	s.mu.Lock()
	left := len(s.running)
	s.mu.Unlock()
	if left != 0 {
		t.Fatal("check kept running after removal")
	}
}
