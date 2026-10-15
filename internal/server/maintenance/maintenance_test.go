package maintenance

import (
	"path/filepath"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
)

func TestWindows(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS', 0)`)
	d.Exec(`INSERT INTO apps (id, server_id, name, manager, process_key, created_at) VALUES ('a1', 's1', 'api', 'pm2', 'api', 0), ('a2', 's1', 'web', 'pm2', 'web', 0)`)
	s, err := New(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_800_000_000_000)
	s.Now = func() time.Time { return now }
	var ended []string
	s.OnEnd = func(kind, id string) { ended = append(ended, kind+":"+id) }

	if s.Active("s1", "a1") {
		t.Fatal("active before any window")
	}
	if _, err := s.Set("app", "a1", 30); err != nil {
		t.Fatal(err)
	}
	if !s.Active("s1", "a1") || s.Active("s1", "a2") {
		t.Fatal("app window must cover only that app")
	}
	if _, err := s.Set("server", "s1", -1); err != nil {
		t.Fatal(err)
	}
	if !s.Active("s1", "a2") || !s.Active("s1", "") {
		t.Fatal("server window must cover the server and all its apps")
	}
	for _, bad := range []int{-2, MaxMinutes + 1} {
		if _, err := s.Set("app", "a1", bad); err == nil {
			t.Errorf("%d minutes accepted", bad)
		}
	}
	if _, err := s.Set("cluster", "s1", 5); err != ErrKind {
		t.Fatal("unknown kind accepted")
	}
	if _, err := s.Set("app", "missing", 5); err == nil {
		t.Fatal("unknown app accepted")
	}

	// Survives a restart.
	s2, _ := New(d, nil)
	s2.Now = s.Now
	if snap := s2.Snapshot(); snap.Apps["a1"] == 0 || snap.Servers["s1"] != Forever {
		t.Fatalf("not persisted: %+v", snap)
	}

	// The timed window expires on Sweep; the forever one does not.
	now = now.Add(31 * time.Minute)
	s.Sweep()
	if len(ended) != 1 || ended[0] != "app:a1" {
		t.Fatalf("expiry: %v", ended)
	}
	var until int64
	d.QueryRow(`SELECT maintenance_until FROM apps WHERE id = 'a1'`).Scan(&until)
	if until != 0 {
		t.Fatal("expired window kept in the database")
	}
	// Ending by hand reports it too.
	s.Set("server", "s1", 0)
	if len(ended) != 2 || ended[1] != "server:s1" || s.Active("s1", "a2") {
		t.Fatalf("manual end: %v", ended)
	}
	s.Set("server", "s1", 0) // ending twice reports once
	if len(ended) != 2 {
		t.Fatal("ended twice")
	}
}
