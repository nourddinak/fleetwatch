import { useEffect, useState, type ReactNode } from "react";
import { AlertTriangle, Clock, FileText, History, Info, Lock, LockKeyhole, LockOpen, Plus, Save, Server as ServerIcon, Trash2, Undo2, UserRound } from "lucide-react";
import { api, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { dateTime, relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, Input, SectionTitle, Spinner, Textarea } from "./ui";
import { cn } from "../utils/cn";

export const CADDY_EXAMPLE = "example.com {\n\treverse_proxy localhost:3000\n}";
export const NGINX_EXAMPLE = "server {\n\tlisten 80;\n\tserver_name example.com;\n\tlocation / {\n\t\tproxy_pass http://localhost:3000;\n\t}\n}";

interface FileRes {
  name: string;
  files: string[];
  exists: boolean;
  content: string;
}

interface Version {
  id: number;
  by: string;
  ts: number;
  content: string;
}

/** Line diff from a to b (longest common subsequence; these files are small). */
export function lineDiff(a: string, b: string): { t: "same" | "add" | "del"; line: string }[] {
  const x = a ? a.split("\n") : [], y = b ? b.split("\n") : [];
  const n = x.length, m = y.length;
  const L = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) L[i][j] = x[i] === y[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
  const out: { t: "same" | "add" | "del"; line: string }[] = [];
  let i = 0, j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) out.push({ t: "same", line: x[i++] }), j++;
    else if (j < m && (i >= n || L[i][j + 1] >= L[i + 1][j])) out.push({ t: "add", line: y[j++] });
    else out.push({ t: "del", line: x[i++] });
  }
  return out;
}

