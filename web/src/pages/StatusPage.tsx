import { useCallback, useEffect, useMemo, useState, type CSSProperties, type FormEvent } from "react";
import { AlertTriangle, Bell, CheckCircle2, Info, Lock, Wrench, XCircle } from "lucide-react";
import { cn } from "../utils/cn";
import { countryName } from "../utils/regions";

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
  latency?: { t: number; ms: number }[];
  places?: string[];
}

/** "Checked from 3 places: Germany, United States": where an uptime check runs. */
function checkedFrom(places: string[]) {
  const names = [...new Set(places.filter(Boolean).map((c) => countryName(c)))];
  const n = places.length;
  return `Checked from ${n} place${n === 1 ? "" : "s"}${names.length ? `: ${names.join(", ")}` : ""}`;
}
type Stage = "investigating" | "identified" | "monitoring" | "resolved" | "update";
interface Notice {
  title: string;
  body: string;
  status: "info" | "maintenance" | "degraded" | "outage" | "resolved";
  start: number;
  end?: number;
  updates?: { at: number; stage: Stage; body: string }[]; // newest first
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
  subscribe?: string[];
  customCss?: string;
  headerHtml?: string;
}
interface LockInfo {
  code: "locked" | "team";
  error: string;
  title: string;
  logo?: string;
  accent: string;
  theme: PublicStatus["theme"];
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
const STAGE: Record<Stage, string> = { investigating: "Investigating", identified: "Identified", monitoring: "Monitoring", resolved: "Resolved", update: "Update" };

const when = (t: number) => new Date(t).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });

