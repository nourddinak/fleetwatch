import { useEffect } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { useApi } from "../api/client";

interface Row {
  group: string;
  name: string;
  uptime: number | null;
  downMinutes: number;
  outages: number;
}
interface Report {
  page: string;
  month: string;
  from: number;
  to: number;
  rows: Row[];
}
interface Notice {
  id: string;
  title: string;
  status: string;
  start: number;
  end: number;
  draft: boolean;
  updates: { at: number; stage: string; body: string }[];
}
interface Page {
  title: string;
  description: string;
  logo: string;
  accent: string;
  homeUrl: string;
  notices: Notice[];
}

const THEMES = {
  light: { bg: "#ffffff", fg: "#111114", muted: "#6b6b74", line: "#e7e7ea", soft: "#f5f5f7", ok: "#15803d", warn: "#b45309", crit: "#b91c1c" },
  dark: { bg: "#0f0f12", fg: "#f3f3ee", muted: "#9a9aa3", line: "#26262c", soft: "#18181d", ok: "#4ade80", warn: "#fbbf24", crit: "#f87171" },
};

const pct = (v: number | null) => (v === null ? "–" : `${v.toFixed(v >= 99.995 ? 2 : 3)}%`);
const dur = (min: number) => (min < 60 ? `${min} min` : min < 1440 ? `${Math.floor(min / 60)} h ${min % 60} min` : `${Math.floor(min / 1440)} d ${Math.floor((min % 1440) / 60)} h`);

