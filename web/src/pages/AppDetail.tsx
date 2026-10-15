import { Link, Outlet, useParams } from "react-router-dom";
import { MaintenanceControl } from "../components/Maintenance";
import { FileText, RotateCw } from "lucide-react";
import { useStore } from "../store/store";
import { AppStatus, Badge, Button, EmptyState, Gate, HealthBadge, LinkTabs, LoadingState, PageHeader } from "../components/ui";

export function useApp() {
  const { id } = useParams();
  const { apps } = useStore();
  return apps.find((a) => a.id === id);
}

export function AppDetail() {
  const a = useApp();
  const { servers, incidents, appsLoaded } = useStore();
  if (!a && !appsLoaded) return <LoadingState label="Loading application…" />;
  if (!a) return <EmptyState title="Application not found" desc="It may have been removed from monitoring." action={<Link to="/applications"><Button>Back to applications</Button></Link>} />;
  const s = servers.find((x) => x.id === a.serverId);
  const base = `/applications/${a.id}`;
  const active = incidents.filter((i) => i.appId === a.id && i.status === "active").length;
  return (
    <>
      <PageHeader
        back={{ to: "/applications", label: "All applications" }}
        title={a.name}
        badge={
          <>
            <AppStatus status={a.status} />
            <HealthBadge status={a.healthStatus} latency={a.latency} />
          </>
        }
        sub={
          <span className="flex flex-wrap gap-x-4 gap-y-1">
            <Link to={`/servers/${a.serverId}`} className="underline-offset-2 hover:underline">{s?.name}</Link>
            <span>{a.manager === "PM2" ? `PM2 · ${a.pm2Name}` : a.manager === "systemd" ? `systemd · ${a.unit}` : "standalone"}</span>
            {a.path && <span className="font-mono">{a.path}</span>}
            {a.domain && <span>{a.domain}</span>}
          </span>
        }
        actions={
          <>
            <MaintenanceControl kind="app" id={a.id} serverId={a.serverId} />
            <Link to={`${base}/logs`}><Button size="sm" variant="secondary"><FileText size={14} /> Logs</Button></Link>
            {a.manager !== "standalone" && <Link to={`${base}/commands?run=builtin:restart`}><Button size="sm"><RotateCw size={14} /> Restart</Button></Link>}
          </>
        }
      />
      <LinkTabs
        tabs={[
          { to: base, label: "Overview", end: true },
          { to: `${base}/logs`, label: "Logs" },
          { to: `${base}/metrics`, label: "Metrics" },
          { to: `${base}/commands`, label: "Commands" },
          { to: `${base}/incidents`, label: "Incidents", badge: active ? <Badge tone="crit">{active}</Badge> : undefined },
          { to: `${base}/health`, label: "Health" },
          { to: `${base}/storage`, label: "Storage" },
          { to: `${base}/git`, label: "Git" },
          { to: `${base}/settings`, label: "Settings" },
        ]}
      />
      <Gate>
        <Outlet />
      </Gate>
    </>
  );
}
