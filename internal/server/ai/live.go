package ai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/valid"
	"github.com/coder/websocket"
)

// A live conversation: the person's voice goes to Gemini as 16 kHz PCM, Gemini's 24 kHz
// voice comes back resampled to 16 kHz, and Gemini may call the tools it was given.

// Tool is something the AI may do during a call.
type Tool struct {
	Name        string
	Description string
	Params      map[string]any // JSON schema properties ("" none)
	Required    []string
	Run         func(ctx context.Context, args map[string]any) (any, error)
}

type heardKey struct{}

// HeardSince is what the person said on the call since t (their words, as transcribed),
// for tools that must wait for an answer, such as a confirmation.
func HeardSince(ctx context.Context, t time.Time) string {
	if f, ok := ctx.Value(heardKey{}).(func(time.Time) string); ok {
		return f(t)
	}
	return ""
}

// WithHeard gives tools run under ctx what the person said since a moment (see HeardSince).
func WithHeard(ctx context.Context, since func(time.Time) string) context.Context {
	return context.WithValue(ctx, heardKey{}, since)
}

// Line is one turn of the transcript.
type Line struct {
	Who  string `json:"who"` // ai | person
	Text string `json:"text"`
}

// Talk is one conversation.
type Talk struct {
	Instructions string
	Opening      string // what the AI is told as the call connects
	Tools        []Tool
	In           <-chan []float32 // the person's voice, 16 kHz frames; closed when the call ends
	Say          func([]float32)  // queue 16 kHz audio for the person
	Hush         func()           // drop the queued audio: they talked over it
	MaxLength    time.Duration
}

