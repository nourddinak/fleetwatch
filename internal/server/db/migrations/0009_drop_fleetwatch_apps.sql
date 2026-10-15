-- Fleetwatch's own services (the dashboard, its update helper, the agent) are not
-- applications. Discovery offered ops-server on the VPS that runs the dashboard.
DELETE FROM apps WHERE manager = 'systemd' AND (process_key LIKE 'ops-server%' OR process_key LIKE 'ops-agent%');
DELETE FROM discoveries WHERE process_key LIKE 'ops-server%' OR process_key LIKE 'ops-agent%';
