import { useEffect, useState, type ButtonHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type InputHTMLAttributes, type TextareaHTMLAttributes } from "react";
import { Link, NavLink } from "react-router-dom";
import { AlertTriangle, ArrowLeft, Check, Copy, Inbox, Loader2, Search, X } from "lucide-react";
import { cn } from "../utils/cn";
import type { AppStatusT, HealthStatus, IncidentStatus, Range, ServerStatusT, Severity } from "../mock/types";
import { RANGES } from "../mock/metrics";
import { copyText } from "../mock/util";

export type Tone = "ok" | "warn" | "crit" | "info" | "neutral" | "accent";

const toneBg: Record<Tone, string> = {
  ok: "bg-ok-soft text-ok",
  warn: "bg-warn-soft text-warn",
  crit: "bg-crit-soft text-crit",
  info: "bg-info-soft text-info",
  neutral: "bg-neutral-soft text-muted",
  accent: "bg-accent text-accent-fg",
};
export const toneDot: Record<Tone, string> = {
  ok: "bg-ok",
  warn: "bg-warn",
  crit: "bg-crit",
  info: "bg-info",
  neutral: "bg-muted",
  accent: "bg-accent",
};
export const toneText: Record<Tone, string> = { ok: "text-ok", warn: "text-warn", crit: "text-crit", info: "text-info", neutral: "text-muted", accent: "text-fg" };

/* ---------- layout primitives ---------- */
export function Card({ className, children, pad = true, onClick }: { className?: string; children: ReactNode; pad?: boolean; onClick?: () => void }) {
  return (
    <div onClick={onClick} className={cn("rounded-3xl border border-line bg-surface", pad && "p-5", className)}>
      {children}
    </div>
  );
}

export function SectionTitle({ title, sub, action, className }: { title: ReactNode; sub?: ReactNode; action?: ReactNode; className?: string }) {
  return (
    <div className={cn("mb-4 flex flex-wrap items-start justify-between gap-2", className)}>
      <div className="min-w-0">
        <h3 className="text-[15px] font-semibold tracking-tight">{title}</h3>
        {sub && <p className="mt-0.5 text-xs text-muted">{sub}</p>}
      </div>
      {action}
    </div>
  );
}

export function PageHeader({ title, sub, actions, back, badge }: { title: ReactNode; sub?: ReactNode; actions?: ReactNode; back?: { to: string; label: string }; badge?: ReactNode }) {
  return (
    <div className="mb-6">
      {back && (
        <Link to={back.to} className="mb-3 inline-flex items-center gap-1.5 text-xs text-muted hover:text-fg">
          <ArrowLeft size={14} /> {back.label}
        </Link>
      )}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">{title}</h1>
            {badge}
          </div>
          {sub && <p className="mt-1.5 text-sm text-muted">{sub}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </div>
  );
}

/* ---------- buttons ---------- */
type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "ghost" | "danger"; size?: "sm" | "md" };
export function Button({ variant = "primary", size = "md", className, ...p }: BtnProps) {
  const v = {
    primary: "bg-accent text-accent-fg hover:opacity-85",
    secondary: "bg-surface2 text-fg border border-line hover:border-fg/30",
    ghost: "text-muted hover:text-fg hover:bg-surface2",
    danger: "bg-crit-soft text-crit hover:opacity-80",
  }[variant];
  return (
    <button
      {...p}
      className={cn("inline-flex items-center justify-center gap-1.5 rounded-full font-medium transition disabled:cursor-not-allowed disabled:opacity-40", size === "sm" ? "px-3.5 py-1.5 text-xs" : "px-5 py-2.5 text-sm", v, className)}
    />
  );
}

export function IconButton({ className, ...p }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button {...p} className={cn("inline-flex h-9 w-9 items-center justify-center rounded-full border border-line bg-surface text-muted transition hover:text-fg", className)} />;
}

/* ---------- badges ---------- */
export function Badge({ tone = "neutral", children, dot, pulse, className }: { tone?: Tone; children: ReactNode; dot?: boolean; pulse?: boolean; className?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-1 text-[11px] font-medium", toneBg[tone], className)}>
      {dot && <span className={cn("h-1.5 w-1.5 rounded-full", toneDot[tone], pulse && "pulse-dot")} />}
      {children}
    </span>
  );
}

