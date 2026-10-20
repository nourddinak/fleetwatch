package ai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
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
	// Report, when set, gets the call's report as well (calls may run side by side, so
	// LastCall may be another's).
	Report *CallReport
	// ProtectOpening lets the AI's first turn play out whole: talking over it does not
	// cut it off (an on-call call's explanation), later turns can be.
	ProtectOpening bool
	// News, when set, brings things that happen during the call (a teammate picked up)
	// for the AI to tell the person at once.
	News <-chan string
}

// How long someone may stay silent on a call: before the AI checks they are there,
// before it lets them go, and before the call is ended anyway. Tests shorten them.
var silenceAsk, silenceBye, silenceHangUp = 12 * time.Second, 10 * time.Second, 20 * time.Second

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
		// Half a second of quiet ends their turn, so answers come quickly (speech is
		// still detected as eagerly as Gemini does by default).
		"realtimeInputConfig": map[string]any{"automaticActivityDetection": map[string]any{
			"endOfSpeechSensitivity": "END_SENSITIVITY_HIGH", "silenceDurationMs": 450}, "activityHandling": activity(t.ProtectOpening)},
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

	// The person's voice, as it comes: quiet phone audio is brought up to a normal
	// speaking level, so Gemini hears it clearly and notices when it stops.
	var boost booster
	var boostMu sync.Mutex
	hungUp := false
	var lastSpeech time.Time // when they last spoke, to time the replies
	interrupts := 0
	var protectUntil time.Time // talking over the AI does not cut it off before then
	if t.ProtectOpening {
		protectUntil = start.Add(time.Hour) // until the opening is known to have played
	}
	go func() {
		run := 0 // frames of their speech in a row
		for frame := range t.In {
			boostMu.Lock()
			frame = boost.apply(frame)
			if boost.talking {
				lastSpeech = s.Now()
				run++
			} else {
				run = 0
			}
			if run == bargeFrames && !s.Now().Before(protectUntil) { // they talk over the AI: what it was still going to say is dropped
				t.Hush()
				interrupts++
			}
			boostMu.Unlock()
			if send(map[string]any{"realtimeInput": map[string]any{"audio": map[string]string{
				"mimeType": "audio/pcm;rate=16000", "data": base64.StdEncoding.EncodeToString(pcm16(frame))}}}) != nil {
				return
			}
		}
		boostMu.Lock()
		hungUp = true
		boostMu.Unlock()
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

	if t.News != nil {
		go func() {
			for {
				select {
				case n := <-t.News:
					send(text("user", "(News during this call, not said by them: "+n+" Tell them in a few words now.)"))
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Silence: someone who picked up but says nothing (busy, phone in a pocket, not
	// hearing). The AI is told, and says what fits in its own words: first it checks
	// they are there, then it lets them go (and may call back); then the call ends.
	var busyUntil, nudgedAt time.Time // the AI speaking or working; the last nudge
	silentEnd := false
	go func() {
		nudges := 0
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			boostMu.Lock()
			last := start
			for _, t := range []time.Time{lastSpeech, busyUntil, nudgedAt} {
				if t.After(last) {
					last = t
				}
			}
			if lastSpeech.After(nudgedAt) {
				nudges = 0 // they spoke: all is well
			}
			quiet := s.Now().Sub(last)
			act := ""
			switch {
			case nudges == 0 && quiet >= silenceAsk:
				act = "(Silence: they have said nothing for a while. They may be busy, away from the phone or not hearing you. Check briefly and naturally, in your own words and their language, whether they are there.)"
			case nudges == 1 && quiet >= silenceBye:
				act = "(Still silence after you asked. Say briefly, in your own words, that they seem busy and you will let them go; if this call is about something that still needs them and you can call back (call_back), say when you will call back and use it. Then end the call.)"
			case nudges == 2 && quiet >= silenceHangUp:
				silentEnd = true
				boostMu.Unlock()
				cancel() // the AI did not end it: hang up anyway
				return
			}
			if act != "" {
				nudges++
				nudgedAt = s.Now()
			}
			boostMu.Unlock()
			if act != "" {
				send(text("user", act))
			}
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
	why := "the AI ended the call"
	var replies []time.Duration // from the end of their speech to the AI's first sound
	turnEnded := start          // when the AI's last turn ended
	newTurn := true
	var firstSound time.Time // when the AI started speaking, for the opening's length
	openingDone := false
	for {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			switch {
			case errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
				why = "the time limit"
			case ctx.Err() != nil:
				why = "the call ended"
				boostMu.Lock()
				if silentEnd {
					why = "they stayed silent"
				}
				boostMu.Unlock()
			default:
				why = "Gemini closed the conversation: " + err.Error()
				slog.Warn("ai call: Gemini Live closed", "err", err)
			}
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
						if firstSound.IsZero() {
							firstSound = s.Now()
						}
						if newTurn {
							newTurn = false
							boostMu.Lock()
							if spoke := lastSpeech; spoke.After(turnEnded) {
								replies = append(replies, s.Now().Sub(spoke))
							}
							boostMu.Unlock()
						}
						out := resample24to16(b)
						d := time.Duration(len(out)) * time.Second / 16000
						spoken += d
						boostMu.Lock()
						if now := s.Now(); busyUntil.Before(now) {
							busyUntil = now
						}
						busyUntil = busyUntil.Add(d) // when what it says has played out
						boostMu.Unlock()
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
				boostMu.Lock()
				busyUntil = s.Now()
				boostMu.Unlock()
				spoken = 0
				newTurn, turnEnded = true, s.Now()
			}
			if sc.TurnComplete {
				newTurn, turnEnded = true, s.Now()
				if !openingDone && !firstSound.IsZero() { // the opening is protected while it plays
					openingDone = true
					boostMu.Lock()
					if t.ProtectOpening {
						protectUntil = firstSound.Add(spoken + 300*time.Millisecond)
					}
					boostMu.Unlock()
				}
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
					boostMu.Lock()
					busyUntil = s.Now().Add(time.Minute)
					boostMu.Unlock()
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
				boostMu.Lock()
				if busyUntil.After(s.Now()) {
					busyUntil = s.Now() // the tool is done: waiting for the AI's answer again
				}
				boostMu.Unlock()
				answers = append(answers, map[string]any{"id": fc.ID, "name": fc.Name, "response": result})
			}
			send(map[string]any{"toolResponse": map[string]any{"functionResponses": answers}})
		}
		if m.GoAway != nil {
			why = "Gemini asked to end the session"
			break
		}
		if ending && m.ServerContent != nil && m.ServerContent.TurnComplete {
			break
		}
	}
	ws.Close(websocket.StatusNormalClosure, "")
	took := s.Now().Sub(start)
	boostMu.Lock()
	if hungUp {
		why = "they hung up"
	}
	rep := CallReport{At: start.UnixMilli(), Seconds: int(took.Seconds()), Frames: boost.frames, Speech: boost.speech,
		LoudestDB: dbfs(boost.peak), Gain: boost.gain, Ended: why, Interruptions: interrupts}
	if len(replies) > 0 {
		var sum time.Duration
		for _, r := range replies {
			sum += r
		}
		rep.ReplyAvgMs, rep.ReplySlowestMs = int((sum / time.Duration(len(replies))).Milliseconds()), int(slices.Max(replies).Milliseconds())
	}
	boostMu.Unlock()
	for _, l := range lines {
		if l.Who == "person" {
			rep.Heard += l.Text
		} else {
			rep.Said += l.Text
		}
	}
	rep.Heard, rep.Said = clip(strings.TrimSpace(rep.Heard), 300), clip(strings.TrimSpace(rep.Said), 300)
	slog.Info("ai call", "replyAvgMs", rep.ReplyAvgMs, "replySlowestMs", rep.ReplySlowestMs, "interruptions", rep.Interruptions, "seconds", rep.Seconds, "frames", rep.Frames, "speechFrames", rep.Speech, "loudestDB", rep.LoudestDB, "gain", rep.Gain, "ended", why, "heard", rep.Heard)
	var failed error
	if len(lines) == 0 && took < 5*time.Second {
		failed = errors.New("Gemini Live ended the conversation right away")
		rep.Error = failed.Error()
	}
	s.mu.Lock()
	s.last = &rep
	s.mu.Unlock()
	if t.Report != nil {
		*t.Report = rep
	}
	return lines, took, failed
}

// CallReport is how the last call went, to see why when one goes wrong.
type CallReport struct {
	At        int64   `json:"at"`
	Seconds   int     `json:"seconds"`
	Frames    int     `json:"frames"`    // 60 ms frames of their audio that arrived
	Speech    int     `json:"speech"`    // of which sounded like speech
	LoudestDB float64 `json:"loudestDb"` // their loudest moment, in dBFS (0 = full scale)
	Gain      float64 `json:"gain"`      // how much their voice was boosted at the end
	Heard     string  `json:"heard"`     // what Gemini understood them to say
	Said      string  `json:"said"`      // what the AI said
	Ended     string  `json:"ended"`
	// ReplyAvgMs and ReplySlowestMs time the AI's answers: from the end of their speech
	// to its first sound; Interruptions counts the times they talked over it.
	ReplyAvgMs     int    `json:"replyAvgMs"`
	ReplySlowestMs int    `json:"replySlowestMs"`
	Interruptions  int    `json:"interruptions"`
	Error          string `json:"error,omitempty"`
}

// LastCall is the report of the last conversation (nil before any).
func (s *Service) LastCall() *CallReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func dbfs(rms float64) float64 {
	if rms <= 0 {
		return -100
	}
	return math.Round(20*math.Log10(rms)*10) / 10
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
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

// booster brings quiet phone audio up to a normal speaking level. Its boost follows the
// speech level only: frames well above the line's background (its quietest moment of
// the last few seconds) count as speech; silence and hiss never raise the boost on their
// own, and nothing is muted.
type booster struct {
	talking bool      // the last frame was speech
	recent  []float64 // the last frames' levels, for the background
	level   float64   // their speaking level
	gain    float64
	frames  int
	speech  int
	peak    float64
}

// bargeFrames of their speech in a row (180 ms) cut the AI off: enough to ignore a click
// or a cough, quick enough to feel like talking over someone.
const bargeFrames = 3

const (
	boostTarget = 0.08 // about -22 dBFS: a clear voice
	boostMax    = 25
	boostWindow = 50 // frames: 3 seconds
)

func (b *booster) apply(f []float32) []float32 {
	var sum float64
	for _, v := range f {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(max(1, len(f))))
	b.frames++
	b.peak = max(b.peak, rms)
	b.recent = append(b.recent, rms)
	if len(b.recent) > boostWindow {
		b.recent = b.recent[1:]
	}
	background := slices.Min(b.recent)
	b.talking = rms > 3*background && rms > 0.0005
	if b.talking { // speech: follow its level, quickly up, more slowly down
		b.speech++
		switch {
		case b.level == 0:
			b.level = rms // their first words set it
		case rms > b.level:
			b.level = 0.6*b.level + 0.4*rms
		default:
			b.level = 0.95*b.level + 0.05*rms
		}
	}
	b.gain = 1 // unchanged until they speak
	if b.level > 0 {
		b.gain = min(boostMax, max(1, boostTarget/b.level))
	}
	out := make([]float32, len(f))
	for i, v := range f {
		out[i] = float32(max(-1, min(1, float64(v)*b.gain)))
	}
	return out
}

// activity is how Gemini treats speech over its answers: with a protected opening it
// keeps answering (what they said is heard after), otherwise it stops at once. Either
// way the dashboard drops queued audio itself (see bargeFrames).
func activity(protect bool) string {
	if protect {
		return "NO_INTERRUPTION"
	}
	return "START_OF_ACTIVITY_INTERRUPTS"
}
