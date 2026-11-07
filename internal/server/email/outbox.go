package email

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/textproto"
	"strings"
	"time"

	"fleetwatch/internal/server/statuspage"
	"fleetwatch/internal/server/valid"
)

// ErrMaybeSent marks a sending error after the provider may already have taken the
// message (the connection broke during or after the data). Such a job is never sent
// again by itself: sending twice is worse than asking.
var ErrMaybeSent = statuspage.ErrMaybeSent

const maxAttempts = 8

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func scanJob(sc interface{ Scan(...any) error }) (Job, error) {
	var j Job
	err := sc.Scan(&j.ID, &j.MessageID, &j.State, &j.Attempts, &j.SendAt, &j.NextAt, &j.LastError, &j.AcceptedAt, &j.From, &j.Subject)
	return j, err
}

const jobCols = `o.id, o.message_id, o.state, o.attempts, o.send_at, o.next_at, o.last_error, o.accepted_at, o.from_addr, COALESCE(m.subject, '')`

func (s *Service) jobFor(messageID int64) (Job, error) {
	return scanJob(s.db.QueryRow(`SELECT `+jobCols+` FROM mail_outbox o LEFT JOIN mail_messages m ON m.id = o.message_id WHERE o.message_id = ?`, messageID))
}

func (s *Service) job(id string) (Job, string, error) {
	var box string
	j, err := scanJob(&withExtra{s.db.QueryRow(`SELECT `+jobCols+`, COALESCE(m.mailbox_id, '') FROM mail_outbox o LEFT JOIN mail_messages m ON m.id = o.message_id WHERE o.id = ?`, id), []any{&box}})
	if errors.Is(err, sql.ErrNoRows) {
		return j, "", ErrNotFound
	}
	return j, box, err
}

