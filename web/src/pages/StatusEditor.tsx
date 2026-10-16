import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowDown, ArrowUp, ExternalLink, Eye, Plus, Radio, Trash2, Upload, X } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CodeBlock, ConfirmDialog, CopyButton, EmptyState, ErrorState, Field, Gate, IconButton, Input, LoadingState, Modal, PageHeader, SectionTitle, Segmented, Select, Spinner, Textarea, ToggleRow } from "../components/ui";
import { StatusView, type PublicStatus } from "./StatusPage";

interface Component {
  kind: "app" | "server";
  id: string;
  name: string;
  description: string;
}
interface Group {
  name: string;
  components: Component[];
}
interface Notice {
  id: string;
  title: string;
  body: string;
  status: "info" | "maintenance" | "degraded" | "outage" | "resolved";
  start: number;
  end: number;
  createdAt: number;
}
interface Config {
  id: string;
  slug: string;
  enabled: boolean;
  title: string;
  description: string;
  logo: string;
  accent: string;
  theme: "auto" | "light" | "dark";
  homeUrl: string;
  homeLabel: string;
  footer: string;
  poweredBy: boolean;
  domain: string;
  historyDays: number;
  showUptime: boolean;
  groups: Group[];
  notices: Notice[];
  updatedAt?: number;
}

const NOTICE_TONE = { info: "info", maintenance: "info", degraded: "warn", outage: "crit", resolved: "ok" } as const;
// datetime-local works in local time without a zone.
const toLocal = (ms: number) => (ms ? new Date(ms - new Date(ms).getTimezoneOffset() * 60_000).toISOString().slice(0, 16) : "");
const fromLocal = (s: string) => (s ? new Date(s).getTime() : 0);

const pageUrl = (slug: string, domain?: string) => (domain ? `https://${domain}` : `${location.origin}/status${slug ? "/" + slug : ""}`);

interface Summary {
  id: string;
  slug: string;
  title: string;
  enabled: boolean;
  domain: string;
  components: number;
  notices: number;
  updatedAt: number;
}

