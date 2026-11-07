import { useCallback, useEffect, useRef, useState } from "react";
import { Archive, ArrowLeft, Download, FileWarning, Forward, Inbox, MailOpen, Paperclip, Reply, ReplyAll, RotateCcw, ShieldAlert, ShieldCheck, Star, Tag, Trash2 } from "lucide-react";
import { api, ApiError } from "../../api/client";
import { useStore } from "../../store/store";
import { cn } from "../../utils/cn";
import { Badge, Button, ConfirmDialog, ErrorState, IconButton, LoadingState, Select } from "../ui";
import { fmtBytes } from "../DiskRescue";
import { act, fullDate, personLabel, type Address, type Body, type Message, type Overview } from "./data";
import type { ComposeInit } from "./Composer";

/** HTML mail in a sandbox: no scripts, its own strict CSP, links open in a new tab. */
function HtmlBody({ html, images }: { html: string; images: boolean }) {
  const ref = useRef<HTMLIFrameElement>(null);
  const [h, setH] = useState(120);
  const csp = `default-src 'none'; style-src 'unsafe-inline'; font-src data:; img-src data:${images ? " https: http:" : ""}`;
  const doc = `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><base target="_blank">` +
    `<style>body{margin:0;font:14px/1.5 system-ui,sans-serif;color:#1f2328;background:#fff;overflow-wrap:anywhere}img{max-width:100%;height:auto}blockquote{margin:0 0 0 .8em;padding-left:.8em;border-left:2px solid #d0d7de;color:#57606a}pre{white-space:pre-wrap}</style>` +
    `</head><body>${images ? html.replace(/data-fw-src=/g, "src=") : html}</body></html>`;
  const measure = useCallback(() => {
    const d = ref.current?.contentDocument;
    if (d?.documentElement) setH(Math.min(Math.max(d.documentElement.scrollHeight, 60), 20000));
  }, []);
  useEffect(() => {
    const d = ref.current?.contentDocument;
    if (!d?.body) return;
    const ro = new ResizeObserver(measure);
    ro.observe(d.body);
    return () => ro.disconnect();
  });
  return (
    // allow-same-origin without allow-scripts: nothing in the mail runs; it lets us size the frame.
    <iframe ref={ref} title="Message" sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox" srcDoc={doc} onLoad={measure} style={{ height: h }} className="w-full rounded-2xl bg-white" />
  );
}

const authTone = (v?: string) => (v === "pass" ? "ok" : v === "fail" || v === "softfail" ? "crit" : "neutral");

