package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/db"
)

func TestUsage(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	os.MkdirAll(filepath.Join(dir, "backups"), 0o700)
	os.WriteFile(filepath.Join(dir, "backups", "a.db"), make([]byte, 5000), 0o600)
	s := &server{Deps{DB: d, DataDir: dir}}
	w := httptest.NewRecorder()
	s.usage(w, httptest.NewRequest("GET", "/api/system/usage", nil), auth.Session{})
	var u usage
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err, w.Body.String())
	}
	names := map[string]int64{}
	for _, f := range u.Storage.Files {
		names[f.Name] = f.Bytes
	}
	if names["backups"] != 5000 || names["ops.db"] == 0 || u.Storage.Total < 5000 {
		t.Fatal("data folder", u.Storage)
	}
	if len(u.Storage.Tables) == 0 || u.Storage.Tables[0].Bytes == 0 {
		t.Fatal("table sizes (dbstat)", u.Storage.Tables)
	}
	if u.Memory.Heap == 0 || u.Goroutines == 0 {
		t.Fatal("runtime", u.Memory)
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	d.Exec(`PRAGMA foreign_keys = OFF`) // results without their checks, for the test
	old, recent := time.Now().AddDate(0, 0, -40).UnixMilli(), time.Now().UnixMilli()
	for _, ts := range []int64{old, old, recent} {
		if _, err := d.Exec(`INSERT INTO uptime_results (check_id, location, ts, ok, status, latency_ms) VALUES ('c1', 'dashboard', ?, 1, 200, 10)`, ts); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{Deps{DB: d, DataDir: dir}}
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/system/cleanup", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.cleanup(w, r, auth.Session{})
		return w
	}
	if w := call(`{"days":5}`); w.Code != 400 {
		t.Fatal("7, 30 or 90 days", w.Code)
	}
	w := call(`{"days":30}`)
	var out struct{ Deleted int64 }
	json.Unmarshal(w.Body.Bytes(), &out)
	var left int
	d.QueryRow(`SELECT COUNT(*) FROM uptime_results`).Scan(&left)
	if w.Code != 200 || out.Deleted != 2 || left != 1 {
		t.Fatal(w.Code, w.Body.String(), left)
	}
}
