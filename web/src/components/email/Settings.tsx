import { useEffect, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { CheckCircle2, Download, ExternalLink, RefreshCw, Trash2, XCircle } from "lucide-react";
import { api, ApiError, useApi } from "../../api/client";
import { useStore } from "../../store/store";
import { Badge, Button, Card, CodeBlock, ConfirmDialog, EmptyState, Field, Input, LoadingState, SearchInput, SectionTitle, Segmented, Select, Spinner } from "../ui";
import { fmtBytes } from "../DiskRescue";
import { fullDate, type Contact, type Job, type Options, type Overview } from "./data";

interface CFStatus {
  connected: boolean; signIn: boolean; accountId?: string; account?: string; worker?: string; url?: string; deployedAt?: number; edgeBytes: number; edgeLimit: number; error?: string;
  permissions: string[]; destinations: { email: string; verified: boolean }[];
}
interface SignIn { app: boolean; appEmail: boolean; accounts: number; account: string }

const EMAIL_SCOPES = [
  ["Email Routing Rules", "Write", "email-routing-rule.write"],
  ["Email Routing Addresses", "Write", "email-routing-address.write"],
  ["Workers Scripts", "Write", "workers-scripts.write"],
];

/** The way in: the dashboard's own "Sign in with Cloudflare", given the email permissions. */
function SignInSteps({ si, publicUrl, onUse, busy }: { si: SignIn; publicUrl: string; onUse: () => void; busy: boolean }) {
  const { toast } = useStore();
  const go = (allow: boolean) =>
    api<{ url: string }>("POST", "/api/emails/cloudflare/signin-url", { allow }).then((r) => (window.location.href = r.url)).catch((e: ApiError) => toast(e.message, "err"));
  const step = (n: number, done: boolean, title: string, body?: ReactNode) => (
    <li className="flex gap-3">
      <span className={done ? "mt-0.5 text-ok" : "mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-surface2 text-[11px] font-semibold"}>{done ? <CheckCircle2 size={18} /> : n}</span>
      <div className="min-w-0 flex-1">
        <div className={done ? "text-sm text-muted line-through" : "text-sm font-medium"}>{title}</div>
        {!done && body && <div className="mt-2 space-y-2 text-xs text-muted">{body}</div>}
      </div>
    </li>
  );
  const https = publicUrl.startsWith("https://") && !/localhost|127\.0\.0\.1/.test(publicUrl);
  return (
    <ol className="space-y-4">
      {step(1, si.app, "Set up Sign in with Cloudflare", <>
        <p>Done once, in Integrations: it is how the dashboard talks to your Cloudflare account, for sites and DNS too.</p>
        <Link to="/integrations?tab=cloudflare"><Button size="sm" variant="secondary">Open Integrations → Cloudflare</Button></Link>
      </>)}
      {si.app && step(2, si.appEmail, "Let the sign-in handle email", <>
        <p>
          Your sign-in app was made before email existed. In Cloudflare open{" "}
          <a className="underline" href="https://dash.cloudflare.com/?to=/:account/oauth-clients" target="_blank" rel="noreferrer">Manage account → OAuth clients <ExternalLink size={11} className="inline" /></a>,
          {" "}pick <b className="text-fg">Fleetwatch</b>, edit its scopes and add these three (leave them Required), then save:
        </p>
        <ul className="space-y-1">{EMAIL_SCOPES.map(([n, l, id]) => <li key={id}><Badge className="mr-1.5">{n} · {l}</Badge><code className="font-mono text-[11px]">{id}</code></li>)}</ul>
        <Button size="sm" onClick={() => go(true)}>I added them: sign in with Cloudflare</Button>
      </>)}
      {si.app && si.appEmail && step(3, !!si.account, "Sign in with Cloudflare", <>
        <p>{si.accounts ? "Your account was connected before email: sign in once more so Cloudflare grants the email permissions." : "Connect your Cloudflare account."} You come straight back here.</p>
        <Button size="sm" onClick={() => go(false)}>Sign in with Cloudflare</Button>
      </>)}
      {si.account && step(4, false, `Turn on email with ${si.account}`, <>
        {https ? <p>Deploys the mail Worker to your account. After that, add a domain and press <b className="text-fg">Set up in Cloudflare</b> on it.</p>
          : <p className="text-warn">Open the dashboard at its public https address first (or set OPS_PUBLIC_URL): Cloudflare's Worker must be able to reach it. Now: {publicUrl}</p>}
        <Button size="sm" onClick={onUse} disabled={busy || !https}>{busy && <Spinner size={13} />} Turn on email</Button>
      </>)}
    </ol>
  );
}

function Cloudflare() {
  const { toast } = useStore();
  const [params, setParams] = useSearchParams();
  const { data, reload, error } = useApi<{ status: CFStatus; publicUrl: string; signIn: SignIn }>("/api/emails/cloudflare");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState("");
  const [manual, setManual] = useState<{ secret: string; url: string } | null>(null);
  const [disc, setDisc] = useState(false);
  const useSignIn = async () => {
    setBusy("signin");
    try {
      const r = await api<{ error?: string }>("POST", "/api/emails/cloudflare/signin");
      toast(r.error ? "Connected, but: " + r.error : "Email is on: the Worker is deployed. Now add a domain.", r.error ? "err" : undefined);
      reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  // Back from Cloudflare's sign-in: say what happened, and finish by itself when it can.
  useEffect(() => {
    const back = params.get("cloudflare");
    if (!back || !data) return;
    const n = new URLSearchParams(params);
    n.delete("cloudflare");
    n.delete("message");
    setParams(n, { replace: true });
    if (back === "error") toast(params.get("message") || "Cloudflare sign-in failed", "err");
    else if (data.signIn.account && !data.status.connected) useSignIn();
    else if (back === "connected" && !data.signIn.account) toast("Signed in, but Cloudflare did not grant the email permissions: check step 2", "err");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, params]);
  if (error) return <p className="text-sm text-crit">{error}</p>;
  if (!data) return <LoadingState />;
  const st = data.status;
  const connect = async () => {
    setBusy("connect");
    try {
      const r = await api<{ error?: string }>("POST", "/api/emails/cloudflare", { token });
      setToken("");
      toast(r.error ? "Connected, but: " + r.error : "Cloudflare connected and the Worker deployed", r.error ? "err" : undefined);
      reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const deploy = async () => {
    setBusy("deploy");
    await api("POST", "/api/emails/cloudflare/deploy").then(() => toast("Worker deployed")).catch((e: ApiError) => toast(e.message, "err"));
    setBusy("");
    reload();
  };
  return (
    <Card>
      <SectionTitle title="Cloudflare" sub="Receiving: Email Routing hands every message to a small Worker, which stores it here and copies it to Gmail." />
      {st.connected ? (
        <div className="space-y-4 text-sm">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="ok" dot>Connected</Badge>
            <span className="text-muted">{st.account}{st.signIn ? " · through Sign in with Cloudflare" : " · with an API token"}</span>
            <span className="text-muted">· Worker <span className="font-mono">{st.worker}</span> {st.deployedAt ? `deployed ${fullDate(st.deployedAt)}` : "not deployed yet"}</span>
          </div>
          {st.error && (
            <div className="flex flex-wrap items-center gap-2 rounded-2xl bg-warn-soft px-4 py-2 text-xs text-warn">
              <span className="min-w-0 flex-1">{st.error}</span>
              {st.signIn && <Button size="sm" variant="secondary" onClick={() => api<{ url: string }>("POST", "/api/emails/cloudflare/signin-url", { allow: false }).then((r) => (window.location.href = r.url))}>Sign in again</Button>}
            </div>
          )}
          <div className="text-xs text-muted">
            Mail posts to <span className="font-mono">{st.url}/api/emails/inbound</span>. While the dashboard is down the Worker still copies mail to Gmail with what it knows ({st.edgeBytes} of {st.edgeLimit} bytes used).
          </div>
          <div>
            <div className="mb-1.5 text-xs font-medium text-muted">Gmail destinations (Cloudflare must verify each before it forwards)</div>
            {st.destinations.length === 0 ? <p className="text-xs text-muted">None yet: add Gmail copies on a domain or address.</p> : (
              <ul className="space-y-1">
                {st.destinations.map((d) => (
                  <li key={d.email} className="flex items-center gap-2 text-xs">
                    {d.verified ? <CheckCircle2 size={14} className="text-ok" /> : <XCircle size={14} className="text-warn" />} {d.email}
                    {!d.verified && <span className="text-muted">— waiting: open the confirmation email Cloudflare sent there</span>}
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="secondary" onClick={deploy} disabled={!!busy}>{busy === "deploy" ? <Spinner size={13} /> : <RefreshCw size={13} />} Deploy the Worker again</Button>
            <Button size="sm" variant="ghost" onClick={() => setDisc(true)}>Disconnect</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-5">
          <SignInSteps si={data.signIn} publicUrl={data.publicUrl} onUse={useSignIn} busy={busy === "signin"} />
          <details className="text-sm">
            <summary className="cursor-pointer text-muted">Or use an API token instead</summary>
            <div className="mt-3 space-y-3">
              <p className="text-xs text-muted">
                Create one at{" "}
                <a className="underline" href="https://dash.cloudflare.com/profile/api-tokens" target="_blank" rel="noreferrer">dash.cloudflare.com → API Tokens <ExternalLink size={11} className="inline" /></a>{" "}
                (Create custom token) with these permissions:
              </p>
              <ul className="list-disc space-y-0.5 pl-5 text-xs text-muted">{st.permissions.map((p) => <li key={p}>{p}</li>)}</ul>
              <div className="flex flex-wrap gap-2">
                <Input type="password" value={token} onChange={(e) => setToken(e.target.value)} placeholder="Paste the API token" className="max-w-md" aria-label="Cloudflare API token" />
                <Button onClick={connect} disabled={!token || !!busy}>{busy === "connect" && <Spinner size={14} />} Connect</Button>
              </div>
            </div>
          </details>
        </div>
      )}
      <details className="mt-5 text-sm">
        <summary className="cursor-pointer text-muted">Manual setup (deploy the Worker yourself with Wrangler)</summary>
        <div className="mt-3 space-y-2 text-xs text-muted">
          <p>The Worker is in the repository at <span className="font-mono">cloudflare/email-worker</span>. Its README lists every step.</p>
          {manual ? (
            <>
              <CodeBlock>{`FLEETWATCH_URL = "${manual.url}"\nwrangler secret put INBOUND_SECRET   # paste: ${manual.secret}`}</CodeBlock>
              <p>Keep this secret private: anyone with it can put mail in your inboxes.</p>
            </>
          ) : (
            <Button size="sm" variant="secondary" onClick={() => api<{ secret: string; url: string }>("POST", "/api/emails/cloudflare/manual-secret").then(setManual).catch((e: ApiError) => toast(e.message, "err"))}>Show the Worker's secret</Button>
          )}
        </div>
      </details>
      <ConfirmDialog open={disc} title="Disconnect Cloudflare?" message="Email stops using this Cloudflare connection. The deployed Worker keeps receiving mail, but forwarding changes will no longer reach it." confirmLabel="Disconnect"
        onCancel={() => setDisc(false)} onConfirm={() => (setDisc(false), api("DELETE", "/api/emails/cloudflare").then(reload))} />
    </Card>
  );
}

function Sending({ ov, reload }: { ov: Overview; reload: () => void }) {
  const { toast } = useStore();
  const [o, setO] = useState<Options>(ov.options);
  const save = () => api("PUT", "/api/emails/options", o).then(() => (toast("Saved"), reload())).catch((e: ApiError) => toast(e.message, "err"));
  return (
    <Card>
      <SectionTitle title="Sending and keeping" sub={<>Mail goes out through the senders of <Link className="underline" to="/integrations?tab=email">Integrations → Email</Link> (Resend for your own domain, or any SMTP). Each domain picks one in its settings.</>} />
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Undo send (seconds)" hint="A sent message waits this long, so Undo can stop it. Once handed to the provider it cannot be recalled.">
          <Input type="number" min={0} max={60} value={o.undoSeconds} onChange={(e) => setO({ ...o, undoSeconds: Number(e.target.value) })} />
        </Field>
        <Field label="Empty Trash after (days)" hint="0 keeps it forever.">
          <Input type="number" min={0} value={o.trashDays} onChange={(e) => setO({ ...o, trashDays: Number(e.target.value) })} />
        </Field>
        <Field label="Empty Spam after (days)" hint="0 keeps it forever. Nothing else is ever deleted by itself.">
          <Input type="number" min={0} value={o.spamDays} onChange={(e) => setO({ ...o, spamDays: Number(e.target.value) })} />
        </Field>
      </div>
      <Button className="mt-4" onClick={save}>Save</Button>
    </Card>
  );
}

interface Grant { memberId: string; scopeKind: "domain" | "address"; scopeId: string; level: "read" | "send" | "manage" }

function Team({ ov }: { ov: Overview }) {
  const { toast } = useStore();
  const { data, reload } = useApi<{ grants: Grant[]; operators: { id: string; username: string; displayName: string }[] }>("/api/emails/grants");
  const [edit, setEdit] = useState<Record<string, Grant[]>>({});
  useEffect(() => {
    if (!data) return;
    const by: Record<string, Grant[]> = {};
    for (const op of data.operators) by[op.id] = data.grants.filter((g) => g.memberId === op.id);
    setEdit(by);
  }, [data]);
  if (!data) return null;
  const scopes = [...ov.domains.map((d) => ({ kind: "domain" as const, id: d.id, label: `All of ${d.name}` })), ...ov.addresses.map((a) => ({ kind: "address" as const, id: a.id, label: a.address }))];
  const save = (id: string) => api("PUT", `/api/emails/grants/${id}`, edit[id].filter((g) => g.scopeId)).then(() => (toast("Access saved"), reload())).catch((e: ApiError) => toast(e.message, "err"));
  return (
    <Card>
      <SectionTitle title="Team access" sub="Mail is yours alone until you share it. Operators can be given a domain or single addresses: read (and organise), send, or manage the addresses. Viewers and clients never see mail." />
      {!data.operators.length ? <p className="text-sm text-muted">No operators in your team. Add one in <Link className="underline" to="/team">Team</Link>.</p> : (
        <div className="space-y-5">
          {data.operators.map((op) => (
            <div key={op.id} className="rounded-2xl border border-line p-4">
              <div className="mb-3 text-sm font-medium">{op.displayName || op.username}</div>
              {(edit[op.id] ?? []).map((g, i) => (
                <div key={i} className="mb-2 flex flex-wrap gap-2">
                  <Select value={`${g.scopeKind}:${g.scopeId}`} onChange={(e) => { const [k, id] = e.target.value.split(":"); setEdit({ ...edit, [op.id]: edit[op.id].map((x, j) => (j === i ? { ...x, scopeKind: k as Grant["scopeKind"], scopeId: id } : x)) }); }} className="min-w-0 flex-1" aria-label="What">
                    <option value=":">Pick a domain or address…</option>
                    {scopes.map((sc) => <option key={sc.kind + sc.id} value={`${sc.kind}:${sc.id}`}>{sc.label}</option>)}
                  </Select>
                  <Select value={g.level} onChange={(e) => setEdit({ ...edit, [op.id]: edit[op.id].map((x, j) => (j === i ? { ...x, level: e.target.value as Grant["level"] } : x)) })} className="w-36" aria-label="Level">
                    <option value="read">Read</option>
                    <option value="send">Read + send</option>
                    <option value="manage">Manage</option>
                  </Select>
                  <Button variant="ghost" size="sm" onClick={() => setEdit({ ...edit, [op.id]: edit[op.id].filter((_, j) => j !== i) })} aria-label="Remove"><Trash2 size={13} /></Button>
                </div>
              ))}
              <div className="flex gap-2">
                <Button size="sm" variant="secondary" onClick={() => setEdit({ ...edit, [op.id]: [...(edit[op.id] ?? []), { memberId: op.id, scopeKind: "domain", scopeId: "", level: "read" }] })}>Add access</Button>
                <Button size="sm" onClick={() => save(op.id)}>Save</Button>
              </div>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

function SpamLists() {
  const { toast } = useStore();
  const { data, reload } = useApi<{ id: string; scope: string; pattern: string; action: "allow" | "block" }[]>("/api/emails/spam-rules");
  const [pattern, setPattern] = useState("");
  const [action, setAction] = useState<"allow" | "block">("block");
  const add = () => api("POST", "/api/emails/spam-rules", { pattern, action, scope: "" }).then(() => (setPattern(""), reload())).catch((e: ApiError) => toast(e.message, "err"));
  return (
    <Card>
      <SectionTitle title="Allow and block lists" sub="Allowed senders skip the spam check; blocked ones always go to Spam (kept, never deleted). &quot;Not spam&quot; on a message adds its sender for that inbox." />
      <div className="mb-4 flex flex-wrap gap-2">
        <Input value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="someone@example.com or @example.com" className="min-w-0 flex-1" aria-label="Sender or domain" />
        <Segmented value={action} onChange={setAction} options={[{ value: "block", label: "Block" }, { value: "allow", label: "Allow" }]} />
        <Button onClick={add} disabled={!pattern}>Add</Button>
      </div>
      {!data?.length ? <p className="text-sm text-muted">No rules yet.</p> : (
        <ul className="divide-y divide-line text-sm">
          {data.map((r) => (
            <li key={r.id} className="flex items-center gap-3 py-2">
              <Badge tone={r.action === "block" ? "crit" : "ok"}>{r.action}</Badge>
              <span className="min-w-0 flex-1 truncate">{r.pattern}</span>
              {r.scope && <span className="text-xs text-muted">one inbox</span>}
              <Button size="sm" variant="ghost" onClick={() => api("DELETE", `/api/emails/spam-rules/${r.id}`).then(reload)} aria-label={`Remove ${r.pattern}`}><Trash2 size={13} /></Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function Contacts() {
  const { toast } = useStore();
  const [q, setQ] = useState("");
  const { data, reload } = useApi<Contact[]>("/api/emails/contacts?q=" + encodeURIComponent(q));
  const [edit, setEdit] = useState<Partial<Contact> | null>(null);
  const save = () => edit && api("POST", edit.id ? `/api/emails/contacts/${edit.id}` : "/api/emails/contacts", edit).then(() => (setEdit(null), reload())).catch((e: ApiError) => toast(e.message, "err"));
  return (
    <Card>
      <SectionTitle title="Contacts" sub="Filled in by itself from who writes to you and who you write to; the composer suggests them." action={<Button size="sm" variant="secondary" onClick={() => setEdit({ address: "", name: "" })}>New contact</Button>} />
      <SearchInput value={q} onChange={setQ} placeholder="Search contacts…" className="mb-3" />
      {edit && (
        <div className="mb-4 grid gap-2 rounded-2xl border border-line p-4 sm:grid-cols-2">
          <Input value={edit.address ?? ""} onChange={(e) => setEdit({ ...edit, address: e.target.value })} placeholder="Email" aria-label="Email" />
          <Input value={edit.name ?? ""} onChange={(e) => setEdit({ ...edit, name: e.target.value })} placeholder="Name" aria-label="Name" />
          <Input value={edit.company ?? ""} onChange={(e) => setEdit({ ...edit, company: e.target.value })} placeholder="Company" aria-label="Company" />
          <Input value={edit.notes ?? ""} onChange={(e) => setEdit({ ...edit, notes: e.target.value })} placeholder="Notes" aria-label="Notes" />
          <div className="flex gap-2"><Button size="sm" onClick={save}>Save</Button><Button size="sm" variant="ghost" onClick={() => setEdit(null)}>Cancel</Button></div>
        </div>
      )}
      {!data ? <LoadingState /> : !data.length ? <p className="text-sm text-muted">No contacts{q ? " match" : " yet"}.</p> : (
        <ul className="max-h-96 divide-y divide-line overflow-y-auto text-sm">
          {data.map((c) => (
            <li key={c.id} className="flex items-center gap-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium">{c.name || c.address}</div>
                <div className="truncate text-xs text-muted">{c.address}{c.company && ` · ${c.company}`}</div>
              </div>
              <span className="text-xs text-muted">{c.uses}×</span>
              <Button size="sm" variant="ghost" onClick={() => setEdit(c)}>Edit</Button>
              <Button size="sm" variant="ghost" onClick={() => api("DELETE", `/api/emails/contacts/${c.id}`).then(reload)} aria-label={`Delete ${c.address}`}><Trash2 size={13} /></Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

interface Usage { bytes: number; messages: number; domains: Record<string, number>; folders: Record<string, number>; largest: { id: number; subject: string; size: number; from: { address: string } }[]; retain: Record<string, { messages: number; bytes: number }>; dir: string }

function Storage({ ov }: { ov: Overview }) {
  const { data } = useApi<{ usage: Usage; database: string; key: string }>("/api/emails/storage");
  if (!data) return null;
  const u = data.usage;
  return (
    <Card>
      <SectionTitle title="Storage, backups and export" sub={`${u.messages} messages, ${fmtBytes(u.bytes)} on this server.`} action={<a href="/api/emails/export"><Button size="sm" variant="secondary"><Download size={13} /> Export everything (MBOX)</Button></a>} />
      <div className="grid gap-4 text-sm sm:grid-cols-2">
        <div>
          <div className="mb-1.5 text-xs font-medium text-muted">By domain</div>
          {ov.domains.map((d) => <div key={d.id} className="flex justify-between py-0.5"><span>{d.name}</span><span className="text-muted">{fmtBytes(u.domains[d.id] ?? 0)}</span></div>)}
          <div className="mb-1.5 mt-4 text-xs font-medium text-muted">By folder</div>
          {Object.entries(u.folders).map(([f, n]) => <div key={f} className="flex justify-between py-0.5"><span className="capitalize">{f}</span><span className="text-muted">{fmtBytes(n)}</span></div>)}
          {Object.entries(u.retain).map(([f, p]) => <p key={f} className="mt-2 text-xs text-warn">The keep rule will empty {p.messages} messages ({fmtBytes(p.bytes)}) from {f} within the hour.</p>)}
        </div>
        <div>
          <div className="mb-1.5 text-xs font-medium text-muted">Largest messages</div>
          {u.largest.map((m) => <div key={m.id} className="flex justify-between gap-3 py-0.5"><span className="truncate">{m.subject || "(no subject)"}</span><span className="shrink-0 text-muted">{fmtBytes(m.size)}</span></div>)}
        </div>
      </div>
      <div className="mt-5 rounded-2xl bg-surface2 p-4 text-xs text-muted">
        <div className="mb-1 font-medium text-fg">Backups</div>
        Add these to a <Link className="underline" to="/backups">backup</Link> of the dashboard's server, in the same plan so they are taken together: the database <span className="font-mono">{data.database}</span> (as SQLite) and the folder <span className="font-mono">{u.dir}</span>.
        The mail files are encrypted with <span className="font-mono">{data.key}</span> (or OPS_SECRET_KEY): keep a copy of it somewhere safe, apart from the backups. Without it a restore cannot be read.
      </div>
    </Card>
  );
}

interface Failure { id: number; rcpt: string; mailFrom: string; size: number; error: string; receivedAt: number; kept: boolean }

function Recovery() {
  const { toast } = useStore();
  const f = useApi<Failure[]>("/api/emails/failures");
  const o = useApi<Job[]>("/api/emails/outbox");
  const stuck = (o.data ?? []).filter((j) => j.state === "failed" || j.state === "unknown" || (j.state === "scheduled" && j.attempts > 0));
  const retry = (id: number) => api("POST", `/api/emails/failures/${id}/retry`).then(() => (toast("Filed"), f.reload())).catch((e: ApiError) => toast(e.message, "err"));
  return (
    <Card>
      <SectionTitle title="Recovery" sub="Mail that arrived but could not be filed is kept here to try again, and outgoing mail that did not go out." />
      <div className="mb-2 text-xs font-medium text-muted">Incoming</div>
      {!f.data?.length ? <p className="mb-4 text-sm text-muted">Nothing waiting.</p> : (
        <ul className="mb-5 divide-y divide-line text-sm">
          {f.data.map((x) => (
            <li key={x.id} className="flex flex-wrap items-center gap-3 py-2">
              <div className="min-w-0 flex-1">
                <div className="truncate">{x.mailFrom || "unknown sender"} → {x.rcpt}</div>
                <div className="truncate text-xs text-muted">{fullDate(x.receivedAt)} · {fmtBytes(x.size)} · {x.error}</div>
              </div>
              {x.kept ? <Button size="sm" variant="secondary" onClick={() => retry(x.id)}>Try again</Button> : <Badge tone="warn">copy lost</Badge>}
              <Button size="sm" variant="ghost" onClick={() => api("DELETE", `/api/emails/failures/${x.id}`).then(f.reload)}>Dismiss</Button>
            </li>
          ))}
        </ul>
      )}
      <div className="mb-2 text-xs font-medium text-muted">Outgoing</div>
      {!stuck.length ? <p className="text-sm text-muted">Nothing stuck.</p> : (
        <ul className="divide-y divide-line text-sm">
          {stuck.map((j) => (
            <li key={j.id} className="flex flex-wrap items-center gap-3 py-2">
              <Badge tone={j.state === "failed" ? "crit" : "warn"}>{j.state}</Badge>
              <div className="min-w-0 flex-1">
                <div className="truncate">{j.subject || "(no subject)"} <span className="text-muted">from {j.from}</span></div>
                <div className="truncate text-xs text-muted">{j.attempts} attempts · {j.lastError}</div>
              </div>
              {j.state === "failed" && <Button size="sm" variant="secondary" onClick={() => api("POST", `/api/emails/outbox/${j.id}/retry`, { force: false }).then(o.reload).catch((e: ApiError) => toast(e.message, "err"))}>Retry</Button>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function EmailSettings({ ov, reload }: { ov: Overview; reload: () => void }) {
  if (!ov.owner) return <EmptyState title="Settings are the owner's" />;
  return (
    <div className="space-y-6">
      <Cloudflare />
      <Sending ov={ov} reload={reload} />
      <Team ov={ov} />
      <SpamLists />
      <Contacts />
      <Storage ov={ov} />
      <Recovery />
    </div>
  );
}
