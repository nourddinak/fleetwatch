import { useEffect, useState, type CSSProperties } from "react";
import { AlertTriangle, CheckCircle2, Info, Wrench, XCircle } from "lucide-react";
import { cn } from "../utils/cn";

// The public status page: no sign-in, no store, no router. Data comes from
// /api/public/status, which holds only what the admin chose to publish.

type State = "operational" | "degraded" | "outage" | "maintenance" | "unknown";
interface Day {
  date: string;
  status: "operational" | "degraded" | "outage" | "nodata";
  downMinutes?: number;
}
interface Component {
  name: string;
  description?: string;
  status: State;
  uptime?: number;
  days?: Day[];
}
interface Notice {
  title: string;
  body: string;
  status: "info" | "maintenance" | "degraded" | "outage" | "resolved";
  start: number;
  end?: number;
}
export interface PublicStatus {
  title: string;
  description?: string;
  logo?: string;
  accent: string;
  theme: "auto" | "light" | "dark";
  homeUrl?: string;
  homeLabel?: string;
  footer?: string;
  poweredBy: boolean;
  historyDays: number;
  overall: "operational" | "degraded" | "partial" | "major" | "maintenance";
  notices: Notice[];
  groups: { name: string; components: Component[] }[];
  updatedAt: number;
}

const OVERALL = {
  operational: ["All systems operational", CheckCircle2, "bg-[var(--sp)] text-[var(--sp-fg)]"],
  degraded: ["Some systems are slow", AlertTriangle, "bg-[#d97706] text-white"],
  partial: ["Some systems are down", XCircle, "bg-[#dc2626] text-white"],
  major: ["Major outage", XCircle, "bg-[#dc2626] text-white"],
  maintenance: ["Maintenance in progress", Wrench, "bg-[#2563eb] text-white"],
} as const;

// Dark text on a light brand colour, white on a dark one.
function onColor(hex: string) {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
  return 0.299 * r + 0.587 * g + 0.114 * b > 170 ? "#0c0c0c" : "#ffffff";
}

const STATE: Record<State, [string, string]> = {
  operational: ["Operational", "text-[var(--sp)]"],
  degraded: ["Degraded", "text-warn"],
  outage: ["Outage", "text-crit"],
  maintenance: ["Maintenance", "text-info"],
  unknown: ["Unknown", "text-muted"],
};

const BAR: Record<Day["status"], string> = { operational: "bg-[var(--sp)]", degraded: "bg-warn", outage: "bg-crit", nodata: "bg-line" };

const NOTICE: Record<Notice["status"], [string, string]> = {
  info: ["Update", "bg-info-soft text-info"],
  maintenance: ["Maintenance", "bg-info-soft text-info"],
  degraded: ["Degraded", "bg-warn-soft text-warn"],
  outage: ["Outage", "bg-crit-soft text-crit"],
  resolved: ["Resolved", "bg-ok-soft text-ok"],
};

const when = (t: number) => new Date(t).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });

