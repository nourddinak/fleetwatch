import { useState } from "react";
import { Cloud, Globe2, Plus, Server as ServerIcon, ShieldCheck, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, IconButton, Input, LoadingState, SectionTitle, Segmented, Spinner } from "./ui";
import type { DnsAccount } from "./DnsAccounts";
import { countryName } from "../utils/regions";
import { relTime } from "../mock/util";

interface Rule {
  id: string;
  mode: "block" | "allow";
  countries: string[];
  ports: number[];
  servers: string[];
}
interface ZoneRule {
  account: string;
  zone: string;
  mode: "block" | "allow";
  countries: string[];
}
interface Geo {
  rules: Rule[];
  zones: ZoneRule[];
  status: Record<string, { at: number; rules: number; error?: string }>;
  me: { ip: string; country?: string };
}

const flag = (cc: string) => (/^[A-Z]{2}$/.test(cc) ? String.fromCodePoint(...[...cc].map((c) => 127397 + c.charCodeAt(0))) : "");
const codes = (text: string) => text.toUpperCase().split(/[^A-Z]+/).filter((c) => c.length === 2);
const names = (list: string[]) => list.map((c) => `${flag(c)} ${countryName(c) || c}`).join(", ");

function describe(mode: string, countries: string[], ports?: number[]) {
  const where = ports && ports.length ? ` on port${ports.length === 1 ? "" : "s"} ${ports.join(", ")}` : "";
  return mode === "allow" ? `Only ${names(countries)} may connect${where}` : `${names(countries)} blocked${where || " on every port"}`;
}

/** Security → Countries: block or allow whole countries on the servers' firewalls and at Cloudflare. */
export function Countries() {
  const { servers, toast } = useStore();
  const geo = useApi<Geo>("/api/geo");
  const dns = useApi<{ accounts: DnsAccount[] }>("/api/me/dns");
  const [adding, setAdding] = useState(false);
  const [del, setDel] = useState<Rule | null>(null);
  if (geo.error) return <ErrorState message={geo.error} onRetry={geo.reload} />;
  if (!geo.data) return <LoadingState />;
  const g = geo.data;
  const remove = async () => {
    const r = del!;
    setDel(null);
    try {
      await api("DELETE", `/api/geo/rules/${r.id}`);
      geo.reload();
      toast("Rule removed from the servers");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <div className="space-y-6">
      <div className="flex items-start gap-3 rounded-2xl border border-line bg-surface px-4 py-3 text-sm">
        <ShieldCheck size={16} className="mt-0.5 shrink-0 text-ok" />
        <span>You are at <b className="font-mono">{g.me.ip}</b>{g.me.country && g.me.country !== "LAN" ? <> in {flag(g.me.country)} {countryName(g.me.country)}</> : null}. Your servers' own addresses and the addresses people signed in from lately (yours included) always get in, so a rule cannot lock you out; so do local networks and replies to the servers' own connections.</span>
      </div>
      <Card>
        <SectionTitle icon={<ServerIcon size={15} />} title="On your servers" sub="The servers' firewalls drop the traffic (nftables, next to your other firewall rules). Country address lists come from ipdeny.com and are refreshed weekly."
          action={!adding && <Button size="sm" onClick={() => setAdding(true)}><Plus size={14} /> Add a rule</Button>} />
        {adding && <RuleForm me={g.me.country} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); geo.reload(); }} />}
        {g.rules.length === 0 ? (
          !adding && <EmptyState icon={<Globe2 size={20} />} title="No country rules" desc="For example: SSH (port 22) only from your own country, or block a country that keeps attacking." />
        ) : (
          <ul className="divide-y divide-line">
            {g.rules.map((r) => (
              <li key={r.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <div className="min-w-0">
                  <div className="text-sm font-medium">{describe(r.mode, r.countries, r.ports)}</div>
                  <div className="text-xs text-muted">{r.servers.length ? r.servers.map((id) => servers.find((s) => s.id === id)?.name ?? id).join(", ") : "Every server"}</div>
                </div>
                <IconButton aria-label="Remove" onClick={() => setDel(r)}><Trash2 size={14} /></IconButton>
              </li>
            ))}
          </ul>
        )}
        {Object.keys(g.status).length > 0 && (
          <div className="mt-3 flex flex-wrap gap-2 border-t border-line pt-3 text-xs">
            {Object.entries(g.status).map(([id, st]) => (
              <span key={id} title={st.error}><Badge tone={st.error ? "crit" : "ok"}>{servers.find((s) => s.id === id)?.name ?? id}: {st.error ? st.error : `${st.rules} rule${st.rules === 1 ? "" : "s"} in place`} · {relTime(st.at)}</Badge></span>
            ))}
          </div>
        )}
      </Card>
      <Card>
        <SectionTitle icon={<Cloud size={15} />} title="At Cloudflare" sub="For websites behind Cloudflare: visitors are stopped before they reach your server. One rule per domain." />
        {!dns.data ? <Spinner /> : dns.data.accounts.length === 0 ? (
          <p className="text-sm text-muted">Connect Cloudflare in Integrations to block countries for your domains.</p>
        ) : (
          <ul className="divide-y divide-line">
            {dns.data.accounts.flatMap((a) => a.zones.map((z) => (
              <ZoneRow key={a.id + z.name} account={a.id} zone={z.name} rule={g.zones.find((x) => x.account === a.id && x.zone === z.name)} onSaved={geo.reload} />
            )))}
          </ul>
        )}
      </Card>
      <ConfirmDialog open={!!del} title="Remove this rule?" confirmLabel="Remove" message={del ? describe(del.mode, del.countries, del.ports) + ": the servers stop applying it." : ""} onCancel={() => setDel(null)} onConfirm={remove} />
    </div>
  );
}

function CountryInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const list = codes(value);
  return (
    <Field label="Countries" hint={list.length ? names(list) : "Two-letter codes, like MA, FR, US."}>
      <Input value={value} className="font-mono" placeholder="MA" onChange={(e) => onChange(e.target.value)} />
    </Field>
  );
}

function RuleForm({ me, onClose, onSaved }: { me?: string; onClose: () => void; onSaved: () => void }) {
  const { servers, toast } = useStore();
  const [mode, setMode] = useState<Rule["mode"]>("allow");
  const [countries, setCountries] = useState(me && me !== "LAN" ? me : "");
  const [ports, setPorts] = useState("22");
  const [picked, setPicked] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const list = codes(countries);
  const save = async () => {
    setBusy(true);
    try {
      await api("POST", "/api/geo/rules", { mode, countries: list, ports: ports.split(/[^0-9]+/).filter(Boolean).map(Number), servers: picked });
      toast("Rule saved: the servers apply it in a few seconds");
      onSaved();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="mb-4 space-y-4 rounded-2xl bg-surface2 p-4">
      <Segmented value={mode} onChange={setMode} options={[{ value: "allow", label: "Allow only these countries" }, { value: "block", label: "Block these countries" }]} />
      <div className="grid gap-4 sm:grid-cols-2">
        <CountryInput value={countries} onChange={setCountries} />
        <Field label="Ports" hint={ports.trim() ? "TCP and UDP." : "Every port."}>
          <div className="flex gap-2">
            <Input value={ports} className="font-mono" placeholder="every port" onChange={(e) => setPorts(e.target.value)} />
            <Button size="sm" variant="ghost" onClick={() => setPorts("22")}>SSH</Button>
            <Button size="sm" variant="ghost" onClick={() => setPorts("")}>All</Button>
          </div>
        </Field>
      </div>
      <Field label="Servers" hint={picked.length ? undefined : "Every server, including ones added later."}>
        <div className="flex flex-wrap gap-2">
          {servers.map((s) => (
            <label key={s.id} className="flex items-center gap-1.5 rounded-full border border-line px-3 py-1 text-sm">
              <input type="checkbox" checked={picked.includes(s.id)} onChange={(e) => setPicked(e.target.checked ? [...picked, s.id] : picked.filter((x) => x !== s.id))} />{s.name}
            </label>
          ))}
        </div>
      </Field>
      {list.length > 0 && <p className="text-sm">{describe(mode, list, ports.split(/[^0-9]+/).filter(Boolean).map(Number))}.</p>}
      {mode === "allow" && !ports.trim() && <p className="text-xs text-warn">Allowing only some countries on every port also stops your websites' visitors from elsewhere. Usually you want this for SSH only.</p>}
      <div className="flex gap-2">
        <Button disabled={busy || list.length === 0} onClick={save}>{busy && <Spinner size={14} />} Save rule</Button>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
      </div>
    </div>
  );
}

function ZoneRow({ account, zone, rule, onSaved }: { account: string; zone: string; rule?: ZoneRule; onSaved: () => void }) {
  const { toast } = useStore();
  const [edit, setEdit] = useState(false);
  const [mode, setMode] = useState<ZoneRule["mode"]>(rule?.mode ?? "block");
  const [countries, setCountries] = useState(rule?.countries.join(", ") ?? "");
  const [busy, setBusy] = useState(false);
  const save = async (list: string[]) => {
    setBusy(true);
    try {
      await api("POST", "/api/geo/zones", { account, zone, mode, countries: list });
      toast(list.length ? `Cloudflare now applies the rule for ${zone}` : `Rule removed from ${zone}`);
      setEdit(false);
      onSaved();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <li className="space-y-3 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-medium">{zone}</div>
          <div className="text-xs text-muted">{rule ? describe(rule.mode, rule.countries) : "Every country"}</div>
        </div>
        <div className="flex gap-1">
          <Button size="sm" variant="ghost" onClick={() => setEdit(!edit)}>{edit ? "Close" : rule ? "Change" : "Set countries"}</Button>
          {rule && !edit && <IconButton aria-label="Remove" disabled={busy} onClick={() => save([])}><Trash2 size={14} /></IconButton>}
        </div>
      </div>
      {edit && (
        <div className="space-y-3 rounded-2xl bg-surface2 p-4">
          <Segmented size="sm" value={mode} onChange={setMode} options={[{ value: "block", label: "Block these countries" }, { value: "allow", label: "Allow only these countries" }]} />
          <CountryInput value={countries} onChange={setCountries} />
          <Button size="sm" disabled={busy || codes(countries).length === 0} onClick={() => save(codes(countries))}>{busy && <Spinner size={13} />} Save at Cloudflare</Button>
        </div>
      )}
    </li>
  );
}
