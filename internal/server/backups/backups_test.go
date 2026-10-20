package backups

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
)

func setup(t *testing.T) (*Service, *incidents.Store, *time.Time, *[]proto.BackupRun) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS-1', 0)`)
	box, _ := secret.New(make([]byte, 32))
	inc := incidents.NewStore(d, live.NewBus())
	s := New(d, box, inc, live.NewBus())
	now := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	inc.Now = s.Now
	var sent []proto.BackupRun
	s.Send = func(_, typ string, v any) error {
		if r, ok := v.(proto.BackupRun); ok {
			sent = append(sent, r)
		}
		return nil
	}
	s.AgentVersion = func(string) string { return "0.9.151" }
	return s, inc, &now, &sent
}

func TestDests(t *testing.T) {
	s, _, _, _ := setup(t)
	for _, bad := range []Dest{
		{Name: "x", Kind: "s3", Endpoint: "r2.example.com", Bucket: "B!", KeyID: "k", Secret: "s"},
		{Name: "x", Kind: "s3", Endpoint: "r2.example.com", Bucket: "backups", KeyID: "k"}, // no secret
		{Name: "x", Kind: "gdrive", Token: "not json"},
		{Name: "x", Kind: "sftp", Host: "nas.local", User: "bk", Path: "relative"},
		{Name: "x", Kind: "ftp"},
	} {
		if _, err := s.SaveDest(bad); err == nil {
			t.Errorf("%+v saved", bad)
		}
	}
	r2, err := s.SaveDest(Dest{Name: "R2", Kind: "s3", Endpoint: "acct.r2.cloudflarestorage.com/", Bucket: "backups", Prefix: "/fw/", KeyID: "AKID", Secret: "SECRET"})
	if err != nil || r2.Endpoint != "https://acct.r2.cloudflarestorage.com" || !r2.HasSecret || r2.Secret != "" {
		t.Fatal(err, r2)
	}
	drive, err := s.SaveDest(Dest{Name: "Drive", Kind: "gdrive", Token: `{"access_token":"a","refresh_token":"r","expiry":"2026-01-01T00:00:00Z"}`})
	if err != nil || drive.Folder != "Fleetwatch backups" {
		t.Fatal(err, drive)
	}
	nas, err := s.SaveDest(Dest{Name: "NAS", Kind: "sftp", Host: "nas.local", User: "bk", Path: "/backups/"})
	if err != nil || !strings.HasPrefix(nas.PublicKey, "ssh-ed25519 ") || nas.Port != 22 {
		t.Fatal(err, nas)
	}
	// Editing keeps the secret and key when none is sent again.
	r2.Bucket = "backups2"
	if r2, err = s.SaveDest(r2); err != nil || !r2.HasSecret {
		t.Fatal(err)
	}
	repo, err := s.repo(r2.ID, "s1")
	if err != nil || repo.Repository != "s3:https://acct.r2.cloudflarestorage.com/backups2/fw/s1" || repo.Env["AWS_SECRET_ACCESS_KEY"] != "SECRET" || len(repo.Password) < 40 {
		t.Fatalf("%v %+v", err, repo)
	}
	if repo, _ := s.repo(drive.ID, "s1"); repo.Repository != "rclone:fw:Fleetwatch backups/s1" || !repo.Rclone || !strings.Contains(repo.Env["RCLONE_CONFIG_FW_TOKEN"], "refresh_token") {
		t.Fatalf("%+v", repo)
	}
	if repo, _ := s.repo(nas.ID, "s1"); repo.Repository != "sftp:bk@nas.local:/backups/s1" || !strings.Contains(repo.SSHKey, "OPENSSH PRIVATE KEY") {
		t.Fatalf("%+v", repo)
	}
	for _, d := range s.Dests() {
		if d.Secret != "" || d.Token != "" {
			t.Fatal("a secret came out")
		}
	}
	if pw, _ := s.Password(r2.ID); pw != repo.Password {
		t.Fatal("password")
	}
	s.SavePlan(Plan{ServerID: "s1", Dest: r2.ID, At: "03:00", KeepDaily: 7, Items: []proto.BackupItem{{Kind: "postgres", Name: "shop"}}})
	if s.DeleteDest(r2.ID) == nil {
		t.Fatal("deleted a place a plan uses")
	}
}

