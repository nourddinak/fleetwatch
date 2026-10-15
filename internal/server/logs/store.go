package logs

import (
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Line is a stored log line as the UI sees it.
type Line struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	ServerID string `json:"serverId"`
	AppID    string `json:"appId"`
	Source   string `json:"source"`
	Stream   string `json:"stream"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Settings is the "logs" settings section.
type Settings struct {
	RetentionDays int `json:"retentionDays"`
	MaxLineKB     int `json:"maxLineKB"`
}

func DefaultSettings() Settings { return Settings{RetentionDays: 7, MaxLineKB: 16} }

func (s Settings) Validate() error {
	if !slices.Contains([]int{1, 3, 7, 14, 30}, s.RetentionDays) {
		return valid.Error("log retention must be 1, 3, 7, 14 or 30 days")
	}
	if !slices.Contains([]int{4, 8, 16, 64}, s.MaxLineKB) {
		return valid.Error("max line length must be 4, 8, 16 or 64 KB")
	}
	return nil
}

type batch struct {
	serverID string
	lines    []proto.LogLine
}

// Store owns the logs table. Writes go through one goroutine that groups incoming
// batches into a transaction every ~250 ms, so a log storm costs few commits.
type Store struct {
	db   *sql.DB
	Live *Live
	// OnStored receives each batch after it is written (incident detection). Optional.
	OnStored func([]Line)
	in       chan batch
	dropped  atomic.Int64

	mu       sync.Mutex
	cfg      Settings
	cfgAt    time.Time
	errCache map[string]int
}

func NewStore(d *sql.DB) *Store {
	return &Store{db: d, Live: NewLive(), in: make(chan batch, 2048)}
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.cfgAt) > 10*time.Second {
		cfg := DefaultSettings()
		if settings.Load(s.db, "logs", &cfg) != nil || cfg.Validate() != nil {
			cfg = DefaultSettings()
		}
		s.cfg, s.cfgAt = cfg, time.Now()
	}
	return s.cfg
}

func (s *Store) SaveSettings(cfg Settings) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := settings.Save(s.db, "logs", cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg, s.cfgAt = cfg, time.Now()
	s.mu.Unlock()
	return nil
}

// Submit queues lines from a server's agent. It never blocks: when the writer is
// overwhelmed the batch is dropped and counted (agents keep their own buffer, but
// ingestion must not stall agent connections).
func (s *Store) Submit(serverID string, lines []proto.LogLine) bool {
	select {
	case s.in <- batch{serverID, lines}:
		return true
	default:
		n := s.dropped.Add(int64(len(lines)))
		slog.Warn("log ingestion queue full; dropping batch", "server", serverID, "lines", len(lines), "dropped_total", n)
		return false
	}
}

// Run is the single writer. It returns when ctx ends, after flushing.
func (s *Store) Run(ctx context.Context) {
	for {
		var pending []batch
		select {
		case <-ctx.Done():
			return
		case b := <-s.in:
			pending = append(pending, b)
		}
		deadline := time.After(250 * time.Millisecond)
		n := len(pending[0].lines)
	gather:
		for n < 5000 {
			select {
			case b := <-s.in:
				pending = append(pending, b)
				n += len(b.lines)
			case <-deadline:
				break gather
			}
		}
		lines, err := s.write(pending)
		if err != nil {
			slog.Error("write logs", "err", err, "lines", n)
			continue
		}
		s.Live.Publish(lines)
		if s.OnStored != nil {
			s.OnStored(lines)
		}
	}
}

func (s *Store) write(batches []batch) ([]Line, error) {
	maxBytes := s.Settings().MaxLineKB << 10
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ins, err := tx.Prepare(`INSERT INTO logs (ts, server_id, app_id, source, stream, severity, message) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer ins.Close()
	var out []Line
	for _, b := range batches {
		// Agents may only write logs for their own server's apps.
		allowed := map[string]bool{}
		rows, err := tx.Query(`SELECT id FROM apps WHERE server_id = ?`, b.serverID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			rows.Scan(&id)
			allowed[id] = true
		}
		rows.Close()
		for _, l := range b.lines {
			if l.AppID != "" && !allowed[l.AppID] {
				continue
			}
			line := Line{TS: l.TS, ServerID: b.serverID, AppID: l.AppID, Source: oneOf(l.Source, "pm2", "systemd", "agent"), Stream: oneOf(l.Stream, "stdout", "stderr", "journal")}
			if line.TS <= 0 || line.TS > now+5*60_000 {
				line.TS = now
			}
			line.Message = Sanitize(l.Message, maxBytes)
			line.Severity = Classify(l, line.Message)
			var appID any
			if line.AppID != "" {
				appID = line.AppID
			}
			res, err := ins.Exec(line.TS, line.ServerID, appID, line.Source, line.Stream, line.Severity, line.Message)
			if err != nil {
				return nil, err
			}
			line.ID, _ = res.LastInsertId()
			out = append(out, line)
		}
	}
	return out, tx.Commit()
}

