-- Who logged in: '' = a person, 'github' = a GitHub Actions runner.
ALTER TABLE ssh_logins ADD COLUMN source TEXT NOT NULL DEFAULT '';
