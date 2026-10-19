import { useEffect, useState } from "react";
import { Navigate, NavLink, Outlet, useNavigate } from "react-router-dom";
import { Activity, Bell, BookOpen, Bot, ClipboardList, Cog, Copy, Database, Download, Hourglass, Info, KeyRound, KeySquare, LifeBuoy, LockKeyhole, LogIn, MonitorSmartphone, PackageCheck, Radar, RefreshCw, ScrollText, Send, Server, ShieldCheck, Siren, Timer, UserRound, Users } from "lucide-react";
import { useStore } from "../store/store";
import { api, deviceLabel, useApi, type ApiError, type AuditRow, type SecuritySettings as SecurityConfig, type SessionInfo } from "../api/client";
import { relTime, dateTime, fmtMB } from "../mock/util";
import { Badge, Button, Card, ErrorState, Field, Gate, Input, KV, LoadingState, Modal, PageHeader, SectionTitle, Segmented, Select, ServerStatus, Spinner, Table, Textarea, Td, Toggle, ToggleRow, Tr, CopyButton } from "../components/ui";
import { cn } from "../utils/cn";
import { UpdateCard } from "../components/PlatformUpdate";
import { DashboardUsage } from "../components/DashboardUsage";
import { PushCard } from "../components/PushCard";
import { MobileCard } from "../components/MobileCard";
import { AlertRulesCard } from "../components/AlertRulesCard";

const GROUPS: { title: string; items: [string, string, typeof Bell][] }[] = [
  { title: "You", items: [["account", "Account", UserRound], ["security", "Security", ShieldCheck], ["2fa", "2FA", KeyRound], ["sessions", "Sessions", MonitorSmartphone]] },
  { title: "Fleet", items: [["servers", "Servers", Server], ["agents", "Agents", Bot], ["logs", "Logs", ScrollText], ["metrics", "Metrics", Activity]] },
  { title: "Alerts", items: [["notifications", "Notifications", Bell], ["incidents", "Incident rules", Siren]] },
  { title: "Access", items: [["team", "Team", Users], ["tokens", "API tokens", KeySquare]] },
  { title: "Platform", items: [["system", "System", Cog]] },
];

