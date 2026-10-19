import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Check, Info, KeyRound, MessageSquareText, MoonStar, PhoneCall, QrCode, Send, Smartphone, Trash2, Users, X } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, Field, Input, SectionTitle, Select, Spinner, WhatsApp } from "./ui";

export interface WaPerson {
  phone: string;
  language: string;
  quietFrom?: string;
  quietTo?: string;
  timeZone?: string;
  briefing?: string;
}
interface WaStatus {
  linked: boolean;
  connected: boolean;
  number?: string;
  name?: string;
  qr?: string;
  code?: string;
  error?: string;
}

export const LANGUAGES: Record<string, string> = {
  "": "Follow the conversation",
  en: "English",
  fr: "French",
  ar: "Arabic",
  ary: "Moroccan Arabic (Darija)",
  es: "Spanish",
  de: "German",
  it: "Italian",
  pt: "Portuguese",
  tr: "Turkish",
  nl: "Dutch",
};

const zone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;

/** Integrations → WhatsApp: the owner links their WhatsApp and sets how each person is reached. */
export function WhatsAppSetup() {
  const { toast } = useStore();
  const data = useApi<{ status: WaStatus; people: (WaPerson & { id: string; name: string })[]; options: WaOptions }>("/api/integrations/whatsapp");
  const [phone, setPhone] = useState("");
  const [byPhone, setByPhone] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [unlink, setUnlink] = useState(false);
  const st = data.data?.status;
  const { reload } = data;
  const linking = !!st && !st.linked && (waiting || !!st.qr || !!st.code);
  useEffect(() => {
    if (!linking) return;
    const t = setInterval(reload, 2000);
    return () => clearInterval(t);
  }, [linking, reload]);
  useEffect(() => {
    if (st && (st.linked || st.error || st.qr || st.code)) setWaiting(false);
  }, [st]);
  const start = async () => {
    setWaiting(true);
    try {
      await api("POST", "/api/integrations/whatsapp/link", { phone: byPhone ? phone : "" });
      reload();
    } catch (e) {
      setWaiting(false);
      toast((e as ApiError).message, "err");
    }
  };
  if (!st) return <Card><Spinner /></Card>;
  return (
    <>
      <Card>
        <SectionTitle icon={<WhatsApp size={18} />} title="Your WhatsApp"
          sub="Link your own WhatsApp, the way WhatsApp Web is linked. On-call alerts call the team from it and send the details as a message, and the team can call it or write to it (voice notes too) to ask the AI about anything: only from the numbers saved below, others get no answer."
          action={st.linked ? <Badge tone={st.connected ? "ok" : "warn"} dot>{st.connected ? "Connected" : "Reconnecting"}</Badge> : undefined} />
        {st.linked ? (
          <div className="flex flex-wrap items-center gap-3">
            <span className="grid h-11 w-11 place-items-center rounded-2xl bg-surface2"><WhatsApp size={22} /></span>
            <div className="min-w-0 flex-1">
              <div className="font-medium">{st.name || "WhatsApp"}</div>
              <div className="font-mono text-xs text-muted">+{st.number}</div>
            </div>
            <Button size="sm" variant="ghost" className="text-crit" onClick={() => setUnlink(true)}><Trash2 size={13} /> Unlink</Button>
          </div>
        ) : st.qr ? (
          <div className="flex flex-col items-center gap-3 text-center sm:flex-row sm:text-left">
            <img src={st.qr} alt="QR code to link WhatsApp" className="h-56 w-56 rounded-2xl bg-white p-2" />
            <ol className="list-decimal space-y-1.5 pl-5 text-left text-sm text-muted">
              <li>Open <b className="text-fg">WhatsApp</b> on your phone.</li>
              <li><b className="text-fg">Settings → Linked devices → Link a device</b>.</li>
              <li>Point the phone at this code. It changes every few seconds; that is normal.</li>
            </ol>
          </div>
        ) : st.code ? (
          <div className="space-y-3 text-center">
            <div className="font-mono text-3xl font-semibold tracking-[0.3em]">{st.code.slice(0, 4)}-{st.code.slice(4)}</div>
            <p className="text-sm text-muted">On that phone: WhatsApp → <b className="text-fg">Settings → Linked devices → Link a device → Link with phone number instead</b>, then type this code. WhatsApp may also show a notification to tap.</p>
          </div>
        ) : (
          <div className="space-y-3">
            {st.error && <p className="flex items-center gap-2 text-xs text-crit"><AlertTriangle size={13} />{st.error}</p>}
            {byPhone && (
              <Field label="Your WhatsApp number" hint="With the country code. WhatsApp sends a code to type on that phone: handy when this page is open on the phone itself.">
                <Input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="+212 6 12 34 56 78" autoComplete="tel" />
              </Field>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <Button onClick={start} disabled={waiting || (byPhone && !phone.trim())}>
                {waiting ? <Spinner size={14} /> : byPhone ? <Smartphone size={14} /> : <QrCode size={14} />} {byPhone ? "Get the code" : "Show the QR code"}
              </Button>
              <button className="text-xs font-medium text-muted hover:text-fg" onClick={() => setByPhone(!byPhone)}>{byPhone ? "Scan a QR code instead" : "Link with a phone number instead"}</button>
            </div>
            <p className="flex gap-2 text-[11px] text-muted"><Info size={12} className="mt-px shrink-0" />WhatsApp does not allow this officially: an account that sends many automated messages can be banned. It is your own number, so keep it to your team.</p>
          </div>
        )}
      </Card>
      <PeopleCard people={data.data!.people} linked={st.linked} onChange={reload} />
      {st.linked && <OptionsCard saved={data.data!.options} onSaved={reload} />}
      {st.linked && <MessagesCard />}
      {st.linked && <Card><SectionTitle icon={<KeyRound size={15} />} title="Your action PIN" sub="What you say or write to confirm an action on WhatsApp, when PINs are asked for." /><PinField /></Card>}
      <ConfirmDialog open={unlink} title="Unlink WhatsApp?" confirmLabel="Unlink" danger
        message="The dashboard is signed out of your WhatsApp and stops sending from it. People's numbers are kept."
        onCancel={() => setUnlink(false)} onConfirm={async () => { setUnlink(false); await api("DELETE", "/api/integrations/whatsapp"); reload(); toast("WhatsApp unlinked"); }} />
    </>
  );
}

function PeopleCard({ people, linked, onChange }: { people: (WaPerson & { id: string; name: string })[]; linked: boolean; onChange: () => void }) {
  return (
    <Card>
      <SectionTitle icon={<Users size={15} />} title="Who gets WhatsApp alerts"
        sub={<>Add each person's number the first time; they can change it later in their account. Who is alerted, in which order and for which incidents is set in <Link to="/oncall" className="underline">On-call</Link>.</>} />
      <div className="space-y-3">
        {people.map((p) => <PersonRow key={p.id} id={p.id} name={p.name} saved={p} linked={linked} onSaved={onChange} />)}
      </div>
    </Card>
  );
}

function PersonRow({ id, name, saved, linked, onSaved }: { id: string; name: string; saved: WaPerson; linked: boolean; onSaved: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-2xl border border-line p-3">
      <button className="flex w-full flex-wrap items-center gap-3 text-left" onClick={() => setOpen(!open)}>
        <span className="min-w-0 flex-1 font-medium">{name}{id === "owner" && <span className="ml-2 text-xs font-normal text-muted">you</span>}</span>
        {saved.phone ? <span className="font-mono text-xs text-muted">+{saved.phone}</span> : <span className="text-xs text-muted">No number</span>}
        {saved.quietFrom && <Badge><MoonStar size={11} /> {saved.quietFrom}–{saved.quietTo}</Badge>}
      </button>
      {open && <PersonForm saved={saved} url={`/api/integrations/whatsapp/people/${id}`} testUrl={linked ? `/api/integrations/whatsapp/people/${id}/test` : undefined} callUrl={linked ? `/api/integrations/whatsapp/people/${id}/call` : undefined} self={id === "owner"} onSaved={() => { onSaved(); setOpen(false); }} />}
    </div>
  );
}

/** Someone's number, language and quiet hours. */
export function PersonForm({ saved, url, testUrl, callUrl, self, onSaved }: { saved: WaPerson; url: string; testUrl?: string; callUrl?: string; self?: boolean; onSaved: () => void }) {
  const { toast } = useStore();
  const [f, setF] = useState<WaPerson>({ ...saved, timeZone: saved.timeZone || zone() });
  const [busy, setBusy] = useState("");
  const quiet = !!f.quietFrom;
  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what);
    try {
      await fn();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  return (
    <div className="mt-3 space-y-3 border-t border-line pt-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="WhatsApp number" hint={self ? "Not the one the dashboard sends from: WhatsApp cannot message itself." : "With the country code."}>
          <Input value={f.phone} onChange={(e) => setF({ ...f, phone: e.target.value })} placeholder="+212 6 12 34 56 78" autoComplete="tel" />
        </Field>
        <Field label="Language" hint="The AI on calls speaks it, and follows if they switch.">
          <Select value={f.language} onChange={(e) => setF({ ...f, language: e.target.value })}>
            {Object.entries(LANGUAGES).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </Select>
        </Field>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={quiet} onChange={(e) => setF({ ...f, quietFrom: e.target.checked ? "22:00" : "", quietTo: e.target.checked ? "07:00" : "" })} />
        Quiet hours (no WhatsApp; push notifications still come)
      </label>
      {quiet && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="From"><Input type="time" value={f.quietFrom} onChange={(e) => setF({ ...f, quietFrom: e.target.value })} /></Field>
          <Field label="Until"><Input type="time" value={f.quietTo} onChange={(e) => setF({ ...f, quietTo: e.target.value })} /></Field>
        </div>
      )}
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={!!f.briefing} onChange={(e) => setF({ ...f, briefing: e.target.checked ? "08:00" : "" })} />
        Morning briefing (a WhatsApp summary of the last 24 hours)
      </label>
      {f.briefing && <Field label="At" className="sm:w-40"><Input type="time" value={f.briefing} onChange={(e) => setF({ ...f, briefing: e.target.value })} /></Field>}
      {(quiet || f.briefing) && (
        <Field label="Time zone" hint="For the quiet hours and the briefing." className="sm:w-72">
          <Input value={f.timeZone} onChange={(e) => setF({ ...f, timeZone: e.target.value })} placeholder="Africa/Casablanca" />
        </Field>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        {callUrl && saved.phone && (
          <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => run("call", async () => {
            toast("Calling on WhatsApp: answer it to hear the alert");
            const r = await api<{ answered: boolean }>("POST", callUrl);
            toast(r.answered ? "The call was answered" : "Nobody answered the call", r.answered ? undefined : "err");
          })}>
            {busy === "call" ? <Spinner size={13} /> : <PhoneCall size={13} />} {busy === "call" ? "Ringing…" : "Call me now"}
          </Button>
        )}
        {testUrl && saved.phone && (
          <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => run("test", async () => { await api("POST", testUrl); toast("Test message sent"); })}>
            {busy === "test" ? <Spinner size={13} /> : <Send size={13} />} Send a test
          </Button>
        )}
        <Button size="sm" disabled={!!busy} onClick={() => run("save", async () => { await api("PUT", url, { phone: f.phone, language: f.language, quietFrom: quiet ? f.quietFrom : "", quietTo: quiet ? f.quietTo : "", briefing: f.briefing ?? "", timeZone: quiet || f.briefing ? f.timeZone : "" }); toast("Saved"); onSaved(); })}>
          {busy === "save" ? <Spinner size={13} /> : <Check size={13} />} Save
        </Button>
      </div>
    </div>
  );
}

/** Settings → Account: one's own WhatsApp number, language and quiet hours. */
export function MyWhatsApp({ explain }: { explain?: boolean }) {
  const data = useApi<{ available: boolean; person: WaPerson; number?: string }>("/api/me/whatsapp");
  if (data.data && !data.data.available && explain) {
    return <Card><SectionTitle icon={<WhatsApp size={17} />} title="Your WhatsApp" sub="The dashboard owner has not linked WhatsApp yet. Once they have, set your number here to get on-call alerts and calls." /></Card>;
  }
  if (!data.data?.available) return null;
  return (
    <Card>
      <SectionTitle icon={<WhatsApp size={17} />} title="Your WhatsApp" sub="Where on-call alerts reach you on WhatsApp, in which language, and when not to." />
      {data.data.number && (
        <p className="mb-1 flex items-center gap-2 rounded-xl bg-surface2/60 p-3 text-xs text-muted"><PhoneCall size={13} className="shrink-0" />
          <span>Call or write to <b className="font-mono text-fg">+{data.data.number}</b> on WhatsApp (voice notes work too), from the number below, to ask the AI about your servers, incidents, logs, uptime or security, to restart an app, or to pass a message on to a teammate (“call Sara and tell her…”, “when Omar calls, tell him…”). Other numbers get no answer.</span></p>
      )}
      <div className="mb-3"><PinField /></div>
      <PersonForm saved={data.data.person} url="/api/me/whatsapp" testUrl={data.data.person.phone ? "/api/me/whatsapp/test" : undefined} callUrl={data.data.person.phone ? "/api/me/whatsapp/call" : undefined} onSaved={data.reload} />
      <div className="mt-6"><MessagesCard bare /></div>
    </Card>
  );
}

interface WaOptions {
  resolvedMessages: boolean;
  followUpMinutes: number;
  requirePin: boolean;
}

/** What follows a page: the "resolved" message and the follow-up call. */
function OptionsCard({ saved, onSaved }: { saved: WaOptions; onSaved: () => void }) {
  const { toast } = useStore();
  const [f, setF] = useState(saved);
  const [busy, setBusy] = useState(false);
  const dirty = JSON.stringify(f) !== JSON.stringify(saved);
  return (
    <Card>
      <SectionTitle icon={<Send size={15} />} title="After a page" sub="What WhatsApp does once someone was paged about an incident. The dashboard also alerts you (push, email, your channels) if WhatsApp stays disconnected for 3 minutes." />
      <div className="space-y-3">
        <label className="flex items-center justify-between gap-3 text-sm">
          <span><b>Message when it is resolved</b><br /><span className="text-xs text-muted">Whoever was paged gets "✅ Resolved: … after 12 min".</span></span>
          <input type="checkbox" checked={f.resolvedMessages} onChange={(e) => setF({ ...f, resolvedMessages: e.target.checked })} />
        </label>
        <label className="flex items-center justify-between gap-3 text-sm">
          <span><b>Ask for a PIN before actions</b><br /><span className="text-xs text-muted">Restarts, redeploys, blocks… asked for on a call or in a chat need the person's own PIN (each sets theirs).</span></span>
          <input type="checkbox" checked={!!f.requirePin} onChange={(e) => setF({ ...f, requirePin: e.target.checked })} />
        </label>
        <Field label="Call back if still open after (minutes)" hint="The AI calls whoever acknowledged it: still down, want a restart, someone else, or more time? 0 = never.">
          <Input type="number" min={0} max={1440} value={f.followUpMinutes} onChange={(e) => setF({ ...f, followUpMinutes: Number(e.target.value) })} className="sm:w-40" />
        </Field>
        <div className="flex justify-end">
          <Button size="sm" disabled={!dirty || busy} onClick={async () => { setBusy(true); try { await api("PUT", "/api/integrations/whatsapp/options", f); toast("Saved"); onSaved(); } catch (e) { toast((e as ApiError).message, "err"); } setBusy(false); }}>
            {busy ? <Spinner size={13} /> : <Check size={13} />} Save
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** One's own action PIN: set, replace or remove. Never shown again once saved. */
function PinField() {
  const { toast } = useStore();
  const me = useApi<{ hasPin?: boolean; requirePin?: boolean }>("/api/me/whatsapp");
  const [pin, setPin] = useState("");
  const [busy, setBusy] = useState(false);
  const save = async (value: string) => {
    setBusy(true);
    try {
      await api("PUT", "/api/me/whatsapp/pin", { pin: value });
      setPin("");
      me.reload();
      toast(value ? "PIN saved" : "PIN removed");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-2">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
        <Field label={me.data?.hasPin ? "Action PIN (set: type a new one to replace it)" : "Action PIN"} hint="4 digits, not all the same (no 1111)." className="flex-1">
          <Input type="password" inputMode="numeric" value={pin} onChange={(e) => setPin(e.target.value.replace(/\D/g, "").slice(0, 4))} placeholder="••••" autoComplete="off" />
        </Field>
        <Button size="sm" disabled={busy || pin.length !== 4 || /^(\d)\1+$/.test(pin)} onClick={() => save(pin)}><Check size={13} /> Save PIN</Button>
        {me.data?.hasPin && <Button size="sm" variant="ghost" disabled={busy} onClick={() => save("")}>Remove</Button>}
      </div>
      {me.data?.requirePin && !me.data.hasPin && <p className="flex items-center gap-1.5 text-xs text-warn"><AlertTriangle size={12} />Actions need a PIN on this dashboard: set yours to restart things by voice or chat.</p>}
    </div>
  );
}

interface Note {
  id: number;
  from: string;
  to: string;
  fromName: string;
  toName: string;
  text: string;
  via: "text" | "call" | "later";
  at: number;
  deliveredAt: number;
  status: "waiting" | "delivered" | "canceled" | "expired";
  canCancel: boolean;
}

const VIA = { text: "texted", call: "by a call", later: "on their next call or chat" };
const NOTE_TONE = { waiting: "warn", delivered: "ok", canceled: undefined, expired: undefined } as const;

/** Messages passed on between the team through the AI: waiting ones can be canceled. */
function MessagesCard({ bare }: { bare?: boolean }) {
  const { toast } = useStore();
  const list = useApi<{ items: Note[]; me: string }>("/api/me/whatsapp/messages");
  const [shown, setShown] = useState(8);
  const items = list.data?.items ?? [];
  const waiting = items.filter((n) => n.status === "waiting").length;
  const cancel = async (id: number) => {
    try {
      await api("DELETE", `/api/me/whatsapp/messages/${id}`);
      list.reload();
      toast("Message canceled");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const body = (
    <>
      <SectionTitle icon={<MessageSquareText size={15} />} title="Messages passed on"
        sub={`What the team asked the AI to tell each other ("tell Sara…", "when Omar calls, tell him…"). Waiting ones are passed on at the person's next call or chat, for ${7} days.`}
        action={waiting > 0 ? <Badge tone="warn">{waiting} waiting</Badge> : undefined} />
      {items.length === 0 && <p className="text-sm text-muted">None yet. Ask the AI on WhatsApp to pass a message on to a teammate.</p>}
      <ul className="space-y-2">
        {items.slice(0, shown).map((n) => (
          <li key={n.id} className="flex items-start gap-3 rounded-2xl border border-line px-3 py-2.5">
            <div className="min-w-0 flex-1">
              <div className="text-xs text-muted">
                <b className="text-fg">{n.from === list.data!.me ? "You" : n.fromName}</b> → <b className="text-fg">{n.to === list.data!.me ? "you" : n.toName}</b> · {new Date(n.at).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}
              </div>
              <p className="mt-0.5 break-words text-sm">{n.text}</p>
              <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted">
                <Badge tone={NOTE_TONE[n.status]}>{n.status}</Badge>
                {n.status === "delivered" ? <span>{VIA[n.via]}, {new Date(n.deliveredAt).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}</span> : n.status === "waiting" ? <span>{n.via === "call" ? "calling them, or " : ""}on their next call or chat</span> : null}
              </div>
            </div>
            {n.canCancel && <Button size="sm" variant="ghost" onClick={() => cancel(n.id)}><X size={13} /> Cancel</Button>}
          </li>
        ))}
      </ul>
      {items.length > shown && <button className="mt-2 text-xs font-medium text-muted hover:text-fg" onClick={() => setShown(shown + 20)}>Show more</button>}
    </>
  );
  return bare ? body : <Card>{body}</Card>;
}