const serverMap: Record<ServerStatusT, [Tone, string]> = { pending: ["neutral", "Waiting for agent"], online: ["ok", "Online"], warning: ["warn", "Warning"], critical: ["crit", "Critical"], disconnected: ["crit", "Disconnected"] };
export const ServerStatus = ({ status }: { status: ServerStatusT }) => (
  <Badge tone={serverMap[status][0]} dot pulse={status === "disconnected"}>
    {serverMap[status][1]}
  </Badge>
);
const appMap: Record<AppStatusT, [Tone, string]> = { online: ["ok", "Online"], offline: ["crit", "Offline"], warning: ["warn", "Warning"], starting: ["info", "Starting"], stopped: ["neutral", "Stopped"], "crash-loop": ["crit", "Crash loop"], unknown: ["neutral", "Unknown"] };
export const appTone = (s: AppStatusT) => appMap[s][0];
export const AppStatus = ({ status }: { status: AppStatusT }) => (
  <Badge tone={appMap[status][0]} dot pulse={status === "starting" || status === "crash-loop"}>
    {appMap[status][1]}
  </Badge>
);
const healthMap: Record<HealthStatus, [Tone, string]> = { healthy: ["ok", "Healthy"], failed: ["crit", "Failed"], unknown: ["neutral", "Unknown"], disabled: ["neutral", "Disabled"] };
export const HealthBadge = ({ status, latency }: { status: HealthStatus; latency?: number | null }) => (
  <Badge tone={healthMap[status][0]} dot>
    {healthMap[status][1]}
    {latency != null && status !== "failed" && <span className="opacity-70">· {latency}ms</span>}
  </Badge>
);
const sevMap: Record<Severity, Tone> = { info: "info", warning: "warn", critical: "crit" };
export const SeverityBadge = ({ severity }: { severity: Severity }) => <Badge tone={sevMap[severity]} className="capitalize">{severity}</Badge>;
const incMap: Record<IncidentStatus, Tone> = { active: "crit", acknowledged: "warn", resolved: "ok", muted: "neutral" };
export const IncidentStatusBadge = ({ status }: { status: IncidentStatus }) => (
  <Badge tone={incMap[status]} dot pulse={status === "active"} className="capitalize">
    {status}
  </Badge>
);

/* ---------- metrics ---------- */
export const levelTone = (v: number, warn = 75, crit = 90): Tone => (v >= crit ? "crit" : v >= warn ? "warn" : "ok");

export function Bar({ value, tone, className }: { value: number; tone?: Tone; className?: string }) {
  const t = tone ?? (value >= 90 ? "crit" : value >= 75 ? "warn" : undefined);
  return (
    <div className={cn("h-1.5 w-full overflow-hidden rounded-full bg-neutral-soft", className)}>
      <div className={cn("h-full rounded-full transition-all duration-700", t ? toneDot[t] : "bg-fg")} style={{ width: `${Math.min(100, Math.max(0, value))}%` }} />
    </div>
  );
}

export function MetricCard({ label, value, unit, sub, tone, to, icon, progress, className }: { label: string; value: ReactNode; unit?: string; sub?: ReactNode; tone?: Tone; to?: string; icon?: ReactNode; progress?: number; className?: string }) {
  const body = (
    <div className={cn("h-full rounded-3xl border border-line bg-surface p-4 transition", to && "hover:border-fg/30", className)}>
      <div className="mb-2 flex items-center justify-between gap-2 text-xs text-muted">
        <span>{label}</span>
        {icon}
      </div>
      <div className="flex items-baseline gap-1">
        <span className={cn("text-2xl font-semibold tracking-tight tabular-nums", tone && toneText[tone])}>{value}</span>
        {unit && <span className="text-sm text-muted">{unit}</span>}
      </div>
      {sub && <div className="mt-1 text-xs text-muted">{sub}</div>}
      {progress !== undefined && <Bar value={progress} className="mt-3" />}
    </div>
  );
  return to ? <Link to={to}>{body}</Link> : body;
}

export function KV({ label, children, mono }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-0.5 py-2">
      <dt className="text-[11px] uppercase tracking-wide text-muted">{label}</dt>
      <dd className={cn("min-w-0 break-words text-sm", mono && "font-mono text-[13px]")}>{children}</dd>
    </div>
  );
}

/* ---------- states ---------- */
export function EmptyState({ title, desc, action, icon }: { title: string; desc?: string; action?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-3xl border border-dashed border-line px-6 py-14 text-center">
      <div className="mb-1 flex h-12 w-12 items-center justify-center rounded-full bg-surface2 text-muted">{icon ?? <Inbox size={20} />}</div>
      <div className="font-medium">{title}</div>
      {desc && <div className="max-w-sm text-sm text-muted">{desc}</div>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  );
}

export function LoadingState({ label = "Loading data…" }: { label?: string }) {
  return (
    <div className="space-y-4" aria-busy>
      <div className="flex items-center gap-2 text-sm text-muted">
        <Loader2 size={16} className="spin" /> {label}
      </div>
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-24 animate-pulse rounded-3xl bg-surface" />
        ))}
      </div>
      <div className="h-64 animate-pulse rounded-3xl bg-surface" />
    </div>
  );
}

