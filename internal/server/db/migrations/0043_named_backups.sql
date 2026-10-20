-- Backups have names now, several per server: runs and copies belong to a backup.
ALTER TABLE backup_runs ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
DROP TABLE backup_snapshots; -- listed again on the next run or refresh
CREATE TABLE backup_copies (
    plan_id   TEXT    PRIMARY KEY,
    server_id TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    data      TEXT    NOT NULL,
    at        INTEGER NOT NULL
);
