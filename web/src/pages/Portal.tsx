import { useSearchParams } from "react-router-dom";
import { AlertTriangle, CheckCircle2, ExternalLink, Globe, LogOut, Radio, Server as AppIcon, XCircle } from "lucide-react";
import { useApi } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, EmptyState, ErrorState, LoadingState, SectionTitle, type Tone } from "../components/ui";
import { ThemeToggle } from "../components/Layout";
import { relTime } from "../mock/util";
import { cn } from "../utils/cn";

interface PortalData {
  name: string;
  brand: { title: string; logo?: string; accent?: string };
  apps: { name: string; status: string; health: string; domain?: string; incidents: { title: string; severity: string; since: number }[] }[];
  checks: { name: string; url: string; status: string; uptime24h: number; uptime30d: number; p95: number; days: { day: number; uptime: number }[] | null }[];
  pages: { title: string; url: string; overall: string }[];
}

const UP: Record<string, [Tone, string]> = {
  up: ["ok", "Up"], down: ["crit", "Down"], partial: ["warn", "Partly down"], slow: ["warn", "Slow"], paused: ["neutral", "Paused"], unknown: ["neutral", "Checking…"],
};
const OVERALL: Record<string, [Tone, string]> = {
  operational: ["ok", "All systems working"], degraded: ["warn", "Some slowness"], partial: ["warn", "Partial outage"], major: ["crit", "Major outage"], maintenance: ["info", "Maintenance"],
};
const pct = (v: number) => (v < 0 ? "—" : `${v >= 99.995 ? "100" : v.toFixed(2)}%`);
const appTone = (a: PortalData["apps"][number]): [Tone, string] =>
  a.incidents.length || a.health === "failed" || ["offline", "stopped", "crash-loop", "errored"].includes(a.status) ? ["crit", "Problem"] : a.status === "warning" ? ["warn", "Warning"] : ["ok", "Running"];

/** A client's portal: only their status pages, sites and apps, branded like their status
 * page. Also the owner's preview of it (/portal?member=). */
export function Portal() {
  const { me, logout } = useStore();
  const [params] = useSearchParams();
  const preview = me?.role !== "client" ? params.get("member") : null;
  const data = useApi<PortalData>(`/api/portal${preview ? `?member=${encodeURIComponent(preview)}` : ""}`);
  if (data.error) return <div className="p-6"><ErrorState message={data.error} onRetry={data.reload} /></div>;
  if (!data.data) return <LoadingState />;
  const d = data.data;
  const accent = d.brand.accent || "#16a34a";
  const problems = d.apps.filter((a) => appTone(a)[0] === "crit").length + d.checks.filter((c) => c.status === "down" || c.status === "partial").length;
  return (
    <div className={cn("min-h-screen bg-bg text-fg", preview && "rounded-3xl border border-line")}>
      <header className="border-b border-line" style={{ borderTop: `4px solid ${accent}` }}>
        <div className="mx-auto flex max-w-5xl items-center justify-between gap-4 px-4 py-4">
          <div className="flex min-w-0 items-center gap-3">
            {d.brand.logo ? <img src={d.brand.logo} alt="" className="h-9 max-w-[10rem] object-contain" /> : <span className="grid h-9 w-9 place-items-center rounded-xl text-white" style={{ background: accent }}><Radio size={18} /></span>}
            <div className="min-w-0">
              <div className="truncate font-semibold">{d.brand.title}</div>
              <div className="text-xs text-muted">{d.name}</div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {preview && <Badge tone="info">Preview of {d.name}'s portal</Badge>}
            <ThemeToggle />
            {!preview && <Button size="sm" variant="ghost" onClick={logout}><LogOut size={14} /> Sign out</Button>}
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-5xl space-y-6 px-4 py-6">
        <div className={cn("flex items-center gap-3 rounded-2xl px-5 py-4 text-sm font-medium", problems ? "bg-crit-soft text-crit" : "bg-ok/10 text-ok")}>
          {problems ? <AlertTriangle size={18} /> : <CheckCircle2 size={18} />}
          {problems ? `${problems} thing${problems === 1 ? " needs" : "s need"} attention: we are on it.` : "Everything is working."}
        </div>
        {d.pages.length > 0 && (
          <Card>
            <SectionTitle icon={<Radio size={15} />} title="Status pages" />
            <ul className="divide-y divide-line">
              {d.pages.map((p) => (
                <li key={p.url} className="flex items-center justify-between gap-3 py-2.5">
                  <span className="text-sm font-medium">{p.title}</span>
                  <span className="flex items-center gap-2">
                    <Badge tone={OVERALL[p.overall]?.[0] ?? "neutral"} dot>{OVERALL[p.overall]?.[1] ?? p.overall}</Badge>
                    <a href={p.url} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost"><ExternalLink size={13} /> Open</Button></a>
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        )}
        {d.checks.length > 0 && (
          <Card>
            <SectionTitle icon={<Globe size={15} />} title="Websites" sub="Checked every minute from outside." />
            <ul className="divide-y divide-line">
              {d.checks.map((c) => (
                <li key={c.url + c.name} className="space-y-2 py-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="min-w-0">
                      <div className="text-sm font-medium">{c.name}</div>
                      <div className="truncate text-xs text-muted">{c.url}</div>
                    </div>
                    <div className="flex items-center gap-3 text-xs text-muted">
                      <span>24 h <b className="text-fg">{pct(c.uptime24h)}</b></span>
                      <span>30 days <b className="text-fg">{pct(c.uptime30d)}</b></span>
                      <Badge tone={UP[c.status]?.[0] ?? "neutral"} dot>{UP[c.status]?.[1] ?? c.status}</Badge>
                    </div>
                  </div>
                  {c.days && c.days.length > 0 && (
                    <div className="flex h-6 gap-[2px]" aria-label="Last 30 days">
                      {c.days.map((x) => (
                        <span key={x.day} title={`${new Date(x.day).toLocaleDateString()}: ${pct(x.uptime)}`}
                          className={cn("flex-1 rounded-sm", x.uptime < 0 ? "bg-line" : x.uptime >= 99.9 ? "bg-ok" : x.uptime >= 98 ? "bg-warn" : "bg-crit")} />
                      ))}
                    </div>
                  )}
                </li>
              ))}
            </ul>
          </Card>
        )}
        {d.apps.length > 0 && (
          <Card>
            <SectionTitle icon={<AppIcon size={15} />} title="Apps" />
            <ul className="divide-y divide-line">
              {d.apps.map((a) => {
                const [tone, label] = appTone(a);
                return (
                  <li key={a.name} className="space-y-1.5 py-3">
                    <div className="flex items-center justify-between gap-3">
                      <div className="min-w-0">
                        <div className="text-sm font-medium">{a.name}</div>
                        {a.domain && <div className="text-xs text-muted">{a.domain}</div>}
                      </div>
                      <Badge tone={tone} dot>{label}</Badge>
                    </div>
                    {a.incidents.map((x, i) => (
                      <div key={i} className="flex items-center gap-2 text-xs text-crit"><XCircle size={12} />{x.title} · since {relTime(x.since)}</div>
                    ))}
                  </li>
                );
              })}
            </ul>
          </Card>
        )}
        {d.pages.length + d.checks.length + d.apps.length === 0 && <EmptyState icon={<Globe size={20} />} title="Nothing to show yet" desc="Your provider has not shared anything here yet." />}
        <p className="text-center text-xs text-muted">Updated {relTime(Date.now())} · refresh the page for the latest</p>
      </main>
    </div>
  );
}
