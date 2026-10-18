import { useState } from "react";
import { Link } from "react-router-dom";
import { Activity, Cpu, FolderTree, HardDrive, LineChart, MemoryStick, Server as ServerIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useStore } from "../store/store";
import { cn } from "../utils/cn";
import { metricMeta, SERVER_METRICS } from "../mock/metrics";
import { toSeries, useApi, type MetricKey, type MetricPoint } from "../api/client";
import type { Metric, Range } from "../mock/types";
import { fmtMB } from "../mock/util";
import { Bar, Card, EmptyState, ErrorState, Gate, LoadingState, PageHeader, SectionTitle, Select, TimeRangeSelector, WithIcon } from "../components/ui";
import { ServerPicker } from "../components/domain";
import { MetricChart, Stats } from "../components/MetricChart";

function Ranking({ title, icon, rows, max, empty }: { title: string; icon: ReactNode; rows: { id: string; name: string; sub?: string; v: number; label: string; to: string }[]; max: number; empty: string }) {
  return (
    <Card>
      <SectionTitle icon={icon} title={title} />
      {rows.length === 0 ? (
        <p className="text-sm text-muted">{empty}</p>
      ) : (
        <ul className="space-y-3">
          {rows.map((r, i) => (
            <li key={r.id}>
              <Link to={r.to} className="block">
                <div className="mb-1 flex items-baseline justify-between gap-3 text-sm">
                  <span className="flex min-w-0 items-center gap-2"><span className={cn("grid h-5 w-5 shrink-0 place-items-center rounded-full text-[10px] font-semibold", i === 0 ? "bg-accent text-accent-fg" : "bg-surface2 text-muted")}>{i + 1}</span><span className="truncate"><span className="font-medium hover:underline">{r.name}</span>{r.sub && <span className="ml-2 text-xs text-muted">{r.sub}</span>}</span></span>
                  <span className="shrink-0 tabular-nums">{r.label}</span>
                </div>
                <Bar value={(r.v / (max || 1)) * 100} tone="accent" />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function MetricsExplorer() {
  const { servers, apps } = useStore();
  const [server, setServer] = useState("all");
  const [metric, setMetric] = useState<Metric>("cpu");
  const [range, setRange] = useState<Range>("24h");
  const { data, error, loading, reload } = useApi<MetricPoint[]>(`/api/metrics?server=${server === "all" ? "" : server}&range=${range}`);
  const meta = metricMeta(metric);
  const points = toSeries(data, metric as MetricKey);
  const scope = server === "all" ? `All servers${servers.length > 1 ? " (average)" : ""}` : servers.find((s) => s.id === server)?.name ?? "—";
  const sn = (id: string) => servers.find((s) => s.id === id)?.name;
  const top = <T,>(arr: T[], f: (x: T) => number) => [...arr].sort((a, b) => f(b) - f(a)).slice(0, 5);

  const live = servers.filter((s) => s.status !== "pending");
  const cpu = top(live, (s) => s.cpu);
  const ram = top(live, (s) => s.ram);
  const disk = top(live, (s) => s.disk);
  const size = top(apps.filter((a) => a.folderMB > 0), (a) => a.folderMB);
  const running = apps.filter((a) => a.pid);
  const appCpu = top(running, (a) => a.cpu);
  const appRam = top(running, (a) => a.ram);

  return (
    <>
      <PageHeader title="Metrics" sub="Infrastructure-wide metrics explorer" />
      <Gate>
        <Card className="mb-6">
          <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <WithIcon icon={<ServerIcon size={15} />}><ServerPicker value={server} onChange={setServer} className="pl-10" /></WithIcon>
            <WithIcon icon={<Activity size={15} />}>
              <Select value={metric} onChange={(e) => setMetric(e.target.value as Metric)} className="pl-10">
                {SERVER_METRICS.map((m) => <option key={m} value={m}>{metricMeta(m).label}</option>)}
              </Select>
            </WithIcon>
          </div>
          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-start gap-3">
              <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl border border-line bg-surface2 text-fg"><LineChart size={16} /></span>
              <div>
                <h3 className="font-semibold">{meta.label} <span className="font-normal text-muted">· {scope}</span></h3>
                <Stats data={points} unit={meta.unit} decimals={1} />
              </div>
            </div>
            <TimeRangeSelector value={range} onChange={setRange} />
          </div>
          {error ? (
            <ErrorState message={error} onRetry={reload} />
          ) : loading && !data ? (
            <LoadingState label="Querying time series…" />
          ) : points.length === 0 ? (
            <EmptyState title="No data in this range" desc={servers.length ? "Data appears a minute after an agent connects." : "Add a server to start collecting metrics."} />
          ) : (
            <MetricChart data={points} unit={meta.unit} kind="area" max={meta.max} height={260} decimals={1} />
          )}
        </Card>
        <div className="grid gap-6 md:grid-cols-2 xl:grid-cols-3">
          <Ranking icon={<Cpu size={15} />} title="Servers with highest CPU" max={100} empty="No connected servers." rows={cpu.map((s) => ({ id: s.id, name: s.name, sub: s.provider, v: s.cpu, label: `${s.cpu.toFixed(1)}%`, to: `/servers/${s.id}/metrics` }))} />
          <Ranking icon={<MemoryStick size={15} />} title="Servers with highest RAM" max={100} empty="No connected servers." rows={ram.map((s) => ({ id: s.id, name: s.name, sub: s.provider, v: s.ram, label: `${s.ram.toFixed(1)}%`, to: `/servers/${s.id}/metrics` }))} />
          <Ranking icon={<HardDrive size={15} />} title="Servers with highest disk usage" max={100} empty="No connected servers." rows={disk.map((s) => ({ id: s.id, name: s.name, sub: s.provider, v: s.disk, label: `${s.disk.toFixed(1)}%`, to: `/servers/${s.id}/storage` }))} />
          <Ranking icon={<Cpu size={15} />} title="Highest CPU apps" max={Math.max(100, appCpu[0]?.cpu ?? 0)} empty="No running applications monitored." rows={appCpu.map((a) => ({ id: a.id, name: a.name, sub: sn(a.serverId), v: a.cpu, label: `${a.cpu.toFixed(1)}%`, to: `/applications/${a.id}/metrics` }))} />
          <Ranking icon={<MemoryStick size={15} />} title="Highest RAM apps" max={appRam[0]?.ram ?? 1} empty="No running applications monitored." rows={appRam.map((a) => ({ id: a.id, name: a.name, sub: sn(a.serverId), v: a.ram, label: fmtMB(a.ram), to: `/applications/${a.id}/metrics` }))} />
          <Ranking icon={<FolderTree size={15} />} title="Largest project folders" max={size[0]?.folderMB} empty="Measured once applications are discovered." rows={size.map((a) => ({ id: a.id, name: a.name, sub: sn(a.serverId), v: a.folderMB, label: fmtMB(a.folderMB), to: `/applications/${a.id}/storage` }))} />
        </div>
      </Gate>
    </>
  );
}