/** Every status page: a main one at /status, and any number at /status/<address>. */
export function StatusPages() {
  const { servers, apps, toast } = useStore();
  const nav = useNavigate();
  const list = useApi<Summary[]>("/api/status-pages");
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [from, setFrom] = useState("");
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState<Summary | null>(null);
  const hasMain = list.data?.some((p) => p.slug === "") ?? false;

  const open = () => {
    setTitle(hasMain ? "" : "Service status");
    setSlug("");
    setFrom("");
    setCreating(true);
  };
  const pickFrom = (v: string) => {
    setFrom(v);
    const [kind, id] = v.split(/:(.*)/s);
    const name = kind === "app" ? apps.find((a) => a.id === id)?.name : servers.find((s) => s.id === id)?.name;
    if (name) {
      setTitle(`${name} status`);
      setSlug(name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40));
    }
  };
  const create = async () => {
    setBusy(true);
    const [kind, id] = from.split(/:(.*)/s);
    const name = kind === "app" ? apps.find((a) => a.id === id)?.name : servers.find((s) => s.id === id)?.name;
    try {
      const out = await api<Config>("POST", "/api/status-pages", {
        title, slug, enabled: false, accent: "#16a34a", theme: "auto", poweredBy: true, historyDays: 90, showUptime: true,
        groups: from && name ? [{ name: "", components: [{ kind, id, name, description: "" }] }] : [],
        notices: [],
      });
      nav(`/status-page/${out.id}`);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const remove = async (p: Summary) => {
    setDel(null);
    try {
      await api("DELETE", `/api/status-pages/${p.id}`);
      toast(`Deleted "${p.title}"`);
      list.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };

  return (
    <>
      <PageHeader
        title="Status pages"
        sub="Public pages your users can check when something is down. Make one for everything, one per app or customer, or both; each has its own address, look and contents."
        actions={<Button size="sm" onClick={open}><Plus size={14} /> New page</Button>}
      />
      <Gate>
        {list.error ? (
          <ErrorState message={list.error} onRetry={list.reload} />
        ) : !list.data ? (
          <LoadingState />
        ) : list.data.length === 0 ? (
          <EmptyState icon={<Radio size={20} />} title="No status pages yet" desc="Create one, choose what it shows, then turn it on." action={<Button onClick={open}><Plus size={14} /> New page</Button>} />
        ) : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {list.data.map((p) => (
              <Card key={p.id} className="flex flex-col gap-3">
                <div className="flex items-start justify-between gap-2">
                  <Link to={`/status-page/${p.id}`} className="min-w-0 truncate font-semibold hover:underline">{p.title}</Link>
                  {p.enabled ? <Badge tone="ok" dot>Public</Badge> : <Badge>Off</Badge>}
                </div>
                <div className="truncate text-xs text-muted">{pageUrl(p.slug, p.domain)}{p.slug === "" && !p.domain ? " · main page" : ""}</div>
                <div className="text-xs text-muted">{p.components} component{p.components === 1 ? "" : "s"} · {p.notices} notice{p.notices === 1 ? "" : "s"}</div>
                <div className="mt-auto flex flex-wrap gap-2 pt-1">
                  <Link to={`/status-page/${p.id}`}><Button size="sm" variant="secondary">Edit</Button></Link>
                  {p.enabled && <a href={`/status${p.slug ? "/" + p.slug : ""}`} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost"><ExternalLink size={14} /> Open</Button></a>}
                  <Button size="sm" variant="ghost" className="ml-auto" onClick={() => setDel(p)}><Trash2 size={14} /></Button>
                </div>
              </Card>
            ))}
          </div>
        )}
      </Gate>

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="New status page"
        footer={
          <>
            <Button variant="ghost" onClick={() => setCreating(false)}>Cancel</Button>
            <Button disabled={busy || !title.trim() || (hasMain && !slug)} onClick={create}>{busy && <Spinner size={14} />} Create</Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="Start with" hint="Optional: a page for one app or server. You can add more later.">
            <Select value={from} onChange={(e) => pickFrom(e.target.value)}>
              <option value="">Empty page</option>
              <optgroup label="Applications">{apps.map((a) => <option key={a.id} value={`app:${a.id}`}>{a.name}</option>)}</optgroup>
              <optgroup label="Whole servers">{servers.map((s) => <option key={s.id} value={`server:${s.id}`}>{s.name}</option>)}</optgroup>
            </Select>
          </Field>
          <Field label="Title">
            <Input value={title} maxLength={80} onChange={(e) => setTitle(e.target.value)} />
          </Field>
          <Field label="Address" hint={hasMain ? "Letters, numbers and dashes." : "Leave empty to make this the main page at /status."}>
            <div className="flex items-center gap-2 text-sm">
              <span className="whitespace-nowrap text-muted">/status/</span>
              <Input value={slug} maxLength={40} placeholder={hasMain ? "my-app" : ""} onChange={(e) => setSlug(e.target.value.toLowerCase())} />
            </div>
          </Field>
          <p className="text-xs text-muted">New pages start turned off, so you can set them up first.</p>
        </div>
      </Modal>
      <ConfirmDialog open={!!del} title="Delete status page?" message={`"${del?.title}" and its notices are deleted, and its address stops working.`} confirmLabel="Delete" danger onConfirm={() => del && remove(del)} onCancel={() => setDel(null)} />
    </>
  );
}

export function StatusEditor() {
  const { servers, apps, getServer, toast } = useStore();
  const { id } = useParams();
  const saved = useApi<Config>(`/api/status-pages/${id}`);
  const [c, setC] = useState<Config | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [preview, setPreview] = useState(false);
  const [logoUrl, setLogoUrl] = useState("");
  const [fetching, setFetching] = useState(false);
  useEffect(() => {
    if (saved.data) setC(saved.data);
  }, [saved.data]);

  if (saved.error) return <ErrorState message={saved.error} onRetry={saved.reload} />;
  if (!c || !saved.data) return <LoadingState />;
  const dirty = JSON.stringify(c) !== JSON.stringify(saved.data);
  const set = (p: Partial<Config>) => setC({ ...c, ...p });
  const setGroup = (gi: number, g: Group | null) => set({ groups: g ? c.groups.map((x, i) => (i === gi ? g : x)) : c.groups.filter((_, i) => i !== gi) });
  const move = <T,>(list: T[], i: number, d: number) => {
    const out = [...list];
    [out[i], out[i + d]] = [out[i + d], out[i]];
    return out;
  };
  const used = new Set(c.groups.flatMap((g) => g.components.map((x) => `${x.kind}:${x.id}`)));
  const addComponent = (gi: number) => {
    const app = apps.find((a) => !used.has(`app:${a.id}`));
    const srv = servers.find((s) => !used.has(`server:${s.id}`));
    const x: Component | null = app ? { kind: "app", id: app.id, name: app.name, description: "" } : srv ? { kind: "server", id: srv.id, name: srv.name, description: "" } : null;
    if (!x) return toast("Every app and server is already on the page", "err");
    setGroup(gi, { ...c.groups[gi], components: [...c.groups[gi].components, x] });
  };

  const save = async (next = c) => {
    setBusy(true);
    try {
      const out = await api<Config>("PUT", `/api/status-pages/${c.id}`, next);
      saved.setData(out);
      setC(out);
      toast("Status page saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  // The server downloads the image once and stores it like an upload.
  const fetchLogo = async () => {
    setFetching(true);
    try {
      const { logo } = await api<{ logo: string }>("POST", "/api/status-pages/logo", { url: logoUrl.trim() });
      set({ logo });
      setLogoUrl("");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setFetching(false);
  };
  const pickLogo = (f?: File) => {
    if (!f) return;
    if (f.size > 256 * 1024) return toast("Logo must be at most 256 KB", "err");
    const r = new FileReader();
    r.onload = () => set({ logo: String(r.result) });
    r.readAsDataURL(f);
  };
  const saveNotice = (n: Notice) => {
    const exists = c.notices.some((x) => x.id === n.id);
    set({ notices: exists ? c.notices.map((x) => (x.id === n.id ? n : x)) : [n, ...c.notices] });
    setNotice(null);
  };
  const publicUrl = pageUrl(saved.data.slug, saved.data.domain);
  const path = `/status${saved.data.slug ? "/" + saved.data.slug : ""}`;

  const sample: PublicStatus = {
    ...c,
    overall: "operational",
    notices: c.notices.filter((n) => n.status !== "resolved").map((n) => ({ ...n, end: n.end || undefined })),
    groups: c.groups.map((g) => ({
      name: g.name,
      components: g.components.map((x) => ({
        name: x.name,
        description: x.description,
        status: "operational",
        uptime: c.showUptime ? 99.95 : undefined,
        days: c.showUptime ? Array.from({ length: c.historyDays }, (_, i) => ({ date: new Date(Date.now() - (c.historyDays - 1 - i) * 86_400_000).toISOString().slice(0, 10), status: i % 37 === 20 ? ("degraded" as const) : ("operational" as const) })) : undefined,
      })),
    })),
    updatedAt: Date.now(),
  };

  return (
    <>
      <PageHeader
        title={saved.data.title}
        back={{ to: "/status-page", label: "All status pages" }}
        sub="Only the names and texts you set here are shown, never hostnames, IPs or incident details."
        actions={
          <>
            <Button size="sm" variant="secondary" onClick={() => setPreview(true)}><Eye size={14} /> Preview</Button>
            {saved.data.enabled && (
              <a href={path} target="_blank" rel="noreferrer">
                <Button size="sm" variant="secondary"><ExternalLink size={14} /> Open</Button>
              </a>
            )}
            <Button size="sm" disabled={!dirty || busy} onClick={() => save()}>{busy && <Spinner size={14} />} Save</Button>
          </>
        }
      />
      <Gate>
        <div className="grid gap-6 pb-20 xl:grid-cols-2">
          <Card>
            <SectionTitle title="Publish" />
            <ToggleRow label="Status page is public" desc="Anyone with the address can see it, without signing in." checked={c.enabled} onChange={(v) => set({ enabled: v })} />
            <Field label="Address" className="mt-2" hint={c.slug ? undefined : "Empty: the main page, at /status."}>
              <div className="flex items-center gap-2 text-sm">
                <span className="whitespace-nowrap text-muted">/status/</span>
                <Input value={c.slug} maxLength={40} onChange={(e) => set({ slug: e.target.value.toLowerCase() })} />
              </div>
            </Field>
            <Field label="Public link" className="mt-4">
              <div className="flex items-center gap-2">
                <Input readOnly value={publicUrl} />
                <CopyButton text={publicUrl} />
              </div>
            </Field>
            <Field label="Own domain (optional)" className="mt-4" hint="That name then shows only the status page; the dashboard is not reachable through it.">
              <Input value={c.domain} placeholder="status.example.com" onChange={(e) => set({ domain: e.target.value })} />
            </Field>
            <DomainGuide domain={c.domain} />
          </Card>

          <Card>
            <SectionTitle title="Look" />
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Title" className="sm:col-span-2">
                <Input value={c.title} maxLength={80} onChange={(e) => set({ title: e.target.value })} />
              </Field>
              <Field label="Intro text" className="sm:col-span-2" hint="Shown under the overall status.">
                <Textarea rows={2} value={c.description} maxLength={1000} onChange={(e) => set({ description: e.target.value })} />
              </Field>
              <Field label="Logo" className="sm:col-span-2" hint="PNG, JPEG, WebP or SVG, up to 256 KB. Upload a file or paste an image link; a linked image is copied here, so it keeps working if the original moves.">
                <div className="flex flex-wrap items-center gap-2">
                  {c.logo && <img src={c.logo} alt="" className="h-9 max-w-[90px] rounded object-contain" />}
                  <label className="inline-flex cursor-pointer items-center gap-1.5 rounded-full border border-line px-3.5 py-2 text-xs hover:bg-surface2">
                    <Upload size={13} /> {c.logo ? "Change" : "Upload"}
                    <input type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml" className="hidden" onChange={(e) => pickLogo(e.target.files?.[0])} />
                  </label>
                  {c.logo && <IconButton aria-label="Remove logo" onClick={() => set({ logo: "" })}><X size={14} /></IconButton>}
                  <span className="text-xs text-muted">or</span>
                  <Input
                    type="url"
                    aria-label="Logo image link"
                    placeholder="https://example.com/logo.png"
                    value={logoUrl}
                    onChange={(e) => setLogoUrl(e.target.value)}
                    onKeyDown={(e) => e.key === "Enter" && logoUrl.trim() && (e.preventDefault(), fetchLogo())}
                    className="min-w-0 flex-1"
                  />
                  <Button size="sm" variant="secondary" disabled={!logoUrl.trim() || fetching} onClick={fetchLogo}>{fetching && <Spinner size={14} />} Use link</Button>
                </div>
              </Field>
              <Field label="Brand colour" hint="Used for the banner and the uptime bars.">
                <div className="flex items-center gap-2">
                  <input type="color" value={c.accent} onChange={(e) => set({ accent: e.target.value })} className="h-10 w-12 cursor-pointer rounded-lg border border-line bg-transparent" aria-label="Brand colour" />
                  <Input value={c.accent} maxLength={7} onChange={(e) => set({ accent: e.target.value })} />
                </div>
              </Field>
              <Field label="Theme">
                <Segmented value={c.theme} onChange={(v) => set({ theme: v })} options={[{ value: "auto", label: "Visitor's choice" }, { value: "light", label: "Light" }, { value: "dark", label: "Dark" }]} />
              </Field>
              <Field label="History">
                <Segmented value={String(c.historyDays)} onChange={(v) => set({ historyDays: Number(v) })} options={["30", "60", "90"].map((d) => ({ value: d, label: `${d} days` }))} />
              </Field>
              <Field label="Website link">
                <Input value={c.homeUrl} placeholder="https://example.com" onChange={(e) => set({ homeUrl: e.target.value })} />
              </Field>
              <Field label="Link text">
                <Input value={c.homeLabel} placeholder="Website" maxLength={40} onChange={(e) => set({ homeLabel: e.target.value })} />
              </Field>
              <Field label="Footer" className="sm:col-span-2" hint="Support contact, terms, anything.">
                <Textarea rows={2} value={c.footer} maxLength={500} onChange={(e) => set({ footer: e.target.value })} />
              </Field>
            </div>
            <ToggleRow label="Show uptime history" desc="Daily bars and the uptime percentage for each component." checked={c.showUptime} onChange={(v) => set({ showUptime: v })} />
            <ToggleRow label='Show "Powered by Fleetwatch"' checked={c.poweredBy} onChange={(v) => set({ poweredBy: v })} />
          </Card>

          <Card className="xl:col-span-2">
            <SectionTitle
              title="Components"
              sub="What visitors see, in groups. Pick an app or a server and give it a public name."
              action={<Button size="sm" variant="secondary" onClick={() => set({ groups: [...c.groups, { name: c.groups.length ? "" : "Services", components: [] }] })}><Plus size={14} /> Group</Button>}
            />
            {c.groups.length === 0 && <p className="py-6 text-center text-sm text-muted">No components yet. Add a group, then add apps or servers to it.</p>}
            <div className="space-y-4">
              {c.groups.map((g, gi) => (
                <div key={gi} className="rounded-2xl border border-line p-4">
                  <div className="flex items-center gap-2">
                    <Input value={g.name} placeholder="Group name (optional)" maxLength={60} onChange={(e) => setGroup(gi, { ...g, name: e.target.value })} />
                    <IconButton aria-label="Move group up" disabled={gi === 0} onClick={() => set({ groups: move(c.groups, gi, -1) })}><ArrowUp size={14} /></IconButton>
                    <IconButton aria-label="Move group down" disabled={gi === c.groups.length - 1} onClick={() => set({ groups: move(c.groups, gi, 1) })}><ArrowDown size={14} /></IconButton>
                    <IconButton aria-label="Remove group" onClick={() => setGroup(gi, null)}><Trash2 size={14} /></IconButton>
                  </div>
                  <div className="mt-3 space-y-2">
                    {g.components.map((x, ci) => {
                      const upd = (p: Partial<Component> | null) =>
                        setGroup(gi, { ...g, components: p ? g.components.map((y, i) => (i === ci ? { ...y, ...p } : y)) : g.components.filter((_, i) => i !== ci) });
                      return (
                        <div key={ci} className="grid gap-2 rounded-xl bg-surface2 p-3 md:grid-cols-[1fr_1fr_1.4fr_auto]">
                          <Select
                            aria-label="App or server"
                            value={`${x.kind}:${x.id}`}
                            onChange={(e) => {
                              const [kind, id] = e.target.value.split(/:(.*)/s) as [Component["kind"], string];
                              const name = kind === "app" ? apps.find((a) => a.id === id)?.name : servers.find((s) => s.id === id)?.name;
                              upd({ kind, id, name: x.name && x.name !== (x.kind === "app" ? apps.find((a) => a.id === x.id)?.name : getServer(x.id)?.name) ? x.name : name ?? x.name });
                            }}
                          >
                            {!apps.some((a) => a.id === x.id) && !servers.some((s) => s.id === x.id) && <option value={`${x.kind}:${x.id}`}>(deleted {x.kind})</option>}
                            <optgroup label="Applications">
                              {apps.map((a) => (
                                <option key={a.id} value={`app:${a.id}`} disabled={used.has(`app:${a.id}`) && !(x.kind === "app" && x.id === a.id)}>
                                  {a.name} · {getServer(a.serverId)?.name ?? "?"}
                                </option>
                              ))}
                            </optgroup>
                            <optgroup label="Whole servers">
                              {servers.map((s) => (
                                <option key={s.id} value={`server:${s.id}`} disabled={used.has(`server:${s.id}`) && !(x.kind === "server" && x.id === s.id)}>{s.name}</option>
                              ))}
                            </optgroup>
                          </Select>
                          <Input aria-label="Public name" placeholder="Public name" value={x.name} maxLength={60} onChange={(e) => upd({ name: e.target.value })} />
                          <Input aria-label="Description" placeholder="Description (optional)" value={x.description} maxLength={200} onChange={(e) => upd({ description: e.target.value })} />
                          <div className="flex items-center gap-1">
                            <IconButton aria-label="Move up" disabled={ci === 0} onClick={() => setGroup(gi, { ...g, components: move(g.components, ci, -1) })}><ArrowUp size={14} /></IconButton>
                            <IconButton aria-label="Move down" disabled={ci === g.components.length - 1} onClick={() => setGroup(gi, { ...g, components: move(g.components, ci, 1) })}><ArrowDown size={14} /></IconButton>
                            <IconButton aria-label="Remove" onClick={() => upd(null)}><Trash2 size={14} /></IconButton>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                  <Button size="sm" variant="ghost" className="mt-2" onClick={() => addComponent(gi)}><Plus size={14} /> Add component</Button>
                </div>
              ))}
            </div>
          </Card>

          <Card className="xl:col-span-2">
            <SectionTitle
              title="Notices"
              sub="Announcements at the top of the page: planned maintenance, an ongoing problem and its updates. Resolved notices stay visible for a day."
              action={<Button size="sm" variant="secondary" onClick={() => setNotice({ id: "", title: "", body: "", status: "info", start: Date.now(), end: 0, createdAt: Date.now() })}><Plus size={14} /> Notice</Button>}
            />
            {c.notices.length === 0 && <p className="py-6 text-center text-sm text-muted">No notices.</p>}
            <div className="divide-y divide-line">
              {c.notices.map((n) => (
                <div key={n.id || n.createdAt} className="flex flex-wrap items-center gap-3 py-3">
                  <Badge tone={NOTICE_TONE[n.status]} className="capitalize">{n.status}</Badge>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{n.title}</div>
                    <div className="text-xs text-muted">{new Date(n.start).toLocaleString()}{n.end ? ` – ${new Date(n.end).toLocaleString()}` : ""}</div>
                  </div>
                  {n.status !== "resolved" && <Button size="sm" variant="ghost" onClick={() => saveNotice({ ...n, status: "resolved", end: n.end && n.end < Date.now() ? n.end : Date.now() })}>Resolve</Button>}
                  <Button size="sm" variant="ghost" onClick={() => setNotice(n)}>Edit</Button>
                  <IconButton aria-label="Delete notice" onClick={() => set({ notices: c.notices.filter((x) => x !== n) })}><Trash2 size={14} /></IconButton>
                </div>
              ))}
            </div>
          </Card>
        </div>
        {dirty && (
          <div className="fixed inset-x-0 bottom-4 z-30 flex justify-center px-4">
            <div className="flex items-center gap-3 rounded-full border border-line bg-surface px-4 py-2 text-sm shadow-lg">
              Unsaved changes
              <Button size="sm" variant="ghost" onClick={() => setC(saved.data)}>Discard</Button>
              <Button size="sm" disabled={busy} onClick={() => save()}>{busy && <Spinner size={14} />} Save</Button>
            </div>
          </div>
        )}
      </Gate>

      {notice && <NoticeDialog notice={notice} onClose={() => setNotice(null)} onSave={saveNotice} />}
      <Modal open={preview} onClose={() => setPreview(false)} title="Preview (sample history)" wide>
        <div className="-m-2 max-h-[70vh] overflow-y-auto rounded-2xl bg-bg">
          <StatusView data={sample} />
        </div>
      </Modal>
    </>
  );
}

function NoticeDialog({ notice, onClose, onSave }: { notice: Notice; onClose: () => void; onSave: (n: Notice) => void }) {
  const [n, setN] = useState(notice);
  const ok = n.title.trim() !== "" && n.start > 0 && (!n.end || n.end >= n.start);
  return (
    <Modal
      open
      onClose={onClose}
      title={notice.id || notice.title ? "Edit notice" : "New notice"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button disabled={!ok} onClick={() => onSave({ ...n, id: n.id || `ntc_${Date.now().toString(36)}` })}>Done</Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="Title">
          <Input value={n.title} maxLength={120} placeholder="Scheduled maintenance" onChange={(e) => setN({ ...n, title: e.target.value })} />
        </Field>
        <Field label="Status">
          <Segmented value={n.status} onChange={(v) => setN({ ...n, status: v })} options={(["info", "maintenance", "degraded", "outage", "resolved"] as const).map((s) => ({ value: s, label: s[0].toUpperCase() + s.slice(1) }))} />
        </Field>
        <Field label="Details">
          <Textarea rows={4} value={n.body} maxLength={2000} onChange={(e) => setN({ ...n, body: e.target.value })} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="From">
            <Input type="datetime-local" value={toLocal(n.start)} onChange={(e) => setN({ ...n, start: fromLocal(e.target.value) })} />
          </Field>
          <Field label="Until (optional)" hint="Empty: until you resolve or delete it.">
            <Input type="datetime-local" value={toLocal(n.end)} onChange={(e) => setN({ ...n, end: fromLocal(e.target.value) })} />
          </Field>
        </div>
        <p className="text-xs text-muted">Save the page afterwards to publish it.</p>
      </div>
    </Modal>
  );
}

/** Step-by-step setup of a page's own domain: DNS (any provider, Cloudflare notes), then Caddy. */
function DomainGuide({ domain }: { domain: string }) {
  const d = domain.trim().toLowerCase() || "status.example.com";
  const labels = d.split(".");
  const name = labels.length > 2 ? labels.slice(0, -2).join(".") : "@"; // most DNS panels want only the part before your domain
  const dashboard = location.hostname;
  const byIp = /^[\d.]+$|:/.test(dashboard) || dashboard === "localhost";
  const file = `/etc/caddy/fleetwatch-status-${d}.caddy`;
  const Step = ({ n, title, children }: { n: number; title: string; children: ReactNode }) => (
    <li className="flex gap-3">
      <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-surface2 text-xs font-semibold">{n}</span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="text-sm font-medium text-fg">{title}</div>
        {children}
      </div>
    </li>
  );
  const Rec = ({ type, value }: { type: string; value: string }) => (
    <div className="overflow-x-auto rounded-xl border border-line">
      <table className="w-full text-left text-xs">
        <thead className="bg-surface2 text-muted">
          <tr><th className="px-3 py-1.5 font-medium">Type</th><th className="px-3 py-1.5 font-medium">Name</th><th className="px-3 py-1.5 font-medium">Target / content</th><th className="px-3 py-1.5 font-medium">TTL</th></tr>
        </thead>
        <tbody className="font-mono text-fg">
          <tr><td className="px-3 py-2">{type}</td><td className="px-3 py-2">{name}</td><td className="px-3 py-2">{value}</td><td className="px-3 py-2">Auto</td></tr>
        </tbody>
      </table>
    </div>
  );
  return (
    <details className="group mt-3 rounded-2xl border border-line p-4 text-xs text-muted" open={!!domain.trim()}>
      <summary className="cursor-pointer select-none text-sm font-medium text-fg">How to set up your own domain</summary>
      <ol className="mt-4 space-y-5">
        <Step n={1} title="Add a DNS record at your domain provider">
          <p>In the DNS settings of your domain (Cloudflare, Namecheap, GoDaddy, Hostinger…), add one record that points the name at this dashboard's server:</p>
          {byIp ? (
            <Rec type="A" value="the public IP address of this dashboard's server" />
          ) : (
            <>
              <Rec type="CNAME" value={dashboard} />
              <p>
                Or, if your provider has no CNAME (or the name is your bare domain): an <b>A</b> record with the same IP address that <span className="font-mono">{dashboard}</span> points to.
              </p>
            </>
          )}
          <p>The name is only the part before your domain: for <span className="font-mono">{d}</span> type <span className="font-mono">{name}</span>. Some providers want the full name; that works too.</p>
        </Step>
        <Step n={2} title="On Cloudflare: start with the grey cloud">
          <p>
            Set <b>Proxy status</b> to <b>DNS only</b> (grey cloud) for now, so the server can get its HTTPS certificate in step 3. Once <span className="font-mono">https://{d}</span> opens, you may switch it to <b>Proxied</b> (orange cloud); then keep <b>SSL/TLS → Overview</b> set to <b>Full (strict)</b>. Not on Cloudflare? Skip this step.
          </p>
          {!byIp && (
            <p>
              If <span className="font-mono">{dashboard}</span> is itself proxied by Cloudflare (orange cloud), use the <b>A</b> record with the server's IP instead of the CNAME, or the grey cloud will not reach the server.
            </p>
          )}
        </Step>
        <Step n={3} title="Tell the web server about the name">
          <p>On this dashboard's server, run (once per domain):</p>
          <CodeBlock>{`sudo ops-server caddy-config ${d} | sudo tee ${file}
grep -qxF 'import ${file}' /etc/caddy/Caddyfile || echo 'import ${file}' | sudo tee -a /etc/caddy/Caddyfile
sudo systemctl reload caddy`}</CodeBlock>
          <p>Caddy then gets a free HTTPS certificate for the name by itself.</p>
        </Step>
        <Step n={4} title="Save this page and open it">
          <p>
            Save, turn the page on, then open <span className="font-mono">https://{d}</span>. New DNS records can take a few minutes (rarely up to a few hours) to work everywhere. Still not loading? Check the record with <span className="font-mono">nslookup {d}</span> and the web server with <span className="font-mono">sudo journalctl -u caddy -n 50</span>.
          </p>
        </Step>
      </ol>
    </details>
  );
}