export function ErrorState({ message = "Could not reach the central server. The last cached data is unavailable.", onRetry }: { message?: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-3xl border border-crit/30 bg-crit-soft/40 px-6 py-14 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-full bg-crit-soft text-crit">
        <AlertTriangle size={20} />
      </div>
      <div className="font-medium">Something went wrong</div>
      <div className="max-w-sm text-sm text-muted">{message}</div>
      {onRetry && (
        <Button className="mt-3" onClick={onRetry}>
          Retry
        </Button>
      )}
    </div>
  );
}

/** Wraps page content with a short fade-in. */
export function Gate({ children }: { children: ReactNode }) {
  const [boot, setBoot] = useState(true);
  useEffect(() => {
    const t = setTimeout(() => setBoot(false), 220);
    return () => clearTimeout(t);
  }, []);
  if (boot) return <LoadingState />;
  return <div className="fade-in">{children}</div>;
}

/* ---------- overlays ---------- */
export function Modal({ open, onClose, title, children, footer, wide }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-[70] flex items-end justify-center bg-black/50 p-0 backdrop-blur-sm sm:items-center sm:p-4" onMouseDown={onClose}>
      <div onMouseDown={(e) => e.stopPropagation()} className={cn("fade-in flex max-h-[92vh] w-full flex-col rounded-t-3xl border border-line bg-surface sm:rounded-3xl", wide ? "sm:max-w-3xl" : "sm:max-w-lg")}>
        <div className="flex items-center justify-between gap-3 border-b border-line px-6 py-4">
          <h3 className="font-semibold">{title}</h3>
          <button onClick={onClose} className="text-muted hover:text-fg" aria-label="Close">
            <X size={18} />
          </button>
        </div>
        <div className="overflow-y-auto px-6 py-5">{children}</div>
        {footer && <div className="flex flex-wrap justify-end gap-2 border-t border-line px-6 py-4">{footer}</div>}
      </div>
    </div>
  );
}

export function ConfirmDialog({ open, title, message, confirmLabel = "Confirm", danger, onConfirm, onCancel, children }: { open: boolean; title: string; message: ReactNode; confirmLabel?: string; danger?: boolean; onConfirm: () => void; onCancel: () => void; children?: ReactNode }) {
  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={title}
      footer={
        <>
          <Button variant="secondary" onClick={onCancel}>
            Cancel
          </Button>
          <Button variant={danger ? "danger" : "primary"} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-sm text-muted">{message}</div>
      {children}
    </Modal>
  );
}

export function Drawer({ open, onClose, title, children }: { open: boolean; onClose: () => void; title: ReactNode; children: ReactNode }) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-[65] bg-black/40 backdrop-blur-sm" onMouseDown={onClose}>
      <aside onMouseDown={(e) => e.stopPropagation()} className="fade-in absolute right-0 top-0 flex h-full w-full max-w-xl flex-col border-l border-line bg-surface">
        <div className="flex items-center justify-between gap-3 border-b border-line px-6 py-4">
          <h3 className="min-w-0 truncate font-semibold">{title}</h3>
          <button onClick={onClose} className="text-muted hover:text-fg" aria-label="Close">
            <X size={18} />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto p-6">{children}</div>
      </aside>
    </div>
  );
}

/* ---------- navigation ---------- */
export function LinkTabs({ tabs }: { tabs: { to: string; label: string; end?: boolean; badge?: ReactNode }[] }) {
  return (
    <div className="mb-6 -mx-1 overflow-x-auto px-1">
      <div className="inline-flex min-w-full gap-1 rounded-full border border-line bg-surface p-1">
        {tabs.map((t) => (
          <NavLink key={t.to} to={t.to} end={t.end} className={({ isActive }) => cn("flex items-center gap-1.5 whitespace-nowrap rounded-full px-4 py-2 text-sm transition", isActive ? "bg-accent font-medium text-accent-fg" : "text-muted hover:text-fg")}>
            {t.label}
            {t.badge}
          </NavLink>
        ))}
      </div>
    </div>
  );
}

