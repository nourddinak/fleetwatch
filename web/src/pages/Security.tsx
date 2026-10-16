import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Pencil, RefreshCw, ShieldCheck, Workflow } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CopyButton, EmptyState, ErrorState, Field, Gate, Input, LoadingState, Modal, PageHeader, Segmented, Select, Spinner, Table, Td, Toggle, Tr, type Tone } from "../components/ui";
import { cn } from "../utils/cn";
import { BlockedIPs, Updates, Vulnerabilities } from "./SecurityAdmin";
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
  const [tab, setTab] = useState<"findings" | "logins" | "blocked" | "updates" | "vulns">("findings");
  return (
    <>
      <PageHeader title="Security" sub="Checks every server twice a day: SSH settings, waiting security updates, failed logins, open ports, the firewall, readable .env files and sites that leak .git or .env. You get a notification for anything new." />
      <div className="mb-5">
        <Segmented value={tab} onChange={setTab} options={[{ value: "findings", label: "Findings" }, { value: "logins", label: "SSH logins" }, { value: "blocked", label: "Blocked IPs" }, { value: "updates", label: "Updates" }, { value: "vulns", label: "Vulnerable packages" }]} />
      </div>
      <Gate>{tab === "findings" ? <Findings /> : tab === "logins" ? <Logins /> : tab === "blocked" ? <BlockedIPs /> : tab === "updates" ? <Updates /> : <Vulnerabilities />}</Gate>
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
        <Badge tone="crit">{count("critical")} critical</Badge>
        <Badge tone="warn">{count("warning")} warnings</Badge>
        <Badge tone="info">{count("info")} tips</Badge>
      </div>
      {list.data.map((r) => {
        const s = getServer(r.serverId);
        return (
          <Card key={r.serverId}>
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div>
                <Link to={`/servers/${r.serverId}`} className="font-semibold hover:underline">{s?.name ?? r.serverId}</Link>
                <div className="text-xs text-muted">{r.oldAgent ? "Agent too old for security checks: update it on the server's Agent tab." : r.at ? `Checked ${relTime(r.at)}` : "Not checked yet"}</div>
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
                    <div className="flex flex-wrap items-start gap-2">
                      <Badge tone={SEV[f.severity]} className="capitalize">{f.severity === "info" ? "tip" : f.severity}</Badge>
                      <span className="min-w-0 flex-1 text-sm font-medium">{f.title}</span>
                      {f.id.startsWith("port:") && <Button size="sm" variant="ghost" onClick={() => expect(r.serverId, f)}>It's meant to be public</Button>}
                    </div>
                    {f.detail && <p className="mt-1 text-sm text-muted">{f.detail}</p>}
                    {f.fix && (
                      <div className="relative mt-2 rounded-xl bg-surface2 p-3 pr-10">
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
                <Td className="whitespace-nowrap text-xs" title={new Date(l.ts).toLocaleString()}>{relTime(l.ts)}</Td>
                <Td><Link to={`/servers/${l.serverId}`} className="hover:underline">{getServer(l.serverId)?.name ?? l.serverId}</Link></Td>
                <Td className="font-mono text-xs">{l.user}</Td>
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
                    {l.method === "publickey" ? "key" : <Badge tone="warn">{l.method}</Badge>}
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
