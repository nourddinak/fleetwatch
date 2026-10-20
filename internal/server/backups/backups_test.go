package backups

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
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
	repo, err := s.repo(r2.ID, "s1", false)
	if err != nil || repo.Repository != "s3:https://acct.r2.cloudflarestorage.com/backups2/fw/s1" || repo.Env["AWS_SECRET_ACCESS_KEY"] != "SECRET" || len(repo.Password) < 40 {
		t.Fatalf("%v %+v", err, repo)
	}
	if repo, _ := s.repo(drive.ID, "s1", false); repo.Repository != "rclone:fw:Fleetwatch backups/s1" || !repo.Rclone || !strings.Contains(repo.Env["RCLONE_CONFIG_FW_TOKEN"], "refresh_token") {
		t.Fatalf("%+v", repo)
	}
	if repo, _ := s.repo(nas.ID, "s1", false); repo.Repository != "sftp:bk@nas.local:/backups/s1" || !strings.Contains(repo.SSHKey, "OPENSSH PRIVATE KEY") {
		t.Fatalf("%+v", repo)
	}
	if f, _ := s.repo(drive.ID, "shop", true); !f.Files || f.Repository != "fw:Fleetwatch backups/shop" || f.Env["RCLONE_CONFIG_FW_TYPE"] != "drive" || f.Password != "" {
		t.Fatalf("%+v", f)
	}
	if f, _ := s.repo(r2.ID, "shop", true); f.Repository != "fw:backups2/fw/shop" || f.Env["RCLONE_CONFIG_FW_SECRET_ACCESS_KEY"] != "SECRET" {
		t.Fatalf("%+v", f)
	}
	if f, _ := s.repo(nas.ID, "shop", true); f.Repository != "fw:/backups/shop" || f.SSHKey == "" || f.Env["RCLONE_CONFIG_FW_HOST"] != "nas.local" {
		t.Fatalf("%+v", f)
	}
	for _, d := range s.Dests() {
		if d.Secret != "" || d.Token != "" {
			t.Fatal("a secret came out")
		}
	}
	if pw, _ := s.Password(r2.ID); pw != repo.Password {
		t.Fatal("password")
	}
	s.SavePlan(Plan{Name: "Shop", ServerID: "s1", Dest: r2.ID, At: "03:00", KeepDaily: 7, Items: []proto.BackupItem{{Kind: "postgres", Name: "shop"}}})
	if s.DeleteDest(r2.ID) == nil {
		t.Fatal("deleted a place a plan uses")
	}
}

