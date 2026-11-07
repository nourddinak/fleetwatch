import { useEffect, useState } from "react";
import { CheckCircle2, CircleAlert, CircleHelp, Globe, Pencil, Plus, RefreshCw, Trash2, Wand2, XCircle } from "lucide-react";
import { api, ApiError, useApi } from "../../api/client";
import { useStore } from "../../store/store";
import { Badge, Button, Card, ConfirmDialog, Drawer, EmptyState, Field, Input, LoadingState, Modal, SectionTitle, Select, Spinner, ToggleRow } from "../ui";
import { fmtBytes } from "../DiskRescue";
import { Editor } from "./Composer";
import type { Address, Check, Domain, Overview } from "./data";

const checkIcon = (c: Check) =>
  c.status === "ok" ? <CheckCircle2 size={14} className="text-ok" /> : c.status === "unknown" || !c.status ? <CircleHelp size={14} className="text-muted" /> : c.status === "error" ? <CircleAlert size={14} className="text-warn" /> : <XCircle size={14} className="text-crit" />;

const listText = (v: string[] | null) => (v ?? []).join(", ");
const textList = (s: string) => s.split(/[,;\s]+/).map((x) => x.trim()).filter(Boolean);

function Checks({ d }: { d: Domain }) {
  const rows: [string, Check, string][] = [
    ["MX", d.checks.mx, "Mail for the domain reaches Cloudflare"],
    ["SPF", d.checks.spf, "Which servers may send for the domain"],
    ["DKIM", d.checks.dkim, "Your provider signs what you send"],
    ["DMARC", d.checks.dmarc, "What receivers do with mail that fails"],
    ["Routing", d.checks.routing, "Cloudflare sends every address to the Worker"],
  ];
  return (
    <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
      {rows.map(([k, c, what]) => (
        <div key={k} className="rounded-2xl border border-line px-3 py-2" title={what}>
          <div className="flex items-center gap-1.5 text-xs font-medium">{checkIcon(c)} {k}</div>
          <div className="mt-0.5 line-clamp-2 break-all text-[11px] text-muted" title={c.detail}>{c.detail || (c.status ? c.status : "not checked yet")}</div>
        </div>
      ))}
    </div>
  );
}

function AddDomain({ open, onClose, onAdded }: { open: boolean; onClose: () => void; onAdded: () => void }) {
  const cf = useApi<{ status: { connected: boolean } }>("/api/emails/cloudflare");
  const [zones, setZones] = useState<{ id: string; name: string }[] | null>(null);
  const [name, setName] = useState("");
  const [zone, setZone] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open && cf.data?.status.connected) api<{ id: string; name: string }[]>("GET", "/api/emails/cloudflare/zones").then(setZones).catch((e: ApiError) => setError(e.message));
  }, [open, cf.data]);
  const add = async () => {
    setBusy(true);
    setError("");
    try {
      await api("POST", "/api/emails/domains", { name: zones && zone ? zones.find((z) => z.id === zone)!.name : name, zoneId: zone });
      onAdded();
      onClose();
    } catch (e) {
      setError((e as ApiError).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={open} onClose={onClose} title="Turn on email for a domain" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={add} disabled={busy || (!name && !zone)}>{busy && <Spinner size={14} />} Add domain</Button></>}>
      {zones && zones.length > 0 ? (
        <Field label="Your Cloudflare domains">
          <Select value={zone} onChange={(e) => setZone(e.target.value)}>
            <option value="">Pick one…</option>
            {zones.map((z) => <option key={z.id} value={z.id}>{z.name}</option>)}
          </Select>
        </Field>
      ) : (
        <Field label="Domain" hint={cf.data && !cf.data.status.connected ? "Connect Cloudflare in Settings to pick from your domains and set them up in one click." : undefined}>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" autoFocus />
        </Field>
      )}
      {error && <p className="mt-3 text-sm text-crit">{error}</p>}
    </Modal>
  );
}

