package ai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
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

func TestKeyAndConversation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta/models" {
			if r.Header.Get("x-goog-api-key") != "good" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":{"message":"API key not valid"}}`))
				return
			}
			w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent"]},` +
				`{"name":"models/gemini-3.1-flash-live-preview","supportedGenerationMethods":["bidiGenerateContent"]},` +
				`{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]}]}`))
			return
		}
		if r.URL.Query().Get("key") != "good" {
			w.WriteHeader(403)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		read := func() map[string]any {
			_, b, err := c.Read(ctx)
			if err != nil {
				return nil
			}
			var m map[string]any
			json.Unmarshal(b, &m)
			return m
		}
		write := func(s string) { c.Write(ctx, websocket.MessageText, []byte(s)) }
		setup := read()["setup"].(map[string]any)
		if setup["model"] != "models/gemini-3.8-live" || !strings.Contains(mustJSON(setup), "get_incident") || !strings.Contains(mustJSON(setup), `"voiceName":"Kore"`) {
			t.Errorf("setup: %v", setup)
		}
		write(`{"setupComplete":{}}`)
		if m := read(); m["clientContent"] == nil {
			t.Errorf("opening: %v", m)
		}
		pcm := make([]byte, 2*2400) // 100 ms at 24 kHz
		binary.LittleEndian.PutUint16(pcm, 1000)
		write(`{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"` + base64.StdEncoding.EncodeToString(pcm) + `"}}]},"outputTranscription":{"text":"Hello, the disk is full."}}}`)
		write(`{"serverContent":{"turnComplete":true}}`)
		write(`{"toolCall":{"functionCalls":[{"id":"1","name":"get_incident","args":{}}]}}`)
		for { // the person's audio may come first
			m := read()
			if m == nil {
				return
			}
			if tr, ok := m["toolResponse"]; ok {
				if !strings.Contains(mustJSON(tr), "disk full on web-1") {
					t.Errorf("tool response: %v", tr)
				}
				break
			}
		}
		write(`{"serverContent":{"inputTranscription":{"text":"I'm on it"}}}`)
		write(`{"toolCall":{"functionCalls":[{"id":"2","name":"end_call","args":{}}]}}`)
		write(`{"serverContent":{"outputTranscription":{"text":"Goodbye."},"turnComplete":true}}`)
		time.Sleep(time.Second)
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
	ctx := context.Background()
	if _, err := s.SetKey(ctx, "bad"); err == nil {
		t.Fatal("bad key accepted")
	}
	st, err := s.SetKey(ctx, "good")
	if err != nil || !st.Configured || st.Config.Model != "gemini-3.8-live" || st.Config.Voice != "Kore" || s.Remaining() != time.Hour {
		t.Fatal(st, err)
	}

	in := make(chan []float32, 4)
	in <- make([]float32, 960)
	var mu sync.Mutex
	var heard int
	lines, _, err := s.Converse(ctx, Talk{
		Instructions: "test", Opening: "go", MaxLength: time.Minute, In: in,
		Say:  func(f []float32) { mu.Lock(); heard += len(f); mu.Unlock() },
		Hush: func() {},
		Tools: []Tool{{Name: "get_incident", Description: "the incident", Run: func(context.Context, map[string]any) (any, error) {
			return "disk full on web-1", nil
		}}},
	})
	if err != nil || len(lines) != 3 || lines[0].Who != "ai" || lines[1].Text != "I'm on it" || lines[2].Text != "Goodbye." {
		t.Fatalf("%+v %v", lines, err)
	}
	if heard != 1600 { // 100 ms at 16 kHz
		t.Fatal("audio", heard)
	}
	s.AddUsage(90 * time.Second)
	if s.Remaining() != time.Hour-90*time.Second {
		t.Fatal(s.Remaining())
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
