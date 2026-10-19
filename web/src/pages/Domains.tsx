import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, Boxes, Clock, Globe, Info, LayoutDashboard, Lock, RefreshCw, Server as ServerIcon, ShieldAlert, ShieldCheck } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { cn } from "../utils/cn";
import { Badge, Button, Card, EmptyState, ErrorState, Gate, LoadingState, PageHeader, Spinner, Table, Td, Tr, type Tone } from "../components/ui";
import { relTime } from "../mock/util";

interface Cert {
  notAfter?: number;
  autoRenew?: boolean;
  issuer?: string;
  error?: string;
  problem?: boolean;
}
interface Domain {
  host: string;
  serverId?: string;
  appId?: string;
  public: Cert;
  origin?: Cert;
  registered?: string;
  expires?: number;
  registrar?: string;
  domainError?: string;
  checkedAt: number;
  renew?: { at: number; running: boolean; ok: boolean; error?: string; log?: string; notAfter?: number; by: string };
}

const DAY = 86_400_000;

function daysLeft(t?: number) {
  return t ? Math.floor((t - Date.now()) / DAY) : null;
}

/** "in 42 days" with a colour from the warning threshold. */
function Expiry({ at, warn, problem, error, autoRenew }: { at?: number; warn: number; problem?: boolean; error?: string; autoRenew?: boolean }) {
  const left = daysLeft(at);
  if (problem) return <span className="text-crit">{error}</span>;
  if (autoRenew && at && at > Date.now()) {
    return (
      <span className="whitespace-nowrap" title={`Short-lived certificate, renewed automatically. Current one valid until ${new Date(at).toLocaleString()}`}>
        <Badge tone="ok">auto-renewed</Badge>
      </span>
    );
  }
  if (left === null) return <span className="text-muted">{error ?? "—"}</span>;
  const tone: Tone = left <= 3 ? "crit" : left <= warn ? "warn" : "ok";
  const label = left < 0 ? "expired" : left === 0 ? "today" : left === 1 ? "tomorrow" : `in ${left} days`;
  return (
    <span className="whitespace-nowrap" title={new Date(at!).toLocaleString()}>
      <Badge tone={tone}>{label}</Badge>
      <span className="ml-2 text-xs text-muted">{new Date(at!).toLocaleDateString()}</span>
    </span>
  );
}

