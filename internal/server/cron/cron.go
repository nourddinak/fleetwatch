package cron

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// late is how long after its time a crontab job may start before it counts as missed.
const late = 2 * time.Minute

type Service struct {
	db        *sql.DB
	Incidents *incidents.Store
	Bus       *live.Bus
	Now       func() time.Time
	Name      func(serverID string) string // a server's name, for incident titles
	// Send asks an agent for a change; AgentVersion says whether it can (cron editing
	// came with EditAgent).
	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string

	mu sync.Mutex // pings
}

func New(d *sql.DB, inc *incidents.Store, bus *live.Bus) *Service {
	return &Service{db: d, Incidents: inc, Bus: bus, Now: time.Now, Name: func(id string) string { return id }}
}

// Job is a job as the UI shows it.
type Job struct {
	proto.CronJob
	Status   string `json:"status"`             // ok | missed | failed | paused | waiting (not run yet) | unknown
	Expected int64  `json:"expected,omitempty"` // when it should last have run, unix ms
	Next     int64  `json:"next,omitempty"`
}

// Server is one server's jobs.
type Server struct {
	ServerID string `json:"serverId"`
	Jobs     []Job  `json:"jobs"`
	HasLog   bool   `json:"hasLog"`
	TimeZone string `json:"timeZone"`
	At       int64  `json:"at"` // when the agent reported
}

// OnReport stores an agent's report and opens or clears its jobs' incidents.
func (s *Service) OnReport(serverID string, data json.RawMessage) {
	var r proto.CronReport
	if json.Unmarshal(data, &r) != nil {
		return
	}
	now := s.Now()
	first := map[string]int64{}
	var raw string
	if s.db.QueryRow(`SELECT first_seen FROM cron_reports WHERE server_id = ?`, serverID).Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &first)
	}
	kept := map[string]int64{}
	for _, j := range r.Jobs {
		if first[j.ID] == 0 {
			first[j.ID] = now.UnixMilli()
		}
		kept[j.ID] = first[j.ID]
	}
	b, _ := json.Marshal(r)
	fs, _ := json.Marshal(kept)
	if _, err := s.db.Exec(`INSERT INTO cron_reports (server_id, data, first_seen, at) VALUES (?, ?, ?, ?)
		ON CONFLICT (server_id) DO UPDATE SET data = excluded.data, first_seen = excluded.first_seen, at = excluded.at`,
		serverID, string(b), string(fs), now.UnixMilli()); err != nil {
		return
	}
	v := s.view(serverID, r, kept, now.UnixMilli(), now)
	open := map[string]bool{}
	for _, j := range v.Jobs {
		k := "cron|" + serverID + "|" + j.ID
		open[k] = true
		what := describe(j.CronJob)
		switch j.Status {
		case "failed":
			s.Incidents.Ensure(incidents.Signal{Type: "cron_failed", Key: k, Severity: "warning", ServerID: serverID,
				Title: fmt.Sprintf("Job failed on %s: %s", s.Name(serverID), what), Sample: j.LastError})
		case "missed":
			s.Incidents.Ensure(incidents.Signal{Type: "cron_missed", Key: k, Severity: "warning", ServerID: serverID,
				Title:  fmt.Sprintf("Job did not run on %s: %s", s.Name(serverID), what),
				Sample: "Due at " + time.UnixMilli(j.Expected).UTC().Format("2006-01-02 15:04 UTC") + " (" + j.Schedule + ")"})
		default:
			s.Incidents.Clear(k, "ran again")
		}
	}
	s.clearGone(serverID, open)
	if s.Bus != nil {
		s.Bus.Publish("cron", v)
	}
}

// clearGone resolves incidents of jobs no longer on the server.
func (s *Service) clearGone(serverID string, keep map[string]bool) {
	rows, err := s.db.Query(`SELECT key FROM incidents WHERE type IN ('cron_failed', 'cron_missed') AND status <> 'resolved' AND key LIKE ?`, "cron|"+serverID+"|%")
	if err != nil {
		return
	}
	var gone []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil && !keep[k] {
			gone = append(gone, k)
		}
	}
	rows.Close()
	for _, k := range gone {
		s.Incidents.Clear(k, "the job was removed")
	}
}

func describe(j proto.CronJob) string {
	c := j.Command
	if j.Source == "timer" {
		c = strings.TrimSuffix(j.File, ".timer")
	}
	if len(c) > 80 {
		c = c[:80] + "…"
	}
	return c
}

