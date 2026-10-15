// Package settings stores JSON settings sections in the settings table.
package settings

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// Load fills v from the stored section. v keeps its current (default) values when the
// section was never saved, and for fields added after it was saved.
func Load(d *sql.DB, key string, v any) error {
	var raw string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

// Save replaces the stored section.
func Save(d *sql.DB, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, string(raw))
	return err
}
