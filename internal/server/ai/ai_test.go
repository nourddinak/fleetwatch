package ai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
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
				`{"name":"models/gemini-3.5-live-translate","supportedGenerationMethods":["bidiGenerateContent"]},` +
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

	models, _ := s.Models(ctx)
	if len(models) != 2 || models[0].ID != "gemini-3.8-live" || !models[0].Recommended || models[1].ID != "gemini-3.1-flash-live-preview" {
		t.Fatalf("models: %+v", models)
	}
	in := make(chan []float32, 64)
	tone := func(amp float32) []float32 {
		f := make([]float32, 960)
		for i := range f {
			f[i] = amp * float32(1-2*(i%2))
		}
		return f
	}
	for range 20 { // background, then they talk over the AI
		in <- tone(0.0005)
	}
	for range 4 {
		in <- tone(0.05)
	}
	var mu sync.Mutex
	var heard, hushed int
	lines, _, err := s.Converse(ctx, Talk{
		Instructions: "test", Opening: "go", MaxLength: time.Minute, In: in,
		Say:  func(f []float32) { mu.Lock(); heard += len(f); mu.Unlock() },
		Hush: func() { mu.Lock(); hushed++; mu.Unlock() },
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
	if r := s.LastCall(); r == nil || r.Frames != 24 || r.Heard != "I'm on it" || r.Said == "" || r.Ended == "" || r.Interruptions != 1 || hushed != 1 {
		t.Fatalf("last call report: %+v", r)
	}
	// An on-call call's explanation is not cut off by talking over it.
	in2 := make(chan []float32, 64)
	for range 20 {
		in2 <- tone(0.0005)
	}
	for range 4 {
		in2 <- tone(0.05)
	}
	hushed = 0
	if _, _, err := s.Converse(ctx, Talk{Instructions: "test", Opening: "go", MaxLength: time.Minute, In: in2, ProtectOpening: true,
		Say: func([]float32) {}, Hush: func() { mu.Lock(); hushed++; mu.Unlock() },
		Tools: []Tool{{Name: "get_incident", Run: func(context.Context, map[string]any) (any, error) { return "disk full on web-1", nil }}},
	}); err != nil || hushed != 0 || s.LastCall().Interruptions != 0 {
		t.Fatal("the opening was cut off", err, hushed)
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

func TestBooster(t *testing.T) {
	frame := func(amp float32) []float32 {
		f := make([]float32, 960)
		for i := range f {
			f[i] = amp
			if i%2 == 1 {
				f[i] = -amp
			}
		}
		return f
	}
	var b booster
	for range 30 { // a quiet line before they speak: the hiss is not blown up
		b.apply(frame(0.0003))
	}
	if f := b.apply(frame(0.0003)); f[0] > 0.001 {
		t.Fatal("silence was boosted to", f[0])
	}
	var out []float32
	for range 40 { // quiet speech (about -48 dBFS) comes out at a normal level
		out = b.apply(frame(0.004))
	}
	if !b.talking {
		t.Fatal("speech not marked as talking")
	}
	if out[0] < 0.05 || b.speech < 30 {
		t.Fatal("quiet speech not boosted", out[0], b.speech)
	}
	var loud booster // a normal voice is left alone
	for range 40 {
		out = loud.apply(frame(0.2))
	}
	if out[0] != 0.2 || loud.gain != 1 {
		t.Fatal("a loud voice was changed", out[0], loud.gain)
	}
	for range 200 { // a long sentence is never muted
		if f := b.apply(frame(0.004)); f[0] == 0 {
			t.Fatal("speech muted")
		}
	}
}

func TestPickChat(t *testing.T) {
	ids := []string{"gemini-2.5-flash", "gemini-3.1-flash-preview", "gemini-3.1-flash", "gemini-3.1-flash-lite", "gemini-3.8-live",
		"gemini-3.1-flash-image", "gemini-2.5-pro", "text-embedding-004"}
	if got := pickChat(ids); got != "gemini-3.1-flash" {
		t.Fatal(got)
	}
	if got := pickChat([]string{"gemini-2.5-pro"}); got != "" {
		t.Fatal(got)
	}
}

func TestChatReply(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1beta/models":
			w.Write([]byte(`{"models":[{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]},` +
				`{"name":"models/gemini-3.1-flash","supportedGenerationMethods":["generateContent"]}]}`))
		case r.URL.Path == "/v1beta/models/gemini-3.1-flash:generateContent":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			n := len(bodies)
			mu.Unlock()
			if n%2 == 1 { // first a tool call, with a thought signature to keep
				w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"list_servers","args":{}},"thoughtSignature":"sig1"}]}}]}`))
				return
			}
			w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"web-1 is fine."}]}}]}`))
		default:
			w.WriteHeader(404)
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
	ctx := context.Background()
	if st, err := s.SetKey(ctx, "k"); err != nil || st.Config.ChatModel != "gemini-3.1-flash" {
		t.Fatal(st, err)
	}
	ran := 0
	c := &Chat{Instructions: "be brief", Tools: []Tool{{Name: "list_servers", Run: func(context.Context, map[string]any) (any, error) { ran++; return "web-1 ok", nil }}}}
	reply, err := s.Reply(ctx, c, "status?", nil, "")
	if err != nil || reply != "web-1 is fine." || ran != 1 {
		t.Fatal(reply, err, ran)
	}
	if !strings.Contains(bodies[1], "sig1") || !strings.Contains(bodies[1], "web-1 ok") || !strings.Contains(bodies[1], "be brief") {
		t.Fatal("the second request must carry the call, its signature and the result:", bodies[1])
	}
	if _, err := s.Reply(ctx, c, "", []byte("OggS"), "audio/ogg"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies[2], `"mimeType":"audio/ogg"`) || !strings.Contains(bodies[2], "status?") {
		t.Fatal("a voice note goes as audio, after the history:", bodies[2])
	}
	for range 12 {
		s.Reply(ctx, c, "again", nil, "")
	}
	if strings.Contains(string(c.contents[0]), "status?") || strings.Contains(strings.Join(func() []string {
		var out []string
		for _, r := range c.contents {
			out = append(out, string(r))
		}
		return out
	}(), ""), "T2dnUw") {
		t.Fatal("old turns and voice notes must not be kept")
	}
}

func TestCallHistory(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	s := New(d, box)
	s.SaveCall(CallRecord{At: 1, Person: "owner", Name: "Nour", Direction: "outgoing", IncidentID: "inc_1", Seconds: 40,
		Summary: "Disk full on web-1; Nour restarted api.", Transcript: []Line{{"ai", "Hello"}, {"person", "restart the api"}}})
	s.SaveCall(CallRecord{At: 2, Person: "mem_1", Name: "Sam", Direction: "incoming", Seconds: 10, Transcript: []Line{{"person", "status?"}}})
	all, _ := s.Calls("", "", 10)
	if len(all) != 2 || all[0].Name != "Sam" || all[1].Transcript[1].Text != "restart the api" {
		t.Fatalf("%+v", all)
	}
	if got, _ := s.Calls("restart", "", 10); len(got) != 1 || got[0].Name != "Nour" {
		t.Fatal("search", got)
	}
	if got, _ := s.Calls("", "inc_1", 10); len(got) != 1 {
		t.Fatal("by incident", got)
	}
	if got, _ := s.Calls("100%_", "", 10); len(got) != 0 {
		t.Fatal("wildcards in the search", got)
	}
}
