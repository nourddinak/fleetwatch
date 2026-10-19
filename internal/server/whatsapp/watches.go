package whatsapp

import (
	"fleetwatch/internal/server/valid"
)

// Reminders and watches someone asked the AI for (whatsapp_watches). The condition is
// kept as the dashboard wrote it; cmd/server checks it.

type Watch struct {
	ID       int64  `json:"id"`
	Person   string `json:"person"`
	What     string `json:"what"`
	Due      int64  `json:"dueAt"` // a reminder's time; 0 = a watch
	Cond     string `json:"-"`
	How      string `json:"how"` // text | call
	At       int64  `json:"at"`
	Expires  int64  `json:"expiresAt"`
	Done     int64  `json:"doneAt"`
	Canceled int64  `json:"canceledAt"`
	Result   string `json:"result"`
	Status   string `json:"status"` // active | done | canceled | expired
}

func (s *Service) AddWatch(w Watch) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO whatsapp_watches (person, what, due_at, cond, how, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.Person, w.What, w.Due, w.Cond, w.How, s.Now().UnixMilli(), w.Expires)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ActiveWatches are the ones still to check (expired ones included: they are closed then).
func (s *Service) ActiveWatches() []Watch {
	return s.watches(`WHERE done_at = 0 AND canceled_at = 0 ORDER BY id`)
}

// Watches lists someone's latest ("" = everyone's), newest first.
func (s *Service) Watches(person string) []Watch {
	if person == "" {
		return s.watches(`ORDER BY id DESC LIMIT 100`)
	}
	return s.watches(`WHERE person = ? ORDER BY id DESC LIMIT 100`, person)
}

// FinishWatch closes one with what happened; false when it was closed already.
func (s *Service) FinishWatch(id int64, result string) bool {
	res, err := s.db.Exec(`UPDATE whatsapp_watches SET done_at = ?, result = ? WHERE id = ? AND done_at = 0 AND canceled_at = 0`, s.Now().UnixMilli(), result, id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// CancelWatch stops an active one; by is who asks ("" = the owner: anyone's).
func (s *Service) CancelWatch(id int64, by string) error {
	q, args := `UPDATE whatsapp_watches SET canceled_at = ? WHERE id = ? AND done_at = 0 AND canceled_at = 0`, []any{s.Now().UnixMilli(), id}
	if by != "" {
		q, args = q+` AND person = ?`, append(args, by)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return valid.Error("this reminder is not active anymore, or is not yours")
	}
	return nil
}

func (s *Service) watches(where string, args ...any) []Watch {
	rows, err := s.db.Query(`SELECT id, person, what, due_at, cond, how, created_at, expires_at, done_at, canceled_at, result FROM whatsapp_watches `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := s.Now().UnixMilli()
	out := []Watch{}
	for rows.Next() {
		var w Watch
		if rows.Scan(&w.ID, &w.Person, &w.What, &w.Due, &w.Cond, &w.How, &w.At, &w.Expires, &w.Done, &w.Canceled, &w.Result) != nil {
			continue
		}
		switch {
		case w.Done > 0 && w.Result == "expired":
			w.Status = "expired"
		case w.Done > 0:
			w.Status = "done"
		case w.Canceled > 0:
			w.Status = "canceled"
		case w.Expires <= now:
			w.Status = "expired"
		default:
			w.Status = "active"
		}
		out = append(out, w)
	}
	return out
}