export function Domains() {
  const { getServer, getApp, subscribeLive, toast, me } = useStore();
  const [showLog, setShowLog] = useState("");
  const renew = async (host: string) => {
    try {
      await api("POST", `/api/domains/${encodeURIComponent(host)}/renew`);
      toast(`Renewing ${host}: up to two minutes`);
      list.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const list = useApi<Domain[]>("/api/domains");
  const rules = useApi<{ sslWarnDays: number; domainWarnDays: number }>("/api/settings/incident-rules");
  const [checking, setChecking] = useState(false);
  const { reload } = list;
  useEffect(
    () =>
      subscribeLive((m) => {
        if (m.type === "domains") {
          reload();
          setChecking(false);
        }
      }),
    [subscribeLive, reload],
  );
  const check = async () => {
    setChecking(true);
    try {
      await api("POST", "/api/domains/check");
    } catch (e) {
      setChecking(false);
      toast((e as ApiError).message, "err");
    }
  };
  const sslWarn = rules.data?.sslWarnDays ?? 14;
  const domainWarn = rules.data?.domainWarnDays ?? 14;

  return (
    <>
      <PageHeader
        title="Domains"
        sub={`Certificates and registrations of your sites. Alerts ${sslWarn} days before a certificate and ${domainWarn} days before a domain expires.`}
        actions={<Button size="sm" variant="secondary" disabled={checking} onClick={check}>{checking ? <Spinner size={14} /> : <RefreshCw size={14} />} Check now</Button>}
      />
      <Gate>
        {list.error ? (
          <ErrorState message={list.error} onRetry={list.reload} />
        ) : !list.data ? (
          <LoadingState />
        ) : list.data.length === 0 ? (
          <EmptyState icon={<Globe size={20} />} title="No domains yet" desc="Domains appear here from your Caddy and Nginx sites and from the domains set on applications. The first check runs shortly after start." />
        ) : (
          <>
          <div className="mb-4 flex flex-wrap gap-2">
            {(() => {
              const soon = (t?: number, w = sslWarn) => { const l = daysLeft(t); return l !== null && l <= w; };
              const problems = list.data.filter((d) => d.public.problem || soon(d.public.notAfter) || soon(d.expires, domainWarn)).length;
              return (
                <>
                  <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-muted"><Globe size={13} /> {list.data.length} domains</span>
                  {problems > 0 ? <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-warn"><AlertTriangle size={13} /> {problems} need attention</span> : <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-ok"><ShieldCheck size={13} /> all certificates fine</span>}
                </>
              );
            })()}
          </div>
          <Card pad={false}>
            <Table head={["Domain", "Certificate", "On the server", "Registration", "Checked"]}>
              {list.data.map((d) => {
                const app = d.appId ? getApp(d.appId) : undefined;
                const server = d.serverId ? getServer(d.serverId) : undefined;
                return (
                  <Tr key={d.host}>
                    <Td>
                      <div className="flex items-center gap-3">
                        <span className={cn("grid h-9 w-9 shrink-0 place-items-center rounded-xl", d.public.problem ? "bg-crit-soft text-crit" : "bg-surface2 text-fg")}>{d.public.problem ? <ShieldAlert size={15} /> : <Lock size={15} />}</span>
                        <div className="min-w-0">
                          <a href={`https://${d.host}`} target="_blank" rel="noreferrer" className="font-medium underline-offset-2 hover:underline">{d.host}</a>
                          <div className="flex flex-wrap items-center gap-x-2 text-xs text-muted">
                            {app ? <Link to={`/applications/${app.id}`} className="inline-flex items-center gap-1 hover:underline"><Boxes size={11} />{app.name}</Link> : null}
                            {server ? <Link to={`/servers/${server.id}`} className="inline-flex items-center gap-1 hover:underline"><ServerIcon size={11} />{server.name}</Link> : !app ? <span className="inline-flex items-center gap-1"><LayoutDashboard size={11} />this dashboard</span> : null}
                          </div>
                        </div>
                      </div>
                    </Td>
                    <Td>
                      <Expiry at={d.public.notAfter} warn={sslWarn} problem={d.public.problem} error={d.public.error} autoRenew={d.public.autoRenew} />
                      {d.public.issuer && !d.public.problem && <div className="text-xs text-muted">{d.public.issuer}</div>}
                      {d.renew?.running ? (
                        <div className="mt-1 inline-flex items-center gap-1.5 text-xs text-muted"><Spinner size={11} /> Renewing…</div>
                      ) : d.renew ? (
                        <div className={cn("mt-1 max-w-xs text-xs", d.renew.ok ? "text-ok" : "text-crit")}>
                          {d.renew.ok ? `Renewed ${relTime(d.renew.at)}` : d.renew.error}
                          {d.renew.log && <button className="ml-1 underline" onClick={() => setShowLog(showLog === d.host ? "" : d.host)}>{showLog === d.host ? "hide" : "Caddy log"}</button>}
                          {showLog === d.host && <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap rounded-lg bg-surface2 p-2 font-mono text-[10px] text-muted">{d.renew.log}</pre>}
                        </div>
                      ) : null}
                      {d.serverId && !d.public.autoRenew && me?.role !== "viewer" && !d.renew?.running && (d.public.problem || (daysLeft(d.public.notAfter) ?? 99) <= sslWarn) && (
                        <button className="mt-1 flex items-center gap-1 text-xs font-medium underline" onClick={() => renew(d.host)} title="Caddy on the server gets a fresh certificate now; the old one stays if it fails">
                          <RefreshCw size={11} /> Renew now
                        </button>
                      )}
                    </Td>
                    <Td>
                      {d.origin ? (
                        <>
                          <Expiry at={d.origin.notAfter} warn={sslWarn} error={d.origin.error} autoRenew={d.origin.autoRenew} />
                          {d.origin.issuer && <div className="text-xs text-muted">{d.origin.issuer}</div>}
                        </>
                      ) : (
                        <span className="text-xs text-muted">not reported</span>
                      )}
                    </Td>
                    <Td>
                      <Expiry at={d.expires} warn={domainWarn} error={d.domainError} />
                      <div className="text-xs text-muted">{[d.registered, d.registrar].filter(Boolean).join(" · ")}</div>
                    </Td>
                    <Td className="whitespace-nowrap text-xs text-muted"><span className="inline-flex items-center gap-1.5"><Clock size={12} />{relTime(d.checkedAt)}</span></Td>
                  </Tr>
                );
              })}
            </Table>
          </Card>
          </>
        )}
        <p className="mt-4 flex gap-2 text-xs text-muted"><Info size={14} className="shrink-0" /><span>
          "Certificate" is what visitors get (behind Cloudflare, Cloudflare's). "On the server" is what your server's own web server presents, as its agent sees it: behind a CDN, that one can expire unnoticed. Registration dates come from each registry's RDAP service; some registries do not offer one.</span>
        </p>
      </Gate>
    </>
  );
}
