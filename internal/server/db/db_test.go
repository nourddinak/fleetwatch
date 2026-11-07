package db

import (
	"path/filepath"
	"testing"
)

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	d.Close()

	// Re-opening must not re-run migrations.
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no migrations recorded")
	}
	if _, err := d.Exec(`INSERT INTO admin (id, username, password_hash, password_changed_at) VALUES (2, 'x', 'h', 0)`); err == nil {
		t.Fatal("admin table accepted a second row")
	}
}

// A database that ran the reverted email feature's 0045 (email_* tables with other
// columns) must still get the Emails page's own tables, and keep the old ones.
func TestMailTablesAfterRevertedEmail0045(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DELETE FROM schema_migrations WHERE version >= 46`,
		`DROP TABLE mail_fts`, `DROP TABLE mail_message_labels`, `DROP TABLE mail_attachments`, `DROP TABLE mail_outbox`, `DROP TABLE mail_messages`,
		`DROP TABLE mail_threads`, `DROP TABLE mail_drafts`, `DROP TABLE mail_folders`, `DROP TABLE mail_grants`, `DROP TABLE mail_spam_rules`,
		`DROP TABLE mail_contacts`, `DROP TABLE mail_inbound_failures`, `DROP TABLE mail_addresses`, `DROP TABLE mail_domains`,
		// what the reverted 0045 left behind
		`CREATE TABLE IF NOT EXISTS email_domains (id TEXT PRIMARY KEY, domain TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS email_messages (id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, is_read INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS idx_email_messages_thread ON email_messages(thread_id)`,
		`INSERT INTO email_domains VALUES ('old', 'old.example', 0)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM mail_messages WHERE unread = 1`).Scan(&n); err != nil {
		t.Fatalf("mail tables missing after upgrade: %v", err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM email_domains`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the old tables must be left alone: %d %v", n, err)
	}
}
