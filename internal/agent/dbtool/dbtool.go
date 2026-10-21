// Package dbtool lets the dashboard look into a server's databases: list them and their
// tables, run one read-only query, and create a database with its own user.
//
// "Read-only" is a guard against accidents, not a wall against the owner (who can run
// any command on the server anyway): one statement, starting with a reading keyword,
// in a read-only session (PostgreSQL, MySQL) or a file opened read-only (SQLite).
package dbtool

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite without the sqlite3 tool on the server

	"fleetwatch/internal/agent/backup"
	"fleetwatch/internal/proto"
)

const (
	maxRows = 500
	timeout = 30 * time.Second
)

var (
	nameRe   = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`) // existing databases
	passRe   = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)
	readKw   = regexp.MustCompile(`(?i)^\s*(select|with|show|explain|describe|desc|values|table|pragma)\b`)
	outfile  = regexp.MustCompile(`(?i)\binto\s+(outfile|dumpfile)\b`)
)

func Handle(ctx context.Context, req proto.DBRequest) proto.DBResult {
	start := time.Now()
	res := proto.DBResult{RunID: req.RunID}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var err error
	switch req.Op {
	case "list":
		res.Databases = backup.Find(ctx).Items
		for _, e := range []string{"postgres", "mysql"} {
			if _, err := exec.LookPath(map[string]string{"postgres": "psql", "mysql": "mysql"}[e]); err == nil {
				res.Engines = append(res.Engines, e)
			}
		}
	case "tables":
		res.Tables, err = tables(ctx, req)
	case "query":
		res.Columns, res.Rows, res.Truncated, err = query(ctx, req.Engine, req.Database, req.Query)
	case "create":
		err = create(ctx, req)
	default:
		err = errors.New("unknown database request")
	}
	if err != nil {
		res.Error = err.Error()
	}
	res.TookMs = time.Since(start).Milliseconds()
	return res
}

// checkQuery allows one reading statement.
func checkQuery(q string) (string, error) {
	q = strings.TrimSpace(q)
	q = strings.TrimSpace(strings.TrimRight(q, "; \n\t"))
	switch {
	case q == "":
		return "", errors.New("write a query")
	case len(q) > 20000:
		return "", errors.New("the query is too long")
	case strings.Contains(q, ";"):
		return "", errors.New("one statement at a time (no ; in the middle)")
	case !readKw.MatchString(q):
		return "", errors.New("only reading queries run here: SELECT, WITH, SHOW, EXPLAIN, DESCRIBE")
	case outfile.MatchString(q):
		return "", errors.New("writing files from a query is not allowed here")
	}
	return q, nil
}

func checkDB(engine, db string) error {
	switch engine {
	case "postgres", "mysql":
		if !dbNameRe.MatchString(db) {
			return errors.New("not a database name")
		}
	case "sqlite":
		if !filepath.IsAbs(db) || strings.Contains(db, "..") || strings.ContainsAny(db, "?#") {
			return errors.New("a SQLite database is a full file path")
		}
	default:
		return errors.New("unknown database kind")
	}
	return nil
}

func query(ctx context.Context, engine, db, q string) ([]string, [][]*string, bool, error) {
	if err := checkDB(engine, db); err != nil {
		return nil, nil, false, err
	}
	q, err := checkQuery(q)
	if err != nil {
		return nil, nil, false, err
	}
	switch engine {
	case "postgres":
		out, err := psql(ctx, db, q)
		if err != nil {
			return nil, nil, false, err
		}
		return fromCSV(out)
	case "mysql":
		out, err := mysql(ctx, db, "SET SESSION TRANSACTION READ ONLY; "+q)
		if err != nil {
			return nil, nil, false, err
		}
		return fromTabs(out)
	}
	return sqliteQuery(ctx, db, q)
}

// psql runs one statement as the postgres user, read-only, with a time limit, as CSV.
func psql(ctx context.Context, db, q string) (string, error) {
	return run(ctx, []string{"PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=15000"},
		"runuser", "-u", "postgres", "--", "psql", "-X", "-q", "--csv", "-v", "ON_ERROR_STOP=1", "-d", db, "-c", q)
}

func mysql(ctx context.Context, db, q string) (string, error) {
	args := []string{"-B", "-e", q}
	if db != "" {
		args = append(args, db)
	}
	return run(ctx, []string{"HOME=/root"}, "mysql", args...)
}

func run(ctx context.Context, env []string, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", fmt.Errorf("%s is not installed on this server", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}, env...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &capped{&out, 8 << 20}, &capped{&errOut, 16 << 10}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("the query took too long (30 seconds)")
		}
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return out.String(), nil
}

type capped struct {
	b   *bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); room > 0 {
		c.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func str(s string) *string { return &s }

// fromCSV reads psql's CSV: a header, then rows (an empty unquoted field is NULL in
// psql's CSV only when it differs from "", so NULL and "" both show as empty here).
func fromCSV(out string) ([]string, [][]*string, bool, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err == io.EOF {
		return []string{}, [][]*string{}, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	rows := [][]*string{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, false, err
		}
		if len(rows) == maxRows {
			return head, rows, true, nil
		}
		row := make([]*string, len(rec))
		for i, v := range rec {
			row[i] = str(v)
		}
		rows = append(rows, row)
	}
	return head, rows, false, nil
}

// fromTabs reads mysql -B: tab-separated, \t \n \\ escaped, NULL for null.
func fromTabs(out string) ([]string, [][]*string, bool, error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return []string{}, [][]*string{}, false, nil
	}
	unescape := strings.NewReplacer(`\t`, "\t", `\n`, "\n", `\\`, `\`, `\0`, "\x00")
	head := strings.Split(lines[0], "\t")
	rows := [][]*string{}
	for _, l := range lines[1:] {
		if len(rows) == maxRows {
			return head, rows, true, nil
		}
		f := strings.Split(l, "\t")
		row := make([]*string, len(f))
		for i, v := range f {
			if v != "NULL" {
				row[i] = str(unescape.Replace(v))
			}
		}
		rows = append(rows, row)
	}
	return head, rows, false, nil
}

func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)") // checkDB: no ? or # in path
}

func sqliteQuery(ctx context.Context, path, q string) ([]string, [][]*string, bool, error) {
	db, err := openSQLite(path)
	if err != nil {
		return nil, nil, false, err
	}
	defer db.Close()
	rs, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil, false, err
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	rows := [][]*string{}
	for rs.Next() {
		if len(rows) == maxRows {
			return cols, rows, true, nil
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, false, err
		}
		row := make([]*string, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
			case []byte:
				row[i] = str(string(x))
			default:
				row[i] = str(fmt.Sprint(x))
			}
		}
		rows = append(rows, row)
	}
	return cols, rows, false, rs.Err()
}

func tables(ctx context.Context, req proto.DBRequest) ([]proto.DBTable, error) {
	if err := checkDB(req.Engine, req.Database); err != nil {
		return nil, err
	}
	var out [][]*string
	var err error
	switch req.Engine {
	case "postgres":
		var csvOut string
		csvOut, err = psql(ctx, req.Database, `SELECT CASE WHEN n.nspname = 'public' THEN c.relname ELSE n.nspname || '.' || c.relname END, GREATEST(c.reltuples, 0)::bigint, pg_total_relation_size(c.oid)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p', 'v', 'm') AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' ORDER BY 1`)
		if err == nil {
			_, out, _, err = fromCSV(csvOut)
		}
	case "mysql":
		var tabOut string
		tabOut, err = mysql(ctx, req.Database, "SELECT table_name, COALESCE(table_rows, 0), COALESCE(data_length + index_length, 0) FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY 1")
		if err == nil {
			_, out, _, err = fromTabs(tabOut)
		}
	case "sqlite":
		var names [][]*string
		_, names, _, err = sqliteQuery(ctx, req.Database, "SELECT name FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name")
		if err == nil {
			db, oerr := openSQLite(req.Database)
			if oerr != nil {
				return nil, oerr
			}
			defer db.Close()
			for i, n := range names {
				var count int64
				if i < 200 && n[0] != nil {
					db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+strings.ReplaceAll(*n[0], `"`, `""`)+`"`).Scan(&count)
				}
				out = append(out, []*string{n[0], str(strconv.FormatInt(count, 10)), nil})
			}
		}
	}
	if err != nil {
		return nil, err
	}
	list := []proto.DBTable{}
	for _, r := range out {
		if len(r) < 2 || r[0] == nil {
			continue
		}
		t := proto.DBTable{Name: *r[0]}
		if r[1] != nil {
			t.Rows, _ = strconv.ParseInt(*r[1], 10, 64)
		}
		if len(r) > 2 && r[2] != nil {
			t.Size, _ = strconv.ParseInt(*r[2], 10, 64)
		}
		list = append(list, t)
	}
	return list, nil
}

// create makes a database and a user that owns it (all rights on it, nothing else).
func create(ctx context.Context, req proto.DBRequest) error {
	if !nameRe.MatchString(req.Database) || !nameRe.MatchString(req.User) {
		return errors.New("names are lowercase letters, digits and _, starting with a letter")
	}
	if !passRe.MatchString(req.Password) {
		return errors.New("invalid password")
	}
	switch req.Engine {
	case "postgres":
		_, err := run(ctx, nil, "runuser", "-u", "postgres", "--", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1",
			"-c", fmt.Sprintf(`CREATE USER %q WITH PASSWORD '%s'`, req.User, req.Password),
			"-c", fmt.Sprintf(`CREATE DATABASE %q OWNER %q`, req.Database, req.User))
		return err
	case "mysql":
		_, err := mysql(ctx, "", fmt.Sprintf("CREATE DATABASE `%s`; CREATE USER '%s'@'localhost' IDENTIFIED BY '%s'; GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'; FLUSH PRIVILEGES;",
			req.Database, req.User, req.Password, req.Database, req.User))
		return err
	}
	return errors.New("databases can be created in PostgreSQL or MySQL")
}