/** The admin's header HTML without anything that could run code or send data. */
function cleanHTML(raw: string): string {
  const doc = new DOMParser().parseFromString(raw, "text/html");
  doc.querySelectorAll("script,style,iframe,frame,frameset,object,embed,link,meta,base,form,input,button,textarea,select,svg,math,template,noscript,audio,video").forEach((e) => e.remove());
  doc.body.querySelectorAll("*").forEach((el) => {
    for (const a of [...el.attributes]) {
      const name = a.name.toLowerCase();
      const url = ["href", "src", "xlink:href", "action", "formaction", "srcset", "background", "poster"].includes(name);
      if (name.startsWith("on") || (url && !/^(https?:|mailto:|tel:|#|\/(?!\/))/i.test(a.value.trim()))) el.removeAttribute(a.name);
    }
    if (el.tagName === "A") el.setAttribute("rel", "noopener noreferrer");
  });
  return doc.body.innerHTML;
}

export function StatusView({ data, slug, preview }: { data: PublicStatus; slug?: string; preview?: boolean }) {
  const [label, Icon, cls] = OVERALL[data.overall];
  const [subscribing, setSubscribing] = useState(false);
  const header = useMemo(() => (data.headerHtml ? cleanHTML(data.headerHtml) : ""), [data.headerHtml]);
  const ways = data.subscribe ?? [];
  return (
    <div style={{ "--sp": data.accent, "--sp-fg": onColor(data.accent) } as CSSProperties} className="sp-page mx-auto flex min-h-screen max-w-3xl flex-col px-4 py-8 sm:py-12">
      {data.customCss && <style>{data.customCss}</style>}
      {header && <div className="sp-custom-header mb-6" dangerouslySetInnerHTML={{ __html: header }} />}
      <header className="sp-header mb-8 flex items-center justify-between gap-4">
        <div className="flex min-w-0 items-center gap-3">
          {data.logo && <img src={data.logo} alt="" className="h-10 w-auto max-w-[160px] object-contain" />}
          <h1 className="truncate text-xl font-semibold tracking-tight sm:text-2xl">{data.title}</h1>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {ways.length > 0 && (
            <button onClick={() => setSubscribing((x) => !x)} className="flex items-center gap-1.5 rounded-full bg-[var(--sp)] px-4 py-2 text-sm font-medium text-[var(--sp-fg)] hover:opacity-90">
              <Bell size={14} /> Get updates
            </button>
          )}
          {data.homeUrl && (
            <a href={data.homeUrl} rel="noopener" className="rounded-full border border-line px-4 py-2 text-sm hover:bg-surface2">
              {data.homeLabel || "Website"}
            </a>
          )}
        </div>
      </header>
      {subscribing && <Subscribe ways={ways} slug={slug} preview={preview} />}

      <div className={cn("sp-banner flex items-center gap-3 rounded-2xl px-5 py-4 text-base font-medium", cls)}>
        <Icon size={22} className="shrink-0" />
        {label}
      </div>
      {data.description && <p className="mt-4 whitespace-pre-wrap text-sm text-muted">{data.description}</p>}

      {data.notices.length > 0 && (
        <section className="mt-8 space-y-3">
          {data.notices.map((n, i) => (
            <article key={i} className="sp-notice rounded-2xl border border-line bg-surface p-5">
              <div className="flex flex-wrap items-center gap-2">
                <span className={cn("rounded-full px-2.5 py-0.5 text-xs font-medium", NOTICE[n.status][1])}>{n.start > Date.now() ? "Scheduled" : NOTICE[n.status][0]}</span>
                <h2 className="font-semibold">{n.title}</h2>
              </div>
              <p className="mt-1 text-xs text-muted">
                {when(n.start)}
                {n.end ? ` – ${when(n.end)}` : ""}
              </p>
              {(n.updates?.length ?? 0) > 0 && (
                <ol className="mt-3 space-y-3 border-l-2 border-line pl-4">
                  {n.updates!.map((u, j) => (
                    <li key={j} className="text-sm">
                      <span className="font-semibold">{STAGE[u.stage]}</span> <span className="text-xs text-muted">{when(u.at)}</span>
                      <p className="whitespace-pre-wrap">{u.body}</p>
                    </li>
                  ))}
                </ol>
              )}
              {n.body && <p className="mt-2 whitespace-pre-wrap text-sm">{n.body}</p>}
            </article>
          ))}
        </section>
      )}

      <main className="mt-8 space-y-6">
        {data.groups.map((g, gi) => (
          <section key={gi} className="sp-group rounded-2xl border border-line bg-surface">
            {g.name && <h2 className="border-b border-line px-5 py-3 font-semibold">{g.name}</h2>}
            <ul className="divide-y divide-line">
              {g.components.map((c, ci) => (
                <li key={ci} className="sp-component px-5 py-4">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="font-medium">{c.name}</div>
                      {c.description && <div className="text-xs text-muted">{c.description}</div>}
                      {c.places && c.places.length > 0 && <div className="sp-places text-xs text-muted">{checkedFrom(c.places)}</div>}
                    </div>
                    <span className={cn("shrink-0 text-sm font-medium", STATE[c.status][1])}>{STATE[c.status][0]}</span>
                  </div>
                  {c.days && <History days={c.days} uptime={c.uptime} />}
                  {c.latency && c.latency.length > 1 && <Latency points={c.latency} />}
                </li>
              ))}
            </ul>
          </section>
        ))}
      </main>

      <footer className="sp-footer mt-auto pt-10 text-center text-xs text-muted">
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
    <div className="sp-bars mt-3">
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

/** Response time of the last 24 hours as a small line. */
function Latency({ points }: { points: { t: number; ms: number }[] }) {
  const end = points[points.length - 1].t;
  const start = end - 24 * 3600_000;
  const top = Math.max(...points.map((p) => p.ms), 1);
  const line = points.map((p) => `${(((p.t - start) / (end - start)) * 100).toFixed(2)},${(38 - (p.ms / top) * 34).toFixed(2)}`).join(" ");
  const avg = Math.round(points.reduce((a, p) => a + p.ms, 0) / points.length);
  return (
    <div className="sp-latency mt-3">
      <svg viewBox="0 0 100 40" preserveAspectRatio="none" className="h-10 w-full" role="img" aria-label={`Average response time ${avg} ms`}>
        <polyline points={line} fill="none" stroke="var(--sp)" strokeWidth="1.5" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
      </svg>
      <div className="mt-1 flex justify-between text-xs text-muted">
        <span>Response time, 24 h</span>
        <span>avg {avg} ms · max {top} ms</span>
      </div>
    </div>
  );
}

function Subscribe({ ways, slug, preview }: { ways: string[]; slug?: string; preview?: boolean }) {
  const [kind, setKind] = useState(ways[0]);
  const [target, setTarget] = useState("");
  const [state, setState] = useState<{ busy?: boolean; ok?: string; err?: string }>({});
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (preview) return setState({ ok: "This is a preview: nothing was sent." });
    setState({ busy: true });
    const r = await fetch("/api/public/subscribe", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ page: slug ?? "", kind, target }) }).catch(() => null);
    if (r?.ok) {
      setTarget("");
      setState({ ok: kind === "email" ? "Almost done: check your inbox and confirm." : "Done: the webhook got a test call and will get updates." });
    } else {
      const j = await r?.json().catch(() => null);
      setState({ err: j?.error ?? "Could not subscribe. Try again later." });
    }
  };
  return (
    <form onSubmit={submit} className="sp-subscribe -mt-4 mb-8 rounded-2xl border border-line bg-surface p-5">
      <div className="mb-3 text-sm font-medium">Get told when something goes down, comes back, or is announced.</div>
      {ways.length > 1 && (
        <div className="mb-3 flex gap-2 text-sm">
          {ways.map((w) => (
            <button type="button" key={w} onClick={() => setKind(w)} className={cn("rounded-full px-3 py-1", kind === w ? "bg-[var(--sp)] text-[var(--sp-fg)]" : "border border-line")}>
              {w === "email" ? "Email" : "Webhook"}
            </button>
          ))}
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <input
          required
          type={kind === "email" ? "email" : "url"}
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          placeholder={kind === "email" ? "you@example.com" : "https://example.com/hooks/status"}
          className="min-w-0 flex-1 rounded-full border border-line bg-surface2 px-4 py-2 text-sm outline-none focus:border-fg/40"
        />
        <button disabled={state.busy} className="rounded-full bg-[var(--sp)] px-5 py-2 text-sm font-medium text-[var(--sp-fg)] disabled:opacity-60">Subscribe</button>
      </div>
      {state.ok && <p className="mt-2 text-sm text-ok">{state.ok}</p>}
      {state.err && <p className="mt-2 text-sm text-crit">{state.err}</p>}
      {kind === "webhook" && <p className="mt-2 text-xs text-muted">We send JSON with a POST; your URL must answer with 2xx.</p>}
    </form>
  );
}

function Locked({ info, slug, onUnlock }: { info: LockInfo; slug?: string; onUnlock: () => void }) {
  const [pw, setPw] = useState("");
  const [err, setErr] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const r = await fetch("/api/public/unlock", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ page: slug ?? "", password: pw }) }).catch(() => null);
    if (r?.ok) return onUnlock();
    const j = await r?.json().catch(() => null);
    setErr(j?.error ?? "Could not check the password.");
  };
  return (
    <div style={{ "--sp": info.accent, "--sp-fg": onColor(info.accent) } as CSSProperties} className="flex min-h-screen items-center justify-center p-4">
      <div className="w-full max-w-sm rounded-2xl border border-line bg-surface p-6 text-center">
        {info.logo ? <img src={info.logo} alt="" className="mx-auto mb-3 h-10 w-auto max-w-[160px] object-contain" /> : <Lock className="mx-auto mb-3 text-muted" size={22} />}
        <h1 className="text-lg font-semibold">{info.title}</h1>
        <p className="mt-1 text-sm text-muted">{info.error}</p>
        {info.code === "locked" ? (
          <form onSubmit={submit} className="mt-4 space-y-2">
            <input type="password" autoFocus required value={pw} onChange={(e) => setPw(e.target.value)} placeholder="Password" className="w-full rounded-full border border-line bg-surface2 px-4 py-2 text-sm outline-none focus:border-fg/40" />
            <button className="w-full rounded-full bg-[var(--sp)] px-4 py-2 text-sm font-medium text-[var(--sp-fg)]">Open</button>
            {err && <p className="text-sm text-crit">{err}</p>}
          </form>
        ) : (
          location.pathname.startsWith("/status") && (
            <a href="/#/login" className="mt-4 inline-block rounded-full bg-[var(--sp)] px-5 py-2 text-sm font-medium text-[var(--sp-fg)]">Sign in</a>
          )
        )}
      </div>
    </div>
  );
}

