package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"fleetwatch/internal/server/valid"
)

// Speak turns text into speech with a Gemini TTS model, in the calls' voice: 16-bit
// mono PCM and its sample rate.
func (s *Service) Speak(ctx context.Context, text string) ([]byte, int, error) {
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
