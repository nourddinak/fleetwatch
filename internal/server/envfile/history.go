package envfile

import (
	"database/sql"
	"strings"
	"time"

	"fleetwatch/internal/server/secret"
)

// keep is how many versions of one file are kept; older ones go first.
const keep = 100

// OutsideFleetwatch is the author of a version found on the server before a save.
const OutsideFleetwatch = "on the server"

// Version is one saved state of an .env file.
type Version struct {
	ID      int64  `json:"id"`
	By      string `json:"by"`
	TS      int64  `json:"ts"`
	Content string `json:"content"`
}

// History keeps every version of files saved from the dashboard, encrypted: apps'
// .env files and Caddyfiles, or servers' Caddyfiles.
type History struct {
	db         *sql.DB
	box        *secret.Box
	table, col string // env_history/app_id or server_file_history/server_id
	kind       string // part of the encryption's associated data
}

func NewHistory(d *sql.DB, box *secret.Box) *History {
	return &History{db: d, box: box, table: "env_history", col: "app_id", kind: "env"}
}

// NewServerHistory keeps the files of servers themselves (their Caddyfile).
func NewServerHistory(d *sql.DB, box *secret.Box) *History {
	return &History{db: d, box: box, table: "server_file_history", col: "server_id", kind: "server"}
}

func (h *History) ad(owner, name string) string { return h.kind + ":" + owner + "/" + name }

// q fills in the table and column names (constants, never input).
func (h *History) q(s string) string {
	return strings.NewReplacer("{t}", h.table, "{c}", h.col).Replace(s)
}

// Add stores a version unless it equals the newest one.
func (h *History) Add(appID, name, by, content string) error {
	if v, err := h.List(appID, name); err == nil && len(v) > 0 && v[0].Content == content {
		return nil
	}
	if _, err := h.db.Exec(h.q(`INSERT INTO {t} ({c}, name, by, ts, content_enc) VALUES (?, ?, ?, ?, ?)`),
		appID, name, by, time.Now().UnixMilli(), h.box.Seal([]byte(content), h.ad(appID, name))); err != nil {
		return err
	}
	_, err := h.db.Exec(h.q(`DELETE FROM {t} WHERE {c} = ? AND name = ? AND id NOT IN
		(SELECT id FROM {t} WHERE {c} = ? AND name = ? ORDER BY id DESC LIMIT ?)`), appID, name, appID, name, keep)
	return err
}

// List returns a file's versions, newest first.
func (h *History) List(appID, name string) ([]Version, error) {
	rows, err := h.db.Query(h.q(`SELECT id, by, ts, content_enc FROM {t} WHERE {c} = ? AND name = ? ORDER BY id DESC`), appID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var v Version
		var enc []byte
		if err := rows.Scan(&v.ID, &v.By, &v.TS, &enc); err != nil {
			return nil, err
		}
		if b, err := h.box.Open(enc, h.ad(appID, name)); err == nil {
			v.Content = string(b)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Delete removes one version (id > 0) or all of a file's versions (id 0).
func (h *History) Delete(appID, name string, id int64) (int64, error) {
	q, args := h.q(`DELETE FROM {t} WHERE {c} = ? AND name = ?`), []any{appID, name}
	if id > 0 {
		q, args = q+` AND id = ?`, append(args, id)
	}
	r, err := h.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
