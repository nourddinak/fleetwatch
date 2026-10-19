package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

func TestPickTTS(t *testing.T) {
	ids := []string{"gemini-3-flash", "gemini-2.5-pro-preview-tts", "gemini-2.5-flash-preview-tts", "gemini-3-flash-tts", "gemini-3.8-live"}
	if got := pickTTS(ids); got != "gemini-3-flash-tts" {
		t.Fatal(got)
	}
	if pickTTS([]string{"gemini-3-flash"}) != "" {
		t.Fatal("no speech model")
	}
}

// Voice notes are read out by the calls' Live model; without it, by a TTS model.
func TestSpeak(t *testing.T) {
	second := base64.StdEncoding.EncodeToString(make([]byte, 2*24000)) // 1 s at 24 kHz
	liveDown := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1beta/models":
			w.Write([]byte(`{"models":[{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]},` +
				`{"name":"models/gemini-3-flash-tts","supportedGenerationMethods":["generateContent"]}]}`))
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"` + second + `"}}]}}]}`))
		case liveDown:
			w.WriteHeader(503)
		default:
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			read := func() map[string]any {
				_, b, _ := c.Read(r.Context())
				var m map[string]any
				json.Unmarshal(b, &m)
				return m
			}
			setup, _ := json.Marshal(read()["setup"])
			if !strings.Contains(string(setup), "gemini-3.8-live") || !strings.Contains(string(setup), "word for word") {
				t.Errorf("setup: %s", setup)
			}
			c.Write(r.Context(), websocket.MessageText, []byte(`{"setupComplete":{}}`))
			if m, _ := json.Marshal(read()); !strings.Contains(string(m), "The disk is fine.") {
				t.Errorf("text: %s", m)
			}
			for range 2 {
				c.Write(r.Context(), websocket.MessageText, []byte(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"`+second+`"}}]}}}`))
			}
			c.Write(r.Context(), websocket.MessageText, []byte(`{"serverContent":{"turnComplete":true}}`))
			time.Sleep(200 * time.Millisecond)
		}
	}))
	defer srv.Close()
	API = srv.URL
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
	before := s.Remaining()
	pcm, rate, err := s.Speak(context.Background(), "The disk is fine.")
	if err != nil || rate != 24000 || len(pcm) != 2*2*24000 {
		t.Fatal("Live", len(pcm), rate, err)
	}
	if before-s.Remaining() != 2*time.Second {
		t.Fatal("counted in the call minutes", before-s.Remaining())
	}
	liveDown = true
	if pcm, _, err := s.Speak(context.Background(), "The disk is fine."); err != nil || len(pcm) != 2*24000 {
		t.Fatal("TTS when Live fails", len(pcm), err)
	}
}
