package whatsapp

import (
	"time"

	"fleetwatch/internal/server/valid"
)

// Messages team members pass on to each other through the AI (whatsapp_notes). One
// kept for later waits for the person's next call or chat, at most NoteTTL.

const NoteTTL = 7 * 24 * time.Hour

type Note struct {
	ID        int64  `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Text      string `json:"text"`
	Via       string `json:"via"` // text | call | later
	At        int64  `json:"at"`
	Delivered int64  `json:"deliveredAt"`
	Canceled  int64  `json:"canceledAt"`
	Status    string `json:"status"` // waiting | delivered | canceled | expired
}

// LeaveNote keeps a message; delivered when it went out already (a text).
func (s *Service) LeaveNote(from, to, text, via string, delivered bool) (int64, error) {
	now := s.Now().UnixMilli()
	at := int64(0)
	if delivered {
		at = now
	}
	res, err := s.db.Exec(`INSERT INTO whatsapp_notes (from_person, to_person, text, via, created_at, delivered_at) VALUES (?, ?, ?, ?, ?, ?)`, from, to, text, via, now, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Waiting are the messages still to pass on to someone, oldest first.
func (s *Service) Waiting(to string) []Note {
	return s.notes(`WHERE to_person = ? AND delivered_at = 0 AND canceled_at = 0 AND created_at > ? ORDER BY id`, to, s.Now().Add(-NoteTTL).UnixMilli())
}

// Notes lists the latest messages from or to someone ("" = everyone's), newest first.
func (s *Service) Notes(person string) []Note {
	if person == "" {
		return s.notes(`ORDER BY id DESC LIMIT 200`)
	}
	return s.notes(`WHERE from_person = ? OR to_person = ? ORDER BY id DESC LIMIT 200`, person, person)
}

// Deliver marks a message passed on; false when it was already, or was canceled.
func (s *Service) Deliver(id int64) bool {
	res, err := s.db.Exec(`UPDATE whatsapp_notes SET delivered_at = ? WHERE id = ? AND delivered_at = 0 AND canceled_at = 0`, s.Now().UnixMilli(), id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// CancelNote stops a waiting message; by is who asks ("" = the owner: any message).
func (s *Service) CancelNote(id int64, by string) error {
	q, args := `UPDATE whatsapp_notes SET canceled_at = ? WHERE id = ? AND delivered_at = 0 AND canceled_at = 0`, []any{s.Now().UnixMilli(), id}
	if by != "" {
		q, args = q+` AND from_person = ?`, append(args, by)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return valid.Error("this message is not waiting anymore, or is not yours to cancel")
	}
	return nil
}

func (s *Service) notes(where string, args ...any) []Note {
	rows, err := s.db.Query(`SELECT id, from_person, to_person, text, via, created_at, delivered_at, canceled_at FROM whatsapp_notes `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	expired := s.Now().Add(-NoteTTL).UnixMilli()
	out := []Note{}
	for rows.Next() {
		var n Note
		if rows.Scan(&n.ID, &n.From, &n.To, &n.Text, &n.Via, &n.At, &n.Delivered, &n.Canceled) != nil {
			continue
		}
		switch {
		case n.Delivered > 0:
			n.Status = "delivered"
		case n.Canceled > 0:
			n.Status = "canceled"
		case n.At <= expired:
			n.Status = "expired"
		default:
			n.Status = "waiting"
		}
		out = append(out, n)
	}
	return out
}
