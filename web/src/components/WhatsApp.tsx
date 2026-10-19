import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Check, Info, MoonStar, QrCode, Send, Smartphone, Trash2, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, Field, Input, SectionTitle, Select, Spinner, WhatsApp } from "./ui";

export interface WaPerson {
  phone: string;
  language: string;
  quietFrom?: string;
  quietTo?: string;
  timeZone?: string;
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
  const data = useApi<{ status: WaStatus; people: (WaPerson & { id: string; name: string })[] }>("/api/integrations/whatsapp");
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
          sub="Link your own WhatsApp, the way WhatsApp Web is linked. On-call alerts are sent from it to the team; calls come next."
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
        sub={<>Add each person's number the first time; they can change it later in their account. Who is alerted, in which order and for which incidents is set in <Link to="/settings/oncall" className="underline">On-call</Link>.</>} />
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
      {open && <PersonForm saved={saved} url={`/api/integrations/whatsapp/people/${id}`} testUrl={linked ? `/api/integrations/whatsapp/people/${id}/test` : undefined} self={id === "owner"} onSaved={() => { onSaved(); setOpen(false); }} />}
    </div>
  );
}

/** Someone's number, language and quiet hours. */
export function PersonForm({ saved, url, testUrl, self, onSaved }: { saved: WaPerson; url: string; testUrl?: string; self?: boolean; onSaved: () => void }) {
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
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="From"><Input type="time" value={f.quietFrom} onChange={(e) => setF({ ...f, quietFrom: e.target.value })} /></Field>
          <Field label="Until"><Input type="time" value={f.quietTo} onChange={(e) => setF({ ...f, quietTo: e.target.value })} /></Field>
          <Field label="Time zone"><Input value={f.timeZone} onChange={(e) => setF({ ...f, timeZone: e.target.value })} placeholder="Africa/Casablanca" /></Field>
        </div>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        {testUrl && saved.phone && (
          <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => run("test", async () => { await api("POST", testUrl); toast("Test message sent"); })}>
            {busy === "test" ? <Spinner size={13} /> : <Send size={13} />} Send a test
          </Button>
        )}
        <Button size="sm" disabled={!!busy} onClick={() => run("save", async () => { await api("PUT", url, { phone: f.phone, language: f.language, quietFrom: quiet ? f.quietFrom : "", quietTo: quiet ? f.quietTo : "", timeZone: quiet ? f.timeZone : "" }); toast("Saved"); onSaved(); })}>
          {busy === "save" ? <Spinner size={13} /> : <Check size={13} />} Save
        </Button>
      </div>
    </div>
  );
}

/** Settings → Account: one's own WhatsApp number, language and quiet hours. */
export function MyWhatsApp() {
  const data = useApi<{ available: boolean; person: WaPerson }>("/api/me/whatsapp");
  if (!data.data?.available) return null;
  return (
    <Card>
      <SectionTitle icon={<WhatsApp size={17} />} title="Your WhatsApp" sub="Where on-call alerts reach you on WhatsApp, in which language, and when not to." />
      <PersonForm saved={data.data.person} url="/api/me/whatsapp" testUrl={data.data.person.phone ? "/api/me/whatsapp/test" : undefined} onSaved={data.reload} />
    </Card>
  );
}