func TestRuns(t *testing.T) {
	s, inc, now, sent := setup(t)
	d, _ := s.SaveDest(Dest{Name: "R2", Kind: "s3", Endpoint: "https://r2.example.com", Bucket: "backups", KeyID: "k", Secret: "s"})
	for _, bad := range []Plan{
		{ServerID: "s1", Dest: d.ID, At: "3am", KeepDaily: 7},
		{ServerID: "s1", Dest: d.ID, At: "03:00"},
		{ServerID: "s1", Dest: d.ID, At: "03:00", KeepDaily: 1, Items: []proto.BackupItem{{Kind: "postgres", Name: "a; drop"}}},
		{ServerID: "s1", Dest: d.ID, At: "03:00", KeepDaily: 1, Items: []proto.BackupItem{{Kind: "folder", Name: "relative"}}},
		{ServerID: "s1", Dest: "nope", At: "03:00", KeepDaily: 1},
	} {
		if _, err := s.SavePlan(bad); err == nil {
			t.Errorf("%+v saved", bad)
		}
	}
	items := []proto.BackupItem{{Kind: "postgres", Name: "shop"}, {Kind: "folder", Name: "/srv/uploads"}}
	if _, err := s.SavePlan(Plan{ServerID: "s1", Enabled: true, Dest: d.ID, At: "03:00", TimeZone: "UTC", KeepDaily: 7, KeepWeekly: 4, Items: items}); err != nil {
		t.Fatal(err)
	}
	s.Tick() // 02:00: not yet
	*now = now.Add(time.Hour + time.Minute)
	s.Tick()
	s.Tick() // once a day
	if len(*sent) != 1 || len((*sent)[0].Items) != 2 || (*sent)[0].KeepWeekly != 4 {
		t.Fatalf("%+v", *sent)
	}
	if _, err := s.RunNow("s1", true); err == nil {
		t.Fatal("two runs at once")
	}
	run := (*sent)[0].RunID
	res := func(failItem bool) {
		r := proto.BackupResult{RunID: run, Started: now.UnixMilli(), Finished: now.UnixMilli() + 1000,
			Items:     []proto.BackupItemResult{{BackupItem: items[0], Snapshot: "abc12345", Added: 100}, {BackupItem: items[1]}},
			Snapshots: []proto.BackupSnapshot{{ID: "abc12345", Time: now.UnixMilli(), Paths: []string{"/postgres/shop.dump"}}}}
		if failItem {
			r.Items[1].Error = "no such folder"
		}
		b, _ := json.Marshal(r)
		s.OnResult("s1", b)
	}
	res(true)
	list, _ := inc.List(incidents.Filter{Status: "open"})
	if runs := s.Runs(nil); len(runs) != 1 || runs[0].Status != "partial" || len(list) != 1 || list[0].Severity != "critical" || !strings.Contains(list[0].Samples[0], "no such folder") {
		t.Fatalf("%+v %+v", s.Runs(nil), list)
	}
	if snaps := s.Snapshots()["s1"]; len(snaps) != 1 {
		t.Fatal(snaps)
	}
	res(false)
	if list, _ := inc.List(incidents.Filter{Status: "open"}); len(list) != 0 || s.Runs(nil)[0].Status != "ok" {
		t.Fatal("not resolved")
	}
	// A new plan made after its time starts the next day.
	s.DeletePlan("s1")
	*sent = nil
	s.SavePlan(Plan{ServerID: "s1", Enabled: true, Dest: d.ID, At: "01:00", TimeZone: "UTC", KeepDaily: 7, Items: items})
	s.Tick()
	if len(*sent) != 0 {
		t.Fatal("ran right after being made")
	}
	if _, err := s.Restore("s1", "../etc"); err == nil {
		t.Fatal("bad snapshot id")
	}
}
