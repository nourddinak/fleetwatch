import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { FolderPlus, Inbox, PenSquare, Plus, Tag } from "lucide-react";
import { api, ApiError, useApi } from "../api/client";
import { useStore } from "../store/store";
import { cn } from "../utils/cn";
import { Button, EmptyState, ErrorState, Input, LoadingState, Modal, PageHeader, SearchInput, Segmented, Select } from "../components/ui";
import { SYSTEM_FOLDERS, shortDate, type Draft, type Job, type Overview } from "../components/email/data";
import { MessageList, type ListQuery } from "../components/email/MessageList";
import { MessageViewer } from "../components/email/MessageViewer";
import { Composer, type ComposeInit } from "../components/email/Composer";
import { DomainsAndAddresses } from "../components/email/Addresses";
import { EmailSettings } from "../components/email/Settings";

type Tab = "mail" | "addresses" | "settings";

function Drafts({ version, onOpen }: { version: number; onOpen: (id: string) => void }) {
  const [ds, setDs] = useState<Draft[] | null>(null);
  useEffect(() => {
    api<Draft[]>("GET", "/api/emails/drafts").then(setDs).catch(() => setDs([]));
  }, [version]);
  if (!ds) return <LoadingState />;
  if (!ds.length) return <EmptyState title="No drafts" desc="What you write is saved here as you type." />;
  return (
    <ul className="divide-y divide-line">
      {ds.map((d) => (
        <li key={d.id}>
          <button type="button" onClick={() => onOpen(d.id)} className="flex w-full items-baseline gap-3 px-4 py-3 text-left hover:bg-surface2">
            <span className="w-40 shrink-0 truncate text-sm text-crit">Draft{d.to.length ? ` · ${d.to.join(", ")}` : ""}</span>
            <span className="min-w-0 flex-1 truncate text-sm">{d.subject || "(no subject)"}</span>
            <span className="shrink-0 text-xs text-muted">{shortDate(d.updatedAt)}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function NewFolder({ kind, onClose, onDone }: { kind: "folder" | "label" | null; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const save = () =>
    api("POST", "/api/emails/folders", { kind, name, color: "" })
      .then(() => (setName(""), onDone(), onClose()))
      .catch((e: ApiError) => setError(e.message));
  return (
    <Modal open={!!kind} onClose={onClose} title={kind === "label" ? "New label" : "New folder"} footer={<Button onClick={save} disabled={!name.trim()}>Create</Button>}>
      <Input value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && save()} placeholder="Name" autoFocus aria-label="Name" />
      {error && <p className="mt-2 text-sm text-crit">{error}</p>}
    </Modal>
  );
}

export function Emails() {
  const { subscribeLive, toast } = useStore();
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") as Tab) || "mail";
  const folder = params.get("folder") || "inbox";
  const label = params.get("label") || "";
  const mailbox = params.get("mailbox") || "";
  const openId = Number(params.get("m")) || 0;
  const [q, setQ] = useState("");
  const [search, setSearch] = useState("");
  const [unread, setUnread] = useState(false);
  const [version, setVersion] = useState(0);
  const [compose, setCompose] = useState<ComposeInit | null>(null);
  const [undo, setUndo] = useState<{ job: Job; left: number } | null>(null);
  const [newFolder, setNewFolder] = useState<"folder" | "label" | null>(null);
  const ov = useApi<Overview>("/api/emails/overview");

  const bump = useCallback(() => {
    setVersion((v) => v + 1);
    ov.reload();
  }, [ov]);
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | undefined;
    const off = subscribeLive((m) => {
      if (m.type !== "emails") return;
      clearTimeout(t);
      t = setTimeout(bump, 500);
    });
    return () => (off(), clearTimeout(t));
  }, [subscribeLive, bump]);
  useEffect(() => {
    const t = setTimeout(() => setSearch(q.trim()), 300);
    return () => clearTimeout(t);
  }, [q]);
  useEffect(() => {
    if (!undo) return;
    if (undo.left <= 0) return setUndo(null);
    const t = setTimeout(() => setUndo((u) => u && { ...u, left: u.left - 1 }), 1000);
    return () => clearTimeout(t);
  }, [undo]);

  const go = (p: Record<string, string | null>) => {
    const n = new URLSearchParams(params);
    for (const [k, v] of Object.entries(p)) if (v) n.set(k, v); else n.delete(k);
    setParams(n);
  };

  if (ov.error) return <ErrorState message={ov.error} onRetry={ov.reload} />;
  if (!ov.data) return <LoadingState />;
  const o = ov.data;

  const onSent = (job: Job) => {
    setCompose(null);
    bump();
    const left = Math.round(job.sendAt - Date.now() / 1000);
    if (left > 60) toast(`Scheduled for ${new Date(job.sendAt * 1000).toLocaleString()}`);
    else if (left > 0) setUndo({ job, left });
    else toast("Sending…");
  };
  const undoSend = async () => {
    if (!undo) return;
    try {
      const r = await api<{ draftId: string }>("POST", `/api/emails/outbox/${undo.job.id}/cancel`);
      setUndo(null);
      setCompose({ draftId: r.draftId });
      bump();
    } catch (e) {
      toast((e as ApiError).message, "err");
      setUndo(null);
    }
  };

  const inboxes = o.addresses.filter((a) => !a.inboxId && a.canRead);
  const query: ListQuery = { folder: label ? "label" : folder, label, mailbox, q: search, unread };
  const nav = [
    ...SYSTEM_FOLDERS.map((f) => ({ key: f.id, label: f.label, count: f.id === "inbox" ? o.counts.inbox : f.id === "spam" ? o.counts.spam : f.id === "drafts" ? o.counts.drafts : 0, active: !label && folder === f.id, to: { folder: f.id, label: null, m: null } })),
    ...o.folders.filter((f) => f.kind === "folder").map((f) => ({ key: f.id, label: f.name, count: f.unread, active: !label && folder === "f:" + f.id, to: { folder: "f:" + f.id, label: null, m: null } })),
  ];
  const labelNav = o.folders.filter((f) => f.kind === "label");
  const noMail = !o.domains.length;

  return (
    <div>
      <PageHeader title="Emails" sub="Mail on your own domains: receive, read, reply and organise here, with a copy in Gmail."
        actions={tab === "mail" && <Button onClick={() => setCompose({})} disabled={!o.addresses.some((a) => a.canSend)}><PenSquare size={15} /> Compose</Button>} />
      <div className="mb-5">
        <Segmented value={tab} onChange={(t) => go({ tab: t === "mail" ? null : t, m: null })} options={[{ value: "mail", label: "Mail" }, { value: "addresses", label: "Domains & addresses" }, ...(o.owner ? [{ value: "settings" as Tab, label: "Settings" }] : [])]} />
      </div>

      {tab === "addresses" && <DomainsAndAddresses ov={o} reload={bump} />}
      {tab === "settings" && <EmailSettings ov={o} reload={bump} />}
      {tab === "mail" && (noMail ? (
        <EmptyState icon={<Inbox size={22} />} title="No mail set up yet" desc={o.owner ? "Add a domain and an address, then connect Cloudflare so mail can arrive." : "The owner has not shared any mail with you."}
          action={o.owner && <Button onClick={() => go({ tab: "addresses" })}><Plus size={15} /> Add a domain</Button>} />
      ) : (
        <div className="grid gap-4 lg:grid-cols-[13rem_minmax(0,1fr)]">
          {/* Folders: a rail on wide screens, a menu on phones. */}
          <nav aria-label="Folders" className="hidden lg:block">
            <ul className="space-y-0.5">
              {nav.map((n) => (
                <li key={n.key}>
                  <button type="button" onClick={() => go(n.to)} className={cn("flex w-full items-center gap-2 rounded-full px-4 py-2 text-left text-sm transition", n.active ? "bg-accent font-medium text-accent-fg" : "text-muted hover:bg-surface2 hover:text-fg")}>
                    <span className="min-w-0 flex-1 truncate">{n.label}</span>
                    {n.count > 0 && <span className="text-xs">{n.count}</span>}
                  </button>
                </li>
              ))}
            </ul>
            {labelNav.length > 0 && (
              <ul className="mt-4 space-y-0.5">
                {labelNav.map((l) => (
                  <li key={l.id}>
                    <button type="button" onClick={() => go({ label: l.id, m: null })} className={cn("flex w-full items-center gap-2 rounded-full px-4 py-2 text-left text-sm", label === l.id ? "bg-accent text-accent-fg" : "text-muted hover:bg-surface2 hover:text-fg")}>
                      <Tag size={13} style={{ color: label === l.id ? undefined : l.color || undefined }} /> <span className="min-w-0 flex-1 truncate">{l.name}</span>
                      {l.unread > 0 && <span className="text-xs">{l.unread}</span>}
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <div className="mt-2 flex gap-1 px-2">
              <Button size="sm" variant="ghost" onClick={() => setNewFolder("folder")}><FolderPlus size={13} /> Folder</Button>
              <Button size="sm" variant="ghost" onClick={() => setNewFolder("label")}><Tag size={13} /> Label</Button>
            </div>
            {inboxes.length > 1 && (
              <>
                <div className="mb-1 mt-5 px-4 text-[11px] font-medium uppercase tracking-wide text-muted">Inboxes</div>
                <ul className="space-y-0.5">
                  <li>
                    <button type="button" onClick={() => go({ mailbox: null, m: null })} className={cn("w-full rounded-full px-4 py-1.5 text-left text-xs", !mailbox ? "font-medium text-fg" : "text-muted hover:text-fg")}>All inboxes</button>
                  </li>
                  {inboxes.map((a) => (
                    <li key={a.id}>
                      <button type="button" onClick={() => go({ mailbox: a.id, m: null })} className={cn("flex w-full items-center gap-2 rounded-full px-4 py-1.5 text-left text-xs", mailbox === a.id ? "bg-surface2 font-medium text-fg" : "text-muted hover:text-fg")} title={a.address}>
                        <span className="min-w-0 flex-1 truncate">{a.address}</span>
                        {(o.counts.boxes[a.id] ?? 0) > 0 && <span>{o.counts.boxes[a.id]}</span>}
                      </button>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </nav>

          <div className="min-w-0">
            <div className="mb-3 flex flex-wrap gap-2">
              <Select className="w-auto lg:hidden" value={label ? "label:" + label : folder} onChange={(e) => e.target.value.startsWith("label:") ? go({ label: e.target.value.slice(6), m: null }) : go({ folder: e.target.value, label: null, m: null })} aria-label="Folder">
                {nav.map((n) => <option key={n.key} value={n.to.folder}>{n.label}{n.count ? ` (${n.count})` : ""}</option>)}
                {labelNav.map((l) => <option key={l.id} value={"label:" + l.id}>Label: {l.name}</option>)}
              </Select>
              {inboxes.length > 1 && (
                <Select className="w-auto lg:hidden" value={mailbox} onChange={(e) => go({ mailbox: e.target.value || null, m: null })} aria-label="Inbox">
                  <option value="">All inboxes</option>
                  {inboxes.map((a) => <option key={a.id} value={a.id}>{a.address}</option>)}
                </Select>
              )}
              <SearchInput value={q} onChange={setQ} placeholder="Search mail (from: subject: has:attachment is:unread…)" className="min-w-0 flex-1 basis-full sm:basis-auto" />
              <Button variant={unread ? "primary" : "secondary"} size="sm" onClick={() => setUnread(!unread)} aria-pressed={unread}>Unread</Button>
            </div>
            <div className={cn("grid gap-4", openId && "xl:grid-cols-[minmax(0,26rem)_minmax(0,1fr)]")}>
              <div className={cn("flex h-[calc(100vh-17rem)] min-h-[24rem] flex-col overflow-hidden rounded-3xl border border-line bg-surface", openId && "hidden xl:flex")}>
                {folder === "drafts" && !label ? (
                  <div className="overflow-y-auto"><Drafts version={version} onOpen={(id) => setCompose({ draftId: id })} /></div>
                ) : (
                  <MessageList query={query} version={version} selectedId={openId} folders={o.folders} onChanged={bump} onOpen={(m) => go({ m: String(m.id) })} />
                )}
              </div>
              {openId > 0 && (
                <div className="min-w-0">
                  <MessageViewer id={openId} ov={o} version={version} onBack={() => go({ m: null })} onCompose={setCompose} onChanged={bump} />
                </div>
              )}
            </div>
          </div>
        </div>
      ))}

      {compose && <Composer init={compose} addresses={o.addresses} maxMessage={o.maxMessage} onClose={() => (setCompose(null), bump())} onSent={onSent} />}
      {undo && (
        <div role="status" className="fixed bottom-4 left-1/2 z-[61] flex -translate-x-1/2 items-center gap-3 rounded-full bg-fg px-5 py-3 text-sm text-bg shadow-2xl">
          Sending in {undo.left}s
          <button type="button" onClick={undoSend} className="font-semibold underline">Undo</button>
        </div>
      )}
      <NewFolder kind={newFolder} onClose={() => setNewFolder(null)} onDone={bump} />
    </div>
  );
}