// view works out each job's state.
func (s *Service) view(serverID string, r proto.CronReport, first map[string]int64, at int64, now time.Time) Server {
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	out := Server{ServerID: serverID, Jobs: []Job{}, HasLog: r.HasLog, TimeZone: r.TimeZone, At: at}
	for _, j := range r.Jobs {
		v := Job{CronJob: j, Status: "ok", Next: j.NextRun}
		if j.Paused {
			v.Status, v.Next = "paused", 0
			out.Jobs = append(out.Jobs, v)
			continue
		}
		if j.Source == "timer" {
			switch {
			case j.Failed:
				v.Status = "failed"
			case j.LastRun == 0:
				v.Status = "waiting"
			}
			out.Jobs = append(out.Jobs, v)
			continue
		}
		sched, err := Parse(j.Schedule)
		if err != nil {
			v.Status = "unknown"
			out.Jobs = append(out.Jobs, v)
			continue
		}
		if n, ok := sched.Next(now.In(loc)); ok {
			v.Next = n.UnixMilli()
		}
		prev, ok := sched.Prev(now.In(loc).Add(-late))
		switch {
		case !r.HasLog:
			v.Status = "unknown" // runs cannot be read on this server
		case !ok || prev.UnixMilli() < first[j.ID]:
			v.Status = "waiting" // added since it was last due
			if j.LastRun > 0 {
				v.Status = "ok"
			}
		case j.LastRun < prev.UnixMilli()-int64(time.Minute/time.Millisecond):
			v.Status, v.Expected = "missed", prev.UnixMilli()
		}
		out.Jobs = append(out.Jobs, v)
	}
	return out
}

