package dbtool

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
)

func TestCheckQuery(t *testing.T) {
	for _, ok := range []string{"select 1", "  WITH x AS (select 1) select * from x;", "SHOW TABLES", "explain select 1", "pragma table_info(users)"} {
		if _, err := checkQuery(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "delete from users", "select 1; drop table users", "update x set a=1", "select * from t into outfile '/tmp/x'", "  insert into t values (1)"} {
		if _, err := checkQuery(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParsers(t *testing.T) {
	cols, rows, cut, err := fromTabs("id\tname\n1\tAda" + `\t` + "Love\n2\tNULL\n")
	if err != nil || len(cols) != 2 || len(rows) != 2 || *rows[0][1] != "Ada\tLove" || rows[1][1] != nil || cut {
		t.Fatal(cols, rows, err)
	}
	cols, rows, _, err = fromCSV("id,name\n1,\"a,b\"\n")
	if err != nil || cols[1] != "name" || *rows[0][1] != "a,b" {
		t.Fatal(cols, rows, err)
	}
}

func TestSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, note TEXT)`)
	for i := range 600 {
		db.Exec(`INSERT INTO users (name) VALUES (?)`, "u"+strings.Repeat("x", i%3))
	}
	db.Close()
	ctx := context.Background()
	res := Handle(ctx, proto.DBRequest{RunID: "1", Op: "tables", Engine: "sqlite", Database: path})
	if res.Error != "" || len(res.Tables) != 1 || res.Tables[0].Name != "users" || res.Tables[0].Rows != 600 {
		t.Fatalf("%+v", res)
	}
	res = Handle(ctx, proto.DBRequest{RunID: "2", Op: "query", Engine: "sqlite", Database: path, Query: "select id, name, note from users"})
	if res.Error != "" || len(res.Rows) != maxRows || !res.Truncated || res.Columns[1] != "name" || res.Rows[0][2] != nil || *res.Rows[0][1] != "u" {
		t.Fatalf("%+v", res.Error)
	}
	// Writing is refused even past the keyword check (the file is opened read-only).
	if _, _, _, err := sqliteQuery(ctx, path, "delete from users"); err == nil {
		t.Fatal("a write went through")
	}
	if res := Handle(ctx, proto.DBRequest{RunID: "3", Op: "query", Engine: "sqlite", Database: "relative.db", Query: "select 1"}); res.Error == "" {
		t.Fatal("a relative path")
	}
	if res := Handle(ctx, proto.DBRequest{RunID: "4", Op: "create", Engine: "postgres", Database: "Bad-Name", User: "x", Password: "abcdefghijklmnop"}); res.Error == "" {
		t.Fatal("a bad name")
	}
}
