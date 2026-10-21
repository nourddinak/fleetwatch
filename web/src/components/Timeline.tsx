import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { AlertOctagon, DatabaseBackup, History, Pencil, RefreshCw, Rocket, Activity } from "lucide-react";
import { api, type ApiError } from "../api/client";
import { Badge, Button, Card, EmptyState, ErrorState, LoadingState, SectionTitle, Segmented } from "./ui";
import { cn } from "../utils/cn";

interface Moment {
  ts: number;
  kind: "deploy" | "change" | "incident" | "status" | "backup" | "event";
  tone?: "ok" | "warn" | "crit";
  text: string;
  who?: string;
  link?: string;
}

const ICON = { deploy: Rocket, change: Pencil, incident: AlertOctagon, status: Activity, backup: DatabaseBackup, event: History };
const TONE = { ok: "bg-ok/15 text-ok", warn: "bg-warn/15 text-warn", crit: "bg-crit/15 text-crit" };
const FILTERS = [
  { value: "all", label: "Everything" },
  { value: "deploy", label: "Deploys" },
  { value: "change", label: "Changes" },
  { value: "incident", label: "Incidents" },
  { value: "status", label: "Status" },
] as const;

function day(ts: number) {
  const d = new Date(ts), now = new Date();
  const same = (a: Date, b: Date) => a.toDateString() === b.toDateString();
  if (same(d, now)) return "Today";
  if (same(d, new Date(now.getTime() - 86400000))) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long", year: d.getFullYear() === now.getFullYear() ? undefined : "numeric" });
}

/** Everything that happened to one app, site or server, newest first: what broke right
 * after what. query is "app=<id>", "site=<id>" or "server=<id>". */
export function Timeline({ query }: { query: string }) {
  const [items, setItems] = useState<Moment[] | null>(null);
  const [more, setMore] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [filter, setFilter] = useState<(typeof FILTERS)[number]["value"]>("all");
  const load = async (before?: number) => {
    setBusy(true);
    try {
      const r = await api<{ items: Moment[]; more: boolean }>("GET", `/api/timeline?${query}${before ? `&before=${before}` : ""}`);
      setItems((old) => (before && old ? [...old, ...r.items] : r.items));
      setMore(r.more);
      setError("");
    } catch (e) {
      setError((e as ApiError).message);
    }
    setBusy(false);
  };
  useEffect(() => {
    setItems(null);
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);
  if (error) return <ErrorState message={error} onRetry={() => load()} />;
  if (!items) return <LoadingState />;
  const shown = items.filter((m) => filter === "all" || m.kind === filter || (filter === "change" && m.kind === "backup"));
  let lastDay = "";
  return (
    <Card>
      <SectionTitle icon={<History size={15} />} title="Timeline" sub="Deploys, changes and who made them, incidents and status, newest first: what broke right after what."
        action={<Button size="sm" variant="ghost" disabled={busy} onClick={() => load()}><RefreshCw size={13} /> Refresh</Button>} />
      <div className="mb-4"><Segmented size="sm" value={filter} onChange={setFilter} options={FILTERS.map((f) => ({ value: f.value, label: f.label }))} /></div>
      {shown.length === 0 ? <EmptyState icon={<History size={20} />} title="Nothing yet" desc="Deploys, restarts, edits and incidents show up here as they happen." /> : (
        <ol className="relative">
          {shown.map((m, i) => {
            const d = day(m.ts);
            const header = d !== lastDay;
            lastDay = d;
            const Icon = ICON[m.kind] ?? History;
            const body = (
              <span className="min-w-0 flex-1">
                <span className="text-sm">{m.text}</span>
                {m.who && <Badge className="ml-2">{m.who}</Badge>}
              </span>
            );
            return (
              <li key={i}>
                {header && <div className="mb-2 mt-4 text-xs font-medium uppercase tracking-wide text-muted first:mt-0">{d}</div>}
                <div className="flex items-start gap-3 py-1.5">
                  <span className="w-16 shrink-0 whitespace-nowrap pt-1 text-right font-mono text-[11px] text-muted">{new Date(m.ts).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}</span>
                  <span className={cn("grid h-7 w-7 shrink-0 place-items-center rounded-full", m.tone ? TONE[m.tone] : "bg-surface2 text-muted")}><Icon size={13} /></span>
                  {m.link ? <Link to={m.link} className="flex min-w-0 flex-1 hover:underline">{body}</Link> : body}
                </div>
              </li>
            );
          })}
        </ol>
      )}
      {more && <div className="mt-4 text-center"><Button size="sm" variant="secondary" disabled={busy} onClick={() => load(items[items.length - 1].ts)}>Load older</Button></div>}
    </Card>
  );
}

export function AppTimeline() {
  const { id = "" } = useParams();
  return <Timeline query={`app=${id}`} />;
}

export function ServerTimeline() {
  const { id = "" } = useParams();
  return <Timeline query={`server=${id}`} />;
}
