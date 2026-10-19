import { useEffect, useState } from "react";
import { AlertTriangle, Check, ExternalLink, Gauge, Info, KeyRound, Mic, PhoneCall, RefreshCw, Trash2 } from "lucide-react";
import { relTime } from "../mock/util";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, Field, Gemini, Input, SectionTitle, Select, Spinner } from "./ui";

interface AIConfig {
  model: string;
  voice: string;
  monthlyMinutes: number;
}
interface AIStatus {
  configured: boolean;
  config: AIConfig;
  usedSeconds: number;
  voices: string[];
}

/** Integrations → AI: the Gemini key, live model, voice and monthly cap of on-call calls. */
export function AISetup() {
  const { toast } = useStore();
  const data = useApi<{ status: AIStatus; models: { id: string; note: string; recommended?: boolean }[]; error?: string; lastCall?: LastCall | null }>("/api/integrations/ai");
  const [key, setKey] = useState("");
  const [f, setF] = useState<AIConfig | null>(null);
  const [busy, setBusy] = useState("");
  const [del, setDel] = useState(false);
  const st = data.data?.status;
  useEffect(() => {
    if (st) setF(st.config);
  }, [st]);
  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what);
    try {
      await fn();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  if (!st || !f) return <Card><Spinner /></Card>;
  const used = Math.round(st.usedSeconds / 60);
  const dirty = JSON.stringify(f) !== JSON.stringify(st.config);
  const keyForm = (
    <div className="space-y-3">
      <ol className="list-decimal space-y-1.5 pl-5 text-sm text-muted">
        <li>Open <a className="inline-flex items-center gap-1 font-medium text-fg underline" href="https://aistudio.google.com/apikey" target="_blank" rel="noreferrer">Google AI Studio → API keys <ExternalLink size={11} /></a> and create a key (the free tier is enough to start).</li>
        <li>Paste it here: Fleetwatch checks it, picks the newest live voice model it can use, and keeps the key encrypted.</li>
      </ol>
      <div className="flex flex-col gap-2 sm:flex-row">
        <Input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder="Gemini API key" className="font-mono" autoComplete="off" />
        <Button className="shrink-0" disabled={!!busy || !key.trim()} onClick={() => run("key", async () => { await api("PUT", "/api/integrations/ai/key", { key }); setKey(""); data.reload(); toast("Gemini is connected: answered on-call calls now talk with the AI"); })}>
          {busy === "key" ? <Spinner size={14} /> : <KeyRound size={14} />} {st.configured ? "Replace the key" : "Connect Gemini"}
        </Button>
      </div>
    </div>
  );
  return (
    <>
      <Card>
        <SectionTitle icon={<Gemini size={17} />} title="AI on calls (Gemini Live)"
          sub="When someone answers an on-call call, a Gemini voice tells them what happened, answers their questions from the dashboard's data, acknowledges the incident when they say they are on it, and can start, stop or restart an app once they confirm (as far as their role allows). The call's transcript goes on the incident."
          action={st.configured ? <Badge tone="ok" dot>Connected</Badge> : undefined} />
        {!st.configured ? keyForm : (
          <div className="space-y-4">
            {data.data?.error && <p className="flex items-center gap-2 text-xs text-crit"><AlertTriangle size={13} />{data.data.error}</p>}
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Live model" hint={data.data!.models.find((m) => m.id === f.model)?.note ?? "Models your key can hold live voice calls with."}>
                <Select value={f.model} onChange={(e) => setF({ ...f, model: e.target.value })}>
                  {(data.data!.models.length ? data.data!.models : [{ id: f.model, note: "", recommended: false }]).map((m) => <option key={m.id} value={m.id}>{m.id}{m.recommended ? " (recommended)" : ""}</option>)}
                </Select>
              </Field>
              <Field label="Voice">
                <Select value={f.voice} onChange={(e) => setF({ ...f, voice: e.target.value })}>
                  {st.voices.map((v) => <option key={v} value={v}>{v}</option>)}
                </Select>
              </Field>
              <Field label="Call minutes a month" hint="Over it, calls play the alert sound instead.">
                <Input type="number" min={0} value={f.monthlyMinutes} onChange={(e) => setF({ ...f, monthlyMinutes: Number(e.target.value) })} />
              </Field>
            </div>
            <div>
              <div className="mb-1 flex items-center justify-between text-xs text-muted"><span className="inline-flex items-center gap-1"><Gauge size={12} /> This month</span><span>{used} of {f.monthlyMinutes} minutes</span></div>
              <div className="h-1.5 overflow-hidden rounded-full bg-surface2"><div className="h-full rounded-full bg-[var(--chart1)]" style={{ width: `${Math.min(100, f.monthlyMinutes ? (used / f.monthlyMinutes) * 100 : 0)}%` }} /></div>
            </div>
            <p className="flex gap-2 rounded-xl bg-surface2/60 p-3 text-[11px] text-muted"><Info size={12} className="mt-px shrink-0" />
              <span><b className="text-fg">On the free tier</b>, every live model is free with no daily limit; what differs is tokens per minute. A 5-minute call uses about 20K, so the recommended model handles one call at a time. If several people may be called at once, pick a <b className="text-fg">native-audio</b> model (1M tokens a minute). Your key's exact limits are in <a className="underline" href="https://aistudio.google.com/" target="_blank" rel="noreferrer">Google AI Studio</a>, under usage and rate limits.</span></p>
            <p className="flex gap-2 text-[11px] text-muted"><Mic size={12} className="mt-px shrink-0" />Try it with Call me now in Integrations → WhatsApp: answered, the AI says it is a test and chats briefly. Each person's language is set with their WhatsApp number; the AI switches if they speak another one.</p>
            <div className="flex flex-wrap justify-between gap-2">
              <Button size="sm" variant="ghost" className="text-crit" onClick={() => setDel(true)}><Trash2 size={13} /> Remove the key</Button>
              <Button size="sm" disabled={!dirty || !!busy} onClick={() => run("save", async () => { await api("PUT", "/api/integrations/ai", f); data.reload(); toast("Saved"); })}>
                {busy === "save" ? <Spinner size={13} /> : <Check size={13} />} Save
              </Button>
            </div>
          </div>
        )}
      </Card>
      {st.configured && data.data?.lastCall && <LastCallCard c={data.data.lastCall} onReload={data.reload} />}
      {st.configured && <Card><SectionTitle icon={<KeyRound size={15} />} title="Replace the key" />{keyForm}</Card>}
      <ConfirmDialog open={del} title="Remove the Gemini key?" confirmLabel="Remove" danger
        message="Answered on-call calls go back to playing the alert sound."
        onCancel={() => setDel(false)} onConfirm={() => { setDel(false); run("del", async () => { await api("DELETE", "/api/integrations/ai"); data.reload(); }); }} />
    </>
  );
}

