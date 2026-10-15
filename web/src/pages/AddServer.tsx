import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Check, CheckCircle2, Loader2 } from "lucide-react";
import { useStore } from "../store/store";
import { api, type ApiError, type EnrollInfo } from "../api/client";
import type { Server } from "../mock/types";
import { Button, Card, Field, Gate, Input, PageHeader, Spinner } from "../components/ui";
import { InstallCommand } from "../components/InstallCommand";
import { cn } from "../utils/cn";

const STEPS = ["Name", "Install agent", "Connected"];

export function AddServer() {
  const { servers, upsertServer } = useStore();
  const nav = useNavigate();
  const [name, setName] = useState("");
  const [provider, setProvider] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [created, setCreated] = useState<{ id: string; enroll: EnrollInfo } | null>(null);

  // The live feed updates the store when the agent connects; that advances the wizard.
  const live = created ? servers.find((s) => s.id === created.id) : undefined;
  const step = !created ? 0 : live && live.status !== "pending" ? 2 : 1;

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      const r = await api<{ server: Server; enroll: EnrollInfo }>("POST", "/api/servers", { name, provider });
      upsertServer(r.server);
      setCreated({ id: r.server.id, enroll: r.enroll });
    } catch (e) {
      setErr((e as ApiError).message);
    }
    setBusy(false);
  };

  return (
    <>
      <PageHeader back={{ to: "/servers", label: "All servers" }} title="Add server" sub="Pair an Ubuntu VPS by installing the Go agent" />
      <Gate>
        <ol className="mb-8 flex flex-wrap items-center gap-2 text-xs">
          {STEPS.map((s, i) => (
            <li key={s} className="flex items-center gap-2">
              <span className={cn("flex h-7 w-7 items-center justify-center rounded-full border text-[11px] font-semibold", i < step ? "border-transparent bg-ok-soft text-ok" : i === step ? "border-transparent bg-accent text-accent-fg" : "border-line text-muted")}>{i < step ? <Check size={13} /> : i + 1}</span>
              <span className={cn("hidden sm:inline", i === step ? "font-medium" : "text-muted")}>{s}</span>
              {i < STEPS.length - 1 && <span className="mx-1 h-px w-6 bg-line" />}
            </li>
          ))}
        </ol>

        <Card className="max-w-2xl">
          {step === 0 && (
            <form onSubmit={create} className="space-y-5">
              <h2 className="text-lg font-semibold">Name your server</h2>
              <Field label="Server name" hint="Shown across the dashboard. You can change it later."><Input value={name} maxLength={64} onChange={(e) => setName(e.target.value)} placeholder="e.g. VPS-US-01" autoFocus /></Field>
              <Field label="Provider label (optional)"><Input value={provider} maxLength={64} onChange={(e) => setProvider(e.target.value)} placeholder="e.g. Hetzner" /></Field>
              {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
              <Button type="submit" disabled={!name.trim() || busy}>{busy && <Spinner size={14} />} Create and get install command</Button>
            </form>
          )}
          {step === 1 && created && (
            <div className="space-y-6">
              <h2 className="text-lg font-semibold">Install the agent on {name}</h2>
              <InstallCommand info={created.enroll} />
              <div className="flex items-center gap-3 rounded-2xl bg-surface2 px-4 py-3 text-sm">
                <Loader2 size={16} className="spin shrink-0 text-muted" />
                <span>Waiting for the agent to connect… this page continues automatically.</span>
              </div>
              <Button variant="ghost" onClick={() => nav(`/servers/${created.id}/agent`)}>Finish later</Button>
            </div>
          )}
          {step === 2 && live && (
            <div className="space-y-5">
              <div className="flex items-center gap-3">
                <span className="flex h-11 w-11 items-center justify-center rounded-full bg-ok-soft text-ok"><CheckCircle2 size={22} /></span>
                <div>
                  <h2 className="text-lg font-semibold">{live.name} is connected</h2>
                  <p className="text-sm text-muted">Agent v{live.agentVersion} · {live.hostname}</p>
                </div>
              </div>
              <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3">
                {[
                  ["OS", live.os || "—"],
                  ["CPU", `${live.cpuCores} cores · ${live.arch}`],
                  ["RAM", `${live.ramGb} GB`],
                  ["Disk", `${live.diskGb} GB`],
                  ["IP", live.ip || "—"],
                  ["Provider", live.provider || "—"],
                ].map(([k, v]) => (
                  <div key={k} className="rounded-2xl bg-surface2 p-3"><dt className="text-[11px] uppercase tracking-wide text-muted">{k}</dt><dd className="truncate font-semibold">{v}</dd></div>
                ))}
              </dl>
              <Button onClick={() => nav(`/servers/${live.id}`)}>Go to server</Button>
            </div>
          )}
        </Card>
      </Gate>
    </>
  );
}
