// Package metrics stores time series at 1-minute, 5-minute and 1-hour resolution.
//
// Samples arrive every few seconds; they are averaged in memory per minute and only
// the per-minute average is written. A periodic job rolls minutes up into 5-minute and
// hourly rows and prunes each table to its retention. The same machinery serves host
// metrics (Kind Server) and per-application metrics (Kind App).
package metrics

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

const (
	min1  = int64(time.Minute / time.Millisecond)
	min5  = 5 * min1
	hour1 = 60 * min1

	Keep1m = 48 * time.Hour
	Keep5m = 8 * 24 * time.Hour
)

// Kind describes one family of tables: <Table>_1m, _5m, _1h keyed by Key.
type Kind struct {
	Table string
	Key   string
	Cols  []string
}

var (
	ServerKind = Kind{"server_metrics", "server_id", []string{"cpu", "mem", "swap", "disk", "load1", "net_rx", "net_tx", "gpu", "gpu_mem"}}
	AppKind    = Kind{"app_metrics", "app_id", []string{"cpu", "mem", "restarts"}}
)

// Sample is one host reading: percentages for cpu/mem/swap/disk/gpu, bytes/s for network.
type Sample struct {
	CPU, Mem, Swap, Disk, Load1, NetRx, NetTx, GPU, GPUMem float64
}

// Values orders a Sample like ServerKind.Cols.
func (s Sample) Values() []float64 {
	return []float64{s.CPU, s.Mem, s.Swap, s.Disk, s.Load1, s.NetRx, s.NetTx, s.GPU, s.GPUMem}
}

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func FromProto(m proto.Metrics) Sample {
	s := Sample{CPU: m.CPU, Mem: pct(m.MemUsed, m.MemTotal), Swap: pct(m.SwapUsed, m.SwapTotal), Disk: pct(m.DiskUsed, m.DiskTotal),
		Load1: m.Load1, NetRx: m.NetRxBps, NetTx: m.NetTxBps}
	// Several GPUs: average utilization, and memory used of all of them together.
	var used, total uint64
	for _, g := range m.GPUs {
		s.GPU += g.Util / float64(len(m.GPUs))
		used, total = used+g.MemUsed, total+g.MemTotal
	}
	s.GPUMem = pct(used, total)
	return s
}

type bucket struct {
	minute int64
	n      int
	sum    []float64
}

type Store struct {
	db   *sql.DB
	kind Kind
	mu   sync.Mutex
	acc  map[string]*bucket
}

func NewStore(d *sql.DB, kind Kind) *Store {
	return &Store{db: d, kind: kind, acc: map[string]*bucket{}}
}

// Add accumulates a sample (ordered like Kind.Cols) taken at ts (unix ms).
// Finishing a minute writes it.
func (s *Store) Add(id string, ts int64, vals []float64) error {
	if len(vals) != len(s.kind.Cols) {
		return fmt.Errorf("metrics: %d values for %d columns", len(vals), len(s.kind.Cols))
	}
	minute := ts / min1 * min1
	s.mu.Lock()
	b := s.acc[id]
	var done *bucket
	if b == nil || b.minute != minute {
		done = b
		b = &bucket{minute: minute, sum: make([]float64, len(vals))}
		s.acc[id] = b
	}
	b.n++
	for i, v := range vals {
		b.sum[i] += v
	}
	s.mu.Unlock()
	if done != nil {
		return s.write(id, done)
	}
	return nil
}

// Flush writes the partial minute for id (call when its source disconnects).
func (s *Store) Flush(id string) error {
	s.mu.Lock()
	b := s.acc[id]
	delete(s.acc, id)
	s.mu.Unlock()
	if b == nil {
		return nil
	}
	return s.write(id, b)
}

// Forget drops buffered data for a deleted entity.
func (s *Store) Forget(id string) {
	s.mu.Lock()
	delete(s.acc, id)
	s.mu.Unlock()
}

func (s *Store) table(res string) string { return s.kind.Table + "_" + res }

