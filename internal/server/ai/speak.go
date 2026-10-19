package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/server/valid"
)

// Speak turns text into speech in the calls' voice: 16-bit mono PCM and its sample
// rate. The calls' Live model reads it out (it counts in the monthly call minutes);
// when it cannot, a Gemini TTS model does.
func (s *Service) Speak(ctx context.Context, text string) ([]byte, int, error) {
	if s.Remaining() > 0 {
		pcm, rate, err := s.speakLive(ctx, text)
		if err == nil {
			s.AddUsage(time.Duration(len(pcm)/2) * time.Second / time.Duration(rate))
			return pcm, rate, nil
		}
		slog.Warn("ai: voice note with the Live model, trying a TTS model", "err", err)
	}
	return s.speakTTS(ctx, text)
}

// speakLive has the Live model read the text out, in one turn.
func (s *Service) speakLive(ctx context.Context, text string) ([]byte, int, error) {
	c, key, ok := s.load()
	if !ok {
		return nil, 0, valid.Error("the AI is not set up")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, liveURL(key), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach Gemini Live: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 << 20)
	send := func(v any) error {
		b, _ := json.Marshal(v)
		return ws.Write(ctx, websocket.MessageText, b)
	}
	if err := send(map[string]any{"setup": map[string]any{
		"model": "models/" + c.Model,
		"generationConfig": map[string]any{"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": c.Voice}}}},
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": "You read WhatsApp messages aloud as a voice note. Say exactly the text you are given, word for word, in its own language, warmly and naturally. Add nothing, leave nothing out, and do not answer it."}}},
	}}); err != nil {
		return nil, 0, err
	}
	if _, raw, err := ws.Read(ctx); err != nil || !strings.Contains(string(raw), "setupComplete") {
		return nil, 0, fmt.Errorf("Gemini Live did not start: %v %.200s", err, raw)
	}
	if err := send(map[string]any{"clientContent": map[string]any{"turns": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": text}}}}, "turnComplete": true}}); err != nil {
		return nil, 0, err
	}
	var pcm []byte
	rate := 24000
	for {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			if len(pcm) > 0 {
				break // closed after the audio: keep it
			}
			return nil, 0, err
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
				TurnComplete bool `json:"turnComplete"`
			} `json:"serverContent"`
		}
		if json.Unmarshal(raw, &m) != nil || m.ServerContent == nil {
			continue
		}
		if t := m.ServerContent.ModelTurn; t != nil {
			for _, p := range t.Parts {
				if p.InlineData == nil || !strings.HasPrefix(p.InlineData.MimeType, "audio/pcm") {
					continue
				}
				if r := rateRe.FindStringSubmatch(p.InlineData.MimeType); r != nil {
					rate, _ = strconv.Atoi(r[1])
				}
				if b, err := base64.StdEncoding.DecodeString(p.InlineData.Data); err == nil {
					pcm = append(pcm, b...)
				}
			}
		}
		if m.ServerContent.TurnComplete {
			break
		}
	}
	if len(pcm) == 0 {
		return nil, 0, errors.New("Gemini Live said nothing")
	}
	return pcm, rate, nil
}

// speakTTS reads the text out with a Gemini TTS model.
func (s *Service) speakTTS(ctx context.Context, text string) ([]byte, int, error) {
	cfg, key, ok := s.load()
	if !ok {
		return nil, 0, valid.Error("the AI is not set up")
	}
	ids, err := modelsWith(ctx, key, "generateContent")
	if err != nil {
		return nil, 0, err
	}
	model := pickTTS(ids)
	if model == "" {
		return nil, 0, errors.New("this key can use no speech model")
	}
	gen := map[string]any{"responseModalities": []string{"AUDIO"}}
	if cfg.Voice != "" {
		gen["speechConfig"] = map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]string{"voiceName": cfg.Voice}}}
	}
	raw, err := generate(ctx, key, model, map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": []map[string]string{{"text": text}}}},
		"generationConfig": gen,
	})
	if err != nil {
		return nil, 0, err
	}
	var c struct {
		Parts []struct {
			InlineData struct {
				MimeType string `json:"mimeType"`
				Data     string `json:"data"`
			} `json:"inlineData"`
		} `json:"parts"`
	}
	json.Unmarshal(raw, &c)
	for _, p := range c.Parts {
		if p.InlineData.Data == "" {
			continue
		}
		pcm, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
		if err != nil {
			return nil, 0, err
		}
		rate := 24000 // "audio/L16;codec=pcm;rate=24000"
		if m := rateRe.FindStringSubmatch(p.InlineData.MimeType); m != nil {
			rate, _ = strconv.Atoi(m[1])
		}
		return pcm, rate, nil
	}
	return nil, 0, errors.New("Gemini gave no speech")
}

var rateRe = regexp.MustCompile(`rate=(\d+)`)

// pickTTS chooses a speech model: the newest "flash" TTS, a full release before a preview.
func pickTTS(ids []string) string {
	best, bestScore := "", -1.0
	for _, id := range ids {
		if !strings.Contains(id, "tts") {
			continue
		}
		score := 0.0
		if m := version.FindStringSubmatch(id); m != nil {
			score, _ = strconv.ParseFloat(m[1], 64)
		}
		if strings.Contains(id, "flash") {
			score += 0.1 // faster and cheaper than pro
		}
		if !strings.Contains(id, "preview") {
			score += 0.05
		}
		if score > bestScore || (score == bestScore && len(id) < len(best)) {
			best, bestScore = id, score
		}
	}
	return best
}