// Converse runs a conversation until the AI ends it, the person hangs up (In closes)
// or MaxLength passes. It answers the transcript and how long it lasted.
func (s *Service) Converse(ctx context.Context, t Talk) ([]Line, time.Duration, error) {
	c, key, ok := s.load()
	if !ok {
		return nil, 0, valid.Error("the AI is not set up")
	}
	start := s.Now()
	ctx, cancel := context.WithTimeout(ctx, t.MaxLength+5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, liveURL(key), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Gemini Live: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 << 20)
	var wmu sync.Mutex
	send := func(v any) error {
		b, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
		return ws.Write(ctx, websocket.MessageText, b)
	}

	ending := false
	tools := map[string]Tool{}
	decls := []map[string]any{{"name": "end_call", "description": "Hang up, after saying goodbye. Use it when the person is done or says goodbye."}}
	for _, tool := range t.Tools {
		tools[tool.Name] = tool
		d := map[string]any{"name": tool.Name, "description": tool.Description}
		if len(tool.Params) > 0 {
			d["parameters"] = map[string]any{"type": "OBJECT", "properties": tool.Params, "required": tool.Required}
		}
		decls = append(decls, d)
	}
	if err := send(map[string]any{"setup": map[string]any{
		"model": "models/" + c.Model,
		"generationConfig": map[string]any{"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": c.Voice}}}},
		"systemInstruction":        map[string]any{"parts": []map[string]string{{"text": t.Instructions}}},
		"tools":                    []map[string]any{{"functionDeclarations": decls}},
		"inputAudioTranscription":  map[string]any{},
		"outputAudioTranscription": map[string]any{},
	}}); err != nil {
		return nil, 0, err
	}
	if _, raw, err := ws.Read(ctx); err != nil {
		return nil, 0, fmt.Errorf("Gemini Live did not start: %w", err)
	} else if !strings.Contains(string(raw), "setupComplete") {
		return nil, 0, fmt.Errorf("Gemini Live did not start: %.200s", raw)
	}
	text := func(role, s string) map[string]any {
		return map[string]any{"clientContent": map[string]any{"turns": []map[string]any{{"role": role, "parts": []map[string]string{{"text": s}}}}, "turnComplete": true}}
	}
	if err := send(text("user", t.Opening)); err != nil {
		return nil, 0, err
	}

	// The person's voice, as it comes.
	go func() {
		for frame := range t.In {
			if send(map[string]any{"realtimeInput": map[string]any{"audio": map[string]string{
				"mimeType": "audio/pcm;rate=16000", "data": base64.StdEncoding.EncodeToString(pcm16(frame))}}}) != nil {
				return
			}
		}
		cancel() // they hung up
	}()
	// Time nearly up: ask for a goodbye.
	go func() {
		select {
		case <-time.After(t.MaxLength - 20*time.Second):
			send(text("user", "The call has to end in a few seconds: say goodbye briefly and end the call."))
		case <-ctx.Done():
		}
	}()

	var lines []Line
	add := func(who, s string) {
		if s == "" {
			return
		}
		if n := len(lines); n > 0 && lines[n-1].Who == who {
			lines[n-1].Text += s
			return
		}
		lines = append(lines, Line{who, s})
	}
	var spoken time.Duration // audio queued since the turn began, to let it play out
	type said struct {
		at   time.Time
		text string
	}
	var heardMu sync.Mutex
	var heard []said
	heardSince := func(t time.Time) string {
		heardMu.Lock()
		defer heardMu.Unlock()
		var b strings.Builder
		for _, h := range heard {
			if !h.at.Before(t) {
				b.WriteString(h.text)
			}
		}
		return b.String()
	}
	for {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			break
		}
		var m struct {
			ServerContent *struct {
				ModelTurn *struct {
					Parts []struct {
						InlineData *struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"modelTurn"`
				TurnComplete        bool                   `json:"turnComplete"`
				Interrupted         bool                   `json:"interrupted"`
				InputTranscription  *struct{ Text string } `json:"inputTranscription"`
				OutputTranscription *struct{ Text string } `json:"outputTranscription"`
			} `json:"serverContent"`
			ToolCall *struct {
				FunctionCalls []struct {
					ID   string         `json:"id"`
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCalls"`
			} `json:"toolCall"`
			GoAway *json.RawMessage `json:"goAway"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if sc := m.ServerContent; sc != nil {
			if sc.ModelTurn != nil {
				for _, p := range sc.ModelTurn.Parts {
					if p.InlineData == nil || !strings.HasPrefix(p.InlineData.MimeType, "audio/pcm") {
						continue
					}
					if b, err := base64.StdEncoding.DecodeString(p.InlineData.Data); err == nil {
						out := resample24to16(b)
						spoken += time.Duration(len(out)) * time.Second / 16000
						t.Say(out)
					}
				}
			}
			if sc.InputTranscription != nil {
				add("person", sc.InputTranscription.Text)
				heardMu.Lock()
				heard = append(heard, said{s.Now(), sc.InputTranscription.Text})
				heardMu.Unlock()
			}
			if sc.OutputTranscription != nil {
				add("ai", sc.OutputTranscription.Text)
			}
			if sc.Interrupted {
				t.Hush()
				spoken = 0
			}
			if sc.TurnComplete {
				if ending {
					select { // let the goodbye play out
					case <-time.After(spoken + 500*time.Millisecond):
					case <-ctx.Done():
					}
					break
				}
				spoken = 0
			}
		}
		if tc := m.ToolCall; tc != nil {
			var answers []map[string]any
			for _, fc := range tc.FunctionCalls {
				var result any = map[string]string{"result": "ok"}
				if fc.Name == "end_call" {
					ending = true
				} else if tool, ok := tools[fc.Name]; ok {
					tctx, tcancel := context.WithTimeout(WithHeard(ctx, heardSince), 40*time.Second)
					v, err := tool.Run(tctx, fc.Args)
					tcancel()
					if err != nil {
						result = map[string]string{"error": err.Error()}
					} else {
						result = map[string]any{"result": v}
					}
				} else {
					result = map[string]string{"error": "no such tool"}
				}
				answers = append(answers, map[string]any{"id": fc.ID, "name": fc.Name, "response": result})
			}
			send(map[string]any{"toolResponse": map[string]any{"functionResponses": answers}})
		}
		if m.GoAway != nil {
			break
		}
		if ending && m.ServerContent != nil && m.ServerContent.TurnComplete {
			break
		}
	}
	ws.Close(websocket.StatusNormalClosure, "")
	if len(lines) == 0 && s.Now().Sub(start) < 5*time.Second {
		return nil, s.Now().Sub(start), errors.New("Gemini Live ended the conversation right away")
	}
	return lines, s.Now().Sub(start), nil
}

// pcm16 turns float samples into 16-bit little-endian PCM.
func pcm16(frame []float32) []byte {
	b := make([]byte, 2*len(frame))
	for i, v := range frame {
		v = max(-1, min(1, v))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(math.Round(float64(v)*32767))))
	}
	return b
}

// resample24to16 turns 24 kHz 16-bit little-endian PCM into 16 kHz float samples.
func resample24to16(b []byte) []float32 {
	n := len(b) / 2
	if n == 0 {
		return nil
	}
	in := make([]float32, n)
	for i := range in {
		in[i] = float32(int16(binary.LittleEndian.Uint16(b[2*i:]))) / 32768
	}
	out := make([]float32, n*2/3)
	for i := range out {
		pos := float64(i) * 1.5
		j := int(pos)
		frac := float32(pos - float64(j))
		next := in[min(j+1, n-1)]
		out[i] = in[j]*(1-frac) + next*frac
	}
	return out
}
