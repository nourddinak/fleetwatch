// Package maintenance pauses alerts for a server or an app for a while (a deploy, a
// reboot). Incidents are still recorded; notifications are not sent. When the window
// ends, OnEnd lets the caller report what is still broken.
package maintenance

import (
	"database/sql"
	"errors"
	"sync"
	"time"

	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/valid"
)

// Forever is the "until I turn it off" end time (unix ms).
const Forever = int64(1) << 62

// MaxMinutes bounds timed windows (a week).
const MaxMinutes = 7 * 24 * 60

var ErrKind = errors.New(`maintenance kind must be "server" or "app"`)

// Snapshot maps ids to the end of their window (unix ms) for active windows only.
type Snapshot struct {
	Servers map[string]int64 `json:"servers"`
	Apps    map[string]int64 `json:"apps"`
}

type Service struct {
	db  *sql.DB
	Bus *live.Bus
	Now func() time.Time
	// OnEnd is called (outside the lock) when a window ends, by expiry or by hand.
	OnEnd func(kind, id string)

	mu     sync.Mutex
	active map[string]map[string]int64 // kind → id → until
}

var tables = map[string]string{"server": "servers", "app": "apps"}

func New(d *sql.DB, bus *live.Bus) (*Service, error) {
	s := &Service{db: d, Bus: bus, Now: time.Now, active: map[string]map[string]int64{"server": {}, "app": {}}}
	for kind, table := range tables {
		rows, err := d.Query(`SELECT id, maintenance_until FROM ` + table + ` WHERE maintenance_until > 0`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var until int64
			if rows.Scan(&id, &until) == nil {
				s.active[kind][id] = until
			}
		}
		rows.Close()
	}
	return s, nil
}

// Set starts a window of minutes (-1 = until turned off) or ends one (0).
func (s *Service) Set(kind, id string, minutes int) (int64, error) {
	table, ok := tables[kind]
	if !ok {
		return 0, ErrKind
	}
	until := int64(0)
	switch {
	case minutes == -1:
		until = Forever
	case minutes > 0 && minutes <= MaxMinutes:
		until = s.Now().Add(time.Duration(minutes) * time.Minute).UnixMilli()
	case minutes != 0:
		return 0, valid.Error("maintenance must last 1 minute to 7 days, or until turned off")
	}
	res, err := s.db.Exec(`UPDATE `+table+` SET maintenance_until = ? WHERE id = ?`, until, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, sql.ErrNoRows
	}
	s.mu.Lock()
	_, was := s.active[kind][id]
	if until > 0 {
		s.active[kind][id] = until
	} else {
		delete(s.active[kind], id)
	}
	s.mu.Unlock()
	s.publish()
	if was && until == 0 && s.OnEnd != nil {
		s.OnEnd(kind, id)
	}
	return until, nil
}

// Active reports whether alerts for this server, or this app, are paused. A server's
// window covers all of its apps.
func (s *Service) Active(serverID, appID string) bool {
	now := s.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active["server"][serverID] > now || (appID != "" && s.active["app"][appID] > now)
}

func (s *Service) Snapshot() Snapshot {
	now := s.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{Servers: map[string]int64{}, Apps: map[string]int64{}}
	for id, until := range s.active["server"] {
		if until > now {
			out.Servers[id] = until
		}
	}
	for id, until := range s.active["app"] {
		if until > now {
			out.Apps[id] = until
		}
	}
	return out
}

// Sweep ends windows whose time is up. Call it every minute or so.
func (s *Service) Sweep() {
	now := s.Now().UnixMilli()
	type ended struct{ kind, id string }
	var done []ended
	s.mu.Lock()
	for kind, ids := range s.active {
		for id, until := range ids {
			if until <= now {
				delete(ids, id)
				done = append(done, ended{kind, id})
			}
		}
	}
	s.mu.Unlock()
	if len(done) == 0 {
		return
	}
	for _, e := range done {
		s.db.Exec(`UPDATE `+tables[e.kind]+` SET maintenance_until = 0 WHERE id = ?`, e.id)
	}
	s.publish()
	for _, e := range done {
		if s.OnEnd != nil {
			s.OnEnd(e.kind, e.id)
		}
	}
}

func (s *Service) publish() {
	if s.Bus != nil {
		s.Bus.Publish("maintenance", s.Snapshot())
	}
}
