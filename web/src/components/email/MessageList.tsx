import { useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { Archive, MailOpen, Mail, Paperclip, RotateCcw, ShieldAlert, ShieldCheck, Star, Trash2 } from "lucide-react";
import { api, ApiError } from "../../api/client";
import { useStore } from "../../store/store";
import { cn } from "../../utils/cn";
import { Badge, Button, ConfirmDialog, EmptyState, ErrorState, IconButton, LoadingState, Select } from "../ui";
import { act, personLabel, shortDate, type Folder, type Message } from "./data";

export interface ListQuery {
  folder: string;
  label?: string;
  mailbox?: string;
  domain?: string;
  q?: string;
  unread?: boolean;
}

const qs = (q: ListQuery, cursor?: string) => {
  const p = new URLSearchParams();
  if (q.folder && q.folder !== "label") p.set("folder", q.folder);
  if (q.folder === "label") p.set("folder", "all");
  for (const k of ["label", "mailbox", "domain", "q"] as const) if (q[k]) p.set(k, q[k]!);
  if (q.unread) p.set("unread", "1");
  if (cursor) p.set("cursor", cursor);
  return p.toString();
};

/** A page of the list at a time, more as you scroll; refreshed by `version`. */
export function MessageList({ query, version, selectedId, onOpen, folders, onChanged }: { query: ListQuery; version: number; selectedId?: number; onOpen: (m: Message) => void; folders: Folder[]; onChanged: () => void }) {
  const { toast } = useStore();
  const [items, setItems] = useState<Message[]>([]);
  const [next, setNext] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [sel, setSel] = useState<Set<number>>(new Set());
  const [purge, setPurge] = useState(false);
  const key = qs(query);
  const busy = useRef(false);

  useEffect(() => {
    let live = true;
    setLoading(true);
    api<{ messages: Message[]; next?: string }>("GET", "/api/emails/messages?" + key)
      .then((p) => {
        if (!live) return;
        setItems(p.messages);
        setNext(p.next);
        setError("");
        setSel((s) => new Set([...s].filter((id) => p.messages.some((m) => m.id === id))));
      })
      .catch((e: ApiError) => live && setError(e.message))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [key, version]);

  useEffect(() => setSel(new Set()), [key]);

  const more = () => {
    if (!next || busy.current) return;
    busy.current = true;
    api<{ messages: Message[]; next?: string }>("GET", "/api/emails/messages?" + qs(query, next))
      .then((p) => (setItems((x) => [...x, ...p.messages.filter((m) => !x.some((y) => y.id === m.id))]), setNext(p.next)))
      .catch(() => {})
      .finally(() => (busy.current = false));
  };

  const scroller = useRef<HTMLDivElement>(null);
  const v = useVirtualizer({ count: items.length, getScrollElement: () => scroller.current, estimateSize: () => 76, overscan: 8 });
  const vItems = v.getVirtualItems();
  useEffect(() => {
    const last = vItems[vItems.length - 1];
    if (last && last.index >= items.length - 10) more();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [vItems, items.length, next]);

  const bulk = async (action: string, extra: { folder?: string; label?: string } = {}) => {
    try {
      const r = await act([...sel], action, extra);
      toast(`${r.changed} ${r.changed === 1 ? "message" : "messages"} updated`);
      setSel(new Set());
      onChanged();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const star = async (m: Message) => {
    await act([m.id], m.starred ? "unstar" : "star").catch(() => {});
    setItems((x) => x.map((y) => (y.id === m.id ? { ...y, starred: !y.starred } : y)));
  };

  if (error) return <ErrorState message={error} onRetry={onChanged} />;
  const allSel = items.length > 0 && sel.size === items.length;
  const f = query.folder;
  const labels = folders.filter((x) => x.kind === "label");
  const moveTo = folders.filter((x) => x.kind === "folder");

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex min-h-12 flex-wrap items-center gap-1.5 border-b border-line px-3 py-1.5">
        <input type="checkbox" aria-label="Select all" checked={allSel} onChange={() => setSel(allSel ? new Set() : new Set(items.map((m) => m.id)))} className="h-4 w-4 accent-current" />
        {sel.size > 0 ? (
          <>
            <span className="mr-1 text-xs text-muted">{sel.size} selected</span>
            {f !== "archive" && f !== "trash" && <IconButton onClick={() => bulk("move", { folder: "archive" })} aria-label="Archive" title="Archive"><Archive size={15} /></IconButton>}
            {f === "spam" ? <IconButton onClick={() => bulk("notspam")} aria-label="Not spam" title="Not spam"><ShieldCheck size={15} /></IconButton> : f !== "trash" && <IconButton onClick={() => bulk("spam")} aria-label="Spam" title="Spam"><ShieldAlert size={15} /></IconButton>}
            {f === "trash" ? <IconButton onClick={() => bulk("restore")} aria-label="Restore" title="Restore"><RotateCcw size={15} /></IconButton> : <IconButton onClick={() => bulk("trash")} aria-label="Trash" title="Trash"><Trash2 size={15} /></IconButton>}
            <IconButton onClick={() => bulk("read")} aria-label="Mark read" title="Mark read"><MailOpen size={15} /></IconButton>
            <IconButton onClick={() => bulk("unread")} aria-label="Mark unread" title="Mark unread"><Mail size={15} /></IconButton>
            {moveTo.length > 0 && (
              <Select value="" onChange={(e) => e.target.value && bulk("move", { folder: e.target.value })} className="w-auto py-1.5 text-xs" aria-label="Move to folder">
                <option value="">Move to…</option>
                <option value="inbox">Inbox</option>
                {moveTo.map((x) => <option key={x.id} value={"f:" + x.id}>{x.name}</option>)}
              </Select>
            )}
            {labels.length > 0 && (
              <Select value="" onChange={(e) => e.target.value && bulk("label", { label: e.target.value })} className="w-auto py-1.5 text-xs" aria-label="Add label">
                <option value="">Label…</option>
                {labels.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
              </Select>
            )}
            {(f === "trash" || f === "spam") && <Button size="sm" variant="danger" onClick={() => setPurge(true)}>Delete forever</Button>}
          </>
        ) : (
          <span className="text-xs text-muted">{loading ? "Loading…" : items.length ? `${items.length}${next ? "+" : ""} messages` : ""}</span>
        )}
      </div>
      {loading && !items.length ? (
        <LoadingState />
      ) : !items.length ? (
        <EmptyState title={query.q ? "Nothing matches" : "Nothing here"} desc={query.q ? "Try fewer words, or from:, subject:, has:attachment, is:unread, before:2026-01-31." : f === "inbox" ? "New mail shows up here as it arrives." : undefined} />
      ) : (
        <div ref={scroller} className="min-h-0 flex-1 overflow-y-auto" style={{ contain: "strict" }}>
          <div style={{ height: v.getTotalSize(), position: "relative" }}>
            {vItems.map((row) => {
              const m = items[row.index];
              const other = m.direction === "out" ? "To " + (m.to.map(personLabel).join(", ") || "—") : personLabel(m.from);
              return (
                <div key={m.id} data-index={row.index} ref={v.measureElement} style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${row.start}px)` }}>
                  <div
                    role="button"
                    tabIndex={0}
                    onClick={() => onOpen(m)}
                    onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), onOpen(m))}
                    className={cn("flex cursor-pointer items-start gap-3 border-b border-line px-3 py-3 transition hover:bg-surface2", selectedId === m.id && "bg-surface2", m.unread && "bg-surface")}
                  >
                    <input type="checkbox" aria-label="Select" checked={sel.has(m.id)} onClick={(e) => e.stopPropagation()} onChange={() => setSel((s) => { const n = new Set(s); if (n.has(m.id)) n.delete(m.id); else n.add(m.id); return n; })} className="mt-1 h-4 w-4" />
                    <button type="button" aria-label={m.starred ? "Unstar" : "Star"} onClick={(e) => (e.stopPropagation(), star(m))} className="mt-0.5 text-muted hover:text-warn">
                      <Star size={15} className={m.starred ? "fill-warn text-warn" : ""} />
                    </button>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-baseline gap-2">
                        <span className={cn("min-w-0 flex-1 truncate text-sm", m.unread ? "font-semibold" : "text-fg/90")}>
                          {other}
                          {(m.threadCount ?? 0) > 1 && <span className="ml-1 text-xs font-normal text-muted">{m.threadCount}</span>}
                        </span>
                        {m.attachments > 0 && <Paperclip size={13} className="shrink-0 text-muted" aria-label="Has attachments" />}
                        <span className={cn("shrink-0 text-xs", m.unread ? "font-semibold" : "text-muted")}>{shortDate(m.receivedAt)}</span>
                      </div>
                      <div className={cn("truncate text-sm", m.unread ? "font-medium" : "text-muted")}>{m.subject || "(no subject)"}</div>
                      <div className="flex items-center gap-1.5">
                        <span className="min-w-0 flex-1 truncate text-xs text-muted">{m.snippet}</span>
                        {m.outbox && m.outbox.state !== "accepted" && <Badge tone={m.outbox.state === "failed" ? "crit" : m.outbox.state === "unknown" ? "warn" : "info"}>{m.outbox.state}</Badge>}
                        {m.labels.map((l) => {
                          const lf = folders.find((x) => x.id === l);
                          return lf && <span key={l} className="shrink-0 rounded-full border border-line px-2 text-[10px] text-muted" style={{ borderColor: lf.color || undefined }}>{lf.name}</span>;
                        })}
                        <span className="hidden shrink-0 text-[10px] text-muted sm:inline">{m.rcpt}</span>
                      </div>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}
      <ConfirmDialog open={purge} danger title="Delete forever?" message={`${sel.size} messages and their original files are deleted from the server. This cannot be undone.`} confirmLabel="Delete forever" onCancel={() => setPurge(false)} onConfirm={() => (setPurge(false), bulk("purge"))} />
    </div>
  );
}