/** A month's uptime for a status page, laid out for A4: printed or saved as PDF for clients. */
export function UptimeReport() {
  const { id = "", month = "" } = useParams();
  const [q, setQ] = useSearchParams();
  const theme = q.get("theme") === "dark" ? "dark" : "light";
  const t = THEMES[theme];
  const report = useApi<Report>(`/api/status-pages/${id}/report?month=${month}`);
  const page = useApi<Page>(`/api/status-pages/${id}`);
  useEffect(() => {
    if (report.data) document.title = `${report.data.page || "Uptime"} – ${monthName(month)}`;
  }, [report.data, month]);
  if (report.error || page.error) {
    return <div style={{ padding: 32, fontFamily: "system-ui" }}>{report.error ?? page.error} <Link to="/login">Sign in</Link></div>;
  }
  if (!report.data || !page.data) return <div style={{ padding: 32, fontFamily: "system-ui" }}>Loading…</div>;
  const r = report.data;
  const p = page.data;
  const accent = p.accent || "#16a34a";
  const known = r.rows.filter((x) => x.uptime !== null);
  const overall = known.length ? known.reduce((a, x) => a + (x.uptime ?? 0), 0) / known.length : null;
  const down = r.rows.reduce((a, x) => a + x.downMinutes, 0);
  const outages = r.rows.reduce((a, x) => a + x.outages, 0);
  const notices = (p.notices ?? []).filter((n) => !n.draft && n.start < r.to && (n.end === 0 || n.end >= r.from)).sort((a, b) => a.start - b.start);
  const tone = (v: number | null) => (v === null ? t.muted : v >= 99.9 ? t.ok : v >= 99 ? t.warn : t.crit);
  const day = (ms: number) => new Date(ms).toLocaleDateString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZone: "UTC" });

  return (
    <div style={{ background: t.bg, color: t.fg, minHeight: "100vh", fontFamily: "Inter, system-ui, sans-serif", printColorAdjust: "exact", WebkitPrintColorAdjust: "exact" }}>
      <style>{`@page { size: A4; margin: 0 } @media print { .no-print { display: none !important } body { background: ${t.bg} } }`}</style>
      <div className="no-print" style={{ position: "sticky", top: 0, display: "flex", gap: 8, justifyContent: "center", padding: 12, background: t.soft, borderBottom: `1px solid ${t.line}` }}>
        <button onClick={() => window.print()} style={btn(accent, "#fff")}>Download PDF</button>
        <button onClick={() => setQ({ theme: theme === "dark" ? "light" : "dark" })} style={btn(t.bg, t.fg, t.line)}>{theme === "dark" ? "Light" : "Dark"} version</button>
        <span style={{ alignSelf: "center", fontSize: 12, color: t.muted }}>In the print window, choose “Save as PDF” and turn on “Background graphics”.</span>
      </div>
      <div style={{ maxWidth: 794, margin: "0 auto", padding: "48px 48px 32px" }}>
        <header style={{ display: "flex", alignItems: "center", gap: 16, borderBottom: `3px solid ${accent}`, paddingBottom: 20 }}>
          {p.logo && <img src={p.logo} alt="" style={{ height: 44, maxWidth: 160, objectFit: "contain" }} />}
          <div style={{ flex: 1 }}>
            <div style={{ fontSize: 12, letterSpacing: 1.2, textTransform: "uppercase", color: t.muted }}>Uptime report</div>
            <div style={{ fontSize: 26, fontWeight: 700 }}>{r.page || p.title}</div>
            <div style={{ fontSize: 14, color: t.muted }}>{monthName(month)} · times in UTC</div>
          </div>
        </header>

        <section style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: 12, margin: "28px 0" }}>
          {[
            ["Overall uptime", pct(overall), tone(overall)],
            ["Downtime", down ? dur(down) : "none", down ? t.warn : t.ok],
            ["Outages", String(outages), outages ? t.warn : t.ok],
          ].map(([k, v, c]) => (
            <div key={k} style={{ background: t.soft, border: `1px solid ${t.line}`, borderRadius: 14, padding: "14px 16px" }}>
              <div style={{ fontSize: 12, color: t.muted }}>{k}</div>
              <div style={{ fontSize: 26, fontWeight: 700, color: c, fontVariantNumeric: "tabular-nums" }}>{v}</div>
            </div>
          ))}
        </section>

        <h2 style={{ fontSize: 15, margin: "0 0 8px" }}>Services</h2>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13 }}>
          <thead>
            <tr style={{ color: t.muted, textAlign: "left" }}>
              <th style={th(t.line)}>Service</th><th style={th(t.line)}>Uptime</th><th style={{ ...th(t.line), textAlign: "right" }}>Downtime</th><th style={{ ...th(t.line), textAlign: "right" }}>Outages</th>
            </tr>
          </thead>
          <tbody>
            {r.rows.length === 0 && <tr><td colSpan={4} style={{ ...td(t.line), color: t.muted }}>No services on this page.</td></tr>}
            {r.rows.map((x, i) => (
              <tr key={i} style={{ breakInside: "avoid" }}>
                <td style={td(t.line)}>{x.group && <span style={{ color: t.muted }}>{x.group} / </span>}{x.name}</td>
                <td style={{ ...td(t.line), width: "38%" }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    <div style={{ flex: 1, height: 6, borderRadius: 3, background: t.line }}>
                      <div style={{ width: `${x.uptime === null ? 0 : Math.max(2, (x.uptime - 90) * 10)}%`, height: 6, borderRadius: 3, background: tone(x.uptime) }} />
                    </div>
                    <span style={{ fontVariantNumeric: "tabular-nums", minWidth: 64, textAlign: "right", color: tone(x.uptime) }}>{pct(x.uptime)}</span>
                  </div>
                </td>
                <td style={{ ...td(t.line), textAlign: "right", color: t.muted }}>{x.downMinutes ? dur(x.downMinutes) : "–"}</td>
                <td style={{ ...td(t.line), textAlign: "right", color: t.muted }}>{x.outages || "–"}</td>
              </tr>
            ))}
          </tbody>
        </table>

        <h2 style={{ fontSize: 15, margin: "28px 0 8px" }}>Notices this month</h2>
        {notices.length === 0 ? (
          <p style={{ fontSize: 13, color: t.muted }}>None.</p>
        ) : (
          notices.map((n) => (
            <div key={n.id} style={{ borderLeft: `3px solid ${n.status === "outage" ? t.crit : n.status === "degraded" ? t.warn : accent}`, padding: "4px 0 4px 12px", margin: "0 0 12px", breakInside: "avoid" }}>
              <div style={{ fontSize: 13, fontWeight: 600 }}>{n.title}</div>
              <div style={{ fontSize: 12, color: t.muted }}>{day(n.start)}{n.end ? ` → ${day(n.end)}` : ""} · {n.status}</div>
              {n.updates.slice(-1).map((u) => <div key={u.at} style={{ fontSize: 12, marginTop: 2 }}>{u.body}</div>)}
            </div>
          ))
        )}

        <footer style={{ marginTop: 40, paddingTop: 12, borderTop: `1px solid ${t.line}`, display: "flex", justifyContent: "space-between", fontSize: 11, color: t.muted }}>
          <span>{p.homeUrl || ""}</span>
          <span>Generated {new Date().toLocaleDateString()} · Fleetwatch</span>
        </footer>
      </div>
    </div>
  );
}

function monthName(m: string) {
  const d = new Date(m + "-01T00:00:00Z");
  return isNaN(d.getTime()) ? m : d.toLocaleDateString(undefined, { month: "long", year: "numeric", timeZone: "UTC" });
}
const btn = (bg: string, fg: string, border?: string) => ({ background: bg, color: fg, border: `1px solid ${border ?? bg}`, borderRadius: 999, padding: "8px 16px", fontSize: 13, fontWeight: 600, cursor: "pointer", whiteSpace: "nowrap" as const });
const th = (line: string) => ({ padding: "8px 6px", borderBottom: `1px solid ${line}`, fontWeight: 500, fontSize: 12 });
const td = (line: string) => ({ padding: "9px 6px", borderBottom: `1px solid ${line}` });
