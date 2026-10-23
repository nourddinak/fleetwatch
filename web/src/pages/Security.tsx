import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Ban, Bug, Clock, Flame, Globe, Globe2, KeyRound, Lightbulb, PackageCheck, Pencil, RefreshCw, Server as ServerIcon, ShieldAlert, ShieldCheck, Terminal, UserRound, Workflow } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CopyButton, EmptyState, ErrorState, Field, Gate, Input, LoadingState, Modal, PageHeader, Segmented, Select, Spinner, Table, Td, Toggle, Tr, type Tone } from "../components/ui";
import { cn } from "../utils/cn";
import { BlockedIPs, Updates, Vulnerabilities } from "./SecurityAdmin";
import { Countries } from "../components/Countries";
import { relTime } from "../mock/util";

interface Finding {
  id: string;
  severity: "info" | "warning" | "critical";
  title: string;
  detail?: string;
  fix?: string;
}
interface Report {
  serverId: string;
  at: number;
  findings: Finding[];
  scanning: boolean;
  oldAgent: boolean;
}
interface Login {
  serverId: string;
  ts: number;
  user: string;
  ip: string;
  method: string;
  key: string;
  country: string;
  newCountry: boolean;
  newKey: boolean;
  source?: string; // "github" = a GitHub Actions runner
  label?: string; // the name given to this key
  automated: boolean;
}

const SEV: Record<Finding["severity"], Tone> = { critical: "crit", warning: "warn", info: "info" };
const flag = (cc: string) => (/^[A-Z]{2}$/.test(cc) ? String.fromCodePoint(...[...cc].map((c) => 127397 + c.charCodeAt(0))) : "");

export function Security() {
  const { me } = useStore();
  const owner = !me || me.role === "owner";
  const [tab, setTab] = useState<"findings" | "logins" | "blocked" | "countries" | "updates" | "vulns">("findings");
  return (
    <>
      <PageHeader title="Security" sub="Checks every server twice a day: SSH settings, waiting security updates, failed logins, open ports, the firewall, readable .env files and sites that leak .git or .env. You get a notification for anything new." />
      <div className="mb-5">
        <Segmented value={tab} onChange={setTab} options={([["findings", "Findings", ShieldAlert], ["logins", "SSH logins", KeyRound], ["blocked", "Blocked IPs", Ban], ...(owner ? [["countries", "Countries", Globe2] as const] : []), ["updates", "Updates", PackageCheck], ["vulns", "Vulnerable packages", Bug]] as const).map(([value, label, Icon]) => ({ value, label: <span className="inline-flex items-center gap-1.5"><Icon size={13} />{label}</span> }))} />
      </div>
      <Gate>{tab === "findings" ? <Findings /> : tab === "logins" ? <Logins /> : tab === "blocked" ? <BlockedIPs /> : tab === "countries" ? <Countries /> : tab === "updates" ? <Updates /> : <Vulnerabilities />}</Gate>
    </>
  );
}

