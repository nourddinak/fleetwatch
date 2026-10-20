package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

// Someone who picks up and says nothing: the AI is asked to check they are there, then
// to let them go, and the call ends when they stay silent.
func TestSilence(t *testing.T) {
	var mu sync.Mutex
	var told []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta/models" {
			w.Write([]byte(`{"models":[{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]}]}`))
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Read(r.Context()) // setup
		c.Write(r.Context(), websocket.MessageText, []byte(`{"setupComplete":{}}`))
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var m struct {
				ClientContent *struct {
					Turns []struct{ Parts []struct{ Text string } }
				}
			}
			if json.Unmarshal(b, &m) == nil && m.ClientContent != nil {
				mu.Lock()
				told = append(told, m.ClientContent.Turns[0].Parts[0].Text)
				mu.Unlock()
			}
		}
	}))
	defer srv.Close()
	API = srv.URL
	silenceAsk, silenceBye, silenceHangUp = 300*time.Millisecond, 300*time.Millisecond, 300*time.Millisecond

	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	s := New(d, box)
	if _, err := s.SetKey(context.Background(), "good"); err != nil {
		t.Fatal(err)
	}
	in := make(chan []float32)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() { // a line that is open but quiet
		for {
			select {
			case in <- make([]float32, 960):
			case <-ctx.Done():
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	var report CallReport
	_, took, err := s.Converse(ctx, Talk{Instructions: "test", Opening: "go", MaxLength: time.Minute, In: in, Say: func([]float32) {}, Hush: func() {}, Report: &report})
	mu.Lock()
	defer mu.Unlock()
	if len(told) != 3 || !strings.Contains(told[1], "Check briefly") || !strings.Contains(told[2], "let them go") {
		t.Fatalf("told the AI: %q", told)
	}
	if report.Ended != "they stayed silent" || took > 10*time.Second {
		t.Fatal(report.Ended, took, err)
	}
}