function FileHistory({ base, name, pw, extra, reloadKey, onRestore, onLocked }: { base: string; name: string; pw: string; extra?: Record<string, string>; reloadKey: number; onRestore: (text: string) => void; onLocked: () => void }) {
  const { toast } = useStore();
  const [list, setList] = useState<Version[] | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const [del, setDel] = useState<Version | "all" | null>(null);
  const failed = (e: unknown) => {
    const er = e as ApiError;
    toast(er.message, "err");
    if (er.code === "wrong_password") onLocked();
  };
  useEffect(() => {
    let live = true;
    setList(null);
    api<Version[]>("POST", `${base}/history`, { ...extra, password: pw, name })
      .then((l) => live && setList(l))
      .catch((e) => live && (setList([]), failed(e)));
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [base, name, pw, reloadKey]);
  const remove = async () => {
    const target = del;
    setDel(null);
    if (!target) return;
    try {
      const r = await api<{ deleted: number }>("DELETE", `${base}/history`, { ...extra, password: pw, name, id: target === "all" ? 0 : target.id });
      setList((l) => (target === "all" ? [] : (l ?? []).filter((v) => v.id !== target.id)));
      toast(`${r.deleted} version${r.deleted === 1 ? "" : "s"} deleted`);
    } catch (e) {
      failed(e);
    }
  };
  return (
    <div className="mt-6 border-t border-line pt-5">
      <SectionTitle icon={<History size={15} />} title={`History of ${name}`} sub="Every save from the dashboard, and what was on the server before it when it had been changed there. Newest first, up to 100."
        action={list && list.length > 0 && <Button size="sm" variant="ghost" onClick={() => setDel("all")}><Trash2 size={13} /> Delete all</Button>} />
      {!list ? <Spinner /> : list.length === 0 ? <p className="text-sm text-muted">No saved versions yet. They appear here from the next save.</p> : (
        <ol className="relative space-y-2 before:absolute before:bottom-3 before:left-[5px] before:top-3 before:w-px before:bg-line">
          {list.map((v, i) => {
            const prev = list[i + 1];
            const diff = open === v.id ? lineDiff(prev?.content ?? "", v.content) : null;
            const changes = prev ? lineDiff(prev.content, v.content).filter((d) => d.t !== "same").length : 0;
            return (
              <li key={v.id} className="relative pl-6">
                <span className={cn("absolute left-0 top-3 h-[11px] w-[11px] rounded-full border-2 border-surface", i === 0 ? "bg-ok" : "bg-muted")} />
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-xl px-2 py-1.5 hover:bg-surface2">
                  <button className="flex min-w-0 flex-1 items-center gap-2 text-left text-sm" onClick={() => setOpen(open === v.id ? null : v.id)}>
                    <span className="inline-flex items-center gap-1.5 font-medium">{v.by === "on the server" ? <ServerIcon size={13} /> : <UserRound size={13} />}{v.by === "on the server" ? "Changed on the server" : v.by}</span>
                    <span className="inline-flex items-center gap-1 text-xs text-muted" title={dateTime(v.ts)}><Clock size={11} />{relTime(v.ts)}</span>
                    {i === 0 && <Badge tone="ok">newest</Badge>}
                    {prev ? <span className="text-xs text-muted">{changes} line{changes === 1 ? "" : "s"} changed</span> : <span className="text-xs text-muted">first version</span>}
                  </button>
                  <Button size="sm" variant="ghost" onClick={() => { onRestore(v.content); toast("Loaded into the editor: press Save to put it back on the server"); }}><Undo2 size={13} /> Restore</Button>
                  <button className="grid h-8 w-8 place-items-center rounded-full text-muted hover:bg-crit-soft hover:text-crit" title="Delete this version" aria-label="Delete this version" onClick={() => setDel(v)}><Trash2 size={13} /></button>
                </div>
                {diff && (
                  <pre className="mt-1 max-h-80 overflow-auto rounded-xl bg-surface2 p-3 font-mono text-[11px] leading-relaxed">
                    {diff.map((d, k) => (
                      <div key={k} className={cn(d.t === "add" && "bg-ok-soft text-ok", d.t === "del" && "bg-crit-soft text-crit line-through decoration-crit/40")}>
                        {d.t === "add" ? "+ " : d.t === "del" ? "- " : "  "}{d.line || " "}
                      </div>
                    ))}
                  </pre>
                )}
              </li>
            );
          })}
        </ol>
      )}
      <ConfirmDialog open={!!del} danger title={del === "all" ? `Delete the whole history of ${name}?` : "Delete this version?"} confirmLabel="Delete"
        message={del === "all" ? "Every saved version of this file is removed from the dashboard. The file on the server stays as it is." : "This saved version is removed from the dashboard. The file on the server stays as it is."}
        onCancel={() => setDel(null)} onConfirm={remove} />
    </div>
  );
}

/**
 * A secret or config file on a server, unlocked with the password: an app's .env or web
 * server files, or a server's Caddy or Nginx configuration. With its history.
 */
export function FileEditor({ base, dir, fixed, known, newName: newRule, extra, icon, title, lockedTitle, lockedText, placeholder, saveHint, savedToast, actions }: {
  base: string; // API path: /api/apps/<id>/env or /api/servers/<id>/web
  dir: string; // where it is, for the subtitle
  fixed?: string; // the only file
  known?: string[]; // files always offered (the first opens first); the server adds the ones it finds
  newName?: { pattern: RegExp; placeholder: string; hint: string }; // files that can be created
  extra?: Record<string, string>; // sent with every request (which web server)
  icon: ReactNode;
  title: string;
  lockedTitle: string;
  lockedText: string;
  placeholder: string;
  saveHint: string;
  savedToast: string;
  actions?: ReactNode;
}) {
  const { toast } = useStore();
  // The password stays in this tab only, so saving does not ask again; leaving the tab forgets it.
  const [pw, setPw] = useState("");
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [files, setFiles] = useState<string[] | null>(null);
  const [name, setName] = useState(fixed ?? known?.[0] ?? ".env");
  const [orig, setOrig] = useState<string | null>(null); // null: the file is not there yet
  const [text, setText] = useState("");
  const [newName, setNewName] = useState("");
  const [saves, setSaves] = useState(0); // reloads the history after a save
  const locked = files === null;
  const lock = () => { setPw(""); setTyped(""); setFiles(null); setText(""); setOrig(null); };
  const open = async (n: string, password = pw) => {
    setBusy(true);
    setErr("");
    try {
      const r = await api<FileRes>("POST", base, { ...extra, password, name: n });
      setPw(password);
      setFiles(r.files ?? []);
      setName(fixed ?? n);
      setOrig(r.exists ? r.content : null);
      setText(r.exists ? r.content : "");
    } catch (e) {
      const er = e as ApiError;
      if (locked) setErr(er.message);
      else toast(er.message, "err");
      if (er.code === "wrong_password") lock();
    }
    setBusy(false);
  };
  const save = async () => {
    setBusy(true);
    try {
      const r = await api<FileRes>("PUT", base, { ...extra, password: pw, name, content: text });
      setFiles(r.files ?? []);
      setOrig(r.content);
      setSaves((n) => n + 1);
      toast(savedToast.replace("{name}", name));
    } catch (e) {
      const er = e as ApiError;
      toast(er.message, "err");
      if (er.code === "wrong_password") lock();
    }
    setBusy(false);
  };
  const dirty = text !== (orig ?? "");

  if (locked) {
    return (
      <Card className="mx-auto max-w-lg">
        <form onSubmit={(e) => { e.preventDefault(); if (typed) open(name, typed); }} className="space-y-4 text-center">
          <span className="mx-auto grid h-12 w-12 place-items-center rounded-2xl bg-surface2">{icon}</span>
          <div>
            <h3 className="text-[15px] font-semibold tracking-tight">{lockedTitle}</h3>
            <p className="mt-1 text-sm text-muted">{lockedText} Every opening and save is recorded in the audit log.</p>
          </div>
          <div className="relative text-left">
            <LockKeyhole size={15} className="pointer-events-none absolute left-4 top-1/2 z-10 -translate-y-1/2 text-muted" />
            <Input type="password" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="Your password" autoComplete="current-password" autoFocus className="pl-10" />
          </div>
          {err && <p role="alert" className="flex items-center justify-center gap-2 text-sm text-crit"><AlertTriangle size={14} />{err}</p>}
          <Button type="submit" className="w-full" disabled={!typed || busy}>{busy ? <Spinner size={14} /> : <LockOpen size={14} />} Unlock</Button>
        </form>
      </Card>
    );
  }
  const shown = fixed ? [] : [...new Set([...(known ?? []), ...files])];
  return (
    <Card>
      <SectionTitle icon={icon} title={title} sub={<span className="font-mono">{dir.replace(/\/$/, "")}/{name}</span>}
        action={<Button size="sm" variant="ghost" onClick={lock}><Lock size={13} /> Lock</Button>} />
      {!fixed && (
        <div className="mb-3 flex flex-wrap items-center gap-2">
          {shown.map((f) => (
            <button key={f} type="button" disabled={busy} onClick={() => f !== name && open(f)}
              className={cn("inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 font-mono text-xs transition", f === name ? "border-fg/40 bg-surface2 text-fg" : "border-line text-muted hover:text-fg", !files.includes(f) && "border-dashed")}>
              <FileText size={12} />{f}
            </button>
          ))}
          {newRule && (
            <form onSubmit={(e) => { e.preventDefault(); const n = newName.trim(); if (newRule.pattern.test(n)) { setNewName(""); open(n); } else toast(newRule.hint, "err"); }} className="flex items-center gap-1">
              <Input value={newName} onChange={(e) => setNewName(e.target.value)} placeholder={newRule.placeholder} className="!w-48 !py-1.5 font-mono text-xs" />
              <Button size="sm" variant="ghost" type="submit"><Plus size={13} /> Open</Button>
            </form>
          )}
        </div>
      )}
      {orig === null && <p className="mb-2 flex items-center gap-2 text-xs text-muted"><Info size={13} />{name} does not exist yet: saving creates it (readable by the folder's owner only).</p>}
      <Textarea rows={18} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} className="font-mono text-[12px] leading-relaxed" placeholder={placeholder} />
      <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
        <p className="flex items-center gap-2 text-xs text-muted"><History size={13} />{saveHint.replace("{name}", name)}</p>
        <div className="flex gap-2">
          {actions}
          <Button size="sm" disabled={!dirty || busy} onClick={save}>{busy ? <Spinner size={13} /> : <Save size={13} />} Save {name}</Button>
        </div>
      </div>
      <FileHistory base={base} name={name} pw={pw} extra={extra} reloadKey={saves} onRestore={setText} onLocked={lock} />
    </Card>
  );
}
