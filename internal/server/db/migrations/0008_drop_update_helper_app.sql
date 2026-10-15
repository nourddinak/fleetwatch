-- ops-server-update.service is Fleetwatch's own one-shot update helper (v0.9.3+), not
-- an application; before v0.9.4 discovery offered it. Its data cascades with the app.
DELETE FROM apps WHERE manager = 'systemd' AND process_key = 'ops-server-update.service';
DELETE FROM discoveries WHERE process_key = 'ops-server-update.service';
