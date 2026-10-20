import { useState } from "react";
import { Link } from "react-router-dom";
import { SenderPicker, useSenders } from "../components/EmailSenders";
import { BadgeCheck, BarChart3, Globe, BellRing, Code2, FileCode2, Lock, Mail, Megaphone, Plus, Send, Sparkles, Trash2, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CodeBlock, ConfirmDialog, CopyButton, Field, IconButton, Input, Modal, SectionTitle, Segmented, Select, Spinner, Table, Td, Textarea, ToggleRow, Tr } from "../components/ui";
import { relTime } from "../mock/util";
import { STAGE_LABEL, type Config, type Notice, type Stage } from "./statusTypes";

type Set = (p: Partial<Config>) => void;

const NOTICE_TONE = { info: "info", maintenance: "info", degraded: "warn", outage: "crit", resolved: "ok" } as const;
const IMPACTS = ["info", "maintenance", "degraded", "outage"] as const;
const IMPACT_LABEL = { info: "Information", maintenance: "Maintenance", degraded: "Degraded", outage: "Outage", resolved: "Resolved" };
// datetime-local works in local time without a zone.
const toLocal = (ms: number) => (ms ? new Date(ms - new Date(ms).getTimezoneOffset() * 60_000).toISOString().slice(0, 16) : "");
const fromLocal = (s: string) => (s ? new Date(s).getTime() : 0);

/* ---------------- notices ---------------- */

type NoticeForm = { mode: "new" | "edit"; n: Notice; notify: boolean } | { mode: "update"; n: Notice; stage: Stage; body: string; notify: boolean };

