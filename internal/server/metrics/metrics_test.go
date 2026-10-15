package metrics

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
)

func setup(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, id := range []string{"a", "b"} {
		if _, err := d.Exec(`INSERT INTO servers (id, name, created_at) VALUES (?, ?, 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(d, ServerKind), d
}

func count(t *testing.T, d *sql.DB, table string) int {
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMinuteBucketsRollupQuery(t *testing.T) {
	s, d := setup(t)
	base := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	// Server a: 10 minutes, 4 samples per minute, CPU = minute index; network 1 MB/s.
	for m := 0; m < 10; m++ {
		for i := 0; i < 4; i++ {
			ts := base.Add(time.Duration(m)*time.Minute + time.Duration(i*15)*time.Second)
			s.Add("a", ts.UnixMilli(), Sample{CPU: float64(m), Mem: 50, NetRx: 1e6}.Values())
		}
	}
	// Server b: constant CPU 100.
	for m := 0; m < 10; m++ {
		s.Add("b", base.Add(time.Duration(m)*time.Minute).UnixMilli(), Sample{CPU: 100}.Values())
	}
	s.Flush("a")
	s.Flush("b")
	if n := count(t, d, "server_metrics_1m"); n != 20 {
		t.Fatalf("1m rows = %d, want 20", n)
	}
	now := base.Add(10 * time.Minute)
	if err := s.Rollup(now); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(now); err != nil { // idempotent
		t.Fatal(err)
	}
	if n := count(t, d, "server_metrics_5m"); n != 4 {
		t.Fatalf("5m rows = %d, want 4", n)
	}
	if n := count(t, d, "server_metrics_1h"); n != 2 {
		t.Fatalf("1h rows = %d, want 2", n)
	}

	rows, err := s.Rows("a", "1h", now)
	pts := ServerPoints(rows)
	if err != nil || len(pts) != 10 {
		t.Fatalf("query a 1h: %d %v", len(pts), err)
	}
	if pts[3].CPU != 3 || pts[3].RAM != 50 || math.Abs(pts[3].NetRx-8) > 1e-9 { // 1 MB/s = 8 Mbps
		t.Fatalf("point: %+v", pts[3])
	}
	allRows, _ := s.Rows("", "1h", now)
	all := ServerPoints(allRows)
	if all[0].CPU != 50 { // average of 0 and 100, not weighted by sample count
		t.Fatalf("all-servers average = %v", all[0].CPU)
	}
	sixRows, _ := s.Rows("a", "6h", now)
	six := ServerPoints(sixRows)
	if len(six) != 2 || six[0].CPU != 2 { // minutes 0-4 average to 2
		t.Fatalf("6h: %+v", six)
	}
	if _, err := s.Rows("a", "1y", now); err != ErrBadRange {
		t.Fatal("bad range accepted")
	}
	if err := s.Prune(base.Add(49*time.Hour), 30); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, "server_metrics_1m"); n != 0 {
		t.Fatalf("1m not pruned: %d", n)
	}
	if n := count(t, d, "server_metrics_5m"); n != 4 {
		t.Fatalf("5m pruned too early: %d", n)
	}
}

func TestSettingsValidation(t *testing.T) {
	if DefaultSettings().Validate() != nil {
		t.Fatal("defaults invalid")
	}
	for _, s := range []Settings{{IntervalSec: 1, RetentionDays: 30}, {IntervalSec: 15, RetentionDays: 0}} {
		if s.Validate() == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}
