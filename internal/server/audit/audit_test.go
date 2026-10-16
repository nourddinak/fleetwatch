package audit

import (
	"path/filepath"
	"testing"

	"fleetwatch/internal/server/db"
)

func TestBackfill(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ins := func(ts int64, action, ip, ua, detail, actor string) {
		d.Exec(`INSERT INTO audit_events (ts, action, ok, ip, user_agent, target, detail, actor) VALUES (?, ?, 1, ?, ?, '', ?, ?)`, ts, action, ip, ua, detail, actor)
	}
	ins(1, "command.run", "1.1.1.1", "A", "x", "")                    // nobody signed in seen: the owner
	ins(2, "login", "2.2.2.2", "B", "password (team member sam)", "") // Sam on browser B
	ins(3, "login", "1.1.1.1", "A", "password + totp", "")            // the owner on browser A
	ins(4, "command.run", "2.2.2.2", "B", "restart", "")              // Sam
	ins(5, "command.run", "1.1.1.1", "A", "stop", "")                 // the owner
	ins(6, "incident.acknowledge", "", "", "by link", "")             // no browser: left alone
	ins(7, "settings.logs", "2.2.2.2", "B", "", "Sam already")        // has one
	n, err := Backfill(d, "Nour")
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
	rows, _ := List(d, "", 10)
	got := map[int64]string{}
	for _, r := range rows {
		got[r.TS] = r.Actor
	}
	if got[1] != "Nour" || got[4] != "sam" || got[5] != "Nour" || got[6] != "" || got[7] != "Sam already" || got[2] != "" {
		t.Fatalf("%v", got)
	}
	if n, _ := Backfill(d, "Nour"); n != 0 {
		t.Fatal("ran twice", n)
	}
}