function SenderPick({ domain, value, onChange }: { domain: string; value: string; onChange: (v: string) => void }) {
  const { data } = useApi<{ senders: { id: string; label: string; from: string; default: boolean }[] }>("/api/emails/options");
  return (
    <Field label="Send through" hint="The senders of Integrations → Email. With a Resend sender for this domain, any address on it can send.">
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Automatic (a sender on {domain}, else the default)</option>
        {data?.senders.map((s) => <option key={s.id} value={s.id}>{s.label} — {s.from}{s.default ? " (default)" : ""}</option>)}
      </Select>
    </Field>
  );
}

function DomainSettings({ d, addresses, owner, onClose, onSaved }: { d: Domain; addresses: Address[]; owner: boolean; onClose: () => void; onSaved: () => void }) {
  const [catchAll, setCatchAll] = useState(d.catchAll);
  const [catchTo, setCatchTo] = useState(d.catchAllTo);
  const [sender, setSender] = useState(d.senderId);
  const [fwd, setFwd] = useState(listText(d.forwardTo));
  const [thr, setThr] = useState(String(d.spamThreshold));
  const [error, setError] = useState("");
  const own = addresses.filter((a) => a.domainId === d.id && !a.inboxId);
  const save = async () => {
    try {
      await api("PUT", `/api/emails/domains/${d.id}`, { catchAll, catchAllTo: catchAll ? catchTo : "", senderId: sender, forwardTo: textList(fwd), spamThreshold: Number(thr) });
      onSaved();
      onClose();
    } catch (e) {
      setError((e as ApiError).message);
    }
  };
  return (
    <Drawer open onClose={onClose} title={`${d.name} settings`}>
      <div className="space-y-5">
        <Field label="Gmail copies" hint="Every address of this domain is copied to these (each address can change it). Cloudflare emails each new destination a link to confirm it first.">
          <Input value={fwd} onChange={(e) => setFwd(e.target.value)} placeholder="you@gmail.com, team@gmail.com" />
        </Field>
        <div className="rounded-2xl border border-line px-4">
          <ToggleRow label="Catch-all" desc="Mail to addresses that do not exist goes to an inbox instead of bouncing." checked={catchAll} onChange={setCatchAll} />
          {catchAll && (
            <div className="pb-3">
              <Select value={catchTo} onChange={(e) => setCatchTo(e.target.value)} aria-label="Catch-all inbox">
                <option value="">Pick the inbox…</option>
                {own.map((a) => <option key={a.id} value={a.id}>{a.address}</option>)}
              </Select>
            </div>
          )}
        </div>
        {owner && <SenderPick domain={d.name} value={sender} onChange={setSender} />}
        <Field label="Spam threshold" hint="Higher lets more through. 5 is a sensible start; nothing is ever deleted, only moved to Spam.">
          <Input type="number" min={1} max={100} step={0.5} value={thr} onChange={(e) => setThr(e.target.value)} />
        </Field>
        {error && <p className="text-sm text-crit">{error}</p>}
        <Button onClick={save}>Save</Button>
      </div>
    </Drawer>
  );
}

const blank = { local: "", displayName: "", inboxId: "", receive: true, send: true, disabled: false, forwardMode: "domain" as Address["forwardMode"], forwardTo: [] as string[], notify: true, signature: "" };

