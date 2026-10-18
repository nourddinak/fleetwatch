package envfile

import (
	"database/sql"
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

// History keeps every version of the .env files, encrypted.
type History struct {
	db  *sql.DB
	box *secret.Box
}

func NewHistory(d *sql.DB, box *secret.Box) *History { return &History{db: d, box: box} }

func ad(appID, name string) string { return "env:" + appID + "/" + name }

// Add stores a version unless it equals the newest one.
func (h *History) Add(appID, name, by, content string) error {
	if v, err := h.List(appID, name); err == nil && len(v) > 0 && v[0].Content == content {
		return nil
	}
	if _, err := h.db.Exec(`INSERT INTO env_history (app_id, name, by, ts, content_enc) VALUES (?, ?, ?, ?, ?)`,
		appID, name, by, time.Now().UnixMilli(), h.box.Seal([]byte(content), ad(appID, name))); err != nil {
		return err
	}
	_, err := h.db.Exec(`DELETE FROM env_history WHERE app_id = ? AND name = ? AND id NOT IN
		(SELECT id FROM env_history WHERE app_id = ? AND name = ? ORDER BY id DESC LIMIT ?)`, appID, name, appID, name, keep)
	return err
}

// List returns a file's versions, newest first.
func (h *History) List(appID, name string) ([]Version, error) {
	rows, err := h.db.Query(`SELECT id, by, ts, content_enc FROM env_history WHERE app_id = ? AND name = ? ORDER BY id DESC`, appID, name)
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
		if b, err := h.box.Open(enc, ad(appID, name)); err == nil {
			v.Content = string(b)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Delete removes one version (id > 0) or all of a file's versions (id 0).
func (h *History) Delete(appID, name string, id int64) (int64, error) {
	q, args := `DELETE FROM env_history WHERE app_id = ? AND name = ?`, []any{appID, name}
	if id > 0 {
		q, args = q+` AND id = ?`, append(args, id)
	}
	r, err := h.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