func (s *Store) write(id string, b *bucket) error {
	if b.n == 0 {
		return nil
	}
	args := []any{id, b.minute}
	for _, v := range b.sum {
		args = append(args, v/float64(b.n))
	}
	_, err := s.db.Exec(fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, ts, %s) VALUES (?, ?%s)`,
		s.table("1m"), s.kind.Key, strings.Join(s.kind.Cols, ", "), strings.Repeat(", ?", len(s.kind.Cols))), args...)
	return err
}

func (s *Store) avgCols(alias bool) string {
	parts := make([]string, len(s.kind.Cols))
	for i, c := range s.kind.Cols {
		parts[i] = "AVG(" + c + ")"
		if alias {
			parts[i] += " " + c
		}
	}
	return strings.Join(parts, ", ")
}

// Rollup recomputes the recent 5-minute and hourly buckets. It is idempotent, so it can
// run every minute; recomputing a short trailing window picks up late minutes.
func (s *Store) Rollup(now time.Time) error {
	ms := now.UnixMilli()
	for _, r := range []struct {
		from, to string
		size     int64
		since    int64
	}{
		{"1m", "5m", min5, (ms - 20*min1) / min5 * min5},
		{"5m", "1h", hour1, (ms - 3*hour1) / hour1 * hour1},
	} {
		q := fmt.Sprintf(`INSERT OR REPLACE INTO %s SELECT %s, ts / %d * %d, %s FROM %s WHERE ts >= ? GROUP BY %s, ts / %d`,
			s.table(r.to), s.kind.Key, r.size, r.size, s.avgCols(false), s.table(r.from), s.kind.Key, r.size)
		if _, err := s.db.Exec(q, r.since); err != nil {
			return err
		}
	}
	return nil
}

// Prune applies retention; hourly data is kept for hourlyDays.
func (s *Store) Prune(now time.Time, hourlyDays int) error {
	for _, q := range []struct {
		res  string
		keep time.Duration
	}{{"1m", Keep1m}, {"5m", Keep5m}, {"1h", time.Duration(hourlyDays) * 24 * time.Hour}} {
		if _, err := s.db.Exec(`DELETE FROM `+s.table(q.res)+` WHERE ts < ?`, now.Add(-q.keep).UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// Range maps a UI range to the resolution and bucket size that give ~60-100 points.
type Range struct {
	Span   time.Duration
	Res    string
	Bucket int64
}

var Ranges = map[string]Range{
	"1h":  {time.Hour, "1m", min1},
	"6h":  {6 * time.Hour, "5m", min5},
	"24h": {24 * time.Hour, "5m", 15 * min1},
	"7d":  {7 * 24 * time.Hour, "1h", 2 * hour1},
	"30d": {30 * 24 * time.Hour, "1h", 8 * hour1},
}

var ErrBadRange = errors.New("unknown range")

// Row is one bucket; V is ordered like Kind.Cols.
type Row struct {
	T int64
	V []float64
}

// Rows returns buckets for one id, or the average over all ids when id is "".
func (s *Store) Rows(id, rangeID string, now time.Time) ([]Row, error) {
	rg, ok := Ranges[rangeID]
	if !ok {
		return nil, ErrBadRange
	}
	inner := make([]string, len(s.kind.Cols))
	outer := make([]string, len(s.kind.Cols))
	for i, c := range s.kind.Cols {
		inner[i] = "AVG(" + c + ") " + c
		outer[i] = "AVG(" + c + ")"
	}
	// Two-level aggregate: per id per bucket first, so an id with more rows in a
	// bucket does not outweigh the others in the all-ids average.
	q := fmt.Sprintf(`SELECT t, %[4]s FROM (
		SELECT %[2]s, ts / %[3]d * %[3]d AS t, %[5]s FROM %[1]s WHERE ts >= ? AND (? = '' OR %[2]s = ?) GROUP BY %[2]s, t
	) GROUP BY t ORDER BY t`, s.table(rg.Res), s.kind.Key, rg.Bucket, strings.Join(outer, ", "), strings.Join(inner, ", "))
	rows, err := s.db.Query(q, now.Add(-rg.Span).UnixMilli(), id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r := Row{V: make([]float64, len(s.kind.Cols))}
		dest := []any{&r.T}
		for i := range r.V {
			dest = append(dest, &r.V[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Point is one host chart bucket in UI units: percentages and Mbps.
type Point struct {
	T      int64   `json:"t"`
	CPU    float64 `json:"cpu"`
	RAM    float64 `json:"ram"`
	Swap   float64 `json:"swap"`
	Disk   float64 `json:"disk"`
	Load   float64 `json:"load"`
	NetRx  float64 `json:"netRx"`
	NetTx  float64 `json:"netTx"`
	GPU    float64 `json:"gpu"`
	GPUMem float64 `json:"gpuMem"`
}

func ServerPoints(rows []Row) []Point {
	out := make([]Point, len(rows))
	for i, r := range rows {
		out[i] = Point{T: r.T, CPU: r.V[0], RAM: r.V[1], Swap: r.V[2], Disk: r.V[3], Load: r.V[4], NetRx: r.V[5] * 8 / 1e6, NetTx: r.V[6] * 8 / 1e6,
			GPU: r.V[7], GPUMem: r.V[8]}
	}
	return out
}

// AppPoint is one application chart bucket. Restarts is the number of restarts that
// happened during the bucket (the stored value is the cumulative counter).
type AppPoint struct {
	T        int64   `json:"t"`
	CPU      float64 `json:"cpu"`
	RAM      float64 `json:"ram"` // MB
	Restarts float64 `json:"restarts"`
}

func AppPoints(rows []Row) []AppPoint {
	out := make([]AppPoint, len(rows))
	for i, r := range rows {
		out[i] = AppPoint{T: r.T, CPU: r.V[0], RAM: r.V[1]}
		if i > 0 && r.V[2] > rows[i-1].V[2] {
			out[i].Restarts = float64(int64(r.V[2] - rows[i-1].V[2] + 0.5))
		}
	}
	return out
}