function OneMessage({ m, open, onToggle, onBody }: { m: Message; open: boolean; onToggle: () => void; onBody: (id: number, b: Body) => void }) {
  const [body, setBody] = useState<Body | null>(null);
  const [error, setError] = useState("");
  const [images, setImages] = useState(false);
  const [headers, setHeaders] = useState<string | null>(null);
  useEffect(() => {
    if (!open || body) return;
    api<{ message: Message; body: Body }>("GET", `/api/emails/messages/${m.id}`)
      .then((r) => (setBody(r.body), onBody(m.id, r.body)))
      .catch((e: ApiError) => setError(e.message));
  }, [open, body, m.id, onBody]);
  const loadImages = () => {
    api<{ body: Body }>("GET", `/api/emails/messages/${m.id}?images=1&peek=1`).then((r) => (setBody(r.body), setImages(true))).catch(() => {});
  };
  const showHeaders = () => {
    if (headers !== null) return setHeaders(null);
    api<{ body: Body }>("GET", `/api/emails/messages/${m.id}?headers=1&peek=1`).then((r) => setHeaders(r.body.headers ?? "")).catch(() => {});
  };
  return (
    <article className="border-b border-line last:border-0">
      <button type="button" onClick={onToggle} className="flex w-full items-start gap-3 px-1 py-3 text-left" aria-expanded={open}>
        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-surface2 text-sm font-semibold uppercase">{personLabel(m.from).slice(0, 1)}</div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className={cn("truncate text-sm", m.unread ? "font-semibold" : "font-medium")}>{personLabel(m.from)}</span>
            {m.from.name && <span className="truncate text-xs text-muted">{m.from.address}</span>}
          </div>
          {open ? (
            <div className="mt-0.5 text-xs text-muted">
              to {m.to.map(personLabel).join(", ") || m.rcpt}
              {m.cc.length > 0 && <> · cc {m.cc.map(personLabel).join(", ")}</>}
              {m.bcc && m.bcc.length > 0 && <> · bcc {m.bcc.map(personLabel).join(", ")}</>}
              {m.direction === "in" && m.rcpt && !m.to.some((p) => p.address === m.rcpt) && <> · received at {m.rcpt}</>}
            </div>
          ) : (
            <div className="truncate text-xs text-muted">{m.snippet}</div>
          )}
        </div>
        <span className="shrink-0 text-xs text-muted">{fullDate(m.sentAt)}</span>
      </button>
      {open && (
        <div className="pb-5 pl-12">
          {m.direction === "in" && (
            <div className="mb-3 flex flex-wrap gap-1.5">
              {(["spf", "dkim", "dmarc"] as const).map((k) => (
                <Badge key={k} tone={authTone(m.auth[k])} className="uppercase">{k} {m.auth[k] ?? "not reported"}</Badge>
              ))}
            </div>
          )}
          {m.folder === "spam" && m.spamWhy.length > 0 && (
            <div className="mb-3 rounded-2xl bg-warn-soft px-4 py-3 text-xs text-warn">
              <div className="mb-1 font-medium">Why this is in Spam (score {m.spamScore.toFixed(1)})</div>
              <ul className="list-disc pl-4">{m.spamWhy.map((w) => <li key={w}>{w}</li>)}</ul>
            </div>
          )}
          {m.outbox && <SendState m={m} />}
          {error ? (
            <p className="text-sm text-crit">{error}</p>
          ) : !body ? (
            <LoadingState label="Opening…" />
          ) : (
            <>
              {body.remoteImages && !images && (
                <div className="mb-3 flex flex-wrap items-center gap-3 rounded-2xl bg-surface2 px-4 py-2 text-xs text-muted">
                  Remote images are blocked: they can tell the sender you opened this.
                  <Button size="sm" variant="secondary" onClick={loadImages}>Load images</Button>
                </div>
              )}
              {body.html ? <HtmlBody html={body.html} images={images} /> : <div className="whitespace-pre-wrap break-words text-sm">{body.text}</div>}
              {body.attachments.length > 0 && (
                <div className="mt-4 flex flex-wrap gap-2">
                  {body.attachments.map((a) => (
                    <a key={a.part} href={`/api/emails/messages/${m.id}/attachments/${a.part}`} download={a.filename} className="flex max-w-full items-center gap-2 rounded-2xl border border-line px-3 py-2 text-xs hover:border-fg/30" title={a.scan === "blocked" ? "A program or script: open it only if you trust the sender" : "Not scanned for viruses"}>
                      {a.scan === "blocked" ? <FileWarning size={14} className="text-crit" /> : <Paperclip size={14} className="text-muted" />}
                      <span className="truncate">{a.filename}</span>
                      <span className="text-muted">{fmtBytes(a.size)}</span>
                      <Badge tone={a.scan === "blocked" ? "crit" : "neutral"}>{a.scan === "blocked" ? "program" : "not scanned"}</Badge>
                      <Download size={13} className="text-muted" />
                    </a>
                  ))}
                </div>
              )}
              <div className="mt-3 flex flex-wrap gap-3 text-xs text-muted">
                <a href={`/api/emails/messages/${m.id}/raw`} className="hover:text-fg">Download .eml</a>
                <button type="button" onClick={showHeaders} className="hover:text-fg">{headers !== null ? "Hide headers" : "Show original headers"}</button>
              </div>
              {headers !== null && <pre className="mt-2 max-h-80 overflow-auto rounded-2xl bg-surface2 p-3 text-[11px] leading-relaxed">{headers}</pre>}
            </>
          )}
        </div>
      )}
    </article>
  );
}

function SendState({ m }: { m: Message }) {
  const { toast } = useStore();
  const j = m.outbox!;
  const [confirm, setConfirm] = useState(false);
  const retry = (force: boolean) =>
    api("POST", `/api/emails/outbox/${j.id}/retry`, { force }).then(() => toast("Sending again")).catch((e: ApiError) => toast(e.message, "err"));
  const text = {
    scheduled: j.sendAt * 1000 > Date.now() + 60000 ? `Scheduled for ${fullDate(j.sendAt)}` : j.attempts ? `Will try again at ${fullDate(j.nextAt)}` : "Sending…",
    sending: "Sending…",
    accepted: "Accepted by the provider (delivery to the inbox is up to them)",
    failed: "Not sent",
    unknown: "Unknown: the connection broke while handing it over, so it may or may not have gone out",
  }[j.state];
  const tone = j.state === "accepted" ? "ok" : j.state === "failed" ? "crit" : j.state === "unknown" ? "warn" : "info";
  return (
    <div className="mb-3 flex flex-wrap items-center gap-2 text-xs">
      <Badge tone={tone} dot>{text}</Badge>
      {j.lastError && <span className="text-muted">{j.lastError}</span>}
      {j.state === "failed" && <Button size="sm" variant="secondary" onClick={() => retry(false)}>Retry</Button>}
      {j.state === "unknown" && <Button size="sm" variant="secondary" onClick={() => setConfirm(true)}>Send again…</Button>}
      <ConfirmDialog open={confirm} title="Send it again?" message="The provider may already have delivered it, so people could get it twice. Send again only if you know it did not arrive." confirmLabel="Send again" onCancel={() => setConfirm(false)} onConfirm={() => (setConfirm(false), retry(true))} />
    </div>
  );
}

