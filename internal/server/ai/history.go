package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"fleetwatch/internal/server/valid"
)

// The history of the AI's calls, and their summaries.

// CallRecord is one call as the history keeps it.
type CallRecord struct {
	ID         string     `json:"id"`
	At         int64      `json:"at"`
	Person     string     `json:"person"`
	Name       string     `json:"name"`
	Direction  string     `json:"direction"` // outgoing | incoming | follow-up | test
	IncidentID string     `json:"incidentId,omitempty"`
	Seconds    int        `json:"seconds"`
	Summary    string     `json:"summary"`
	Transcript []Line     `json:"transcript"`
	Report     CallReport `json:"report"`
}

// SaveCall keeps a call in the history (the newest thousand stay).
func (s *Service) SaveCall(r CallRecord) error {
	if r.ID == "" {
		b := make([]byte, 8)
		rand.Read(b)
		r.ID = "call_" + hex.EncodeToString(b)
	}
	lines, _ := json.Marshal(r.Transcript)
	rep, _ := json.Marshal(r.Report)
	if _, err := s.db.Exec(`INSERT INTO ai_calls (id, at, person, name, direction, incident_id, seconds, summary, transcript, report) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.At, r.Person, r.Name, r.Direction, r.IncidentID, r.Seconds, r.Summary, string(lines), string(rep)); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM ai_calls WHERE id NOT IN (SELECT id FROM ai_calls ORDER BY at DESC LIMIT 1000)`)
	return err
}

// Calls lists the history, newest first: those whose name, summary or transcript has q,
// or those of one incident.
func (s *Service) Calls(q, incidentID string, limit int) ([]CallRecord, error) {
	where, args := `1 = 1`, []any{}
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + strings.ReplaceAll(strings.ReplaceAll(q, "%", ""), "_", "") + "%"
		where += ` AND (name LIKE ? OR summary LIKE ? OR transcript LIKE ?)`
		args = append(args, like, like, like)
	}
	if incidentID != "" {
		where += ` AND incident_id = ?`
		args = append(args, incidentID)
	}
	rows, err := s.db.Query(`SELECT id, at, person, name, direction, incident_id, seconds, summary, transcript, report FROM ai_calls WHERE `+where+
		` ORDER BY at DESC LIMIT ?`, append(args, max(1, min(limit, 200)))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallRecord{}
	for rows.Next() {
		var r CallRecord
		var lines, rep string
		if rows.Scan(&r.ID, &r.At, &r.Person, &r.Name, &r.Direction, &r.IncidentID, &r.Seconds, &r.Summary, &lines, &rep) == nil {
			json.Unmarshal([]byte(lines), &r.Transcript)
			json.Unmarshal([]byte(rep), &r.Report)
			if r.Transcript == nil {
				r.Transcript = []Line{}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// Summarize writes two or three plain lines about a call, from its transcript.
func (s *Service) Summarize(ctx context.Context, what, transcript string) (string, error) {
	c, key, ok := s.load()
	if !ok {
		return "", valid.Error("the AI is not set up")
	}
	model := c.ChatModel
	if model == "" {
		ids, err := chatModels(ctx, key)
		if err != nil {
			return "", err
		}
		model = pickChat(ids)
	}
	content, err := generate(ctx, key, model, map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": "Summarize this " + what +
			" in two or three short plain lines, no markdown: what it was about, what was decided or done (actions run, acknowledged or not), and how it ended.\n\n" + transcript}}}},
	})
	if err != nil {
		return "", err
	}
	var got struct {
		Parts []struct {
			Text    string `json:"text"`
			Thought bool   `json:"thought"`
		} `json:"parts"`
	}
	json.Unmarshal(content, &got)
	var b strings.Builder
	for _, p := range got.Parts {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String()), nil
}