// Outbox lists jobs that are not done (and recent failures) the person may see.
func (s *Service) Outbox(a Access) ([]Job, error) {
	where, args := a.mailboxFilter("m.mailbox_id")
	rows, err := s.db.Query(`SELECT `+jobCols+` FROM mail_outbox o JOIN mail_messages m ON m.id = o.message_id
		WHERE (o.state != 'accepted' OR o.accepted_at > ?) AND `+where+` ORDER BY o.created_at DESC LIMIT 200`, append([]any{s.Now().Add(-24 * time.Hour).Unix()}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Cancel undoes a send that has not left yet: the message goes back to Drafts.
func (s *Service) Cancel(a Access, jobID string) (string, error) {
	j, box, err := s.job(jobID)
	if err != nil || !a.Mailbox(box, "send") && !a.AddressCan(addressIDOf(s, j.From), "send") {
		return "", ErrNotFound
	}
	res, err := s.db.Exec(`UPDATE mail_outbox SET state = 'canceled' WHERE id = ? AND state = 'scheduled'`, jobID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", valid.Error("too late: the message has already been handed to the provider")
	}
	var draft, rel string
	s.db.QueryRow(`SELECT draft_id FROM mail_outbox WHERE id = ?`, jobID).Scan(&draft)
	s.db.QueryRow(`SELECT raw_path FROM mail_messages WHERE id = ?`, j.MessageID).Scan(&rel)
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	tx.Exec(`DELETE FROM mail_outbox WHERE id = ?`, jobID)
	tx.Exec(`DELETE FROM mail_fts WHERE rowid = ?`, j.MessageID)
	tx.Exec(`DELETE FROM mail_messages WHERE id = ?`, j.MessageID)
	tx.Exec(`UPDATE mail_drafts SET queued = 0, updated_at = ? WHERE id = ?`, s.Now().Unix(), draft)
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.removeFile(rel)
	s.changed()
	return draft, nil
}

func addressIDOf(s *Service, addr string) string {
	a, _ := s.addressByEmail(addr)
	return a.ID
}

// Retry sends a failed job again. A job whose fate is unknown needs force: it may
// reach people twice.
func (s *Service) Retry(a Access, jobID string, force bool) error {
	j, box, err := s.job(jobID)
	if err != nil || !a.Mailbox(box, "send") && !a.AddressCan(addressIDOf(s, j.From), "send") {
		return ErrNotFound
	}
	switch {
	case j.State == "failed":
	case j.State == "unknown" && force:
	case j.State == "unknown":
		return valid.Error("the provider may already have sent this one: retry only if you are sure it did not arrive")
	default:
		return valid.Error("only failed messages can be retried")
	}
	now := s.Now().Unix()
	_, err = s.db.Exec(`UPDATE mail_outbox SET state = 'scheduled', attempts = 0, next_at = ?, last_error = '' WHERE id = ? AND state = ?`, now, jobID, j.State)
	s.poke()
	return err
}

// Run sends due jobs until ctx ends.
func (s *Service) Run(ctx context.Context) {
	// A job left "sending" by a crash may or may not have gone out.
	s.db.Exec(`UPDATE mail_outbox SET state = 'unknown', last_error = 'the dashboard stopped while sending' WHERE state = 'sending'`)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		case <-sweep.C:
			s.Sweep()
			continue
		case <-s.edgeSoon:
			time.Sleep(2 * time.Second) // gather a burst of changes into one deploy
			select {
			case <-s.edgeSoon:
			default:
			}
			c, cancel := context.WithTimeout(ctx, time.Minute)
			if err := s.PushEdge(c); err != nil {
				slog.Warn("email: could not update the Worker", "err", err)
			}
			cancel()
			continue
		}
		s.sendDue(ctx)
	}
}

func (s *Service) sendDue(ctx context.Context) {
	for range 20 {
		var id string
		err := s.db.QueryRow(`SELECT id FROM mail_outbox WHERE state = 'scheduled' AND next_at <= ? ORDER BY next_at LIMIT 1`, s.Now().Unix()).Scan(&id)
		if err != nil {
			return
		}
		s.sendOne(ctx, id)
	}
}

func (s *Service) sendOne(ctx context.Context, id string) {
	res, err := s.db.Exec(`UPDATE mail_outbox SET state = 'sending', attempts = attempts + 1 WHERE id = ? AND state = 'scheduled'`, id)
	if n, _ := res.RowsAffected(); err != nil || n == 0 {
		return
	}
	var msgID int64
	var sender, from, rcpts, draft string
	var attempts int
	s.db.QueryRow(`SELECT message_id, sender_id, from_addr, rcpts, draft_id, attempts FROM mail_outbox WHERE id = ?`, id).Scan(&msgID, &sender, &from, &rcpts, &draft, &attempts)
	raw, err := s.rawOf(msgID)
	if err == nil {
		if s.Mail == nil {
			err = valid.Error("email sending is not set up")
		} else {
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err = s.Mail.SendRaw(c, sender, from, listJSON(rcpts), raw)
			cancel()
		}
	}
	now := s.Now()
	switch state, retry := classify(err, attempts); state {
	case "accepted":
		s.db.Exec(`UPDATE mail_outbox SET state = 'accepted', accepted_at = ?, last_error = '' WHERE id = ?`, now.Unix(), id)
		var files string
		if s.db.QueryRow(`SELECT files FROM mail_drafts WHERE id = ?`, draft).Scan(&files) == nil {
			s.db.Exec(`DELETE FROM mail_drafts WHERE id = ?`, draft)
			var fs []File
			jsonInto(files, &fs)
			for _, f := range fs {
				s.removeFile("uploads/" + f.ID)
			}
		}
	case "scheduled":
		s.db.Exec(`UPDATE mail_outbox SET state = 'scheduled', next_at = ?, last_error = ? WHERE id = ?`, now.Add(retry).Unix(), trunc(err.Error(), 500), id)
	default:
		s.db.Exec(`UPDATE mail_outbox SET state = ?, last_error = ? WHERE id = ?`, state, trunc(err.Error(), 500), id)
		slog.Warn("email: sending failed", "job", id, "state", state, "err", trunc(err.Error(), 200))
	}
	s.changed()
}

// classify decides what an attempt's outcome means: accepted, try again later
// (scheduled, after the delay), failed for good, or unknown.
func classify(err error, attempts int) (string, time.Duration) {
	if err == nil {
		return "accepted", 0
	}
	if errors.Is(err, ErrMaybeSent) {
		return "unknown", 0
	}
	var te *textproto.Error
	var input valid.Error
	if errors.As(err, &te) && te.Code >= 500 || errors.As(err, &input) || errors.Is(err, ErrNotFound) {
		return "failed", 0
	}
	if attempts >= maxAttempts {
		return "failed", 0
	}
	d := time.Duration(1<<min(attempts, 10)) * 30 * time.Second // 1, 2, 4 … minutes
	return "scheduled", min(d, 6*time.Hour)
}

func trunc(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) > n {
		return s[:n]
	}
	return s
}