const quote = (m: Message, b: Body | undefined) => {
  const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);
  const inner = b ? (b.html || `<div style="white-space:pre-wrap">${esc(b.text)}</div>`) : esc(m.snippet);
  return { esc, inner };
};

export function MessageViewer({ id, ov, version, onBack, onCompose, onChanged }: { id: number; ov: Overview; version: number; onBack: () => void; onCompose: (c: ComposeInit) => void; onChanged: () => void }) {
  const { toast } = useStore();
  const [thread, setThread] = useState<Message[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<Set<number>>(new Set([id]));
  const [purge, setPurge] = useState(false);
  const bodies = useRef(new Map<number, Body>());
  const onBody = useCallback((mid: number, b: Body) => bodies.current.set(mid, b), []);

  const load = useCallback(() => {
    api<{ message: Message }>("GET", `/api/emails/messages/${id}?peek=1`)
      .then((r) => api<Message[]>("GET", `/api/emails/threads/${r.message.threadId}${r.message.folder === "trash" ? "?trash=1" : ""}`))
      .then((t) => setThread(t))
      .catch((e: ApiError) => setError(e.message));
  }, [id]);
  useEffect(() => {
    setThread(null);
    setOpen(new Set([id]));
    load();
  }, [id, load]);
  useEffect(() => {
    if (version) load(); // new mail or a send-state change, while it is open
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version]);

  if (error) return <ErrorState message={error} onRetry={load} />;
  if (!thread) return <LoadingState label="Opening…" />;
  const m = thread.find((x) => x.id === id) ?? thread[thread.length - 1];
  const mine = new Set(ov.addresses.map((a) => a.address));
  const senders = ov.addresses.filter((a) => a.canSend);
  const fromFor = (x: Message): Address | undefined =>
    senders.find((a) => a.address === x.rcpt) ?? senders.find((a) => a.id === x.mailboxId) ?? senders.find((a) => x.to.some((p) => p.address === a.address)) ?? senders[0];

  const run = async (action: string, extra: { folder?: string; label?: string } = {}, back = false) => {
    // Marks apply to the whole conversation; moves only to the messages where this one is.
    const marks = ["read", "unread", "star", "unstar", "label", "unlabel"].includes(action);
    const ids = thread.filter((x) => marks || x.folder === m.folder).map((x) => x.id);
    try {
      await act(ids, action, extra);
      onChanged();
      if (back) onBack();
      else load();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };

  const reply = (all: boolean) => {
    const last = [...thread].reverse().find((x) => x.direction === "in") ?? m;
    const target = last.direction === "out" ? last.to.map((p) => p.address) : [last.replyTo || last.from.address];
    const cc = all ? [...last.to, ...last.cc].map((p) => p.address).filter((a) => !mine.has(a) && !target.includes(a)) : [];
    const { esc, inner } = quote(last, bodies.current.get(last.id));
    onCompose({
      fromId: fromFor(last)?.id,
      to: target,
      cc,
      subject: /^re:/i.test(last.subject) ? last.subject : "Re: " + last.subject,
      replyToId: last.id,
      html: `<div class="fw-quote"><br><div>On ${esc(fullDate(last.sentAt))}, ${esc(personLabel(last.from))} &lt;${esc(last.from.address)}&gt; wrote:</div><blockquote>${inner}</blockquote></div>`,
    });
  };
  const forward = () => {
    const { esc, inner } = quote(m, bodies.current.get(m.id));
    onCompose({
      fromId: fromFor(m)?.id,
      subject: /^fwd?:/i.test(m.subject) ? m.subject : "Fwd: " + m.subject,
      forwardId: m.id,
      html: `<div class="fw-quote"><br><div>---------- Forwarded message ----------<br>From: ${esc(personLabel(m.from))} &lt;${esc(m.from.address)}&gt;<br>Date: ${esc(fullDate(m.sentAt))}<br>Subject: ${esc(m.subject)}<br>To: ${esc(m.to.map((p) => p.address).join(", "))}</div><br>${inner}</div>`,
    });
  };

  const labels = ov.folders.filter((f) => f.kind === "label");
  const folders = ov.folders.filter((f) => f.kind === "folder");
  const inTrash = m.folder === "trash";
  const inSpam = m.folder === "spam";

  return (
    <div className="min-w-0">
      <div className="mb-3 flex flex-wrap items-center gap-1.5">
        <IconButton onClick={onBack} aria-label="Back to the list" title="Back"><ArrowLeft size={16} /></IconButton>
        {!inTrash && !inSpam && m.folder !== "archive" && <IconButton onClick={() => run("move", { folder: "archive" }, true)} aria-label="Archive" title="Archive"><Archive size={16} /></IconButton>}
        {m.folder === "archive" && <IconButton onClick={() => run("move", { folder: "inbox" }, true)} aria-label="Move to Inbox" title="Move to Inbox"><Inbox size={16} /></IconButton>}
        {inSpam ? (
          <IconButton onClick={() => run("notspam", {}, true)} aria-label="Not spam" title="Not spam (and trust this sender)"><ShieldCheck size={16} /></IconButton>
        ) : (
          !inTrash && m.direction === "in" && <IconButton onClick={() => run("spam", {}, true)} aria-label="Report spam" title="Spam"><ShieldAlert size={16} /></IconButton>
        )}
        {inTrash ? (
          <IconButton onClick={() => run("restore", {}, true)} aria-label="Restore" title="Restore"><RotateCcw size={16} /></IconButton>
        ) : (
          <IconButton onClick={() => run("trash", {}, true)} aria-label="Move to Trash" title="Trash"><Trash2 size={16} /></IconButton>
        )}
        {(inTrash || inSpam) && <Button size="sm" variant="danger" onClick={() => setPurge(true)}>Delete forever</Button>}
        <IconButton onClick={() => run("unread", {}, true)} aria-label="Mark unread" title="Mark unread"><MailOpen size={16} /></IconButton>
        <IconButton onClick={() => run(m.starred ? "unstar" : "star")} aria-label={m.starred ? "Unstar" : "Star"} title="Star"><Star size={16} className={m.starred ? "fill-warn text-warn" : ""} /></IconButton>
        {folders.length > 0 && (
          <Select value="" onChange={(e) => e.target.value && run("move", { folder: e.target.value }, true)} className="w-auto py-2 text-xs" aria-label="Move to folder">
            <option value="">Move to…</option>
            <option value="inbox">Inbox</option>
            {folders.map((f) => <option key={f.id} value={"f:" + f.id}>{f.name}</option>)}
          </Select>
        )}
        {labels.length > 0 && (
          <Select value="" onChange={(e) => e.target.value && run(m.labels.includes(e.target.value) ? "unlabel" : "label", { label: e.target.value })} className="w-auto py-2 text-xs" aria-label="Label">
            <option value="">Label…</option>
            {labels.map((l) => <option key={l.id} value={l.id}>{m.labels.includes(l.id) ? "✓ " : ""}{l.name}</option>)}
          </Select>
        )}
      </div>
      <h2 className="mb-1 break-words text-lg font-semibold">{m.subject || "(no subject)"}</h2>
      {m.labels.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1">
          {m.labels.map((l) => {
            const f = ov.folders.find((x) => x.id === l);
            return f && <Badge key={l}><Tag size={11} style={{ color: f.color || undefined }} /> {f.name}</Badge>;
          })}
        </div>
      )}
      <div className="rounded-3xl border border-line bg-surface px-4">
        {thread.map((x) => (
          <OneMessage key={x.id} m={x} open={open.has(x.id)} onBody={onBody} onToggle={() => setOpen((s) => { const n = new Set(s); if (n.has(x.id)) n.delete(x.id); else n.add(x.id); return n; })} />
        ))}
      </div>
      {senders.length > 0 && !inTrash && (
        <div className="mt-4 flex flex-wrap gap-2">
          <Button variant="secondary" onClick={() => reply(false)}><Reply size={15} /> Reply</Button>
          <Button variant="secondary" onClick={() => reply(true)}><ReplyAll size={15} /> Reply all</Button>
          <Button variant="secondary" onClick={forward}><Forward size={15} /> Forward</Button>
        </div>
      )}
      <ConfirmDialog open={purge} danger title="Delete forever?" message="These messages and their original files are deleted from the server. This cannot be undone (backups made before keep them)." confirmLabel="Delete forever"
        onCancel={() => setPurge(false)} onConfirm={() => (setPurge(false), run("purge", {}, true))} />
    </div>
  );
}