export function NoticesTab({ c, set, onNotices }: { c: Config; set: Set; onNotices: (list: Notice[]) => void }) {
  const { toast } = useStore();
  const [form, setForm] = useState<NoticeForm | null>(null);
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState<Notice | null>(null);
  const refresh = async () => onNotices((await api<Config>("GET", `/api/status-pages/${c.id}`)).notices);
  const run = async (f: () => Promise<unknown>, done: string) => {
    setBusy(true);
    try {
      await f();
      await refresh();
      setForm(null);
      toast(done);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const submit = () => {
    if (!form) return;
    const base = `/api/status-pages/${c.id}/notices`;
    if (form.mode === "update") return run(() => api("POST", `${base}/${form.n.id}/updates`, { stage: form.stage, body: form.body, notify: form.notify }), "Update posted");
    if (form.mode === "new") return run(() => api("POST", base, { ...form.n, notify: form.notify }), form.n.draft ? "Draft saved" : "Notice posted");
    return run(() => api("PUT", `${base}/${form.n.id}`, { ...form.n, notify: form.notify }), "Notice saved");
  };
  const publish = (n: Notice) => run(() => api("PUT", `/api/status-pages/${c.id}/notices/${n.id}`, { ...n, draft: false, notify: true }), "Published");
  const remove = (n: Notice) => run(() => api("DELETE", `/api/status-pages/${c.id}/notices/${n.id}`), "Notice deleted");
  const blank = (): Notice => ({ id: "", title: "", body: "", status: "outage", start: Date.now(), end: 0, createdAt: 0, updates: [], draft: false });

  return (
    <div className="grid gap-6">
      <Card>
        <SectionTitle icon={<Sparkles size={15} />} title="Automatic notices" sub="Saved with the page." />
        <Field label="When a component goes down">
          <Segmented
            value={c.autoNotices}
            onChange={(v) => set({ autoNotices: v })}
            options={[
              { value: "off", label: "Do nothing" },
              { value: "draft", label: "Draft a notice, I publish it" },
              { value: "publish", label: "Publish a notice right away" },
            ]}
          />
        </Field>
        <p className="mt-2 text-xs text-muted">
          {c.autoNotices === "draft"
            ? "A draft appears here and your notification channels tell you; nothing is public until you press Publish. An unpublished draft disappears when the component works again."
            : c.autoNotices === "publish"
              ? "A notice goes up after about a minute of outage and is resolved when the component works again. Subscribers are told both times."
              : "Subscribers still hear when something goes down or comes back."}
        </p>
        <ToggleRow label="Post a notice when maintenance mode is turned on" desc="For the apps and servers on this page; it closes when maintenance ends." checked={!c.skipMaintNotices} onChange={(v) => set({ skipMaintNotices: !v })} />
      </Card>

      <Card>
        <SectionTitle icon={<Megaphone size={15} />}
          title="Notices"
          sub="Shown at the top of the page with their timeline. Changes here take effect right away."
          action={<Button size="sm" variant="secondary" onClick={() => setForm({ mode: "new", n: blank(), notify: true })}><Plus size={14} /> Notice</Button>}
        />
        {c.notices.length === 0 && <p className="py-6 text-center text-sm text-muted">No notices.</p>}
        <div className="divide-y divide-line">
          {c.notices.map((n) => (
            <div key={n.id} className="py-4">
              <div className="flex flex-wrap items-center gap-2">
                <Badge tone={NOTICE_TONE[n.status]}>{IMPACT_LABEL[n.status]}</Badge>
                {n.draft && <Badge tone="warn">Draft</Badge>}
                {n.auto && <Badge>automatic</Badge>}
                {n.maint && <Badge>maintenance switch</Badge>}
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{n.title}</span>
                {n.draft && <Button size="sm" disabled={busy} onClick={() => publish(n)}>Publish</Button>}
                {n.status !== "resolved" && (
                  <Button size="sm" variant="secondary" onClick={() => setForm({ mode: "update", n, stage: n.updates.length ? "monitoring" : "investigating", body: "", notify: !n.draft })}>
                    Post update
                  </Button>
                )}
                <Button size="sm" variant="ghost" onClick={() => setForm({ mode: "edit", n, notify: false })}>Edit</Button>
                <IconButton aria-label="Delete notice" onClick={() => setDel(n)}><Trash2 size={14} /></IconButton>
              </div>
              <div className="mt-1 text-xs text-muted">{new Date(n.start).toLocaleString()}{n.end ? ` – ${new Date(n.end).toLocaleString()}` : ""}</div>
              {(n.body || n.updates.length > 0) && (
                <ol className="mt-3 space-y-2 border-l border-line pl-4">
                  {[...n.updates].reverse().map((u, i) => (
                    <li key={i} className="text-sm">
                      <span className="font-medium">{STAGE_LABEL[u.stage]}</span> <span className="text-xs text-muted">{new Date(u.at).toLocaleString()}</span>
                      <div className="whitespace-pre-wrap text-muted">{u.body}</div>
                    </li>
                  ))}
                  {n.body && <li className="whitespace-pre-wrap text-sm text-muted">{n.body}</li>}
                </ol>
              )}
            </div>
          ))}
        </div>
      </Card>

      {form && (
        <Modal
          open
          onClose={() => setForm(null)}
          title={form.mode === "new" ? "New notice" : form.mode === "edit" ? "Edit notice" : `Update: ${form.n.title}`}
          footer={
            <>
              <Button variant="ghost" onClick={() => setForm(null)}>Cancel</Button>
              <Button disabled={busy || (form.mode === "update" ? !form.body.trim() : !form.n.title.trim() || (!!form.n.end && form.n.end < form.n.start))} onClick={submit}>
                {busy && <Spinner size={14} />} {form.mode === "update" ? "Post update" : form.mode === "new" && !form.n.draft ? "Post" : "Save"}
              </Button>
            </>
          }
        >
          {form.mode === "update" ? (
            <div className="space-y-4">
              <Field label="Stage">
                <Segmented value={form.stage} onChange={(v) => setForm({ ...form, stage: v })} options={(Object.keys(STAGE_LABEL) as Stage[]).map((s) => ({ value: s, label: STAGE_LABEL[s] }))} />
              </Field>
              <Field label="What is new" hint={form.stage === "resolved" ? "Resolving closes the notice; it stays visible for a day." : undefined}>
                <Textarea rows={4} maxLength={2000} value={form.body} onChange={(e) => setForm({ ...form, body: e.target.value })} placeholder="We found the cause and are rolling out a fix." />
              </Field>
              {!form.n.draft && <ToggleRow label="Tell subscribers" checked={form.notify} onChange={(v) => setForm({ ...form, notify: v })} />}
            </div>
          ) : (
            <div className="space-y-4">
              <Field label="Title">
                <Input value={form.n.title} maxLength={120} placeholder="Checkout is slow" onChange={(e) => setForm({ ...form, n: { ...form.n, title: e.target.value } })} />
              </Field>
              <Field label="Impact">
                <Segmented value={form.n.status === "resolved" ? "info" : form.n.status} onChange={(v) => setForm({ ...form, n: { ...form.n, status: v } })} options={IMPACTS.map((s) => ({ value: s, label: IMPACT_LABEL[s] }))} />
              </Field>
              {form.mode === "new" && (
                <Field label="First message" hint="It starts the notice's timeline; add updates later.">
                  <Textarea rows={3} maxLength={2000} value={form.n.body} onChange={(e) => setForm({ ...form, n: { ...form.n, body: e.target.value } })} />
                </Field>
              )}
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="From" hint="A future time makes it scheduled maintenance.">
                  <Input type="datetime-local" value={toLocal(form.n.start)} onChange={(e) => setForm({ ...form, n: { ...form.n, start: fromLocal(e.target.value) } })} />
                </Field>
                <Field label="Until (optional)" hint="Empty: until resolved.">
                  <Input type="datetime-local" value={toLocal(form.n.end)} onChange={(e) => setForm({ ...form, n: { ...form.n, end: fromLocal(e.target.value) } })} />
                </Field>
              </div>
              {form.mode === "new" && <ToggleRow label="Save as a draft" desc="Not public until you publish it." checked={form.n.draft} onChange={(v) => setForm({ ...form, n: { ...form.n, draft: v } })} />}
              {form.mode === "new" && !form.n.draft && <ToggleRow label="Tell subscribers" checked={form.notify} onChange={(v) => setForm({ ...form, notify: v })} />}
            </div>
          )}
        </Modal>
      )}
      <ConfirmDialog open={!!del} title="Delete notice?" message={`"${del?.title}" disappears from the page. Subscribers are not told.`} confirmLabel="Delete" danger onConfirm={() => del && (remove(del), setDel(null))} onCancel={() => setDel(null)} />
    </div>
  );
}

/* ---------------- subscribers ---------------- */

interface Subscriber {
  id: string;
  kind: "email" | "webhook";
  target: string;
  confirmed: boolean;
  createdAt: number;
  lastSentAt: number;
  lastError: string;
}

export function SubscribersTab({ c, set }: { c: Config; set: Set }) {
  const { toast } = useStore();
  const list = useApi<Subscriber[]>(`/api/status-pages/${c.id}/subscribers`);
  const senders = useSenders();
  const mailReady = (senders.data?.length ?? 0) > 0;
  const act = async (f: () => Promise<unknown>, done: string) => {
    try {
      await f();
      toast(done);
      list.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <div className="grid gap-6">
      <Card>
        <SectionTitle icon={<BellRing size={15} />} title="Let visitors subscribe" sub="A “Get updates” button on the page. Subscribers hear when something goes down or comes back, and about your notices. Saved with the page." />
        <ToggleRow label="By email" desc={mailReady ? "They confirm by email first; every message has an unsubscribe link." : "Add an email sender first (Integrations → Email)."} checked={c.subscribe} onChange={(v) => set({ subscribe: v })}>
          {!mailReady && <Link to="/integrations?tab=email"><Button size="sm" variant="ghost"><Mail size={14} /> Add a sender</Button></Link>}
        </ToggleRow>
        {mailReady && (
          <Field label="Send emails from" hint={<>Subscriptions, updates and this page's reports. Senders are in <Link to="/integrations?tab=email" className="underline">Integrations → Email</Link>.</>}>
            <SenderPicker value={c.sender ?? ""} onChange={(sender) => set({ sender })} />
          </Field>
        )}
        <ToggleRow label="By webhook" desc="For other systems: a JSON POST to their https:// URL. Addresses inside your own network are refused." checked={c.subscribeHooks} onChange={(v) => set({ subscribeHooks: v })} />
      </Card>
      <Card pad={false}>
        <div className="p-5 pb-0">
          <SectionTitle icon={<Users size={15} />} title={`Subscribers${list.data ? ` (${list.data.length})` : ""}`} />
        </div>
        {!list.data ? (
          <div className="p-5"><Spinner /></div>
        ) : list.data.length === 0 ? (
          <p className="p-5 pt-0 text-sm text-muted">Nobody yet.</p>
        ) : (
          <Table head={["Subscriber", "State", "Last message", ""]}>
            {list.data.map((x) => (
              <Tr key={x.id}>
                <Td>
                  <div className="max-w-[320px] truncate font-medium" title={x.target}>{x.target}</div>
                  <div className="text-xs text-muted">{x.kind} · since {new Date(x.createdAt).toLocaleDateString()}</div>
                </Td>
                <Td>{x.confirmed ? <Badge tone="ok">active</Badge> : <Badge tone="warn">not confirmed</Badge>}</Td>
                <Td className="text-xs">
                  {x.lastSentAt ? relTime(x.lastSentAt) : <span className="text-muted">none yet</span>}
                  {x.lastError && <div className="max-w-[260px] truncate text-crit" title={x.lastError}>{x.lastError}</div>}
                </Td>
                <Td className="whitespace-nowrap text-right">
                  {x.confirmed && <IconButton aria-label="Send a test message" title="Send a test message" onClick={() => act(() => api("POST", `/api/status-pages/${c.id}/subscribers/${x.id}/test`), "Test message sent")}><Send size={14} /></IconButton>}
                  <IconButton aria-label="Remove subscriber" onClick={() => act(() => api("DELETE", `/api/status-pages/${c.id}/subscribers/${x.id}`), "Subscriber removed")}><Trash2 size={14} /></IconButton>
                </Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  );
}

/* ---------------- access ---------------- */

export function AccessTab({ c, set }: { c: Config; set: Set }) {
  return (
    <Card>
      <SectionTitle icon={<Lock size={15} />} title="Who can see this page" sub="Saved with the page." />
      <Segmented
        value={c.access}
        onChange={(v) => set({ access: v })}
        options={[
          { value: "public", label: "Everyone" },
          { value: "password", label: "Anyone with the password" },
          { value: "team", label: "Only signed-in team members" },
        ]}
      />
      {c.access === "password" && (
        <Field label={c.hasPassword ? "New password (optional)" : "Password"} className="mt-4 max-w-sm" hint={c.hasPassword ? "A password is set. A new one signs everyone out of the page." : "At least 6 characters. Share it with your customer or team."}>
          <Input type="password" autoComplete="new-password" value={c.password ?? ""} onChange={(e) => set({ password: e.target.value })} />
        </Field>
      )}
      <ul className="mt-4 list-disc space-y-1 pl-5 text-xs text-muted">
        {c.access === "public" && <li>Shown to anyone with the link; the badge and the JSON data are public too.</li>}
        {c.access === "password" && <li>Visitors enter the password once per browser (remembered for 30 days).</li>}
        {c.access === "team" && <li>Only people signed in to this dashboard. On the page's own domain sign-in does not carry over, so team pages are meant for the dashboard address.</li>}
        {c.access !== "public" && <li>Private pages are hidden from search engines, and their badge is turned off.</li>}
      </ul>
    </Card>
  );
}

/* ---------------- share & reports ---------------- */

interface Report {
  page: string;
  month: string;
  rows: { group: string; name: string; uptime: number | null; downMinutes: number; outages: number }[];
}

export function ShareTab({ c, set, pageUrl, savedPublic }: { c: Config; set: Set; pageUrl: string; savedPublic: boolean }) {
  const { toast, me } = useStore();
  const months = Array.from({ length: 12 }, (_, i) => {
    const d = new Date();
    d.setUTCDate(1);
    d.setUTCMonth(d.getUTCMonth() - i);
    return d.toISOString().slice(0, 7);
  });
  const [month, setMonth] = useState(months[1]);
  const report = useApi<Report>(`/api/status-pages/${c.id}/report?month=${month}`);
  const [to, setTo] = useState(c.report.to || me?.email || "");
  const [sending, setSending] = useState(false);
  const origin = c.domain ? `https://${c.domain}` : location.origin;
  const badge = `${origin}/api/public/badge.svg${c.slug && !c.domain ? `?page=${c.slug}` : ""}`;
  const json = `${origin}/api/public/status${c.slug && !c.domain ? `/${c.slug}` : ""}`;
  const email = async () => {
    setSending(true);
    try {
      await api("POST", `/api/status-pages/${c.id}/report/email`, { month, to });
      toast("Report sent");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setSending(false);
  };
  return (
    <div className="grid gap-6 xl:grid-cols-2">
      <Card>
        <SectionTitle icon={<BadgeCheck size={15} />} title="Badge" sub="A live status image for your website or a GitHub README." />
        {savedPublic ? (
          <div className="space-y-3">
            <img src={`/api/public/badge.svg${c.slug ? `?page=${c.slug}` : ""}`} alt="Status badge" className="h-5" />
            <Field label="Markdown (GitHub README)">
              <CodeBlock>{`[![Status](${badge})](${pageUrl})`}</CodeBlock>
            </Field>
            <Field label="HTML (your website)">
              <CodeBlock>{`<a href="${pageUrl}"><img src="${badge}" alt="Status"></a>`}</CodeBlock>
            </Field>
            <p className="text-xs text-muted">
              Change the left text with <span className="font-mono">label=</span>, e.g. <span className="font-mono">{badge}{badge.includes("?") ? "&" : "?"}label=API</span>.
            </p>
            <Field label="Live data (JSON, for your own widget)">
              <div className="flex items-center gap-2"><Input readOnly value={json} /><CopyButton text={json} /></div>
            </Field>
          </div>
        ) : (
          <p className="text-sm text-muted">Available once the page is saved, turned on and visible to everyone.</p>
        )}
      </Card>

      <WidgetCard c={c} savedPublic={savedPublic} />

      <Card>
        <SectionTitle icon={<BarChart3 size={15} />} title="Uptime report" sub="Monthly uptime of every component (UTC)." />
        <div className="flex flex-wrap items-center gap-2">
          <Select className="max-w-[160px]" value={month} onChange={(e) => setMonth(e.target.value)}>
            {months.map((m) => <option key={m} value={m}>{m}{m === months[0] ? " (so far)" : ""}</option>)}
          </Select>
          <a href={`/api/status-pages/${c.id}/report?month=${month}&format=csv`} download><Button size="sm" variant="secondary">Download CSV</Button></a>
          <a href={`#/report/${c.id}/${month}?theme=light`} target="_blank" rel="noreferrer"><Button size="sm" variant="secondary">PDF (light)</Button></a>
          <a href={`#/report/${c.id}/${month}?theme=dark`} target="_blank" rel="noreferrer"><Button size="sm" variant="secondary">PDF (dark)</Button></a>
        </div>
        <div className="mt-3">
          {!report.data ? (
            report.error ? <p className="text-sm text-crit">{report.error}</p> : <Spinner />
          ) : report.data.rows.length === 0 ? (
            <p className="text-sm text-muted">No components on this page.</p>
          ) : (
            <table className="w-full text-sm">
              <tbody className="divide-y divide-line">
                {report.data.rows.map((r, i) => (
                  <tr key={i}>
                    <td className="py-1.5">{r.group ? <span className="text-muted">{r.group} / </span> : null}{r.name}</td>
                    <td className="py-1.5 text-right font-mono">{r.uptime === null ? "–" : `${r.uptime.toFixed(3)}%`}</td>
                    <td className="py-1.5 text-right text-xs text-muted">{r.downMinutes} min · {r.outages} outage{r.outages === 1 ? "" : "s"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-4">
          <Input className="min-w-0 flex-1" value={to} placeholder="boss@example.com, team@example.com" onChange={(e) => setTo(e.target.value)} />
          <Button size="sm" variant="secondary" disabled={!to.trim() || sending} onClick={email}>{sending ? <Spinner size={14} /> : <Mail size={14} />} Email it</Button>
        </div>
        <ToggleRow label="Email last month's report on the 1st of every month" desc={c.report.lastSent ? `Last sent: ${c.report.lastSent}` : "Saved with the page."} checked={c.report.monthly} onChange={(v) => set({ report: { ...c.report, monthly: v, to: c.report.to || to } })} />
        {c.report.monthly && (
          <Field label="Send to">
            <Input value={c.report.to} placeholder="boss@example.com" onChange={(e) => set({ report: { ...c.report, to: e.target.value } })} />
          </Field>
        )}
      </Card>
    </div>
  );
}

/* ---------------- custom code ---------------- */

export function CodeTab({ c, set }: { c: Config; set: Set }) {
  return (
    <div className="grid gap-6 xl:grid-cols-2">
      <Card>
        <SectionTitle icon={<Code2 size={15} />} title="Custom CSS" sub="Saved with the page. Styles only this page." />
        <Textarea rows={14} className="font-mono text-xs" spellCheck={false} maxLength={20000} value={c.customCss} placeholder={`.sp-banner { border-radius: 4px; }\n.sp-component { padding: 20px 24px; }`} onChange={(e) => set({ customCss: e.target.value })} />
        <p className="mt-2 text-xs text-muted">
          Hooks: <span className="font-mono">.sp-page .sp-header .sp-custom-header .sp-banner .sp-notice .sp-group .sp-component .sp-bars .sp-latency .sp-subscribe .sp-footer</span>. The brand colour is <span className="font-mono">var(--sp)</span>.
        </p>
      </Card>
      <Card>
        <SectionTitle icon={<FileCode2 size={15} />} title="Custom header HTML" sub="Shown above the status, e.g. your navigation or an announcement." />
        <Textarea rows={14} className="font-mono text-xs" spellCheck={false} maxLength={10000} value={c.headerHtml} placeholder={`<nav><a href="https://example.com">Home</a> · <a href="https://example.com/support">Support</a></nav>`} onChange={(e) => set({ headerHtml: e.target.value })} />
        <p className="mt-2 text-xs text-muted">Plain HTML: scripts, frames, forms and event handlers are removed, so it cannot run code on your visitors' browsers.</p>
      </Card>
    </div>
  );
}

/** The status widget for the admin's own website, and their chat's (Crisp, Intercom). */
export function WidgetCard({ c, savedPublic, title }: { c: { slug: string; domain: string }; savedPublic: boolean; title?: string }) {
  const [pos, setPos] = useState("bottom-right");
  const [mode, setMode] = useState("both");
  const [chat, setChat] = useState("auto");
  const attrs = [c.slug && !c.domain ? `data-page="${c.slug}"` : "", pos !== "bottom-right" ? `data-position="${pos}"` : "", mode !== "both" ? `data-mode="${mode}"` : "", chat !== "auto" ? `data-chat="${chat}"` : ""].filter(Boolean).join(" ");
  const snippet = `<script src="${c.domain ? `https://${c.domain}` : location.origin}/api/public/widget.js"${attrs ? " " + attrs : ""} async></script>`;
  return (
    <Card>
      <SectionTitle icon={<Globe size={15} />} title={title ?? "Website widget"} sub="A small status pill in a corner of your own website, and a card while a notice is open (“We know about the problem…”). Visitors chatting with you through Crisp or Intercom hear about it there too." />
      {!savedPublic ? (
        <p className="text-sm text-muted">Available once the page is saved, turned on and visible to everyone.</p>
      ) : (
        <div className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Corner"><Select value={pos} onChange={(e) => setPos(e.target.value)}><option value="bottom-right">Bottom right</option><option value="bottom-left">Bottom left</option></Select></Field>
            <Field label="Shows"><Select value={mode} onChange={(e) => setMode(e.target.value)}><option value="both">Pill and notices</option><option value="pill">Only the pill</option><option value="banner">Only notices</option></Select></Field>
            <Field label="Chat"><Select value={chat} onChange={(e) => setChat(e.target.value)}><option value="auto">Crisp or Intercom, if on the site</option><option value="crisp">Crisp</option><option value="intercom">Intercom</option><option value="none">None</option></Select></Field>
          </div>
          <Field label="Paste before </body> on your site">
            <div className="flex items-start gap-2"><CodeBlock>{snippet}</CodeBlock><CopyButton text={snippet} /></div>
          </Field>
          <ul className="space-y-1 text-xs text-muted">
            <li><b className="text-fg">Crisp:</b> while a notice is open, visitors see “We know about the problem: …” in the chat, once each.</li>
            <li><b className="text-fg">Intercom:</b> the widget sends the event <span className="font-mono">status-incident</span> (title, status, update, url). In Intercom, make a Workflow or Post triggered by it, showing your message, once.</li>
          </ul>
        </div>
      )}
    </Card>
  );
}