func TestRuns(t *testing.T) {
	s, inc, now, sent := setup(t)
	d, _ := s.SaveDest(Dest{Name: "R2", Kind: "s3", Endpoint: "https://r2.example.com", Bucket: "backups", KeyID: "k", Secret: "s"})
	for _, bad := range []Plan{
		{ServerID: "s1", Dest: d.ID, At: "03:00", KeepDaily: 7}, // no name
		{Name: "x", ServerID: "s1", Dest: d.ID, At: "3am", KeepDaily: 7},
		{Name: "x", ServerID: "s1", Dest: d.ID, At: "03:00"},
		{Name: "x", ServerID: "s1", Dest: d.ID, At: "03:00", KeepDaily: 1, Items: []proto.BackupItem{{Kind: "postgres", Name: "a; drop"}}},
		{Name: "x", ServerID: "s1", Dest: d.ID, At: "03:00", KeepDaily: 1, Items: []proto.BackupItem{{Kind: "folder", Name: "relative"}}},
		{Name: "x", ServerID: "s1", Dest: "nope", At: "03:00", KeepDaily: 1},
	} {
		if _, err := s.SavePlan(bad); err == nil {
			t.Errorf("%+v saved", bad)
		}
	}
	items := []proto.BackupItem{{Kind: "postgres", Name: "shop"}, {Kind: "folder", Name: "/srv/uploads"}}
	shop, err := s.SavePlan(Plan{Name: "Shop database", ServerID: "s1", Enabled: true, Dest: d.ID, At: "03:00", TimeZone: "UTC", KeepDaily: 7, KeepWeekly: 4, Items: items})
	if err != nil || shop.Folder != "shop-database" {
		t.Fatal(err, shop)
	}
	// A second backup of the same server, same time, same name: its own folder, and it
	// waits for the first to finish.
	shop2, _ := s.SavePlan(Plan{Name: "Shop database", ServerID: "s1", Enabled: true, Dest: d.ID, At: "03:00", TimeZone: "UTC", KeepDaily: 7, Items: items[:1]})
	if shop2.Folder != "shop-database-2" {
		t.Fatal(shop2.Folder)
	}
	s.Tick() // 02:00: not yet
	*now = now.Add(time.Hour + time.Minute)
	s.Tick()
	s.Tick() // once a day
	if len(*sent) != 1 || !strings.HasSuffix((*sent)[0].Repo.Repository, "/backups/"+(*sent)[0].Repo.Repository[strings.LastIndex((*sent)[0].Repo.Repository, "/")+1:]) {
		t.Fatalf("%+v", *sent)
	}
	if _, err := s.RunNow(shop.ID, true); err == nil {
		t.Fatal("two runs at once")
	}
	// res answers a run like the agent would.
	res := func(run string, failItem bool) {
		r := proto.BackupResult{RunID: run, Started: now.UnixMilli(), Finished: now.UnixMilli() + 1000,
			Items:     []proto.BackupItemResult{{BackupItem: items[0], Snapshot: "abc12345", Added: 100}, {BackupItem: items[1]}},
			Snapshots: []proto.BackupSnapshot{{ID: "abc12345", Time: now.UnixMilli(), Paths: []string{"/postgres/shop.dump"}}}}
		if failItem {
			r.Items[1].Error = "no such folder"
		}
		b, _ := json.Marshal(r)
		s.OnResult("s1", b)
	}
	first := s.Runs(nil)[0].PlanID
	res((*sent)[0].RunID, true)
	list, _ := inc.List(incidents.Filter{Status: "open"})
	if runs := s.Runs(nil); len(runs) != 1 || runs[0].Status != "partial" || len(list) != 1 || list[0].Severity != "critical" || !strings.Contains(list[0].Samples[0], "no such folder") ||
		!strings.Contains(list[0].Title, "Shop database") {
		t.Fatalf("%+v %+v", s.Runs(nil), list)
	}
	if snaps := s.Snapshots()[first]; len(snaps) != 1 {
		t.Fatal(snaps)
	}
	s.Tick() // the server is free now: the other backup's turn
	if len(*sent) != 2 || (*sent)[1].Repo.Repository == (*sent)[0].Repo.Repository {
		t.Fatalf("%+v", *sent)
	}
	res((*sent)[1].RunID, false)
	if list, _ := inc.List(incidents.Filter{Status: "open"}); len(list) != 1 || !slices.ContainsFunc(s.Runs(nil), func(r Run) bool { return r.PlanID != first && r.Status == "ok" }) { // the first one still failed
		t.Fatal("not resolved", s.Runs(nil))
	}
	// A new plan made after its time starts the next day.
	s.DeletePlan(shop.ID)
	s.DeletePlan(shop2.ID)
	*sent = nil
	late, _ := s.SavePlan(Plan{Name: "Late", ServerID: "s1", Enabled: true, Dest: d.ID, At: "01:00", TimeZone: "UTC", KeepDaily: 7, Items: items})
	s.Tick()
	if len(*sent) != 0 {
		t.Fatal("ran right after being made")
	}
	if _, err := s.Restore(late.ID, "../etc"); err == nil {
		t.Fatal("bad snapshot id")
	}
}

func TestOldPlansKeepTheirFolder(t *testing.T) {
	s, _, _, _ := setup(t)
	settings.Save(s.db, plansKey, map[string]Plan{"s1": {ServerID: "s1", At: "03:00", KeepDaily: 7}})
	s.db.Exec(`INSERT INTO backup_runs (id, server_id, started, finished, status, data) VALUES ('br_old', 's1', 1, 2, 'ok', '{}')`)
	list := s.Plans()
	if len(list) != 1 || list[0].Folder != "s1" || list[0].Name == "" || !strings.HasPrefix(list[0].ID, "bp_") {
		t.Fatalf("%+v", list)
	}
	if runs := s.Runs(nil); len(runs) != 1 || runs[0].PlanID != list[0].ID {
		t.Fatalf("old runs not moved: %+v", runs)
	}
	if again := s.Plans(); again[0].ID != list[0].ID {
		t.Fatal("converted twice")
	}
}
