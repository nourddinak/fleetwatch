import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { Bold, Clock, Italic, Link2, List, ListOrdered, Minimize2, Paperclip, Quote, RemoveFormatting, Send, Trash2, Underline, X } from "lucide-react";
import { api, ApiError } from "../../api/client";
import { cn } from "../../utils/cn";
import { Button, Input, Select, Spinner } from "../ui";
import { fmtBytes } from "../DiskRescue";
import type { Address, Contact, Draft, FileRef, Job } from "./data";

/** A small rich-text editor: contentEditable with the browser's own formatting commands. */
export function Editor({ html, onChange, minHeight = 220, autoFocus }: { html: string; onChange: (html: string) => void; minHeight?: number; autoFocus?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const last = useRef(html);
  useEffect(() => {
    // Only push outside changes (a new signature, a loaded draft) into the DOM, not our own typing.
    if (ref.current && html !== last.current) {
      ref.current.innerHTML = html;
      last.current = html;
    }
  }, [html]);
  useEffect(() => {
    if (ref.current) ref.current.innerHTML = html;
    if (autoFocus) ref.current?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const cmd = (c: string, v?: string) => {
    ref.current?.focus();
    document.execCommand(c, false, v);
    emit();
  };
  const emit = () => {
    last.current = ref.current?.innerHTML ?? "";
    onChange(last.current);
  };
  const link = () => {
    const url = prompt("Link address (https://…)");
    if (url && /^(https?:|mailto:)/i.test(url.trim())) cmd("createLink", url.trim());
  };
  const tools = [
    { icon: Bold, label: "Bold", run: () => cmd("bold") },
    { icon: Italic, label: "Italic", run: () => cmd("italic") },
    { icon: Underline, label: "Underline", run: () => cmd("underline") },
    { icon: Link2, label: "Link", run: link },
    { icon: List, label: "Bulleted list", run: () => cmd("insertUnorderedList") },
    { icon: ListOrdered, label: "Numbered list", run: () => cmd("insertOrderedList") },
    { icon: Quote, label: "Quote", run: () => cmd("formatBlock", "blockquote") },
    { icon: RemoveFormatting, label: "Clear formatting", run: () => cmd("removeFormat") },
  ];
  return (
    <div className="rounded-2xl border border-line bg-surface2">
      <div className="flex flex-wrap gap-0.5 border-b border-line p-1.5" role="toolbar" aria-label="Formatting">
        {tools.map((t) => (
          <button key={t.label} type="button" title={t.label} aria-label={t.label} onMouseDown={(e) => e.preventDefault()} onClick={t.run} className="rounded-full p-2 text-muted transition hover:bg-surface hover:text-fg">
            <t.icon size={15} />
          </button>
        ))}
      </div>
      <div
        ref={ref}
        contentEditable
        suppressContentEditableWarning
        role="textbox"
        aria-multiline="true"
        aria-label="Message"
        onInput={emit}
        onPaste={(e) => {
          // Paste as plain text: pasted HTML brings fonts, colors and trackers along.
          e.preventDefault();
          document.execCommand("insertText", false, e.clipboardData.getData("text/plain"));
        }}
        className="mail-editor max-w-none overflow-y-auto px-4 py-3 text-sm outline-none [&_blockquote]:border-l-2 [&_blockquote]:border-line [&_blockquote]:pl-3 [&_blockquote]:text-muted [&_ol]:list-decimal [&_ol]:pl-6 [&_ul]:list-disc [&_ul]:pl-6 [&_a]:underline"
        style={{ minHeight }}
      />
    </div>
  );
}

const reAddr = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;
const bare = (v: string) => (v.match(/<([^>]+)>/)?.[1] ?? v).trim();

/** Addresses as chips, with suggestions from your contacts. */
function Recipients({ label, value, onChange, autoFocus }: { label: string; value: string[]; onChange: (v: string[]) => void; autoFocus?: boolean }) {
  const [text, setText] = useState("");
  const [sugs, setSugs] = useState<Contact[]>([]);
  const [hi, setHi] = useState(0);
  useEffect(() => {
    const q = text.trim();
    if (q.length < 2) return setSugs([]);
    const t = setTimeout(() => {
      api<Contact[]>("GET", "/api/emails/suggest?q=" + encodeURIComponent(q)).then((r) => (setSugs(r.filter((c) => !value.includes(c.address))), setHi(0))).catch(() => setSugs([]));
    }, 200);
    return () => clearTimeout(t);
  }, [text, value]);
  const add = (v: string) => {
    const parts = v.split(/[,;\s]+/).map(bare).filter(Boolean);
    if (parts.length) onChange([...value, ...parts.filter((p) => !value.includes(p))]);
    setText("");
    setSugs([]);
  };
  const key = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (sugs.length && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      setHi((h) => (h + (e.key === "ArrowDown" ? 1 : sugs.length - 1)) % sugs.length);
    } else if (e.key === "Enter" || e.key === "," || e.key === "Tab") {
      if (sugs.length && e.key !== ",") {
        e.preventDefault();
        add(sugs[hi].address);
      } else if (text.trim()) {
        e.preventDefault();
        add(text);
      }
    } else if (e.key === "Backspace" && !text && value.length) {
      onChange(value.slice(0, -1));
    }
  };
  return (
    <div className="relative flex min-h-11 flex-wrap items-center gap-1.5 border-b border-line py-1.5">
      <span className="w-10 shrink-0 text-xs text-muted">{label}</span>
      {value.map((v) => (
        <span key={v} className={cn("inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs", reAddr.test(v) ? "bg-surface2" : "bg-crit-soft text-crit")} title={reAddr.test(v) ? v : "Not an email address"}>
          {v}
          <button type="button" aria-label={`Remove ${v}`} onClick={() => onChange(value.filter((x) => x !== v))} className="text-muted hover:text-fg">
            <X size={12} />
          </button>
        </span>
      ))}
      <input
        value={text}
        autoFocus={autoFocus}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={key}
        onBlur={() => setTimeout(() => text.trim() && add(text), 150)}
        aria-label={label}
        className="min-w-[8rem] flex-1 bg-transparent py-1 text-sm outline-none"
      />
      {sugs.length > 0 && (
        <ul role="listbox" className="absolute left-10 top-full z-20 mt-1 w-72 max-w-[85vw] overflow-hidden rounded-2xl border border-line bg-surface shadow-xl">
          {sugs.map((c, i) => (
            <li key={c.address} role="option" aria-selected={i === hi}>
              <button type="button" onMouseDown={(e) => (e.preventDefault(), add(c.address))} className={cn("block w-full px-3 py-2 text-left text-sm", i === hi ? "bg-surface2" : "hover:bg-surface2")}>
                <div className="truncate font-medium">{c.name || c.address}</div>
                {c.name && <div className="truncate text-xs text-muted">{c.address}</div>}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const sigBlock = (html: string) => (html ? `<div class="fw-signature"><br>-- <br>${html}</div>` : "");

function withSignature(body: string, sig: string) {
  const doc = new DOMParser().parseFromString(`<div id="r">${body}</div>`, "text/html");
  const root = doc.getElementById("r")!;
  root.querySelectorAll(".fw-signature").forEach((n) => n.remove());
  const block = sigBlock(sig);
  const quote = root.querySelector(".fw-quote");
  if (quote) quote.insertAdjacentHTML("beforebegin", block);
  else root.insertAdjacentHTML("beforeend", block);
  return root.innerHTML;
}

export interface ComposeInit {
  draftId?: string;
  fromId?: string;
  to?: string[];
  cc?: string[];
  subject?: string;
  html?: string; // quoted text for replies and forwards
  replyToId?: number;
  forwardId?: number;
}

export function Composer({ init, addresses, maxMessage, onClose, onSent }: { init: ComposeInit; addresses: Address[]; maxMessage: number; onClose: () => void; onSent: (job: Job) => void }) {
  const senders = addresses.filter((a) => a.canSend);
  const [d, setD] = useState<Draft>({ id: init.draftId ?? "", fromId: init.fromId ?? senders[0]?.id ?? "", to: init.to ?? [], cc: init.cc ?? [], bcc: [], subject: init.subject ?? "", html: "", replyToId: init.replyToId, forwardId: init.forwardId, files: [], updatedAt: 0 });
  const [loaded, setLoaded] = useState(!init.draftId);
  const [showCc, setShowCc] = useState(!!init.cc?.length);
  const [saving, setSaving] = useState<"" | "saving" | "saved">("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [uploading, setUploading] = useState(0);
  const [schedule, setSchedule] = useState(false);
  const [when, setWhen] = useState("");
  const [small, setSmall] = useState(false);
  const dirty = useRef(false);
  const idRef = useRef(init.draftId ?? "");
  const fileInput = useRef<HTMLInputElement>(null);

  // A new message starts with its sender's signature, above any quoted text.
  useEffect(() => {
    if (init.draftId) {
      api<Draft>("GET", "/api/emails/drafts/" + init.draftId)
        .then((x) => (setD(x), setShowCc(x.cc.length + x.bcc.length > 0), setLoaded(true)))
        .catch((e: ApiError) => setError(e.message));
      return;
    }
    const sig = senders.find((a) => a.id === d.fromId)?.signature ?? "";
    setD((x) => ({ ...x, html: withSignature("<p><br></p>" + (init.html ?? ""), sig) }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const change = (p: Partial<Draft>) => {
    dirty.current = true;
    setD((x) => ({ ...x, ...p }));
  };
  const changeFrom = (fromId: string) => {
    const sig = senders.find((a) => a.id === fromId)?.signature ?? "";
    change({ fromId, html: withSignature(d.html, sig) });
  };

  const save = async (x = d): Promise<Draft> => {
    setSaving("saving");
    const body = { ...x, id: undefined, updatedAt: undefined };
    const out = idRef.current ? await api<Draft>("PUT", "/api/emails/drafts/" + idRef.current, body) : await api<Draft>("POST", "/api/emails/drafts", body);
    idRef.current = out.id;
    dirty.current = false;
    setSaving("saved");
    return out;
  };

  // Autosave a moment after typing stops.
  useEffect(() => {
    if (!loaded || !dirty.current) return;
    const t = setTimeout(() => save().catch((e: ApiError) => (setSaving(""), setError(e.message))), 1500);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [d, loaded]);

  const total = d.files.reduce((n, f) => n + f.size, 0);
  const upload = async (list: FileList | null) => {
    if (!list) return;
    for (const f of Array.from(list)) {
      if ((total + f.size) * 1.37 > maxMessage) {
        setError(`${f.name} would take the message over ${fmtBytes(maxMessage)}, the most providers accept.`);
        continue;
      }
      setUploading((n) => n + 1);
      try {
        const fd = new FormData();
        fd.append("file", f);
        const res = await fetch("/api/emails/uploads", { method: "POST", body: fd, credentials: "same-origin" });
        const j = await res.json();
        if (!res.ok) throw new Error(j.error ?? "Upload failed");
        const ref = j as FileRef;
        dirty.current = true;
        setD((x) => ({ ...x, files: [...x.files, ref] }));
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setUploading((n) => n - 1);
      }
    }
  };

  const send = async () => {
    setError("");
    const bad = [...d.to, ...d.cc, ...d.bcc].find((a) => !reAddr.test(a));
    if (bad) return setError(`"${bad}" is not an email address.`);
    if (!d.to.length && !d.cc.length && !d.bcc.length) return setError("Add at least one recipient.");
    let sendAt = 0;
    if (schedule) {
      sendAt = Math.floor(new Date(when).getTime() / 1000);
      if (!when || sendAt < Date.now() / 1000) return setError("Pick a time in the future.");
    }
    setBusy(true);
    try {
      const saved = await save();
      const job = await api<Job>("POST", `/api/emails/drafts/${saved.id}/send`, { sendAt });
      onSent(job);
    } catch (e) {
      setError((e as ApiError).message);
    } finally {
      setBusy(false);
    }
  };

  const discard = async () => {
    if (idRef.current) await api("DELETE", "/api/emails/drafts/" + idRef.current).catch(() => {});
    onClose();
  };

  const close = async () => {
    if (dirty.current) await save().catch(() => {});
    onClose();
  };

  if (!senders.length) {
    return (
      <Shell title="New message" onClose={onClose} small={false} setSmall={() => {}}>
        <p className="p-6 text-sm text-muted">You have no address you may send from. Add one in Domains & addresses (with sending on), or ask the owner.</p>
      </Shell>
    );
  }

  return (
    <Shell title={d.subject || "New message"} onClose={close} small={small} setSmall={setSmall} note={saving === "saving" ? "Saving…" : saving === "saved" ? "Draft saved" : ""}>
      {!loaded ? (
        <div className="p-6"><Spinner /></div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="px-5">
            <div className="flex items-center gap-2 border-b border-line py-1.5">
              <span className="w-10 shrink-0 text-xs text-muted">From</span>
              <Select value={d.fromId} onChange={(e) => changeFrom(e.target.value)} className="border-0 bg-transparent px-0" aria-label="From">
                {senders.map((a) => (
                  <option key={a.id} value={a.id}>{a.displayName ? `${a.displayName} <${a.address}>` : a.address}</option>
                ))}
              </Select>
              {!showCc && <button type="button" className="shrink-0 text-xs text-muted hover:text-fg" onClick={() => setShowCc(true)}>Cc / Bcc</button>}
            </div>
            <Recipients label="To" value={d.to} onChange={(to) => change({ to })} autoFocus={!init.to?.length && !init.draftId} />
            {showCc && <Recipients label="Cc" value={d.cc} onChange={(cc) => change({ cc })} />}
            {showCc && <Recipients label="Bcc" value={d.bcc} onChange={(bcc) => change({ bcc })} />}
            <input value={d.subject} onChange={(e) => change({ subject: e.target.value })} placeholder="Subject" aria-label="Subject" className="w-full border-b border-line bg-transparent py-3 text-sm outline-none" />
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-3">
            <Editor html={d.html} onChange={(html) => change({ html })} autoFocus={!!init.to?.length} />
            {(d.files.length > 0 || uploading > 0 || d.forwardId) && (
              <div className="mt-3 flex flex-wrap gap-2">
                {d.forwardId ? <span className="rounded-full bg-surface2 px-3 py-1 text-xs text-muted">The original's attachments are included</span> : null}
                {d.files.map((f) => (
                  <span key={f.id} className="inline-flex items-center gap-1.5 rounded-full bg-surface2 px-3 py-1 text-xs">
                    <Paperclip size={12} /> {f.name} <span className="text-muted">{fmtBytes(f.size)}</span>
                    <button type="button" aria-label={`Remove ${f.name}`} onClick={() => change({ files: d.files.filter((x) => x.id !== f.id) })} className="text-muted hover:text-fg"><X size={12} /></button>
                  </span>
                ))}
                {uploading > 0 && <span className="inline-flex items-center gap-1.5 text-xs text-muted"><Spinner size={12} /> Uploading…</span>}
              </div>
            )}
            {error && <p className="mt-3 text-sm text-crit" role="alert">{error}</p>}
          </div>
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-5 py-3">
            <Button onClick={send} disabled={busy || uploading > 0}>
              {busy ? <Spinner size={14} /> : <Send size={14} />} {schedule ? "Schedule" : "Send"}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setSchedule(!schedule)} aria-pressed={schedule}><Clock size={14} /> {schedule ? "Send now instead" : "Schedule"}</Button>
            {schedule && <Input type="datetime-local" value={when} onChange={(e) => setWhen(e.target.value)} className="w-auto" aria-label="Send at" />}
            <input ref={fileInput} type="file" multiple hidden onChange={(e) => (upload(e.target.files), (e.target.value = ""))} />
            <Button variant="ghost" size="sm" onClick={() => fileInput.current?.click()}><Paperclip size={14} /> Attach</Button>
            <span className="flex-1" />
            <button type="button" onClick={discard} className="rounded-full p-2 text-muted hover:bg-surface2 hover:text-crit" aria-label="Discard draft" title="Discard draft"><Trash2 size={16} /></button>
          </div>
        </div>
      )}
    </Shell>
  );
}

function Shell({ title, onClose, children, small, setSmall, note }: { title: string; onClose: () => void; children: React.ReactNode; small: boolean; setSmall: (v: boolean) => void; note?: string }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === "Escape" && !small && setSmall(true);
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [small, setSmall]);
  return (
    <div role="dialog" aria-label={title} className={cn("fixed z-[60] flex flex-col overflow-hidden border border-line bg-surface shadow-2xl", small ? "bottom-0 right-4 h-12 w-72 rounded-t-2xl" : "inset-0 sm:inset-auto sm:bottom-4 sm:right-4 sm:h-[min(44rem,calc(100vh-2rem))] sm:w-[min(40rem,calc(100vw-2rem))] sm:rounded-3xl")}>
      <div className="flex h-12 shrink-0 items-center gap-2 border-b border-line px-5">
        <button type="button" className="min-w-0 flex-1 truncate text-left text-sm font-semibold" onClick={() => setSmall(!small)}>{title}</button>
        {note && !small && <span className="text-xs text-muted">{note}</span>}
        <button type="button" onClick={() => setSmall(!small)} className="rounded-full p-1 text-muted hover:bg-surface2 hover:text-fg" aria-label={small ? "Expand" : "Minimize"}><Minimize2 size={15} /></button>
        <button type="button" onClick={onClose} className="rounded-full p-1 text-muted hover:bg-surface2 hover:text-fg" aria-label="Close (keeps the draft)"><X size={17} /></button>
      </div>
      {!small && children}
    </div>
  );
}