export function SettingsLayout() {
  const { me } = useStore();
  // Team members only manage their own account; everything else is the owner's.
  const groups = me && me.role !== "owner"
    ? [{ title: "You", items: GROUPS[0].items.filter(([to]) => to === "account" || to === "2fa") }]
    : GROUPS;
  return (
    <>
      <PageHeader title="Settings" sub="Account, security, collection, notifications and rules" />
      <div className="grid gap-6 lg:grid-cols-[220px_1fr]">
        <nav className="-mx-1 flex gap-1 overflow-x-auto px-1 lg:sticky lg:top-24 lg:mx-0 lg:h-fit lg:flex-col lg:gap-4 lg:overflow-visible lg:rounded-3xl lg:border lg:border-line lg:bg-surface lg:p-3">
          {groups.map((g) => (
            <div key={g.title} className="flex gap-1 lg:flex-col lg:gap-0.5">
              <div className="hidden px-3 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted/70 lg:block">{g.title}</div>
              {g.items.map(([to, label, Icon]) => (
                <NavLink key={to} to={`/settings/${to}`} className={({ isActive }) => cn("flex items-center gap-2.5 whitespace-nowrap rounded-full px-3.5 py-2 text-sm transition", isActive ? "bg-accent font-medium text-accent-fg" : "text-muted hover:bg-surface2 hover:text-fg")}>
                  <Icon size={15} className="shrink-0" />
                  {label}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
        <div className="min-w-0">
          <Gate>
            <Outlet />
          </Gate>
        </div>
      </div>
    </>
  );
}
export const SettingsIndex = () => <Navigate to="/settings/account" replace />;

function SaveBar({ dirty, onSave, onReset, busy }: { dirty: boolean; onSave: () => void; onReset: () => void; busy?: boolean }) {
  return (
    <div className="mt-6 flex gap-2 border-t border-line pt-5">
      <Button disabled={!dirty || busy} onClick={onSave}>{busy && <Spinner size={14} />} Save changes</Button>
      <Button variant="ghost" disabled={!dirty} onClick={onReset}>Reset</Button>
    </div>
  );
}

/* ---------------- Account ---------------- */
export function AccountSettings() {
  const { me, refreshMe, toast } = useStore();
  const [f, setF] = useState({ displayName: me?.displayName ?? "", email: me?.email ?? "" });
  const [pw, setPw] = useState({ cur: "", next: "", confirm: "" });
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState<"" | "profile" | "pw">("");
  if (!me) return null;
  const dirty = f.displayName !== me.displayName || f.email !== me.email;
  const strength = Math.min(4, (pw.next.length >= 12 ? 1 : 0) + (/[A-Z]/.test(pw.next) ? 1 : 0) + (/\d/.test(pw.next) ? 1 : 0) + (/[^\w]/.test(pw.next) ? 1 : 0));
  const saveProfile = async () => {
    setBusy("profile");
    try {
      await api("PUT", "/api/account", f);
      await refreshMe();
      toast("Profile saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const changePw = async () => {
    if (!pw.cur) return setErr("Enter your current password.");
    if (pw.next.length < 12) return setErr("New password must be at least 12 characters.");
    if (pw.next !== pw.confirm) return setErr("Passwords do not match.");
    setErr("");
    setBusy("pw");
    try {
      await api("POST", "/api/account/password", { current: pw.cur, next: pw.next });
      setPw({ cur: "", next: "", confirm: "" });
      await refreshMe();
      toast("Password changed. Other sessions were signed out.");
    } catch (e) {
      setErr((e as ApiError).message);
    }
    setBusy("");
  };
  return (
    <div className="space-y-6">
      {me.role !== "owner" && (
        <>
          <MobileCard />
          <AlertRulesCard />
          <PushCard personal enabled onChange={() => {}} />
        </>
      )}
      <Card>
        <SectionTitle icon={<UserRound size={15} />} title="Profile" />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Username" hint={me.role === "owner" ? "Set on the server with ADMIN_USERNAME" : `Team member (${me.role}); the owner manages your access.`}><Input value={me.username} disabled /></Field>
          <Field label="Display name"><Input value={f.displayName} onChange={(e) => setF({ ...f, displayName: e.target.value })} /></Field>
          <Field label="Email" className="sm:col-span-2"><Input type="email" value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} /></Field>
        </div>
        <SaveBar dirty={dirty} busy={busy === "profile"} onSave={saveProfile} onReset={() => setF({ displayName: me.displayName, email: me.email })} />
      </Card>
      <Card>
        <SectionTitle icon={<LockKeyhole size={15} />} title="Change password" sub={`Last changed ${relTime(me.passwordChangedAt)} · changing it signs out every other session`} />
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Current password"><Input type="password" autoComplete="current-password" value={pw.cur} onChange={(e) => setPw({ ...pw, cur: e.target.value })} /></Field>
          <Field label="New password"><Input type="password" autoComplete="new-password" value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} /></Field>
          <Field label="Confirm new password"><Input type="password" autoComplete="new-password" value={pw.confirm} onChange={(e) => setPw({ ...pw, confirm: e.target.value })} /></Field>
        </div>
        <div className="mt-3 flex gap-1">{[0, 1, 2, 3].map((i) => <span key={i} className={cn("h-1.5 flex-1 rounded-full", i < strength ? (strength < 3 ? "bg-warn" : "bg-ok") : "bg-neutral-soft")} />)}</div>
        {err && <div role="alert" className="mt-3 rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
        <Button className="mt-4" onClick={changePw} disabled={busy === "pw"}>{busy === "pw" && <Spinner size={14} />} Update password</Button>
      </Card>
    </div>
  );
}

/* ---------------- Security ---------------- */
export function SecuritySettings() {
  const { toast } = useStore();
  const sec = useApi<SecurityConfig>("/api/settings/security");
  const logins = useApi<AuditRow[]>("/api/audit/logins");
  const events = useApi<AuditRow[]>("/api/audit");
  const [f, setF] = useState<SecurityConfig | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (sec.data) setF(sec.data);
  }, [sec.data]);
  if (sec.error) return <ErrorState message={sec.error} onRetry={sec.reload} />;
  if (!f || !sec.data) return <LoadingState />;
  const dirty = JSON.stringify(f) !== JSON.stringify(sec.data);
  const save = async () => {
    setBusy(true);
    try {
      sec.setData(await api<SecurityConfig>("PUT", "/api/settings/security", f));
      toast("Security settings saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-6">
      <Card>
        <SectionTitle icon={<Timer size={15} />} title="Session & access" />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Session timeout (idle)">
            <Select value={f.sessionTimeoutMin} onChange={(e) => setF({ ...f, sessionTimeoutMin: Number(e.target.value) })}>
              {[15, 30, 60, 120, 480, 1440].map((n) => <option key={n} value={n}>{n >= 60 ? `${n / 60} hour${n > 60 ? "s" : ""}` : `${n} minutes`}</option>)}
            </Select>
          </Field>
          <Field label="Lock out an IP after failed logins" hint="Locked for 15 minutes"><Input type="number" min={3} max={20} value={f.lockoutAfter} onChange={(e) => setF({ ...f, lockoutAfter: Number(e.target.value) })} /></Field>
        </div>
        <div className="mt-3 divide-y divide-line">
          <ToggleRow label="Re-authenticate before running commands" desc="Ask for your password on Stop / Restart / custom commands" checked={f.reauthForCommands} onChange={(v) => setF({ ...f, reauthForCommands: v })} />
          <ToggleRow label="2FA code for dangerous actions" desc="Deleting servers, apps, sites, members or status pages, blocking addresses, firewall changes, installing updates, cleaning disks and changing these settings ask for a 2FA code each time (the password, for someone without 2FA). Also for the team." checked={!!f.confirmDangerous} onChange={(v) => setF({ ...f, confirmDangerous: v })} />
          <ToggleRow label="Require 2FA for everyone" desc="Team members without two-factor authentication must turn it on before they can use the dashboard. Turn on your own first." checked={!!f.require2FA} onChange={(v) => setF({ ...f, require2FA: v })} />
          <ToggleRow label="Allow signing in with GitHub" desc="The login page offers Continue with GitHub to everyone who linked their GitHub account (needs GitHub connected in Integrations). Turned off, only passwords work." checked={!f.noGitHubLogin} onChange={(v) => setF({ ...f, noGitHubLogin: !v })} />
        </div>
        <SaveBar dirty={dirty} busy={busy} onSave={save} onReset={() => setF(sec.data)} />
      </Card>
      <Card pad={false}>
        <div className="p-5 pb-0"><SectionTitle icon={<LogIn size={15} />} title="Login history" sub="Last 25 sign-in attempts" /></div>
        {logins.error ? (
          <div className="p-5 text-sm text-crit">{logins.error}</div>
        ) : (
          <Table head={["When", "Device", "IP", "Method", "Result"]}>
            {(logins.data ?? []).map((l, i) => (
              <Tr key={i}>
                <Td className="whitespace-nowrap text-xs">{dateTime(l.ts)}</Td>
                <Td className="whitespace-nowrap" title={l.userAgent}>{deviceLabel(l.userAgent)}</Td>
                <Td className="font-mono text-xs">{l.ip}</Td>
                <Td className="whitespace-nowrap text-muted">{l.detail}</Td>
                <Td><Badge tone={l.ok ? "ok" : "crit"}>{l.ok ? "Success" : "Failed"}</Badge></Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
      <Card pad={false}>
        <div className="p-5 pb-0"><SectionTitle icon={<ClipboardList size={15} />} title="Audit log" sub="Last 200 administrative actions: sign-ins, 2FA, pairing, commands, settings" /></div>
        {events.error ? (
          <div className="p-5 text-sm text-crit">{events.error}</div>
        ) : (
          <Table head={["When", "Action", "Target", "Detail", "IP", "Result"]}>
            {(events.data ?? []).map((l, i) => (
              <Tr key={i}>
                <Td className="whitespace-nowrap text-xs">{dateTime(l.ts)}</Td>
                <Td className="whitespace-nowrap font-mono text-xs">{l.action}</Td>
                <Td className="max-w-[180px] truncate font-mono text-xs" title={l.target}>{l.target || "—"}</Td>
                <Td className="max-w-[240px] truncate text-muted" title={l.detail}>{l.detail || "—"}</Td>
                <Td className="font-mono text-xs">{l.ip}</Td>
                <Td><Badge tone={l.ok ? "ok" : "crit"}>{l.ok ? "OK" : "Failed"}</Badge></Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  );
}

/* ---------------- 2FA ---------------- */
export function TwoFASettings() {
  const { me, refreshMe, toast } = useStore();
  const [modal, setModal] = useState<null | "enable" | "disable" | "regen">(null);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [setup, setSetup] = useState<{ secret: string; qr: string } | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  if (!me) return null;

  const close = () => {
    setModal(null);
    setPassword("");
    setCode("");
    setSetup(null);
    setErr("");
    setUseRecovery(false);
  };
  const run = async (fn: () => Promise<void>) => {
    setErr("");
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      setErr((e as ApiError).message);
    }
    setBusy(false);
  };
  const startSetup = () =>
    run(async () => {
      setSetup(await api<{ secret: string; qr: string }>("POST", "/api/2fa/setup", { password }));
      setPassword("");
    });
  const enable = () =>
    run(async () => {
      const r = await api<{ recoveryCodes: string[] }>("POST", "/api/2fa/enable", { code });
      close();
      setCodes(r.recoveryCodes);
      await refreshMe();
      toast("Two-factor authentication enabled");
    });
  const disable = () =>
    run(async () => {
      await api("POST", "/api/2fa/disable", { password, code, recovery: useRecovery });
      close();
      await refreshMe();
      toast("2FA disabled", "info");
    });
  const regen = () =>
    run(async () => {
      const r = await api<{ recoveryCodes: string[] }>("POST", "/api/2fa/recovery-codes", { password });
      close();
      setCodes(r.recoveryCodes);
      await refreshMe();
      toast("New recovery codes generated. Old codes no longer work.");
    });

  const errBox = err && <div role="alert" className="mt-4 rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>;
  const pwField = <Field label="Current password"><Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus /></Field>;
  const codeInput = (
    <Input value={code} onChange={(e) => setCode(useRecovery ? e.target.value : e.target.value.replace(/\D/g, "").slice(0, 6))} inputMode={useRecovery ? "text" : "numeric"} autoComplete="one-time-code" placeholder={useRecovery ? "xxxxx-xxxxx" : "123456"} className="text-center font-mono tracking-[0.3em]" />
  );

  return (
    <div className="space-y-6">
      <Card>
        <SectionTitle icon={<KeyRound size={15} />} title="Two-factor authentication" action={<Badge tone={me.twofaEnabled ? "ok" : "warn"} dot>{me.twofaEnabled ? "Enabled" : "Disabled"}</Badge>} />
        <p className="text-sm text-muted">{me.twofaEnabled ? "A 6-digit code from your authenticator app is required after your password." : "Add a second step to protect access to your infrastructure."}</p>
        <div className="mt-4">{me.twofaEnabled ? <Button variant="danger" onClick={() => setModal("disable")}>Disable 2FA</Button> : <Button onClick={() => setModal("enable")}><ShieldCheck size={15} /> Enable 2FA</Button>}</div>
      </Card>
      {me.twofaEnabled && (
        <Card>
          <SectionTitle icon={<LifeBuoy size={15} />} title="Recovery codes" sub={`${me.recoveryCodesLeft} of 10 unused. Each code works once. Codes are only shown when generated.`} action={<Button size="sm" variant="secondary" onClick={() => setModal("regen")}><RefreshCw size={13} /> Regenerate</Button>} />
          {me.recoveryCodesLeft <= 3 && <p className="text-sm text-warn">You are running low on recovery codes. Regenerate a new set.</p>}
        </Card>
      )}

      <Modal
        open={modal === "enable"}
        onClose={close}
        title="Enable two-factor authentication"
        footer={<><Button variant="secondary" onClick={close}>Cancel</Button>{setup ? <Button onClick={enable} disabled={busy || code.length !== 6}>{busy && <Spinner size={14} />} Verify & enable</Button> : <Button onClick={startSetup} disabled={busy || !password}>{busy && <Spinner size={14} />} Continue</Button>}</>}
      >
        {!setup ? (
          <form onSubmit={(e) => { e.preventDefault(); startSetup(); }}>{pwField}</form>
        ) : (
          <div className="flex flex-col items-center gap-4 sm:flex-row sm:items-start">
            <img src={setup.qr} alt="QR code for your authenticator app" className="h-44 w-44 rounded-2xl bg-white p-2" />
            <div className="min-w-0 flex-1 space-y-3 text-sm">
              <p className="text-muted">1. Scan this QR code with Google Authenticator, 1Password, Authy or similar.</p>
              <div><div className="text-[11px] uppercase tracking-wide text-muted">Or enter the key manually</div><code className="break-all font-mono text-xs">{setup.secret}</code></div>
              <Field label="2. Enter the 6-digit code">{codeInput}</Field>
            </div>
          </div>
        )}
        {errBox}
      </Modal>

      <Modal
        open={modal === "disable"}
        onClose={close}
        title="Disable two-factor authentication?"
        footer={<><Button variant="secondary" onClick={close}>Cancel</Button><Button variant="danger" onClick={disable} disabled={busy || !password || !code}>{busy && <Spinner size={14} />} Disable 2FA</Button></>}
      >
        <p className="mb-4 text-sm text-muted">Your account will only be protected by your password.</p>
        <div className="space-y-4">
          {pwField}
          <Field label={useRecovery ? "Recovery code" : "Authenticator code"}>{codeInput}</Field>
          <button className="text-xs text-muted underline-offset-2 hover:text-fg hover:underline" onClick={() => { setUseRecovery(!useRecovery); setCode(""); }}>
            {useRecovery ? "Use authenticator code" : "Use a recovery code instead"}
          </button>
        </div>
        {errBox}
      </Modal>

      <Modal
        open={modal === "regen"}
        onClose={close}
        title="Regenerate recovery codes"
        footer={<><Button variant="secondary" onClick={close}>Cancel</Button><Button onClick={regen} disabled={busy || !password}>{busy && <Spinner size={14} />} Regenerate</Button></>}
      >
        <p className="mb-4 text-sm text-muted">Your current recovery codes will stop working.</p>
        <form onSubmit={(e) => { e.preventDefault(); regen(); }}>{pwField}</form>
        {errBox}
      </Modal>

      <Modal open={codes !== null} onClose={() => setCodes(null)} title="Save your recovery codes" footer={<Button onClick={() => setCodes(null)}>I have saved them</Button>}>
        <p className="mb-4 text-sm text-muted">Each code signs you in once if you lose your authenticator. <strong className="text-fg">They will not be shown again.</strong></p>
        <div className="grid grid-cols-2 gap-2">
          {codes?.map((c) => <code key={c} className="rounded-xl bg-surface2 px-3 py-2 text-center font-mono text-sm">{c}</code>)}
        </div>
        <CopyButton text={codes?.join("\n") ?? ""} label="Copy all" className="mt-4 border border-line" />
      </Modal>
    </div>
  );
}

/* ---------------- Sessions ---------------- */
export function SessionSettings() {
  const { toast, logout } = useStore();
  const { data, error, reload } = useApi<SessionInfo[]>("/api/sessions");
  if (error) return <ErrorState message={error} onRetry={reload} />;
  if (!data) return <LoadingState />;
  const others = data.filter((s) => !s.current).length;
  const act = async (fn: () => Promise<unknown>, msg: string) => {
    try {
      await fn();
      toast(msg);
      reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const everywhere = () => act(() => api("POST", "/api/sessions/revoke-others"), "Signed out everywhere").then(logout);
  return (
    <Card pad={false}>
      <div className="p-5 pb-0">
        <SectionTitle icon={<MonitorSmartphone size={15} />}
          title="Active sessions"
          sub={`${data.length} signed-in browser${data.length === 1 ? "" : "s"}`}
          action={
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="secondary" disabled={!others} onClick={() => act(() => api("POST", "/api/sessions/revoke-others"), "Logged out all other sessions")}>Log out all others</Button>
              <Button size="sm" variant="danger" onClick={everywhere}>Sign out everywhere</Button>
            </div>
          }
        />
      </div>
      <Table head={["Browser / device", "IP", "Created", "Last active", "Expires", ""]}>
        {data.map((s) => (
          <Tr key={s.id}>
            <Td className="whitespace-nowrap font-medium" title={s.userAgent}>{deviceLabel(s.userAgent)} {s.current && <Badge tone="ok" className="ml-1">This device</Badge>}</Td>
            <Td className="font-mono text-xs">{s.ip}</Td>
            <Td className="whitespace-nowrap text-xs text-muted">{dateTime(s.createdAt)}</Td>
            <Td className="whitespace-nowrap text-xs text-muted">{s.current ? "now" : relTime(s.lastActive)}</Td>
            <Td className="whitespace-nowrap text-xs text-muted">{dateTime(s.expiresAt)}</Td>
            <Td>{!s.current && <Button size="sm" variant="ghost" onClick={() => act(() => api("DELETE", `/api/sessions/${encodeURIComponent(s.id)}`), "Session logged out")}>Log out</Button>}</Td>
          </Tr>
        ))}
      </Table>
    </Card>
  );
}

/* ---------------- Servers & agents ---------------- */
export function ServersSettings() {
  const { servers, apps } = useStore();
  const nav = useNavigate();
  return (
    <Card pad={false}>
      <div className="p-5 pb-0"><SectionTitle icon={<Server size={15} />} title="Servers" action={<Button size="sm" onClick={() => nav("/servers/new")}>Add server</Button>} /></div>
      {servers.length === 0 ? (
        <p className="p-5 text-sm text-muted">No servers yet.</p>
      ) : (
        <Table head={["Server", "Status", "Provider", "Apps", ""]}>
          {servers.map((s) => (
            <Tr key={s.id}>
              <Td><div className="font-medium">{s.name}</div><div className="text-xs text-muted">{s.ip}</div></Td>
              <Td><ServerStatus status={s.status} /></Td>
              <Td className="text-muted">{s.provider || "—"}</Td>
              <Td>{apps.filter((a) => a.serverId === s.id).length}</Td>
              <Td><Button size="sm" variant="ghost" onClick={() => nav(`/servers/${s.id}/settings`)}>Configure</Button></Td>
            </Tr>
          ))}
        </Table>
      )}
    </Card>
  );
}

export function AgentsSettings() {
  const { servers, toast } = useStore();
  const nav = useNavigate();
  const [busy, setBusy] = useState(false);
  const latest = servers[0]?.latestAgentVersion;
  const paired = servers.filter((s) => s.status !== "pending");
  const outdated = paired.filter((s) => s.status !== "disconnected" && s.agentVersion && s.agentVersion !== s.latestAgentVersion);
  const auto = useApi<{ autoUpdate: boolean }>("/api/settings/agents");
  const updateAll = async () => {
    setBusy(true);
    const results = await Promise.allSettled(outdated.map((s) => api("POST", `/api/servers/${s.id}/update-agent`)));
    const failed = results.filter((r) => r.status === "rejected").length;
    toast(failed ? `${outdated.length - failed} updates sent, ${failed} failed` : `Update sent to ${outdated.length} agent${outdated.length === 1 ? "" : "s"}. They restart on v${latest} in a few seconds.`, failed ? "err" : "info");
    setBusy(false);
  };
  return (
    <div className="space-y-6">
      <Card>
        <SectionTitle icon={<PackageCheck size={15} />} title="Agent version" action={outdated.length > 0 && <Button size="sm" disabled={busy} onClick={updateAll}>{busy && <Spinner size={13} />} Update {outdated.length} agent{outdated.length === 1 ? "" : "s"}</Button>} />
        <p className="text-sm text-muted">This server ships agent <b className="text-fg">v{latest ?? "—"}</b>. Agents update in place: each downloads the new binary from this dashboard, checks its SHA-256 and restarts. Agents older than v0.8 need a new install command once.</p>
      </Card>
      <Card>
        <ToggleRow
          label="Always update agents automatically"
          desc="Whenever an agent connects on an older version than this dashboard ships, it installs the new one (checksum-verified) and restarts. Turning this on also updates the outdated agents connected now."
          checked={!!auto.data?.autoUpdate}
          onChange={async (v) => {
            try {
              auto.setData(await api<{ autoUpdate: boolean }>("PUT", "/api/settings/agents", { autoUpdate: v }));
              toast(v ? "Agents now update automatically" : "Automatic agent updates turned off");
            } catch (e) {
              toast((e as ApiError).message, "err");
            }
          }}
        />
      </Card>
      <Card pad={false}>
        <div className="p-5 pb-0"><SectionTitle icon={<Bot size={15} />} title="Agents" /></div>
        {paired.length === 0 ? (
          <p className="p-5 text-sm text-muted">No agents have connected yet.</p>
        ) : (
          <Table head={["Server", "Version", "Last heartbeat", "Latency", ""]}>
            {paired.map((s) => (
              <Tr key={s.id}>
                <Td className="font-medium">{s.name}</Td>
                <Td className="whitespace-nowrap">v{s.agentVersion} {s.agentVersion !== s.latestAgentVersion && <Badge tone="warn" className="ml-1">outdated</Badge>}</Td>
                <Td className="whitespace-nowrap text-muted">{s.lastSeen ? relTime(s.lastSeen) : "never"}</Td>
                <Td>{s.status === "disconnected" ? "—" : `${s.latencyMs} ms`}</Td>
                <Td><Button size="sm" variant="ghost" onClick={() => nav(`/servers/${s.id}/agent`)}>Manage</Button></Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  );
}

/* ---------------- Logs ---------------- */
export function LogSettings() {
  const { toast } = useStore();
  const cfg = useApi<{ retentionDays: number; maxLineKB: number }>("/api/settings/logs");
  const stats = useApi<{ lines: number; oldestTs: number; dropped: number }>("/api/logs/stats");
  const [f, setF] = useState<{ retentionDays: number; maxLineKB: number } | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (cfg.data) setF(cfg.data);
  }, [cfg.data]);
  if (cfg.error) return <ErrorState message={cfg.error} onRetry={cfg.reload} />;
  if (!f || !cfg.data) return <LoadingState />;
  const dirty = JSON.stringify(f) !== JSON.stringify(cfg.data);
  const save = async () => {
    setBusy(true);
    try {
      cfg.setData(await api("PUT", "/api/settings/logs", f));
      toast("Log settings saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-6">
      <Card>
        <SectionTitle icon={<Hourglass size={15} />} title="Log retention" sub="Older log lines are deleted from the central server every 10 minutes" />
        <Segmented value={String(f.retentionDays)} onChange={(v) => setF({ ...f, retentionDays: Number(v) })} options={[1, 3, 7, 14, 30].map((d) => ({ value: String(d), label: `${d} day${d > 1 ? "s" : ""}` }))} />
        {f.retentionDays === 7 && <span className="ml-3 text-xs text-muted">default</span>}
        <div className="mt-5 grid gap-4 sm:grid-cols-2">
          <Field label="Max line length" hint="Longer lines are cut on the agent and the server"><Select value={f.maxLineKB} onChange={(e) => setF({ ...f, maxLineKB: Number(e.target.value) })}>{[4, 8, 16, 64].map((n) => <option key={n} value={n}>{n} KB</option>)}</Select></Field>
        </div>
        <SaveBar dirty={dirty} busy={busy} onSave={save} onReset={() => setF(cfg.data)} />
      </Card>
      <Card>
        <SectionTitle icon={<Database size={15} />} title="Stored logs" />
        {stats.data ? (
          <dl className="grid gap-x-6 sm:grid-cols-3">
            <KV label="Lines stored (approx.)">{stats.data.lines.toLocaleString()}</KV>
            <KV label="Oldest line">{stats.data.oldestTs ? `${dateTime(stats.data.oldestTs)} (${relTime(stats.data.oldestTs)})` : "—"}</KV>
            <KV label="Dropped by server since start">{stats.data.dropped.toLocaleString()}</KV>
          </dl>
        ) : (
          <Spinner />
        )}
      </Card>
    </div>
  );
}

/* ---------------- Metrics ---------------- */
export function MetricsSettings() {
  const { toast, servers } = useStore();
  const cfg = useApi<{ intervalSec: number; retentionDays: number }>("/api/settings/metrics");
  const [f, setF] = useState<{ intervalSec: number; retentionDays: number } | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (cfg.data) setF(cfg.data);
  }, [cfg.data]);
  if (cfg.error) return <ErrorState message={cfg.error} onRetry={cfg.reload} />;
  if (!f || !cfg.data) return <LoadingState />;
  const dirty = JSON.stringify(f) !== JSON.stringify(cfg.data);
  // Rows per server: 48h of minutes + 8d of 5-minute buckets + hourly for the retention; ~90 bytes each.
  const mb = (servers.length * (48 * 60 + 8 * 288 + 24 * f.retentionDays) * 90) / 1024 / 1024;
  const save = async () => {
    setBusy(true);
    try {
      cfg.setData(await api("PUT", "/api/settings/metrics", f));
      toast("Metrics settings saved. Agents use the new interval immediately.");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Card>
      <SectionTitle icon={<Activity size={15} />} title="Metrics collection" />
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Collection interval" hint="How often agents sample the host"><Select value={f.intervalSec} onChange={(e) => setF({ ...f, intervalSec: Number(e.target.value) })}>{[5, 10, 15, 30, 60].map((n) => <option key={n} value={n}>{n} seconds</option>)}</Select></Field>
        <Field label="Long-term retention" hint="Hourly averages are kept this long"><Select value={f.retentionDays} onChange={(e) => setF({ ...f, retentionDays: Number(e.target.value) })}>{[7, 14, 30, 60, 90, 180, 365].map((n) => <option key={n} value={n}>{n} days</option>)}</Select></Field>
      </div>
      <div className="mt-5 space-y-1 rounded-2xl bg-surface2 p-4 text-sm">
        <div><span className="text-muted">Downsampling:</span> 1-minute averages for 48 hours, 5-minute averages for 8 days, hourly averages for {f.retentionDays} days.</div>
        <div><span className="text-muted">Estimated storage:</span> <b>{fmtMB(mb)}</b> <span className="text-muted">for {servers.length} server{servers.length === 1 ? "" : "s"}</span></div>
      </div>
      <SaveBar dirty={dirty} busy={busy} onSave={save} onReset={() => setF(cfg.data)} />
    </Card>
  );
}

/* ---------------- Notifications ---------------- */
interface Channel {
  provider: "push" | "ntfy" | "telegram" | "discord" | "webhook";
  enabled: boolean;
  configured: boolean;
  fields: Record<string, string>;
  secrets: Record<string, boolean>;
}
type NotifyRules = Record<string, boolean>;
type FieldDef = [key: string, label: string, placeholder: string, secret?: boolean];
const CHANNEL_FIELDS: Record<Exclude<Channel["provider"], "push">, { title: string; help: string; fields: FieldDef[] }> = {
  ntfy: {
    title: "ntfy",
    help: "Use a hard-to-guess topic on ntfy.sh, or your own ntfy server. Token or username/password only if the topic is protected.",
    fields: [["url", "Server URL", "https://ntfy.sh"], ["topic", "Topic", "fleetwatch-…"], ["token", "Access token", "tk_…", true], ["username", "Username", "optional"], ["password", "Password", "optional", true]],
  },
  telegram: {
    title: "Telegram",
    help: "Create a bot with @BotFather, send it a message, then use your chat ID (or a group ID starting with -100).",
    fields: [["botToken", "Bot token", "123456:ABC…", true], ["chatId", "Chat ID", "-100…"]],
  },
  discord: {
    title: "Discord",
    help: "Channel settings → Integrations → Webhooks → New webhook → Copy URL.",
    fields: [["webhook", "Webhook URL", "https://discord.com/api/webhooks/…", true]],
  },
  webhook: {
    title: "Custom webhook",
    help: "Fleetwatch POSTs a JSON body (event, severity, title, message, server, app, incident, link) to your URL, e.g. n8n, Zapier, Slack workflows, Home Assistant or your own endpoint. With a signing secret, requests carry X-Fleetwatch-Signature: sha256=<HMAC of the body>.",
    fields: [["url", "Webhook URL", "https://example.com/hooks/fleetwatch", true], ["secret", "Signing secret (optional)", "any random string", true]],
  },
};

function ChannelCard({ ch, onSaved }: { ch: Channel; onSaved: (l: Channel[]) => void }) {
  const { toast } = useStore();
  const def = CHANNEL_FIELDS[ch.provider as Exclude<Channel["provider"], "push">];
  const [enabled, setEnabled] = useState(ch.enabled);
  const [f, setF] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<"" | "save" | "test">("");
  const [res, setRes] = useState<{ ok: boolean; msg: string } | null>(null);
  const dirty = enabled !== ch.enabled || Object.keys(f).length > 0;
  const save = async () => {
    setBusy("save");
    try {
      const fields = Object.fromEntries(def.fields.map(([k, , , secret]) => [k, f[k] ?? (secret ? "" : ch.fields[k] ?? "")]));
      onSaved(await api<Channel[]>("PUT", `/api/notifications/channels/${ch.provider}`, { enabled, fields }));
      setF({});
      toast(`${def.title} settings saved`);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const test = async () => {
    setBusy("test");
    try {
      const h = await api<{ result: string; error?: string; latencyMs: number }>("POST", `/api/notifications/channels/${ch.provider}/test`);
      setRes(h.result === "delivered" ? { ok: true, msg: `Delivered in ${h.latencyMs} ms` } : { ok: false, msg: h.error ?? "Failed" });
    } catch (e) {
      setRes({ ok: false, msg: (e as ApiError).message });
    }
    setBusy("");
  };
  return (
    <Card>
      <SectionTitle icon={<Send size={15} />} title={def.title} sub={def.help} action={<Toggle checked={enabled} onChange={setEnabled} />} />
      <div className="grid gap-4 sm:grid-cols-2">
        {def.fields.map(([k, label, ph, secret]) => (
          <Field key={k} label={label} className={def.fields.length === 1 ? "sm:col-span-2" : undefined} hint={secret && ch.secrets[k] ? "Saved and encrypted. Leave empty to keep it." : undefined}>
            <Input
              type={secret ? "password" : "text"}
              autoComplete="off"
              className="font-mono"
              value={f[k] ?? (secret ? "" : ch.fields[k] ?? "")}
              placeholder={secret && ch.secrets[k] ? `•••••••• ${ch.fields[k + "Hint"] ?? ""}` : ph}
              onChange={(e) => setF({ ...f, [k]: e.target.value })}
            />
          </Field>
        ))}
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Button size="sm" disabled={!dirty || !!busy} onClick={save}>{busy === "save" && <Spinner size={13} />} Save</Button>
        <Button size="sm" variant="secondary" disabled={!ch.configured || dirty || !!busy} onClick={test}>{busy === "test" ? <Spinner size={13} /> : <Bell size={13} />} Send test</Button>
        {res && <span className={cn("text-xs", res.ok ? "text-ok" : "text-crit")}>{res.ok ? "✓" : "✕"} {res.msg}</span>}
        {!ch.configured && <span className="text-xs text-muted">Save the settings before testing.</span>}
      </div>
    </Card>
  );
}

const NOTIFY_RULE_ROWS: [string, string, string][] = [
  ["serverOffline", "Server offline", "Agent disconnected for longer than the grace period"],
  ["serverRecovered", "Server recovered", "Agent reconnected"],
  ["appOffline", "App offline", "Process stopped, errored or failed unexpectedly"],
  ["appRecovered", "App recovered", "Process is running again"],
  ["crashLoop", "Crash loop", "Process keeps restarting"],
  ["healthFailed", "Health check failed", "Consecutive failures reached"],
  ["healthRecovered", "Health check recovered", "Health check passes again"],
  ["resources", "CPU, RAM and disk", "Server resource thresholds exceeded"],
  ["repeatedErrors", "Repeated errors", "Same error repeated within the window"],
  ["otherIncidents", "Other incidents", "Expiring certificates and domains, repeated restarts, port no longer listening"],
  ["otherResolved", "Other recoveries", "Resolution of resource, error and other incidents"],
];

export function NotificationSettings() {
  const { toast } = useStore();
  const chans = useApi<Channel[]>("/api/notifications/channels");
  const rules = useApi<NotifyRules>("/api/settings/notify-rules");
  const [r, setR] = useState<NotifyRules | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (rules.data) setR(rules.data);
  }, [rules.data]);
  if (chans.error || rules.error) return <ErrorState message={(chans.error || rules.error)!} onRetry={() => { chans.reload(); rules.reload(); }} />;
  if (!chans.data || !r || !rules.data) return <LoadingState />;
  const saveRules = async () => {
    setBusy(true);
    try {
      rules.setData(await api<NotifyRules>("PUT", "/api/settings/notify-rules", r));
      toast("Notification rules saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-6">
      {chans.data.map((ch) =>
        ch.provider === "push" ? (
          <div key="push" className="space-y-6">
            <MobileCard />
            <AlertRulesCard />
            <PushCard enabled={ch.enabled} onChange={chans.reload} />
          </div>
        ) : (
          <ChannelCard key={ch.provider + JSON.stringify(ch)} ch={ch} onSaved={chans.setData} />
        ),
      )}
      <Card>
        <SectionTitle icon={<Bell size={15} />} title="Notification rules" sub="Which incident events send a notification. Per-server and per-app overrides live on their settings pages. A 10-minute cooldown prevents repeats." />
        <div className="divide-y divide-line">
          {NOTIFY_RULE_ROWS.map(([k, label, desc]) => <ToggleRow key={k} label={label} desc={desc} checked={!!r[k]} onChange={(v) => setR({ ...r, [k]: v })} />)}
        </div>
        <SaveBar dirty={JSON.stringify(r) !== JSON.stringify(rules.data)} busy={busy} onSave={saveRules} onReset={() => setR(rules.data)} />
      </Card>
    </div>
  );
}

/* ---------------- Incident rules ---------------- */
type IncidentRules = Record<string, number | boolean>;
const INCIDENT_RULE_ROWS: [string, string, string, string][] = [
  ["restartThreshold", "Restart count threshold", "restarts", "Open “Repeated Restarts” after this many restarts…"],
  ["restartWindowMin", "…within window", "minutes", "Window for the restart count"],
  ["repeatedErrorCount", "Repeated error count", "occurrences", "Same (normalized) error message grouped into one incident"],
  ["errorWindowMin", "Error grouping window", "minutes", "Errors within this window are counted together"],
  ["healthFailures", "Health consecutive failures", "failures", "Open “Health Check Failed” after N failures in a row"],
  ["cpuThreshold", "High CPU threshold", "%", "Server CPU above this…"],
  ["cpuMinutes", "…for", "minutes", "…for this long opens “High CPU”"],
  ["ramThreshold", "High RAM threshold", "%", "Server RAM above this…"],
  ["ramMinutes", "…for", "minutes", "…for this long opens “High RAM”"],
  ["diskThreshold", "Disk threshold", "%", "Any watched disk above this (critical at 95%)"],
  ["offlineGraceSec", "Agent offline grace", "seconds", "Disconnections shorter than this never open an incident"],
  ["sslWarnDays", "Certificate warning", "days ahead", "Alert this long before an SSL certificate expires (critical at 3 days)"],
  ["domainWarnDays", "Domain warning", "days ahead", "Alert this long before a domain registration expires (critical at 3 days)"],
];

export function IncidentRulesSettings() {
  const { toast } = useStore();
  const rules = useApi<IncidentRules>("/api/settings/incident-rules");
  const [f, setF] = useState<IncidentRules | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (rules.data) setF(rules.data);
  }, [rules.data]);
  if (rules.error) return <ErrorState message={rules.error} onRetry={rules.reload} />;
  if (!f || !rules.data) return <LoadingState />;
  const save = async () => {
    setBusy(true);
    try {
      rules.setData(await api<IncidentRules>("PUT", "/api/settings/incident-rules", f));
      toast("Incident rules saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-6">
    <Card>
      <SectionTitle icon={<Siren size={15} />} title="Incident detection rules" sub="Thresholds the central server uses to open incidents. Incidents resolve automatically when the condition clears." />
      <div className="divide-y divide-line">
        {INCIDENT_RULE_ROWS.map(([k, label, unit, desc]) => (
          <div key={k} className="flex flex-wrap items-center justify-between gap-3 py-3">
            <div><div className="text-sm font-medium">{label}</div><div className="text-xs text-muted">{desc}</div></div>
            <div className="flex items-center gap-2"><Input type="number" min={1} className="!w-24" value={f[k] as number} onChange={(e) => setF({ ...f, [k]: Number(e.target.value) })} /><span className="w-20 text-xs text-muted">{unit}</span></div>
          </div>
        ))}
        <ToggleRow label="Unusual activity" desc="Learns what each server normally does at each hour over two weeks, and opens “Unusual Activity” when CPU, memory or network traffic is far above that (e.g. 80% CPU at 3am on a server that idles at 10%)."
          checked={!!f.anomalies} onChange={(v) => setF({ ...f, anomalies: v })} />
      </div>
      <SaveBar dirty={JSON.stringify(f) !== JSON.stringify(rules.data)} busy={busy} onSave={save} onReset={() => setF(rules.data)} />
    </Card>
    <Runbooks />
    </div>
  );
}

/** "How to fix" steps per incident type: shown in the incident, its alerts and the phone app. */
function Runbooks() {
  const { toast } = useStore();
  const data = useApi<{ runbooks: Record<string, string>; types: Record<string, string> }>("/api/runbooks");
  const [edit, setEdit] = useState<{ type: string; text: string } | null>(null);
  if (!data.data) return null;
  const save = async () => {
    if (!edit) return;
    try {
      await api("PUT", `/api/runbooks/${edit.type}`, { text: edit.text });
      data.setData({ ...data.data!, runbooks: { ...data.data!.runbooks, [edit.type]: edit.text.trim() } });
      toast(edit.text.trim() ? "Runbook saved" : "Runbook removed");
      setEdit(null);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <Card>
      <SectionTitle icon={<BookOpen size={15} />} title="Runbooks" sub="Steps to fix each kind of incident. They appear in the incident, in its notifications and on the phone, so whoever is woken up knows what to do." />
      <div className="divide-y divide-line">
        {Object.entries(data.data.types).sort((a, b) => a[1].localeCompare(b[1])).map(([type, label]) => {
          const text = data.data!.runbooks[type];
          return (
            <div key={type} className="flex flex-wrap items-center justify-between gap-3 py-3">
              <div className="min-w-0 flex-1">
                <div className="text-sm font-medium">{label}</div>
                <div className="truncate text-xs text-muted">{text ? text.split("\n")[0] : "No runbook yet"}</div>
              </div>
              <Button size="sm" variant={text ? "ghost" : "secondary"} onClick={() => setEdit({ type, text: text ?? "" })}>{text ? "Edit" : "Write"}</Button>
            </div>
          );
        })}
      </div>
      {edit && (
        <Modal open wide onClose={() => setEdit(null)} title={`Runbook: ${data.data.types[edit.type]}`}
          footer={<><Button variant="ghost" onClick={() => setEdit(null)}>Cancel</Button><Button onClick={save}>Save</Button></>}>
          <Textarea rows={12} value={edit.text} onChange={(e) => setEdit({ ...edit, text: e.target.value })}
            placeholder={"1. Open the app's logs and look for the first error.\n2. Restart it from the Commands tab.\n3. Still failing? Roll back: git checkout the previous commit and restart.\n4. Call Sam: +33 …"} />
        </Modal>
      )}
    </Card>
  );
}

/* ---------------- System ---------------- */
export function SystemSettings() {
  const { toast } = useStore();
  const disc = useApi<{ roots: string[] }>("/api/settings/discovery");
  const health = useApi<{ version: string }>("/api/health");
  const [roots, setRoots] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (disc.data) setRoots(disc.data.roots.join("\n"));
  }, [disc.data]);
  if (disc.error) return <ErrorState message={disc.error} onRetry={disc.reload} />;
  if (roots === null || !disc.data) return <LoadingState />;
  const dirty = roots !== disc.data.roots.join("\n");
  const save = async () => {
    setBusy(true);
    try {
      disc.setData(await api<{ roots: string[] }>("PUT", "/api/settings/discovery", { roots: roots.split("\n").map((r) => r.trim()).filter(Boolean) }));
      toast("Discovery roots saved. Agents use them on their next scan.");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-6">
      <DashboardUsage />
      <Card>
        <SectionTitle icon={<Radar size={15} />} title="Discovery" sub="PM2 processes and systemd services are found wherever they live. These directories are additionally scanned for project folders (one level deep)." />
        <Field label="Project folder roots" hint="One absolute path per line, up to 10">
          <Textarea rows={4} value={roots} onChange={(e) => setRoots(e.target.value)} className="font-mono" />
        </Field>
        <SaveBar dirty={dirty} busy={busy} onSave={save} onReset={() => setRoots(disc.data!.roots.join("\n"))} />
      </Card>
      <Card>
        <SectionTitle icon={<Download size={15} />} title="Updates" sub="New Fleetwatch releases from GitHub, installed with one click" />
        <UpdateCard />
      </Card>
      <Card>
        <SectionTitle icon={<Info size={15} />} title="About" />
        <dl className="grid grid-cols-2 gap-y-2 text-sm">
          {[["Central server", `v${health.data?.version ?? "—"}`], ["Agent protocol", "v1 (JSON over WebSocket)"], ["Database", "SQLite (WAL) in OPS_DATA_DIR"]].map(([k, v]) => <div key={k} className="contents"><dt className="text-muted">{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </Card>
    </div>
  );
}

export { Copy };
