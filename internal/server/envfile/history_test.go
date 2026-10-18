package envfile

import (
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

func TestHistory(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'web', 0)`)
	if _, err := d.Exec(`INSERT INTO apps (id, server_id, name, manager, process_key, path, created_at) VALUES ('a1', 's1', 'shop', 'pm2', 'shop', '/srv/shop', 0)`); err != nil {
		t.Skip("apps schema differs: ", err)
	}
	box, _ := secret.New(make([]byte, 32))
	h := NewHistory(d, box)
	h.Add("a1", ".env", OutsideFleetwatch, "A=1")
	h.Add("a1", ".env", "Nour", "A=2")
	h.Add("a1", ".env", "Nour", "A=2") // unchanged: not stored twice
	h.Add("a1", ".env.production", "Nour", "B=1")
	v, _ := h.List("a1", ".env")
	if len(v) != 2 || v[0].Content != "A=2" || v[0].By != "Nour" || v[1].By != OutsideFleetwatch {
		t.Fatalf("%+v", v)
	}
	var raw []byte
	d.QueryRow(`SELECT content_enc FROM env_history LIMIT 1`).Scan(&raw)
	if strings.Contains(string(raw), "A=") {
		t.Fatal("stored in the clear")
	}
	if n, _ := h.Delete("a1", ".env", v[1].ID); n != 1 {
		t.Fatal("delete one")
	}
	if n, _ := h.Delete("a1", ".env", 0); n != 1 {
		t.Fatal("clear")
	}
	if v, _ := h.List("a1", ".env.production"); len(v) != 1 {
		t.Fatal("other file touched")
	}
}
