import { Link, Outlet, useParams } from "react-router-dom";
import { MaintenanceControl } from "../components/Maintenance";
import { Activity, AlertOctagon, Cog, FileCode2, FileText, Folder, GitBranch, Globe, HardDrive, HeartPulse, History, KeyRound, Layers, LayoutGrid, RotateCw, ScrollText, Server as ServerIcon, Settings, SquareTerminal } from "lucide-react";
import { AppIcon } from "../components/AppsTable";
import { webServers } from "../utils/web";
import { useStore } from "../store/store";
import { AppStatus, Badge, Button, EmptyState, Gate, HealthBadge, LinkTabs, LoadingState, PageHeader } from "../components/ui";

export function useApp() {
  const { id } = useParams();
  const { apps } = useStore();
  return apps.find((a) => a.id === id);
}

export function AppDetail() {
  const a = useApp();
  const { servers, incidents, appsLoaded, me } = useStore();
  if (!a && !appsLoaded) return <LoadingState label="Loading application…" />;
  if (!a) return <EmptyState title="Application not found" desc="It may have been removed from monitoring." action={<Link to="/applications"><Button>Back to applications</Button></Link>} />;
  const s = servers.find((x) => x.id === a.serverId);
  const base = `/applications/${a.id}`;
  const active = incidents.filter((i) => i.appId === a.id && i.status === "active").length;
  return (
    <>
      <PageHeader
        back={{ to: "/applications", label: "All applications" }}
        title={<span className="flex items-center gap-3"><AppIcon a={a} size={40} /><span className="min-w-0 break-words">{a.name}</span></span>}
        badge={
          <>
            <AppStatus status={a.status} />
            <HealthBadge status={a.healthStatus} latency={a.latency} />
          </>
        }
        sub={
          <span className="flex flex-wrap gap-x-4 gap-y-1">
            <Link to={`/servers/${a.serverId}`} className="inline-flex items-center gap-1.5 underline-offset-2 hover:underline"><ServerIcon size={13} />{s?.name}</Link>
            <span className="inline-flex items-center gap-1.5">{a.manager === "PM2" ? <Layers size={13} /> : <Cog size={13} />}{a.manager === "PM2" ? `PM2 · ${a.pm2Name}` : a.manager === "systemd" ? `systemd · ${a.unit}` : "standalone"}</span>
            {a.path && <span className="inline-flex min-w-0 items-center gap-1.5 font-mono"><Folder size={13} className="shrink-0" /><span className="truncate">{a.path}</span></span>}
            {a.domain && <a href={`https://${a.domain}`} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1.5 hover:underline"><Globe size={13} />{a.domain}</a>}
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
          { to: base, label: "Overview", end: true, icon: <LayoutGrid size={14} /> },
          { to: `${base}/logs`, label: "Logs", icon: <ScrollText size={14} /> },
          { to: `${base}/metrics`, label: "Metrics", icon: <Activity size={14} /> },
          { to: `${base}/commands`, label: "Commands", icon: <SquareTerminal size={14} /> },
          { to: `${base}/incidents`, label: "Incidents", icon: <AlertOctagon size={14} />, badge: active ? <Badge tone="crit">{active}</Badge> : undefined },
          { to: `${base}/timeline`, label: "Timeline", icon: <History size={14} /> },
          { to: `${base}/health`, label: "Health", icon: <HeartPulse size={14} /> },
          { to: `${base}/storage`, label: "Storage", icon: <HardDrive size={14} /> },
          { to: `${base}/git`, label: "Git", icon: <GitBranch size={14} /> },
          ...(!me || me.role === "owner" ? [{ to: `${base}/env`, label: ".env", icon: <KeyRound size={14} /> }, ...(!s || webServers(s)?.length !== 0 ? [{ to: `${base}/web`, label: "Web server", icon: <FileCode2 size={14} /> }] : [])] : []),
          { to: `${base}/settings`, label: "Settings", icon: <Settings size={14} /> },
        ]}
      />
      <Gate>
        <Outlet />
      </Gate>
    </>
  );
}