export default function StatusPage() {
  const slug = document.getElementById("root")?.dataset.slug;
  const [data, setData] = useState<PublicStatus | null>(null);
  const [locked, setLocked] = useState<LockInfo | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(
    () =>
      fetch("/api/public/status" + (slug ? "/" + encodeURIComponent(slug) : ""))
        .then(async (r) => {
          if (r.status === 401) return setLocked(await r.json());
          if (!r.ok) throw r.status === 404 ? "This status page is not available." : "Could not load the status.";
          setData(await r.json());
          setLocked(null);
          setError("");
        })
        .catch((e) => setError(typeof e === "string" ? e : "Could not load the status.")),
    [slug],
  );
  useEffect(() => {
    load();
    const t = setInterval(load, 60_000);
    return () => clearInterval(t);
  }, [load]);
  const look = data ?? locked;
  useEffect(() => {
    if (!look) return;
    document.title = look.title;
    const dark = look.theme === "dark" || (look.theme === "auto" && window.matchMedia?.("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", !!dark);
    if (look.logo) document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.setAttribute("href", look.logo);
  }, [look]);

  if (locked && !data) return <Locked info={locked} slug={slug} onUnlock={load} />;
  if (!data) {
    return <div className="flex min-h-screen items-center justify-center p-6 text-sm text-muted">{error ? <span className="flex items-center gap-2"><Info size={16} />{error}</span> : "Loading…"}</div>;
  }
  return <StatusView data={data} slug={slug} />;
}
