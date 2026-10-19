package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
