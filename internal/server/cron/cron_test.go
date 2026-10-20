package cron

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
)

func TestSchedule(t *testing.T) {
	at := time.Date(2026, 10, 21, 10, 7, 30, 0, time.UTC) // a Wednesday
	for _, c := range []struct{ expr, prev, next string }{
		{"*/5 * * * *", "10:05", "10:10"},
		{"0 3 * * *", "Oct 21 03:00", "Oct 22 03:00"},
		{"@hourly", "10:00", "11:00"},
		{"30 9 * * mon-fri", "Oct 21 09:30", "Oct 22 09:30"},
		{"0 0 1 * *", "Oct 1 00:00", "Nov 1 00:00"},
		{"15,45 */2 * * *", "Oct 21 08:45", "Oct 21 10:15"},
		{"0 12 * * 0", "Oct 18 12:00", "Oct 25 12:00"},
	} {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatal(c.expr, err)
		}
		p, _ := s.Prev(at)
		n, _ := s.Next(at)
		f := func(x time.Time, want string) string {
			if len(want) == 5 {
				return x.Format("15:04")
			}
			return x.Format("Jan 2 15:04")
		}
		if f(p, c.prev) != c.prev || f(n, c.next) != c.next {
			t.Errorf("%s: prev %s next %s", c.expr, p, n)
		}
	}
	for _, bad := range []string{"* * *", "61 * * * *", "*/0 * * * *", "@reboot"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func setup(t *testing.T) (*Service, *incidents.Store, *time.Time) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS-1', 0)`)
	inc := incidents.NewStore(d, live.NewBus())
	now := time.Date(2026, 10, 21, 10, 7, 0, 0, time.UTC)
	s := New(d, inc, live.NewBus())
	s.Now = func() time.Time { return now }
	inc.Now = s.Now
	return s, inc, &now
}

func open(t *testing.T, inc *incidents.Store) map[string]incidents.Incident {
	list, err := inc.List(incidents.Filter{Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]incidents.Incident{}
	for _, x := range list {
		out[x.TypeKey] = x
	}
	return out
}

func TestJobs(t *testing.T) {
	s, inc, now := setup(t)
	report := func(lastBackup int64, timerFailed bool) {
		r := proto.CronReport{HasLog: true, TimeZone: "UTC", Jobs: []proto.CronJob{
			{ID: "a", Source: "crontab", User: "deploy", Schedule: "*/5 * * * *", Command: "php artisan schedule:run", LastRun: now.Add(-2 * time.Minute).UnixMilli()},
			{ID: "b", Source: "crontab", User: "root", Schedule: "0 3 * * *", Command: "/opt/backup.sh", LastRun: lastBackup},
			{ID: "timer:sync.timer", Source: "timer", File: "sync.timer", Schedule: "hourly", Failed: timerFailed, LastError: "exit-code (exit status 2)", LastRun: 1},
		}}
		b, _ := json.Marshal(r)
		s.OnReport("s1", b)
	}
	// First seen today: the backup due at 03:00 is not called missed.
	report(0, false)
	if len(open(t, inc)) != 0 {
		t.Fatalf("%+v", open(t, inc))
	}
	list := s.List(nil)
	if len(list) != 1 || list[0].Jobs[1].Status != "waiting" || list[0].Jobs[0].Status != "ok" {
		t.Fatalf("%+v", list)
	}
	// The next day the 03:00 run did not happen; the timer failed.
	*now = now.Add(24 * time.Hour)
	report(0, true)
	o := open(t, inc)
	if o["cron_missed"].Title == "" || o["cron_failed"].Title == "" || len(o) != 2 {
		t.Fatalf("%+v", o)
	}
	// It ran; the timer passed: both resolved.
	report(now.Add(-7*time.Hour).UnixMilli(), false)
	if len(open(t, inc)) != 0 {
		t.Fatalf("%+v", open(t, inc))
	}
	// Without a readable log nothing is called missed.
	b, _ := json.Marshal(proto.CronReport{TimeZone: "UTC", Jobs: []proto.CronJob{{ID: "b", Source: "crontab", Schedule: "0 3 * * *", Command: "/opt/backup.sh"}}})
	s.OnReport("s1", b)
	if len(open(t, inc)) != 0 || s.List(nil)[0].Jobs[0].Status != "unknown" {
		t.Fatal("missed without a log")
	}
}

func TestPings(t *testing.T) {
	s, inc, now := setup(t)
	if _, err := s.SavePing(Ping{Name: "Backup", EveryMin: 0}); err == nil {
		t.Fatal("saved without an interval")
	}
	p, err := s.SavePing(Ping{Name: "Backup", EveryMin: 60, GraceMin: 10})
	if err != nil || p.Token == "" || p.Status != "waiting" {
		t.Fatal(err, p)
	}
	if s.Ping("nope", false, "") == nil {
		t.Fatal("unknown token accepted")
	}
	*now = now.Add(80 * time.Minute)
	s.Sweep()
	if open(t, inc)["cron_missed"].Title == "" {
		t.Fatal("late heartbeat not reported")
	}
	if err := s.Ping(p.Token, false, ""); err != nil || len(open(t, inc)) != 0 || s.Pings()[0].Status != "ok" {
		t.Fatal("ping did not resolve it", err)
	}
	s.Ping(p.Token, true, "disk full")
	if x := open(t, inc)["cron_failed"]; x.Title == "" || s.Pings()[0].Status != "failed" {
		t.Fatalf("%+v", open(t, inc))
	}
	if s.DeletePing(p.ID) != nil || len(open(t, inc)) != 0 || len(s.Pings()) != 0 {
		t.Fatal("delete")
	}
}
