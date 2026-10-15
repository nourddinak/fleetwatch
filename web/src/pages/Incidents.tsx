import { useMemo, useState } from "react";
import { AlertOctagon } from "lucide-react";
import { useStore } from "../store/store";
import { Button, EmptyState, FilterBar, Gate, MetricCard, PageHeader, SearchInput, Select } from "../components/ui";
import { ApplicationPicker, IncidentCard, IncidentDrawer, ServerPicker } from "../components/domain";

export function Incidents() {
  const { incidents, apps } = useStore();
  const [q, setQ] = useState("");
  const [sev, setSev] = useState("all");
  const [status, setStatus] = useState("all");
  const [server, setServer] = useState("all");
  const [app, setApp] = useState("all");
  const [type, setType] = useState("all");
  const [open, setOpen] = useState<string | null>(null);

  const list = useMemo(() => {
    const s = q.trim().toLowerCase();
    return incidents
      .filter((i) => (sev === "all" || i.severity === sev) && (status === "all" || i.status === status) && (server === "all" || i.serverId === server) && (app === "all" || i.appId === app) && (type === "all" || i.type === type) && (!s || [i.title, i.type, i.id, apps.find((a) => a.id === i.appId)?.name ?? ""].join(" ").toLowerCase().includes(s)))
      .sort((a, b) => {
        const rank = { active: 0, acknowledged: 1, muted: 2, resolved: 3 };
        return rank[a.status] - rank[b.status] || b.lastAt - a.lastAt;
      });
  }, [incidents, apps, q, sev, status, server, app, type]);

  const types = useMemo(() => [...new Set(incidents.map((i) => i.type))].sort(), [incidents]);
  const count = (s: string) => incidents.filter((i) => i.status === s).length;
  const reset = () => { setQ(""); setSev("all"); setStatus("all"); setServer("all"); setApp("all"); setType("all"); };

  return (
    <>
      <PageHeader title="Incidents" sub="Detected problems across servers and applications" />
      <Gate>
        <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4">
          <MetricCard label="Active" value={count("active")} tone={count("active") ? "crit" : "ok"} sub={`${incidents.filter((i) => i.status === "active" && i.severity === "critical").length} critical`} />
          <MetricCard label="Acknowledged" value={count("acknowledged")} tone={count("acknowledged") ? "warn" : undefined} />
          <MetricCard label="Muted" value={count("muted")} />
          <MetricCard label="Resolved" value={count("resolved")} tone="ok" sub="last 90 days" />
        </div>
        <FilterBar>
          <SearchInput value={q} onChange={setQ} placeholder="Search incidents…" className="lg:w-64" />
          <Select value={sev} onChange={(e) => setSev(e.target.value)} className="lg:!w-auto">
            <option value="all">All severities</option>
            <option value="critical">Critical</option>
            <option value="warning">Warning</option>
            <option value="info">Info</option>
          </Select>
          <Select value={status} onChange={(e) => setStatus(e.target.value)} className="lg:!w-auto">
            <option value="all">All statuses</option>
            <option value="active">Active</option>
            <option value="acknowledged">Acknowledged</option>
            <option value="resolved">Resolved</option>
            <option value="muted">Muted</option>
          </Select>
          <ServerPicker value={server} onChange={(v) => { setServer(v); setApp("all"); }} className="lg:!w-auto" />
          <ApplicationPicker value={app} onChange={setApp} serverId={server} className="lg:!w-auto" />
          <Select value={type} onChange={(e) => setType(e.target.value)} className="lg:!w-auto">
            <option value="all">All types</option>
            {types.map((t) => <option key={t}>{t}</option>)}
          </Select>
          <Button variant="ghost" size="sm" onClick={reset}>Reset</Button>
          <span className="text-xs text-muted lg:ml-auto">{list.length} incidents</span>
        </FilterBar>
        {list.length === 0 ? (
          <EmptyState icon={<AlertOctagon size={20} />} title={incidents.length === 0 ? "No incidents — all quiet" : "No incidents match"} desc={incidents.length === 0 ? "Everything is healthy. New problems will show up here." : "Adjust the filters."} action={incidents.length > 0 ? <Button variant="secondary" onClick={reset}>Reset filters</Button> : undefined} />
        ) : (
          <div className="space-y-2.5">{list.map((i) => <IncidentCard key={i.id} incident={i} onOpen={() => setOpen(i.id)} />)}</div>
        )}
        <IncidentDrawer incidentId={open} onClose={() => setOpen(null)} />
      </Gate>
    </>
  );
}