func oneOf(v string, allowed ...string) string {
	if slices.Contains(allowed, v) {
		return v
	}
	return allowed[0]
}

// Query selects lines; zero values mean "any". Results are newest first.
type Query struct {
	AppID, ServerID, Text        string
	Severities, Sources, Streams []string
	From, To                     int64 // unix ms
	Before                       int64 // id cursor: only lines with id < Before
	Limit                        int
}

// ftsQuery turns user text into a safe FTS5 query: every word becomes a quoted
// prefix term, so punctuation cannot inject FTS syntax. Words are ANDed.
func ftsQuery(text string) string {
	var terms []string
	// SQLite treats NUL as the end of the string, which breaks the quoted phrase.
	for _, w := range strings.Fields(strings.ReplaceAll(text, "\x00", " ")) {
		w = strings.ReplaceAll(w, `"`, `""`)
		terms = append(terms, `"`+w+`"*`)
	}
	return strings.Join(terms, " ")
}

func inList(col string, vals []string, args *[]any) string {
	ph := make([]string, len(vals))
	for i, v := range vals {
		ph[i] = "?"
		*args = append(*args, v)
	}
	return col + " IN (" + strings.Join(ph, ",") + ")"
}

const maxLimit = 500

func (s *Store) Search(q Query) ([]Line, error) {
	if q.Limit <= 0 || q.Limit > maxLimit {
		q.Limit = 200
	}
	var where []string
	var args []any
	from := "logs l"
	if t := ftsQuery(q.Text); t != "" {
		from = "logs_fts f JOIN logs l ON l.id = f.rowid"
		where = append(where, "logs_fts MATCH ?")
		args = append(args, t)
	}
	if q.AppID != "" {
		where, args = append(where, "l.app_id = ?"), append(args, q.AppID)
	}
	if q.ServerID != "" {
		where, args = append(where, "l.server_id = ?"), append(args, q.ServerID)
	}
	if q.From > 0 {
		where, args = append(where, "l.ts >= ?"), append(args, q.From)
	}
	if q.To > 0 {
		where, args = append(where, "l.ts <= ?"), append(args, q.To)
	}
	if q.Before > 0 {
		where, args = append(where, "l.id < ?"), append(args, q.Before)
	}
	if len(q.Severities) > 0 {
		where = append(where, inList("l.severity", q.Severities, &args))
	}
	if len(q.Sources) > 0 {
		where = append(where, inList("l.source", q.Sources, &args))
	}
	if len(q.Streams) > 0 {
		where = append(where, inList("l.stream", q.Streams, &args))
	}
	sqlText := `SELECT l.id, l.ts, l.server_id, COALESCE(l.app_id, ''), l.source, l.stream, l.severity, l.message FROM ` + from
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY l.id DESC LIMIT ?"
	args = append(args, q.Limit)
	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.TS, &l.ServerID, &l.AppID, &l.Source, &l.Stream, &l.Severity, &l.Message); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Prune deletes lines older than the retention, in small chunks so writers are
// never blocked for long. It returns the number of deleted lines.
func (s *Store) Prune(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-time.Duration(s.Settings().RetentionDays) * 24 * time.Hour).UnixMilli()
	var total int64
	for ctx.Err() == nil {
		res, err := s.db.Exec(`DELETE FROM logs WHERE id IN (SELECT id FROM logs WHERE ts < ? LIMIT 5000)`, cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < 5000 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return total, nil
}

// RefreshErrorCounts recounts error/critical lines per app over the last 24 hours.
// Run it periodically; ErrorCounts serves the cached result.
func (s *Store) RefreshErrorCounts(now time.Time) error {
	rows, err := s.db.Query(`SELECT app_id, COUNT(*) FROM logs WHERE ts >= ? AND app_id IS NOT NULL AND severity IN ('error', 'critical') GROUP BY app_id`, now.Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		m[id] = n
	}
	s.mu.Lock()
	s.errCache = m
	s.mu.Unlock()
	return rows.Err()
}

func (s *Store) ErrorCount(appID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errCache[appID]
}

// Stats summarizes stored logs for the settings page.
type Stats struct {
	Lines    int64 `json:"lines"`
	OldestTS int64 `json:"oldestTs"`
	Dropped  int64 `json:"dropped"` // batches dropped by the server since start
}

func (s *Store) Stats() (Stats, error) {
	var st Stats
	var minID, maxID sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(id), MAX(id) FROM logs`).Scan(&minID, &maxID); err != nil {
		return st, err
	}
	if minID.Valid {
		// Retention deletes from the oldest end, so the id span is a cheap, close estimate.
		st.Lines = maxID.Int64 - minID.Int64 + 1
		s.db.QueryRow(`SELECT ts FROM logs WHERE id = ?`, minID.Int64).Scan(&st.OldestTS)
	}
	st.Dropped = s.dropped.Load()
	return st, nil
}
