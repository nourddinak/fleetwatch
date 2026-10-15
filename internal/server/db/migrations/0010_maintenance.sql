-- Maintenance windows: alerts for the server (and its apps) or the app are paused
-- until this time (unix ms); 0 = not in maintenance.
ALTER TABLE servers ADD COLUMN maintenance_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN maintenance_until INTEGER NOT NULL DEFAULT 0;