export function StatusView({ data }: { data: PublicStatus }) {
  const [label, Icon, cls] = OVERALL[data.overall];
  return (
    <div style={{ "--sp": data.accent, "--sp-fg": onColor(data.accent) } as CSSProperties} className="mx-auto flex min-h-screen max-w-3xl flex-col px-4 py-8 sm:py-12">
      <header className="mb-8 flex items-center justify-between gap-4">
        <div className="flex min-w-0 items-center gap-3">
          {data.logo && <img src={data.logo} alt="" className="h-10 w-auto max-w-[160px] object-contain" />}
          <h1 className="truncate text-xl font-semibold tracking-tight sm:text-2xl">{data.title}</h1>
        </div>
        {data.homeUrl && (
          <a href={data.homeUrl} rel="noopener" className="shrink-0 rounded-full border border-line px-4 py-2 text-sm hover:bg-surface2">
            {data.homeLabel || "Website"}
          </a>
        )}
      </header>

      <div className={cn("flex items-center gap-3 rounded-2xl px-5 py-4 text-base font-medium", cls)}>
        <Icon size={22} className="shrink-0" />
        {label}
      </div>
      {data.description && <p className="mt-4 whitespace-pre-wrap text-sm text-muted">{data.description}</p>}

      {data.notices.length > 0 && (
        <section className="mt-8 space-y-3">
          {data.notices.map((n, i) => (
            <article key={i} className="rounded-2xl border border-line bg-surface p-5">
              <div className="flex flex-wrap items-center gap-2">
                <span className={cn("rounded-full px-2.5 py-0.5 text-xs font-medium", NOTICE[n.status][1])}>{n.start > Date.now() ? "Scheduled" : NOTICE[n.status][0]}</span>
                <h2 className="font-semibold">{n.title}</h2>
              </div>
              {n.body && <p className="mt-2 whitespace-pre-wrap text-sm">{n.body}</p>}
              <p className="mt-3 text-xs text-muted">
                {when(n.start)}
                {n.end ? ` – ${when(n.end)}` : ""}
              </p>
            </article>
          ))}
        </section>
      )}

      <main className="mt-8 space-y-6">
        {data.groups.map((g, gi) => (
          <section key={gi} className="rounded-2xl border border-line bg-surface">
            {g.name && <h2 className="border-b border-line px-5 py-3 font-semibold">{g.name}</h2>}
            <ul className="divide-y divide-line">
              {g.components.map((c, ci) => (
                <li key={ci} className="px-5 py-4">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="font-medium">{c.name}</div>
                      {c.description && <div className="text-xs text-muted">{c.description}</div>}
                    </div>
                    <span className={cn("shrink-0 text-sm font-medium", STATE[c.status][1])}>{STATE[c.status][0]}</span>
                  </div>
                  {c.days && <History days={c.days} uptime={c.uptime} />}
                </li>
              ))}
            </ul>
          </section>
        ))}
      </main>

      <footer className="mt-auto pt-10 text-center text-xs text-muted">
        {data.footer && <p className="mb-2 whitespace-pre-wrap">{data.footer}</p>}
        <p>
          Updated {new Date(data.updatedAt).toLocaleTimeString(undefined, { timeStyle: "short" })}
          {data.poweredBy && (
            <>
              {" · "}Powered by{" "}
              <a href="https://github.com/nourddinak/fleetwatch" rel="noopener" className="underline-offset-2 hover:underline">
                Fleetwatch
              </a>
            </>
          )}
        </p>
      </footer>
    </div>
  );
}

function History({ days, uptime }: { days: Day[]; uptime?: number }) {
  const phone = 30; // older bars hide on narrow screens
  return (
    <div className="mt-3">
      <div className="flex h-8 gap-[2px]">
        {days.map((d, i) => (
          <div
            key={d.date}
            title={`${new Date(d.date + "T00:00:00Z").toLocaleDateString(undefined, { dateStyle: "medium", timeZone: "UTC" })}: ${
              d.status === "nodata" ? "no data" : d.status === "outage" ? `down ${d.downMinutes} min` : d.status
            }`}
            className={cn("flex-1 rounded-sm transition hover:opacity-70", BAR[d.status], i < days.length - phone && "hidden sm:block")}
          />
        ))}
      </div>
      <div className="mt-1.5 flex justify-between text-xs text-muted">
        <span>
          <span className="sm:hidden">{Math.min(phone, days.length)}</span>
          <span className="hidden sm:inline">{days.length}</span> days ago
        </span>
        {uptime !== undefined && <span>{uptime.toFixed(uptime === 100 ? 0 : 2)}% uptime</span>}
        <span>Today</span>
      </div>
    </div>
  );
}

export default function StatusPage() {
  const slug = document.getElementById("root")?.dataset.slug;
  const [data, setData] = useState<PublicStatus | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let stop = false;
    const load = () =>
      fetch("/api/public/status" + (slug ? "/" + encodeURIComponent(slug) : ""))
        .then((r) => (r.ok ? r.json() : Promise.reject(r.status === 404 ? "This status page is not available." : "Could not load the status.")))
        .then((d: PublicStatus) => !stop && (setData(d), setError("")))
        .catch((e) => !stop && setError(typeof e === "string" ? e : "Could not load the status."));
    load();
    const t = setInterval(load, 60_000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [slug]);
  useEffect(() => {
    if (!data) return;
    document.title = data.title;
    const dark = data.theme === "dark" || (data.theme === "auto" && window.matchMedia?.("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", !!dark);
    if (data.logo) document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.setAttribute("href", data.logo);
  }, [data]);

  if (!data) {
    return <div className="flex min-h-screen items-center justify-center p-6 text-sm text-muted">{error ? <span className="flex items-center gap-2"><Info size={16} />{error}</span> : "Loading…"}</div>;
  }
  return <StatusView data={data} />;
}