function AddressEditor({ domain, address, addresses, onClose, onSaved }: { domain: Domain; address?: Address; addresses: Address[]; onClose: () => void; onSaved: () => void }) {
  const [a, setA] = useState(address ? { ...blank, ...address, forwardTo: address.forwardTo ?? [] } : blank);
  const [fwd, setFwd] = useState(listText(a.forwardTo));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const inboxes = addresses.filter((x) => !x.inboxId && x.id !== address?.id && x.canManage);
  const set = (p: Partial<typeof a>) => setA((x) => ({ ...x, ...p }));
  const save = async () => {
    setBusy(true);
    setError("");
    try {
      const body = { domainId: domain.id, local: a.local, displayName: a.displayName, inboxId: a.inboxId, receive: a.receive, send: a.send, disabled: a.disabled, forwardMode: a.forwardMode, forwardTo: textList(fwd), notify: a.notify, signature: a.signature };
      if (address) await api("PUT", `/api/emails/addresses/${address.id}`, body);
      else await api("POST", "/api/emails/addresses", body);
      onSaved();
      onClose();
    } catch (e) {
      setError((e as ApiError).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Drawer open onClose={onClose} title={address ? address.address : `New address on ${domain.name}`}>
      <div className="space-y-5">
        {!address && (
          <Field label="Address">
            <div className="flex items-center gap-2">
              <Input value={a.local} onChange={(e) => set({ local: e.target.value.toLowerCase() })} placeholder="contact" autoFocus />
              <span className="shrink-0 text-sm text-muted">@{domain.name}</span>
            </div>
          </Field>
        )}
        <Field label="Display name" hint="What people see next to the address when you write.">
          <Input value={a.displayName} onChange={(e) => set({ displayName: e.target.value })} placeholder="Nashop support" />
        </Field>
        <Field label="Inbox" hint="An alias delivers into another address's inbox, keeping the address it was sent to.">
          <Select value={a.inboxId} onChange={(e) => set({ inboxId: e.target.value })}>
            <option value="">Its own inbox</option>
            {inboxes.map((x) => <option key={x.id} value={x.id}>Alias of {x.address}</option>)}
          </Select>
        </Field>
        <div className="rounded-2xl border border-line px-4 divide-y divide-line">
          <ToggleRow label="Receives mail" checked={a.receive} onChange={(v) => set({ receive: v })} />
          <ToggleRow label="Can send" checked={a.send} onChange={(v) => set({ send: v })} />
          <ToggleRow label="Alert on new mail" desc="Through your notification channels (Telegram, Discord, webhooks, the phone app)." checked={a.notify} onChange={(v) => set({ notify: v })} />
          <ToggleRow label="Turned off" desc="Refuses mail and cannot send, but keeps its inbox." checked={a.disabled} onChange={(v) => set({ disabled: v })} />
        </div>
        <Field label="Gmail copies">
          <Select value={a.forwardMode} onChange={(e) => set({ forwardMode: e.target.value as Address["forwardMode"] })}>
            <option value="domain">Like the domain ({listText(domain.forwardTo) || "none"})</option>
            <option value="custom">Its own list</option>
            <option value="off">No copies</option>
          </Select>
        </Field>
        {a.forwardMode === "custom" && <Input value={fwd} onChange={(e) => setFwd(e.target.value)} placeholder="you@gmail.com, other@gmail.com" aria-label="Gmail copies" />}
        <div>
          <span className="mb-1.5 block text-xs font-medium text-muted">Signature</span>
          <Editor html={a.signature} onChange={(signature) => set({ signature })} minHeight={90} />
        </div>
        {error && <p className="text-sm text-crit">{error}</p>}
        <Button onClick={save} disabled={busy}>{busy && <Spinner size={14} />} Save</Button>
      </div>
    </Drawer>
  );
}

export interface CFRule { id: string; enabled: boolean; address: string; action: "forward" | "worker" | "drop" | string; to: string[]; ours: boolean }
export interface CFDomain { zoneId: string; name: string; domainId?: string; routing: "on" | "off" | "unknown"; rules: CFRule[]; catchAll?: CFRule; error?: string }

const ruleTarget = (r: CFRule) => (r.ours ? "Fleetwatch" : r.action === "forward" ? r.to.join(", ") : r.action === "drop" ? "Dropped" : r.action === "worker" ? `Worker ${r.to.join(", ")}` : r.action || "—");

/** A Cloudflare domain not yet used for email: its routing as Cloudflare has it. */
function CloudflareZone({ z, busy, disabled, onAdopt }: { z: CFDomain; busy: boolean; disabled: boolean; onAdopt: () => void }) {
  const rows = [...z.rules, ...(z.catchAll ? [{ ...z.catchAll, address: "Catch-all (any other address)" }] : [])];
  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <h3 className="mr-auto text-lg font-semibold">{z.name}</h3>
        <Badge tone={z.routing === "on" ? "ok" : "neutral"} dot>Email Routing {z.routing}</Badge>
        <Button size="sm" onClick={onAdopt} disabled={disabled}>{busy ? <Spinner size={13} /> : <Wand2 size={13} />} Use for email</Button>
      </div>
      {z.error ? <p className="text-xs text-warn">{z.error}</p> : rows.length === 0 ? (
        <p className="text-sm text-muted">No routing rules in Cloudflare yet.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted">
              <tr><th className="py-2 pr-3 font-medium">Custom address</th><th className="py-2 pr-3 font-medium">Goes to</th><th className="py-2 font-medium">Status</th></tr>
            </thead>
            <tbody className="divide-y divide-line">
              {rows.map((r) => (
                <tr key={r.id + r.address} className={r.enabled ? "" : "opacity-50"}>
                  <td className="py-2.5 pr-3 font-medium">{r.address}</td>
                  <td className="max-w-[18rem] truncate py-2.5 pr-3 text-xs text-muted" title={ruleTarget(r)}>{ruleTarget(r)}</td>
                  <td className="py-2.5 text-xs">{r.enabled ? <Badge tone="ok">Active</Badge> : <Badge>Off</Badge>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-3 text-xs text-muted">Use for email: these addresses are created here with the same Gmail copies, the catch-all keeps catching, and Cloudflare sends the domain's mail through Fleetwatch. The old rules are turned off, not deleted.</p>
    </Card>
  );
}

/** Cloudflare rules that still send some of a domain's addresses straight to Gmail. */
function Bypass({ zone, busy, onAdopt }: { zone?: CFDomain; busy: boolean; onAdopt: (z: CFDomain) => void }) {
  const left = zone?.rules.filter((r) => r.enabled && !r.ours) ?? [];
  if (!left.length) return null;
  return (
    <div className="mt-4 rounded-2xl bg-warn-soft px-4 py-3 text-xs">
      <div className="mb-1 font-medium text-warn">Cloudflare still routes these itself, past Fleetwatch:</div>
      <ul className="space-y-0.5 text-muted">{left.map((r) => <li key={r.id}>{r.address} → {ruleTarget(r)}</li>)}</ul>
      <div className="mt-2 flex flex-wrap items-center gap-3">
        <Button size="sm" variant="secondary" onClick={() => onAdopt(zone!)} disabled={busy}>{busy ? <Spinner size={13} /> : <Wand2 size={13} />} Bring them in</Button>
        <span className="text-muted">Creates them here with the same Gmail copies and turns their Cloudflare rules off (not deleted).</span>
      </div>
    </div>
  );
}

export function DomainsAndAddresses({ ov, reload }: { ov: Overview; reload: () => void }) {
  const { toast } = useStore();
  const [adding, setAdding] = useState(false);
  const [settings, setSettings] = useState<Domain | null>(null);
  const [edit, setEdit] = useState<{ d: Domain; a?: Address } | null>(null);
  const [del, setDel] = useState<{ kind: "domain" | "address"; id: string; name: string } | null>(null);
  const [working, setWorking] = useState("");
  const [steps, setSteps] = useState<{ steps: string[]; error?: string } | null>(null);
  const [cf, setCf] = useState<CFDomain[] | null>(null);
  const [cfError, setCfError] = useState("");
  const loadCf = () => {
    if (!ov.owner) return;
    api<CFDomain[]>("GET", "/api/emails/cloudflare/domains").then((x) => (setCf(x), setCfError(""))).catch((e: ApiError) => (setCf([]), setCfError(e.message)));
  };
  useEffect(loadCf, [ov.owner]);
  const adopt = async (z: CFDomain) => {
    setWorking(z.zoneId + "adopt");
    try {
      setSteps(await api<{ steps: string[]; error?: string }>("POST", `/api/emails/cloudflare/domains/${z.zoneId}/adopt`));
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setWorking("");
    reload();
    loadCf();
  };

  const check = async (d: Domain) => {
    setWorking(d.id + "check");
    await api("POST", `/api/emails/domains/${d.id}/check`).catch((e: ApiError) => toast(e.message, "err"));
    setWorking("");
    reload();
  };
  const setup = async (d: Domain) => {
    setWorking(d.id + "setup");
    try {
      setSteps(await api<{ steps: string[]; error?: string }>("POST", `/api/emails/domains/${d.id}/setup`));
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setWorking("");
    reload();
    loadCf();
  };
  const remove = async () => {
    if (!del) return;
    try {
      await api("DELETE", `/api/emails/${del.kind === "domain" ? "domains" : "addresses"}/${del.id}`);
      toast(`${del.name} removed`);
      reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setDel(null);
  };

  return (
    <div className="space-y-6">
      <SectionTitle title="Domains & addresses" sub="Create as many addresses as you like: one Cloudflare rule per domain sends them all here." action={ov.owner && <Button onClick={() => setAdding(true)}><Plus size={15} /> Add domain</Button>} />
      {!ov.domains.length && !cf?.length && <EmptyState icon={<Globe size={22} />} title="No domains yet" desc={ov.owner ? "Add one of your domains, then create addresses on it." : "The owner has not shared any domain with you."} />}
      {ov.domains.map((d) => {
        const list = ov.addresses.filter((a) => a.domainId === d.id);
        return (
          <Card key={d.id}>
            <div className="mb-4 flex flex-wrap items-center gap-2">
              <h3 className="mr-auto text-lg font-semibold">{d.name}</h3>
              {d.catchAll && <Badge tone="info">catch-all</Badge>}
              {d.canManage && <Button size="sm" variant="secondary" onClick={() => check(d)} disabled={!!working}>{working === d.id + "check" ? <Spinner size={13} /> : <RefreshCw size={13} />} Check</Button>}
              {ov.owner && <Button size="sm" variant="secondary" onClick={() => setup(d)} disabled={!!working} title="SPF, Email Routing, the catch-all rule to the Worker, DMARC">{working === d.id + "setup" ? <Spinner size={13} /> : <Wand2 size={13} />} Set up in Cloudflare</Button>}
              {d.canManage && <Button size="sm" variant="secondary" onClick={() => setSettings(d)}>Settings</Button>}
              {ov.owner && <Button size="sm" variant="ghost" onClick={() => setDel({ kind: "domain", id: d.id, name: d.name })} aria-label={`Remove ${d.name}`}><Trash2 size={13} /></Button>}
            </div>
            {d.canManage && <Checks d={d} />}
            {ov.owner && <Bypass zone={cf?.find((z) => z.domainId === d.id)} busy={!!working} onAdopt={adopt} />}
            <div className="mt-5 overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="text-left text-xs text-muted">
                  <tr><th className="py-2 pr-3 font-medium">Address</th><th className="py-2 pr-3 font-medium">Inbox</th><th className="py-2 pr-3 font-medium">Gmail copies</th><th className="py-2 pr-3 font-medium">Mail</th><th /></tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {list.map((a) => {
                    const fwd = a.forwardMode === "off" ? "none" : a.forwardMode === "custom" ? listText(a.forwardTo) : listText(d.forwardTo) && `${listText(d.forwardTo)} (domain)`;
                    return (
                      <tr key={a.id} className={a.disabled ? "opacity-50" : ""}>
                        <td className="py-2.5 pr-3">
                          <div className="font-medium">{a.address}</div>
                          <div className="text-xs text-muted">{a.displayName}{!a.receive && " · does not receive"}{!a.send && " · cannot send"}{a.disabled && " · off"}</div>
                        </td>
                        <td className="py-2.5 pr-3 text-xs text-muted">{a.inboxId ? "→ " + (ov.addresses.find((x) => x.id === a.inboxId)?.address ?? "another inbox") : "own"}</td>
                        <td className="max-w-[16rem] truncate py-2.5 pr-3 text-xs text-muted" title={fwd || undefined}>{a.canManage ? fwd || "none" : "—"}</td>
                        <td className="py-2.5 pr-3 text-xs text-muted">{a.inboxId ? "" : `${a.total} · ${fmtBytes(a.bytes)}`}{a.unread > 0 && <Badge tone="info" className="ml-2">{a.unread} new</Badge>}</td>
                        <td className="py-2.5 text-right whitespace-nowrap">
                          {a.canManage && <Button size="sm" variant="ghost" onClick={() => setEdit({ d, a })} aria-label={`Edit ${a.address}`}><Pencil size={13} /></Button>}
                          {d.canManage && <Button size="sm" variant="ghost" onClick={() => setDel({ kind: "address", id: a.id, name: a.address })} aria-label={`Delete ${a.address}`}><Trash2 size={13} /></Button>}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
              {!list.length && <p className="py-3 text-sm text-muted">No addresses yet.</p>}
            </div>
            {d.canManage && <Button className="mt-3" size="sm" variant="secondary" onClick={() => setEdit({ d })}><Plus size={13} /> New address</Button>}
          </Card>
        );
      })}
      {ov.owner && cfError && <p className="text-xs text-muted">Cloudflare domains: {cfError}</p>}
      {ov.owner && cf === null && <LoadingState label="Reading your Cloudflare domains…" />}
      {cf && cf.some((z) => !z.domainId) && (
        <div className="space-y-4">
          <SectionTitle title="Your other Cloudflare domains" sub="How Cloudflare routes their mail today. Use one for email and its rules come along: each address keeps its Gmail copies, and the mail also lands here." />
          {cf.filter((z) => !z.domainId).map((z) => (
            <CloudflareZone key={z.zoneId} z={z} busy={working === z.zoneId + "adopt"} disabled={!!working} onAdopt={() => adopt(z)} />
          ))}
        </div>
      )}
      <AddDomain open={adding} onClose={() => setAdding(false)} onAdded={reload} />
      {settings && <DomainSettings d={settings} addresses={ov.addresses} owner={ov.owner} onClose={() => setSettings(null)} onSaved={reload} />}
      {edit && <AddressEditor domain={edit.d} address={edit.a} addresses={ov.addresses} onClose={() => setEdit(null)} onSaved={reload} />}
      <ConfirmDialog open={!!del} danger title={`Remove ${del?.name}?`} message={del?.kind === "domain" ? "Its addresses go too. A domain that still has mail cannot be removed: export or delete the mail first." : "An address with mail in its inbox cannot be deleted: turn it off instead to keep the mail."}
        confirmLabel="Remove" onCancel={() => setDel(null)} onConfirm={remove} />
      <Modal open={!!steps} onClose={() => setSteps(null)} title="Cloudflare setup">
        <ul className="space-y-2 text-sm">
          {steps?.steps.map((s) => <li key={s} className="flex gap-2"><CheckCircle2 size={16} className="mt-0.5 shrink-0 text-ok" /> {s}</li>)}
          {steps?.error && <li className="flex gap-2 text-crit"><XCircle size={16} className="mt-0.5 shrink-0" /> {steps.error}</li>}
        </ul>
        <p className="mt-4 text-xs text-muted">DNS changes can take a few minutes to show up. Use Check to see the records as the world sees them.</p>
      </Modal>
    </div>
  );
}
