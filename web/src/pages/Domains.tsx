import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Globe, RefreshCw } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, EmptyState, ErrorState, Gate, LoadingState, PageHeader, Spinner, Table, Td, Tr, type Tone } from "../components/ui";
import { relTime } from "../mock/util";

interface Cert {
  notAfter?: number;
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
}

const DAY = 86_400_000;

function daysLeft(t?: number) {
  return t ? Math.floor((t - Date.now()) / DAY) : null;
}

/** "in 42 days" with a colour from the warning threshold. */
function Expiry({ at, warn, problem, error }: { at?: number; warn: number; problem?: boolean; error?: string }) {
  const left = daysLeft(at);
  if (problem) return <span className="text-crit">{error}</span>;
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
  const { getServer, getApp, subscribeLive, toast } = useStore();
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
          <EmptyState icon={<Globe size={20} />} title="No domains yet" desc="Domains appear here from your Caddy sites and from the domains set on applications. The first check runs shortly after start." />
        ) : (
          <Card pad={false}>
            <Table head={["Domain", "Certificate", "On the server", "Registration", "Checked"]}>
              {list.data.map((d) => {
                const app = d.appId ? getApp(d.appId) : undefined;
                const server = d.serverId ? getServer(d.serverId) : undefined;
                return (
                  <Tr key={d.host}>
                    <Td>
                      <a href={`https://${d.host}`} target="_blank" rel="noreferrer" className="font-medium underline-offset-2 hover:underline">{d.host}</a>
                      <div className="text-xs text-muted">
                        {app ? <Link to={`/applications/${app.id}`} className="hover:underline">{app.name}</Link> : null}
                        {app && server ? " · " : null}
                        {server ? <Link to={`/servers/${server.id}`} className="hover:underline">{server.name}</Link> : !app ? "this dashboard" : null}
                      </div>
                    </Td>
                    <Td>
                      <Expiry at={d.public.notAfter} warn={sslWarn} problem={d.public.problem} error={d.public.error} />
                      {d.public.issuer && !d.public.problem && <div className="text-xs text-muted">{d.public.issuer}</div>}
                    </Td>
                    <Td>
                      {d.origin ? (
                        <>
                          <Expiry at={d.origin.notAfter} warn={sslWarn} error={d.origin.error} />
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
                    <Td className="whitespace-nowrap text-xs text-muted">{relTime(d.checkedAt)}</Td>
                  </Tr>
                );
              })}
            </Table>
          </Card>
        )}
        <p className="mt-4 text-xs text-muted">
          "Certificate" is what visitors get (behind Cloudflare, Cloudflare's). "On the server" is what your server's own web server presents, as its agent sees it: behind a CDN, that one can expire unnoticed. Registration dates come from each registry's RDAP service; some registries do not offer one.
        </p>
      </Gate>
    </>
  );
}
