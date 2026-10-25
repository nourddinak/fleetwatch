-- Clients: members who only see a portal of their own apps, uptime checks and status
-- pages (JSON: {"apps": [...], "checks": [...], "pages": [...]}).
ALTER TABLE members ADD COLUMN portal TEXT NOT NULL DEFAULT '{}';