export function Segmented<T extends string>({ value, onChange, options, size = "md" }: { value: T; onChange: (v: T) => void; options: { value: T; label: ReactNode }[]; size?: "sm" | "md" }) {
  return (
    <div className="inline-flex max-w-full flex-wrap gap-0.5 rounded-full border border-line bg-surface2 p-0.5">
      {options.map((o) => (
        <button key={o.value} onClick={() => onChange(o.value)} className={cn("whitespace-nowrap rounded-full transition", size === "sm" ? "px-3 py-1 text-xs" : "px-3.5 py-1.5 text-xs", value === o.value ? "bg-accent font-medium text-accent-fg" : "text-muted hover:text-fg")}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function TimeRangeSelector({ value, onChange }: { value: Range; onChange: (r: Range) => void }) {
  return <Segmented value={value} onChange={onChange} size="sm" options={RANGES.map((r) => ({ value: r.id, label: r.label }))} />;
}

/* ---------- forms ---------- */
const fieldCls = "w-full rounded-full border border-line bg-surface2 px-4 py-2.5 text-sm outline-none transition placeholder:text-muted/70 focus:border-fg/40 disabled:opacity-50";
export const Input = ({ className, ...p }: InputHTMLAttributes<HTMLInputElement>) => <input {...p} className={cn(fieldCls, className)} />;
export const Select = ({ className, children, ...p }: SelectHTMLAttributes<HTMLSelectElement>) => (
  <select {...p} className={cn(fieldCls, "cursor-pointer pr-8", className)}>
    {children}
  </select>
);
export const Textarea = ({ className, ...p }: TextareaHTMLAttributes<HTMLTextAreaElement>) => <textarea {...p} className={cn(fieldCls, "rounded-2xl", className)} />;

export function Field({ label, hint, children, className }: { label: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cn("block", className)}>
      <span className="mb-1.5 block text-xs font-medium text-muted">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-muted">{hint}</span>}
    </label>
  );
}

export function SearchInput({ value, onChange, placeholder = "Search…", className }: { value: string; onChange: (v: string) => void; placeholder?: string; className?: string }) {
  return (
    <div className={cn("relative", className)}>
      <Search size={15} className="pointer-events-none absolute left-4 top-1/2 -translate-y-1/2 text-muted" />
      <input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} className={cn(fieldCls, "pl-10 pr-9")} />
      {value && (
        <button onClick={() => onChange("")} className="absolute right-3 top-1/2 -translate-y-1/2 text-muted hover:text-fg" aria-label="Clear search">
          <X size={14} />
        </button>
      )}
    </div>
  );
}

export function Toggle({ checked, onChange, disabled }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <button role="switch" aria-checked={checked} disabled={disabled} onClick={() => onChange(!checked)} className={cn("relative h-6 w-11 shrink-0 rounded-full transition disabled:opacity-40", checked ? "bg-accent" : "bg-neutral-soft border border-line")}>
      <span className={cn("absolute top-0.5 h-5 w-5 rounded-full transition-all", checked ? "left-[22px] bg-accent-fg" : "left-0.5 bg-muted")} />
    </button>
  );
}

export function ToggleRow({ label, desc, checked, onChange, children }: { label: string; desc?: string; checked: boolean; onChange: (v: boolean) => void; children?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 py-3">
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{label}</div>
        {desc && <div className="text-xs text-muted">{desc}</div>}
      </div>
      {children}
      <Toggle checked={checked} onChange={onChange} />
    </div>
  );
}

export function FilterBar({ children }: { children: ReactNode }) {
  return <div className="mb-5 grid grid-cols-1 gap-2 sm:grid-cols-2 lg:flex lg:flex-wrap lg:items-center">{children}</div>;
}

/* ---------- tables ---------- */
export function Table({ head, children, className }: { head: string[]; children: ReactNode; className?: string }) {
  return (
    <div className={cn("overflow-x-auto", className)}>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-muted">
            {head.map((h) => (
              <th key={h} className="whitespace-nowrap px-4 py-3 font-medium">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-line">{children}</tbody>
      </table>
    </div>
  );
}
export const Td = ({ children, className, title }: { children?: ReactNode; className?: string; title?: string }) => <td title={title} className={cn("px-4 py-3 align-middle", className)}>{children}</td>;
export const Tr = ({ children, onClick, className }: { children: ReactNode; onClick?: () => void; className?: string }) => (
  <tr onClick={onClick} className={cn("transition hover:bg-surface2/60", onClick && "cursor-pointer", className)}>
    {children}
  </tr>
);

/* ---------- misc ---------- */
export function CopyButton({ text, label, className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      onClick={(e) => {
        e.stopPropagation();
        copyText(text);
        setDone(true);
        setTimeout(() => setDone(false), 1400);
      }}
      className={cn("inline-flex items-center gap-1 rounded-full px-2 py-1 text-xs text-muted transition hover:bg-surface2 hover:text-fg", className)}
      title="Copy"
    >
      {done ? <Check size={13} className="text-ok" /> : <Copy size={13} />}
      {label && <span>{done ? "Copied" : label}</span>}
    </button>
  );
}

export function CodeBlock({ children, copy = true, className }: { children: string; copy?: boolean; className?: string }) {
  return (
    <div className={cn("relative rounded-2xl border border-line bg-surface2 p-4", className)}>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all font-mono text-[12.5px] leading-relaxed">{children}</pre>
      {copy && <CopyButton text={children} className="absolute right-2 top-2" />}
    </div>
  );
}

export function Spinner({ size = 16 }: { size?: number }) {
  return <Loader2 size={size} className="spin" />;
}