// List is every server's jobs; allowed limits it to some servers (nil = all).
func (s *Service) List(allowed func(serverID string) bool) []Server {
	out := []Server{}
	rows, err := s.db.Query(`SELECT server_id, data, first_seen, at FROM cron_reports ORDER BY server_id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	now := s.Now()
	for rows.Next() {
		var id, data, fs string
		var at int64
		if rows.Scan(&id, &data, &fs, &at) != nil || (allowed != nil && !allowed(id)) {
			continue
		}
		var r proto.CronReport
		first := map[string]int64{}
		if json.Unmarshal([]byte(data), &r) != nil {
			continue
		}
		json.Unmarshal([]byte(fs), &first)
		out = append(out, s.view(id, r, first, at, now))
	}
	return out
}

// ---------- changes ----------

// EditAgent is the first agent version that changes cron jobs.
const EditAgent = "v0.9.151"

// Change asks a server's agent to add, change, pause, resume, delete or run a job. The
// result comes back by itself (OnResult), live as "cron.result".
func (s *Service) Change(serverID string, c proto.CronChange) (string, error) {
	if s.Send == nil {
		return "", errors.New("not connected")
	}
	if s.AgentVersion != nil && release.Newer(EditAgent, s.AgentVersion(serverID)) {
		return "", valid.Error("this server's agent is too old to change cron jobs; update the agent first")
	}
	switch c.Op {
	case "add", "update":
		c.Schedule, c.Command = strings.Join(strings.Fields(c.Schedule), " "), strings.TrimSpace(c.Command)
		if _, err := Parse(c.Schedule); err != nil {
			return "", valid.Error("the schedule must be five fields like */5 * * * *, or @hourly, @daily, @weekly, @monthly")
		}
		if c.Command == "" || len(c.Command) > 2000 || strings.ContainsAny(c.Command, "\n\r") {
			return "", valid.Error("the command must be one line (up to 2000 characters)")
		}
		if c.Op == "add" && strings.TrimSpace(c.User) == "" {
			c.User = "root"
		}
	case "delete", "pause", "resume", "run":
	default:
		return "", valid.Error("unknown change")
	}
	if c.Op != "add" && c.JobID == "" {
		return "", valid.Error("which job?")
	}
	c.RunID = "cr_" + randHex(8)
	if err := s.Send(serverID, proto.TypeCronChange, c); err != nil {
		return "", valid.Error("the server is not connected")
	}
	return c.RunID, nil
}

// OnResult passes an agent's answer to the page that asked.
func (s *Service) OnResult(serverID string, data json.RawMessage) {
	var r proto.CronResult
	if json.Unmarshal(data, &r) != nil || s.Bus == nil {
		return
	}
	s.Bus.Publish("cron.result", struct {
		ServerID string `json:"serverId"`
		proto.CronResult
	}{serverID, r})
}

// ---------- heartbeat links ----------

// Ping is a job that calls its own link when it finishes (curl https://…/ping/<token>).
type Ping struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	EveryMin  int    `json:"everyMin"` // expected at least this often
	GraceMin  int    `json:"graceMin"` // and this long late at most
	LastPing  int64  `json:"lastPing"`
	LastFail  int64  `json:"lastFail"` // the last call was a failure, at; 0 once it succeeds again
	Message   string `json:"message"`  // sent with the last failure
	CreatedAt int64  `json:"createdAt"`
	Status    string `json:"status"` // ok | late | failed | waiting (read-only)
}

const pingsKey = "cronPings"

var ErrNoPing = errors.New("no such heartbeat")

func (s *Service) pings() []Ping {
	list := []Ping{}
	settings.Load(s.db, pingsKey, &list)
	return list
}

func (p Ping) status(now time.Time) string {
	last := max(p.LastPing, p.LastFail)
	switch {
	case p.LastFail > 0:
		return "failed"
	case last == 0 && now.Sub(time.UnixMilli(p.CreatedAt)) <= p.due():
		return "waiting"
	case now.Sub(time.UnixMilli(max(last, p.CreatedAt))) > p.due():
		return "late"
	}
	return "ok"
}

func (p Ping) due() time.Duration { return time.Duration(p.EveryMin+p.GraceMin) * time.Minute }

// Pings lists the heartbeat links with their state.
func (s *Service) Pings() []Ping {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.pings()
	now := s.Now()
	for i := range list {
		list[i].Status = list[i].status(now)
	}
	return list
}

// SavePing adds a heartbeat link (no ID) or changes one.
func (s *Service) SavePing(p Ping) (Ping, error) {
	p.Name = strings.TrimSpace(p.Name)
	switch {
	case p.Name == "" || len(p.Name) > 80:
		return Ping{}, valid.Error("give it a name (up to 80 characters)")
	case p.EveryMin < 1 || p.EveryMin > 60*24*31:
		return Ping{}, valid.Error("how often it runs: between 1 minute and 31 days")
	case p.GraceMin < 0 || p.GraceMin > 60*24:
		return Ping{}, valid.Error("the grace time is at most a day")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.pings()
	if p.ID == "" {
		if len(list) >= 200 {
			return Ping{}, valid.Error("at most 200 heartbeats")
		}
		p.ID, p.Token, p.CreatedAt = "hb_"+randHex(6), randHex(16), s.Now().UnixMilli()
		p.LastPing, p.LastFail, p.Message = 0, 0, ""
		list = append(list, p)
	} else {
		found := false
		for i := range list {
			if list[i].ID == p.ID {
				list[i].Name, list[i].EveryMin, list[i].GraceMin = p.Name, p.EveryMin, p.GraceMin
				p, found = list[i], true
			}
		}
		if !found {
			return Ping{}, ErrNoPing
		}
	}
	p.Status = p.status(s.Now())
	return p, settings.Save(s.db, pingsKey, list)
}

func (s *Service) DeletePing(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.pings()
	for i, p := range list {
		if p.ID == id {
			s.Incidents.Clear("ping|"+id, "the heartbeat was deleted")
			return settings.Save(s.db, pingsKey, append(list[:i], list[i+1:]...))
		}
	}
	return ErrNoPing
}

// Ping records a call to a heartbeat link: success, or failure with a message.
func (s *Service) Ping(token string, failed bool, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.pings()
	for i := range list {
		p := &list[i]
		if p.Token != token || token == "" {
			continue
		}
		now := s.Now().UnixMilli()
		if failed {
			p.LastFail, p.Message = now, message
			s.Incidents.Ensure(incidents.Signal{Type: "cron_failed", Key: "ping|" + p.ID, Severity: "warning", Title: "Job failed: " + p.Name, Sample: message})
		} else {
			p.LastPing, p.LastFail, p.Message = now, 0, ""
			s.Incidents.Clear("ping|"+p.ID, "checked in again")
		}
		return settings.Save(s.db, pingsKey, list)
	}
	return ErrNoPing
}

// Sweep opens an incident for each heartbeat that is late (every minute).
func (s *Service) Sweep() {
	for _, p := range s.Pings() {
		if p.Status == "late" {
			s.Incidents.Ensure(incidents.Signal{Type: "cron_missed", Key: "ping|" + p.ID, Severity: "warning",
				Title: "Job did not check in: " + p.Name, Sample: fmt.Sprintf("Expected every %d min (+%d min grace)", p.EveryMin, p.GraceMin)})
		}
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