function Findings() {
  const { getServer, toast } = useStore();
  const list = useApi<Report[]>("/api/security");
  const { reload } = list;
  const scanning = list.data?.some((r) => r.scanning);
  useEffect(() => {
    if (!scanning) return;
    const t = setTimeout(reload, 4000);
    return () => clearTimeout(t);
  }, [scanning, list.data, reload]);
  const scan = async (id: string) => {
    try {
      await api("POST", `/api/servers/${id}/security/scan`);
      reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const expect = async (serverId: string, f: Finding) => {
    try {
      await api("PUT", "/api/security/expected", { serverId, port: Number(f.id.split(":")[1]), expected: true });
      toast("Marked as expected");
      reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  if (list.error) return <ErrorState message={list.error} onRetry={reload} />;
  if (!list.data) return <LoadingState />;
  if (list.data.length === 0) return <EmptyState icon={<ShieldCheck size={20} />} title="No servers yet" desc="Add a server; its agent checks it right after connecting." />;
  const count = (s: Finding["severity"]) => list.data!.reduce((n, r) => n + r.findings.filter((f) => f.severity === s).length, 0);
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap gap-2">
        <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-crit"><Flame size={13} /> {count("critical")} critical</span>
        <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-warn"><AlertTriangle size={13} /> {count("warning")} warnings</span>
        <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-info"><Lightbulb size={13} /> {count("info")} tips</span>
      </div>
      {list.data.map((r) => {
        const s = getServer(r.serverId);
        return (
          <Card key={r.serverId}>
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div className="flex min-w-0 items-center gap-3">
                <span className={cn("grid h-10 w-10 shrink-0 place-items-center rounded-xl", r.findings.some((f) => f.severity === "critical") ? "bg-crit-soft text-crit" : r.findings.some((f) => f.severity === "warning") ? "bg-warn-soft text-warn" : "bg-ok-soft text-ok")}>
                  {r.findings.some((f) => f.severity !== "info") ? <ShieldAlert size={17} /> : <ShieldCheck size={17} />}
                </span>
                <div className="min-w-0">
                <Link to={`/servers/${r.serverId}`} className="font-semibold hover:underline">{s?.name ?? r.serverId}</Link>
                <div className="flex items-center gap-1.5 text-xs text-muted"><Clock size={11} />{r.oldAgent ? "Agent too old for security checks: update it on the server's Agent tab." : r.at ? `Checked ${relTime(r.at)}` : "Not checked yet"}</div>
                </div>
              </div>
              <Button size="sm" variant="secondary" disabled={r.scanning || r.oldAgent || s?.status === "disconnected"} onClick={() => scan(r.serverId)}>
                {r.scanning ? <Spinner size={14} /> : <RefreshCw size={14} />} {r.scanning ? "Checking…" : "Check now"}
              </Button>
            </div>
            {r.findings.length === 0 ? (
              r.at ? <p className="flex items-center gap-2 text-sm text-ok"><ShieldCheck size={16} /> No problems found.</p> : null
            ) : (
              <ul className="divide-y divide-line">
                {r.findings.map((f) => (
                  <li key={f.id} className="py-3">
                    <div className="flex flex-wrap items-start gap-3">
                      {(() => {
                        const Icon = f.severity === "critical" ? Flame : f.severity === "warning" ? AlertTriangle : Lightbulb;
                        return <span className={cn("grid h-7 w-7 shrink-0 place-items-center rounded-lg", { critical: "bg-crit-soft text-crit", warning: "bg-warn-soft text-warn", info: "bg-info-soft text-info" }[f.severity])}><Icon size={14} /></span>;
                      })()}
                      <span className="min-w-0 flex-1 pt-1 text-sm font-medium">{f.title} <Badge tone={SEV[f.severity]} className="ml-1 capitalize">{f.severity === "info" ? "tip" : f.severity}</Badge></span>
                      {f.id.startsWith("port:") && <Button size="sm" variant="ghost" onClick={() => expect(r.serverId, f)}><Globe size={13} /> It's meant to be public</Button>}
                    </div>
                    {f.detail && <p className="mt-1 pl-10 text-sm text-muted">{f.detail}</p>}
                    {f.fix && (
                      <div className="relative ml-10 mt-2 rounded-xl bg-surface2 p-3 pr-10">
                        <div className="mb-1 flex items-center gap-1.5 text-[11px] font-medium text-muted"><Terminal size={11} /> Fix</div>
                        <pre className="whitespace-pre-wrap break-words font-mono text-xs">{f.fix}</pre>
                        <CopyButton text={f.fix} className="absolute right-2 top-2" />
                      </div>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </Card>
        );
      })}
    </div>
  );
}

function Logins() {
  const { servers, getServer, toast } = useStore();
  const [server, setServer] = useState("");
  const [onlyNew, setOnlyNew] = useState(false);
  const [people, setPeople] = useState(true);
  const [naming, setNaming] = useState<{ key: string; label: string } | null>(null);
  const list = useApi<{ items: Login[]; hidden: number }>(`/api/security/logins?server=${server}&new=${onlyNew ? 1 : 0}&people=${people ? 1 : 0}`);
  const saveName = async () => {
    if (!naming) return;
    try {
      await api("PUT", "/api/security/key-label", naming);
      toast(naming.label ? `Key named “${naming.label}”` : "Name removed");
      setNaming(null);
      list.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const items = list.data?.items ?? [];
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-x-5 gap-y-3">
        <Select className="max-w-[220px]" value={server} onChange={(e) => setServer(e.target.value)}>
          <option value="">All servers</option>
          {servers.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
        </Select>
        <label className="flex items-center gap-2 text-sm">
          <Toggle checked={people} onChange={setPeople} /> People only
        </label>
        <label className="flex items-center gap-2 text-sm">
          <Toggle checked={onlyNew} onChange={setOnlyNew} /> Only first-time countries and keys
        </label>
      </div>
      {people && (list.data?.hidden ?? 0) > 0 && (
        <p className="flex flex-wrap items-center gap-2 rounded-2xl bg-surface2 px-4 py-3 text-sm">
          <Workflow size={15} className="text-muted" />
          {list.data!.hidden} automated login{list.data!.hidden === 1 ? "" : "s"} hidden: GitHub Actions deploys and keys you named. They never raise alerts.
          <button className="font-medium underline underline-offset-2" onClick={() => setPeople(false)}>Show them</button>
        </p>
      )}
      {list.error ? (
        <ErrorState message={list.error} onRetry={list.reload} />
      ) : !list.data ? (
        <LoadingState />
      ) : items.length === 0 ? (
        <EmptyState icon={<ShieldCheck size={20} />} title={people ? "No logins by people" : "No SSH logins recorded"} desc="Agents report successful SSH logins every few minutes (agent 0.9.32 or newer). The first week is read when an agent starts." />
      ) : (
        <Card pad={false}>
          <Table head={["When", "Server", "User", "From", "How"]}>
            {items.map((l, i) => (
              <Tr key={i} className={cn(l.automated && "opacity-60")}>
                <Td className="whitespace-nowrap text-xs" title={new Date(l.ts).toLocaleString()}><span className="inline-flex items-center gap-1.5"><Clock size={12} className="text-muted" />{relTime(l.ts)}</span></Td>
                <Td><Link to={`/servers/${l.serverId}`} className="inline-flex items-center gap-1.5 hover:underline"><ServerIcon size={12} className="text-muted" />{getServer(l.serverId)?.name ?? l.serverId}</Link></Td>
                <Td className="font-mono text-xs"><span className="inline-flex items-center gap-1.5"><UserRound size={12} className="text-muted" />{l.user}</span></Td>
                <Td>
                  <div className="flex flex-wrap items-center gap-1.5">
                    {l.source === "github" ? (
                      <Badge tone="info"><Workflow size={11} className="mr-1 inline" />GitHub Actions</Badge>
                    ) : l.label ? (
                      <Badge tone="info"><Workflow size={11} className="mr-1 inline" />{l.label}</Badge>
                    ) : null}
                    <span className="font-mono text-xs">{l.ip}</span>
                    <span className="text-xs text-muted">{l.country === "LAN" ? "private network" : l.country ? `${flag(l.country)} ${l.country}` : ""}</span>
                    {l.newCountry && <Badge tone="warn">new country</Badge>}
                  </div>
                </Td>
                <Td>
                  <div className="flex flex-wrap items-center gap-1.5 text-xs">
                    {l.method === "publickey" ? <span className="inline-flex items-center gap-1"><KeyRound size={12} className="text-muted" />key</span> : <Badge tone="warn">{l.method}</Badge>}
                    {l.key && <span className="max-w-[200px] truncate font-mono text-muted" title={l.key}>{l.label ? `“${l.label}”` : l.key}</span>}
                    {l.newKey && <Badge tone="warn">new key</Badge>}
                    {l.key && (
                      <button className="text-muted hover:text-fg" title="Name this key" aria-label="Name this key" onClick={() => setNaming({ key: l.key, label: l.label ?? "" })}>
                        <Pencil size={12} />
                      </button>
                    )}
                  </div>
                </Td>
              </Tr>
            ))}
          </Table>
        </Card>
      )}
      <Modal
        open={!!naming}
        onClose={() => setNaming(null)}
        title="Name this SSH key"
        footer={
          <>
            <Button variant="ghost" onClick={() => setNaming(null)}>Cancel</Button>
            <Button onClick={saveName}>Save</Button>
          </>
        }
      >
        <div className="space-y-3">
          <p className="font-mono text-xs text-muted break-all">{naming?.key}</p>
          <Field label="Name" hint="For deploy keys of CI systems (GitLab, Jenkins, your own scripts): named keys count as automated, so their logins are hidden from “People only” and never raise alerts. Leave empty to remove the name.">
            <Input autoFocus value={naming?.label ?? ""} maxLength={60} placeholder="GitLab deploy" onChange={(e) => naming && setNaming({ ...naming, label: e.target.value })} />
          </Field>
        </div>
      </Modal>
    </div>
  );
}
