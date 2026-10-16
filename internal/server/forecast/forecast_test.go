package forecast

import (
	"testing"
	"time"

	"fleetwatch/internal/server/metrics"
)

func rows(n int, step time.Duration, v func(i int) float64) []metrics.Row {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	out := make([]metrics.Row, n)
	for i := range out {
		out[i] = metrics.Row{T: start.Add(time.Duration(i) * step).UnixMilli(), V: []float64{0, v(i), 0, v(i)}}
	}
	return out
}

func TestDiskOutlook(t *testing.T) {
	// 60% rising 2 points a day (2-hour buckets, 3 days): full in 20 days.
	d, ok := DiskOutlook(rows(36, 2*time.Hour, func(i int) float64 { return 54 + float64(i)/6 }), 3)
	if !ok || d.PerDay != 2 || d.FullDays < 19 || d.FullDays > 21 {
		t.Fatalf("rising: %+v %v", d, ok)
	}
	if _, ok := DiskOutlook(rows(36, 2*time.Hour, func(i int) float64 { return 60 }), 3); ok {
		t.Fatal("a flat disk has no full date")
	}
	if _, ok := DiskOutlook(rows(5, 2*time.Hour, func(i int) float64 { return float64(i) }), 3); ok {
		t.Fatal("too little data")
	}
	noisy := func(i int) float64 { return 60 + float64((i*37)%11) - 5 }
	if _, ok := DiskOutlook(rows(36, 2*time.Hour, noisy), 3); ok {
		t.Fatal("noise is not a trend")
	}
}

func TestMemoryGrowth(t *testing.T) {
	rs := rows(36, 2*time.Hour, func(i int) float64 { return 200 + float64(i)*5 }) // +60 MB a day from 200
	m, ok := MemoryGrowth(rs, 1, rs[0].T)
	if !ok || m.FromMB != 200 || m.NowMB != 375 || m.PctPerDay < 25 || m.PctPerDay > 35 {
		t.Fatalf("leak: %+v %v", m, ok)
	}
	if _, ok := MemoryGrowth(rs, 1, rs[30].T); ok {
		t.Fatal("restarted 12 hours ago: too early to tell")
	}
	if _, ok := MemoryGrowth(rows(36, 2*time.Hour, func(i int) float64 { return 300 + float64(i%2) }), 1, 0); ok {
		t.Fatal("steady memory flagged as a leak")
	}
}
