package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/proto"
)

func TestHubConcurrentSend(t *testing.T) {
	var serverConn *websocket.Conn
	connReady := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		serverConn = c
		close(connReady)
		defer c.CloseNow()
		for {
			_, _, err := c.Read(r.Context())
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.CloseNow()

	<-connReady

	hub := &Hub{
		conns: map[string]*agentConn{
			"s1": {ws: serverConn},
		},
	}

	const concurrency = 25
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := hub.Send("s1", proto.TypeConfig, proto.Config{MetricsIntervalSec: id*10 + j}); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent hub.Send failed: %v", err)
		}
	}
}

func TestHubSendWhilePingInFlight(t *testing.T) {
	var serverConn *websocket.Conn
	connReady := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		serverConn = c
		close(connReady)
		defer c.CloseNow()
		for {
			_, _, err := c.Read(r.Context())
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.CloseNow()

	<-connReady

	// Simulate high-RTT client: delay reading (and therefore replying with Pong) by 250ms.
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		time.Sleep(250 * time.Millisecond)
		for {
			if _, _, err := clientConn.Read(ctx); err != nil {
				return
			}
		}
	}()

	ac := &agentConn{ws: serverConn, cancel: func() {}}
	hub := &Hub{
		conns: map[string]*agentConn{
			"s1": ac,
		},
	}

	pingDone := make(chan error, 1)
	go func() {
		pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
		defer pcancel()
		pingDone <- ac.ws.Ping(pctx)
	}()

	// Give Ping a moment to send its control frame and block awaiting Pong.
	time.Sleep(30 * time.Millisecond)

	sendStart := time.Now()
	if err := hub.Send("s1", proto.TypeConfig, proto.Config{MetricsIntervalSec: 5}); err != nil {
		t.Fatalf("hub.Send failed while Ping in flight: %v", err)
	}
	if elapsed := time.Since(sendStart); elapsed > 150*time.Millisecond {
		t.Fatalf("hub.Send blocked on in-flight Ping (%v)", elapsed)
	}

	if err := <-pingDone; err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	clientConn.CloseNow()
	<-clientDone
}
