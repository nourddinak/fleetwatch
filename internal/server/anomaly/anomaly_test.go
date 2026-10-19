package anomaly

import (
	"path/filepath"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
)

func TestJudge(t *testing.T) {
	cpu := watched[0]
	usual := make([]float64, 20)
	for i := range usual {
		usual[i] = 10 + float64(i%3) // idles around 10-12%
	}
	if open, _, mean := judge(cpu, usual, 80); !open || mean < 10 || mean > 12 {
		t.Fatalf("80%% at idle time not flagged (mean %v)", mean)
	}
	if open, _, _ := judge(cpu, usual, 30); open {
		t.Fatal("30% is below the 40% floor")
	}
	if open, _, _ := judge(cpu, usual[:5], 95); open {
		t.Fatal("judged with too little history")
	}
	busy := make([]float64, 20)
	for i := range busy {
		busy[i] = 70 + float64(i%2)*10 // normally busy at this hour
	}
	if open, clear, _ := judge(cpu, busy, 85); open || !clear {
		t.Fatal("a normally busy hour flagged")
	}
}

func TestCheck(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS-1', 0)`)
	now := time.Date(2026, 10, 16, 3, 30, 0, 0, time.UTC)
	for day := 1; day <= 10; day++ { // 03:00 idles at 10% CPU
		for _, h := range []int{2, 3, 4} {
			ts := time.Date(2026, 10, 16-day, h, 0, 0, 0, time.UTC).UnixMilli()
			d.Exec(`INSERT INTO server_metrics_1h (server_id, ts, cpu, mem, swap, disk, load1, net_rx, net_tx) VALUES ('s1', ?, 10, 30, 0, 50, 0.2, 1000, 1000)`, ts)
		}
	}
	for m := range 15 {
		d.Exec(`INSERT INTO server_metrics_1m (server_id, ts, cpu, mem, swap, disk, load1, net_rx, net_tx) VALUES ('s1', ?, 85, 31, 0, 50, 3, 1000, 1000)`,
			now.Add(-time.Duration(m)*time.Minute).UnixMilli())
	}
	inc := incidents.NewStore(d, live.NewBus())
	on := true
	s := &Service{DB: d, Incidents: inc, ServerIDs: func() []string { return []string{"s1"} }, Name: func(string) string { return "VPS-1" },
		Enabled: func() bool { return on }, Now: func() time.Time { return now }}
	s.Check()
	list, _ := inc.List(incidents.Filter{Status: "open"})
	if len(list) != 1 || list[0].TypeKey != "anomaly" || list[0].Title != "Unusual CPU on VPS-1: 85%" {
		t.Fatalf("%+v", list)
	}
	n := s.Normal("s1")
	if r := n["cpu"]; r.Mean != 10 || r.Low != 10 || r.High != 10 || r.Alert != 40 || n["mem"].Mean != 30 || !s.Learned("s1") {
		t.Fatalf("the normal range: %+v", n)
	}
	if s.Learned("s2") || len(s.Normal("s2")) != 0 {
		t.Fatal("a server without history is not learned")
	}
	on = false
	s.Check()
	if list, _ := inc.List(incidents.Filter{Status: "open"}); len(list) != 0 {
		t.Fatal("not cleared when turned off")
	}
}
