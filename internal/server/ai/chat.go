package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/valid"
)

// WhatsApp text chats with the AI: one person's messages (and voice notes) go to a
// Gemini text model with the same tools as calls, and its answer goes back as text.

// Chat is one person's conversation; it keeps the last few exchanges.
type Chat struct {
	Instructions string
	Tools        []Tool

	mu       sync.Mutex
	contents []json.RawMessage // Gemini contents, the model's kept as they came (thought signatures)
	said     []heardLine
	last     time.Time
}

type heardLine struct {
	at   time.Time
	text string
}

// Idle is how long the conversation has been quiet.
func (c *Chat) Idle(now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Sub(c.last)
}

func (c *Chat) heardSince(t time.Time) string {
	var b strings.Builder
	for _, h := range c.said {
		if !h.at.Before(t) {
			b.WriteString(h.text + " ")
		}
	}
	return b.String()
}

const chatTurns = 8 // exchanges kept

// Reply answers one message: text, and/or audio such as a voice note (mime like audio/ogg).
func (s *Service) Reply(ctx context.Context, c *Chat, text string, audio []byte, mime string) (string, error) {
	cfg, key, ok := s.load()
	if !ok {
		return "", valid.Error("the AI is not set up")
	}
	model := cfg.ChatModel
	if model == "" {
		ids, err := chatModels(ctx, key)
		if err != nil {
			return "", err
		}
		if model = pickChat(ids); model == "" {
			return "", valid.Error("this key can use no text model")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = s.Now()
	var parts []map[string]any
	if len(audio) > 0 {
		parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(audio)}})
		if text == "" {
			text = "(a voice note)"
		}
	}
	parts = append(parts, map[string]any{"text": text})
	c.said = append(c.said, heardLine{s.Now(), text})
	user, _ := json.Marshal(map[string]any{"role": "user", "parts": parts})
	c.contents = append(c.contents, user)
	turnStart := len(c.contents) - 1

	tools := map[string]Tool{}
	var decls []map[string]any
	for _, t := range c.Tools {
		tools[t.Name] = t
		d := map[string]any{"name": t.Name, "description": t.Description}
		if len(t.Params) > 0 {
			d["parameters"] = map[string]any{"type": "OBJECT", "properties": t.Params, "required": t.Required}
		}
		decls = append(decls, d)
	}
	reply := ""
	for step := 0; step < 8 && reply == ""; step++ {
		body := map[string]any{
			"systemInstruction": map[string]any{"parts": []map[string]string{{"text": c.Instructions}}},
			"contents":          c.contents,
		}
		if len(decls) > 0 {
			body["tools"] = []map[string]any{{"functionDeclarations": decls}}
		}
		content, err := generate(ctx, key, model, body)
		if err != nil {
			c.contents = c.contents[:turnStart] // a failed turn is not kept half done
			return "", err
		}
		c.contents = append(c.contents, content)
		var got struct {
			Parts []struct {
				Text         string `json:"text"`
				Thought      bool   `json:"thought"`
				FunctionCall *struct {
					ID   string         `json:"id"`
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		}
		json.Unmarshal(content, &got)
		var answers []map[string]any
		var texts []string
		for _, p := range got.Parts {
			switch {
			case p.FunctionCall != nil:
				var result any
				if t, ok := tools[p.FunctionCall.Name]; ok {
					tctx, cancel := context.WithTimeout(WithHeard(ctx, c.heardSince), 40*time.Second)
					v, err := t.Run(tctx, p.FunctionCall.Args)
					cancel()
					if err != nil {
						result = map[string]string{"error": err.Error()}
					} else {
						result = map[string]any{"result": v}
					}
				} else {
					result = map[string]string{"error": "no such tool"}
				}
				fr := map[string]any{"name": p.FunctionCall.Name, "response": result}
				if p.FunctionCall.ID != "" {
					fr["id"] = p.FunctionCall.ID
				}
				answers = append(answers, map[string]any{"functionResponse": fr})
			case p.Text != "" && !p.Thought:
				texts = append(texts, p.Text)
			}
		}
		if len(answers) > 0 {
			b, _ := json.Marshal(map[string]any{"role": "user", "parts": answers})
			c.contents = append(c.contents, b)
			continue
		}
		reply = strings.TrimSpace(strings.Join(texts, "\n"))
		if reply == "" {
			reply = "(no answer)"
		}
	}
	if reply == "" {
		reply = "Sorry, that took too many steps. Ask me again, maybe more simply."
	}
	if len(audio) > 0 { // the voice note itself is not sent again with later messages
		b, _ := json.Marshal(map[string]any{"role": "user", "parts": []map[string]string{{"text": "(a voice note, answered above)"}}})
		c.contents[turnStart] = b
	}
	c.trim()
	return reply, nil
}

// trim keeps the last chatTurns exchanges, starting at one of the person's messages.
func (c *Chat) trim() {
	var starts []int
	for i, raw := range c.contents {
		if bytes.Contains(raw, []byte(`"role":"user"`)) && !bytes.Contains(raw, []byte(`"functionResponse"`)) {
			starts = append(starts, i)
		}
	}
	if len(starts) > chatTurns {
		c.contents = slices.Clone(c.contents[starts[len(starts)-chatTurns]:])
	}
	if len(c.said) > 50 {
		c.said = c.said[len(c.said)-50:]
	}
}

// generate asks a text model for the next content.
func generate(ctx context.Context, key, model string, body any) (json.RawMessage, error) {
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, API+"/v1beta/models/"+model+":generateContent", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Gemini: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Candidates []struct {
			Content      json.RawMessage `json:"content"`
			FinishReason string          `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	switch {
	case resp.StatusCode != http.StatusOK:
		msg := strings.TrimSuffix(out.Error.Message, ".")
		if msg == "" {
			msg = resp.Status
		}
		return nil, errors.New("Gemini: " + msg)
	case len(out.Candidates) == 0 || len(out.Candidates[0].Content) == 0:
		why := cmpOr(out.PromptFeedback.BlockReason, "no answer")
		if len(out.Candidates) > 0 {
			why = cmpOr(out.Candidates[0].FinishReason, why)
		}
		return nil, errors.New("Gemini gave no answer (" + why + ")")
	}
	return out.Candidates[0].Content, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// chatModels lists the models the key can generate text with.
func chatModels(ctx context.Context, key string) ([]string, error) {
	return modelsWith(ctx, key, "generateContent")
}

var version = regexp.MustCompile(`gemini-(\d+(?:\.\d+)?)`)

// pickChat chooses a text model for chats: the newest "flash" (fast, generous free
// tier), a full release before a preview.
func pickChat(ids []string) string {
	best, bestScore := "", -1.0
	for _, id := range ids {
		if !strings.Contains(id, "flash") || strings.ContainsAny(id, " ") {
			continue
		}
		skip := false
		for _, w := range []string{"live", "tts", "image", "audio", "embedding", "thinking", "lite", "exp"} {
			skip = skip || strings.Contains(id, w)
		}
		if skip {
			continue
		}
		score := 0.0
		if m := version.FindStringSubmatch(id); m != nil {
			score, _ = strconv.ParseFloat(m[1], 64)
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
