import { useMemo } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Boxes, Plus } from "lucide-react";
import { useStore } from "../store/store";
import { AppsTable } from "../components/AppsTable";
import { Button, Card, EmptyState, FilterBar, Gate, LoadingState, PageHeader, SearchInput, Select } from "../components/ui";
import { ServerPicker } from "../components/domain";

export function Applications() {
  const { apps, appsLoaded, incidents, openAddApp } = useStore();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const get = (k: string, d = "all") => params.get(k) ?? d;
  const set = (k: string, v: string) => {
    const p = new URLSearchParams(params);
    if (!v || v === "all" || v === "") p.delete(k);
    else p.set(k, v);
    setParams(p, { replace: true });
  };
  const q = get("q", "");
  const server = get("server");
  const manager = get("manager");
  const runtime = get("runtime");
  const status = get("status");
  const inc = get("incidents");
  const domain = get("domain");

  const runtimes = Array.from(new Set(apps.flatMap((a) => (a.runtime ? a.runtime.split(" + ") : [])))).sort();
  const list = useMemo(() => {
    const s = q.trim().toLowerCase();
    const incCount = (id: string) => incidents.filter((i) => i.appId === id && i.status === "active").length;
    return apps
      .filter((a) => {
        if (server !== "all" && a.serverId !== server) return false;
        if (manager !== "all" && a.manager !== manager) return false;
        if (runtime !== "all" && !a.runtime.split(" + ").includes(runtime)) return false; // "Go + React" matches Go and React
        if (status === "down" ? a.status !== "offline" && a.status !== "crash-loop" : status === "attention" ? a.status === "online" && a.healthStatus !== "failed" : status !== "all" && a.status !== status) return false;
        if (inc === "with" && !incCount(a.id)) return false;
        if (inc === "without" && incCount(a.id)) return false;
        if (domain === "with" && !a.domain) return false;
        if (domain === "without" && a.domain) return false;
        if (s && ![a.name, a.path, a.domain ?? "", a.pm2Name, a.unit, ...a.ports.map((p) => String(p.port))].join(" ").toLowerCase().includes(s)) return false;
        return true;
      })
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [apps, incidents, q, server, manager, runtime, status, inc, domain]);

  const clear = () => setParams({}, { replace: true });
  const filtered = [q, server, manager, runtime, status, inc, domain].some((v) => v && v !== "all");

  return (
    <>
      <PageHeader title="Applications" sub={`${apps.length} applications across all servers: PM2, systemd and static sites`} actions={<Button onClick={() => openAddApp()}><Plus size={15} /> Add application</Button>} />
      <Gate>
        <FilterBar>
          <SearchInput value={q} onChange={(v) => set("q", v)} placeholder="Search name, path, domain, port…" className="lg:w-72" />
          <ServerPicker value={server} onChange={(v) => set("server", v)} className="lg:!w-auto" />
          <Select value={manager} onChange={(e) => set("manager", e.target.value)} className="lg:!w-auto">
            <option value="all">All managers</option>
            <option>PM2</option>
            <option>systemd</option>
            <option value="standalone">Standalone</option>
          </Select>
          <Select value={runtime} onChange={(e) => set("runtime", e.target.value)} className="lg:!w-auto">
            <option value="all">All runtimes</option>
            {runtimes.map((r) => <option key={r}>{r}</option>)}
          </Select>
          <Select value={status} onChange={(e) => set("status", e.target.value)} className="lg:!w-auto">
            <option value="all">All statuses</option>
            <option value="attention">Needs attention</option>
            <option value="down">Offline or crash-looping</option>
            {["online", "warning", "starting", "offline", "stopped", "crash-loop", "unknown"].map((s) => <option key={s} value={s}>{s}</option>)}
          </Select>
          <Select value={inc} onChange={(e) => set("incidents", e.target.value)} className="lg:!w-auto">
            <option value="all">Any incidents</option>
            <option value="with">With active incidents</option>
            <option value="without">No incidents</option>
          </Select>
          <Select value={domain} onChange={(e) => set("domain", e.target.value)} className="lg:!w-auto">
            <option value="all">Any domain</option>
            <option value="with">Has domain</option>
            <option value="without">No domain</option>
          </Select>
          {filtered && <Button variant="ghost" size="sm" onClick={clear}>Clear filters</Button>}
          <span className="text-xs text-muted lg:ml-auto">{list.length} of {apps.length}</span>
        </FilterBar>
        {!appsLoaded ? (
          <LoadingState label="Loading applications…" />
        ) : list.length === 0 ? (
          <EmptyState icon={<Boxes size={20} />} title={apps.length === 0 ? "No applications monitored" : "No applications match"} desc={apps.length === 0 ? "Review discoveries or add one manually." : "Try removing a filter."} action={<Button onClick={() => (apps.length === 0 ? nav("/discoveries") : clear())}>{apps.length === 0 ? "Open discoveries" : "Clear filters"}</Button>} />
        ) : (
          <Card pad={false}>
            <AppsTable apps={list} />
          </Card>
        )}
      </Gate>
    </>
  );
}