interface LastCall {
  at: number;
  seconds: number;
  frames: number;
  speech: number;
  loudestDb: number;
  gain: number;
  heard: string;
  said: string;
  ended: string;
  error?: string;
  replyAvgMs: number;
  replySlowestMs: number;
  interruptions: number;
}

/** How the last call went: whether their voice arrived, what Gemini heard, why it ended. */
function LastCallCard({ c, onReload }: { c: LastCall; onReload: () => void }) {
  const voice = c.frames === 0 ? { tone: "crit" as const, text: "No audio of theirs reached the dashboard." }
    : c.speech === 0 ? { tone: "warn" as const, text: `Their audio arrived (${c.frames} chunks) but sounded like silence (loudest ${c.loudestDb} dB).` }
    : { tone: "ok" as const, text: `Their voice arrived: loudest ${c.loudestDb} dB, boosted ×${c.gain.toFixed(1)}.` };
  return (
    <Card>
      <SectionTitle icon={<PhoneCall size={15} />} title="Last call" sub={`${relTime(c.at)} · ${c.seconds} s · ended: ${c.ended}`}
        action={<Button size="sm" variant="ghost" onClick={onReload}><RefreshCw size={13} /></Button>} />
      <dl className="space-y-2 text-sm">
        <div className="flex gap-2"><dt className="w-24 shrink-0 text-muted">Their voice</dt><dd><Badge tone={voice.tone} dot>{voice.tone === "ok" ? "OK" : "Problem"}</Badge> <span className="text-xs">{voice.text}</span></dd></div>
        {c.replyAvgMs > 0 && <div className="flex gap-2"><dt className="w-24 shrink-0 text-muted">Replies</dt><dd className="text-xs">took {(c.replyAvgMs / 1000).toFixed(1)} s on average, {(c.replySlowestMs / 1000).toFixed(1)} s at most{c.interruptions > 0 ? ` · you cut it off ${c.interruptions}×` : ""}</dd></div>}
        <div className="flex gap-2"><dt className="w-24 shrink-0 text-muted">AI heard</dt><dd className="text-xs">{c.heard || <span className="text-muted">nothing</span>}</dd></div>
        <div className="flex gap-2"><dt className="w-24 shrink-0 text-muted">AI said</dt><dd className="text-xs">{c.said || <span className="text-muted">nothing</span>}</dd></div>
        {c.error && <div className="flex gap-2"><dt className="w-24 shrink-0 text-muted">Error</dt><dd className="text-xs text-crit">{c.error}</dd></div>}
      </dl>
    </Card>
  );
}
