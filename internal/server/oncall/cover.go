package oncall

import (
	"time"

	"fleetwatch/internal/server/valid"
)

// Cover requests: someone asks the team to cover their on-call for a while ("can
// someone cover me Saturday?"); the first who says yes gets an override for it.

type CoverRequest struct {
	ID       int64  `json:"id"`
	Person   string `json:"person"` // who needs cover
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Note     string `json:"note"`
	At       int64  `json:"at"`
	TakenBy  string `json:"takenBy"`
	TakenAt  int64  `json:"takenAt"`
	Canceled int64  `json:"canceledAt"`
	Status   string `json:"status"` // open | taken | canceled | past
}

// AskCover opens a request and tells the team (OnCoverAsked).
func (s *Service) AskCover(person string, from, to time.Time, note string) (CoverRequest, error) {
	now := s.Now()
	known := false
	for _, p := range s.People() {
		known = known || p.ID == person
	}
	switch {
	case !known:
		return CoverRequest{}, valid.Error("who needs cover?")
	case !to.After(from):
		return CoverRequest{}, valid.Error("the cover must end after it starts")
	case !to.After(now):
		return CoverRequest{}, valid.Error("that time is already past")
	case to.Sub(from) > 31*24*time.Hour:
		return CoverRequest{}, valid.Error("ask for a month of cover at most")
	case len(note) > 500:
		return CoverRequest{}, valid.Error("keep the note short")
	}
	res, err := s.db.Exec(`INSERT INTO oncall_cover_requests (person, from_ms, to_ms, note, created_at) VALUES (?, ?, ?, ?, ?)`,
		person, from.UnixMilli(), to.UnixMilli(), note, now.UnixMilli())
	if err != nil {
		return CoverRequest{}, err
	}
	id, _ := res.LastInsertId()
	r, err := s.coverRequest(id)
	if err == nil && s.OnCoverAsked != nil {
		go s.OnCoverAsked(r)
	}
	return r, err
}

// TakeCover gives a request to whoever says yes first: an override puts them on call.
func (s *Service) TakeCover(id int64, by string) (CoverRequest, error) {
	r, err := s.coverRequest(id)
	if err != nil {
		return r, err
	}
	if r.Person == by {
		return r, valid.Error("that is your own request")
	}
	if r.Status != "open" {
		return r, valid.Error("this request is not open anymore (" + r.Status + ")")
	}
	res, err := s.db.Exec(`UPDATE oncall_cover_requests SET taken_by = ?, taken_at = ? WHERE id = ? AND taken_by = '' AND canceled_at = 0`, by, s.Now().UnixMilli(), id)
	if err != nil {
		return r, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, valid.Error("someone took it just before")
	}
	c := s.Config()
	c.Overrides = append(c.Overrides, Override{Person: by, From: r.From, To: r.To})
	if _, err := s.Save(c); err != nil {
		s.db.Exec(`UPDATE oncall_cover_requests SET taken_by = '', taken_at = 0 WHERE id = ?`, id)
		return r, err
	}
	r, _ = s.coverRequest(id)
	if s.OnCoverTaken != nil {
		go s.OnCoverTaken(r)
	}
	return r, nil
}

// CancelCover withdraws an open request; by is who asks ("" = the owner: any).
func (s *Service) CancelCover(id int64, by string) error {
	q, args := `UPDATE oncall_cover_requests SET canceled_at = ? WHERE id = ? AND taken_by = '' AND canceled_at = 0`, []any{s.Now().UnixMilli(), id}
	if by != "" {
		q, args = q+` AND person = ?`, append(args, by)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return valid.Error("this request is not open anymore, or is not yours")
	}
	return nil
}

// CoverRequests are the latest requests, newest first.
func (s *Service) CoverRequests() []CoverRequest {
	return s.coverRequests(`ORDER BY id DESC LIMIT 50`)
}

func (s *Service) coverRequest(id int64) (CoverRequest, error) {
	list := s.coverRequests(`WHERE id = ?`, id)
	if len(list) == 0 {
		return CoverRequest{}, valid.Error("no such cover request")
	}
	return list[0], nil
}

func (s *Service) coverRequests(where string, args ...any) []CoverRequest {
	rows, err := s.db.Query(`SELECT id, person, from_ms, to_ms, note, created_at, taken_by, taken_at, canceled_at FROM oncall_cover_requests `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	now := s.Now().UnixMilli()
	out := []CoverRequest{}
	for rows.Next() {
		var r CoverRequest
		if rows.Scan(&r.ID, &r.Person, &r.From, &r.To, &r.Note, &r.At, &r.TakenBy, &r.TakenAt, &r.Canceled) != nil {
			continue
		}
		switch {
		case r.TakenBy != "":
			r.Status = "taken"
		case r.Canceled > 0:
			r.Status = "canceled"
		case r.To <= now:
			r.Status = "past"
		default:
			r.Status = "open"
		}
		out = append(out, r)
	}
	return out
}
