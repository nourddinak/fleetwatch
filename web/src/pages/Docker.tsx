import { useSearchParams } from "react-router-dom";
import { Container, Server as ServerIcon } from "lucide-react";
import { useStore } from "../store/store";
import { Card, EmptyState, Gate, PageHeader, Segmented, Select } from "../components/ui";
import { cn } from "../utils/cn";
import { Sites } from "./Sites";
import { ServerDocker } from "./ServerDocker";

/** Docker in one place: apps deployed from GitHub, or one server's containers (however
 * they were started), stacks, images and volumes at a time. */
export function Docker() {
  const { servers } = useStore();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") === "servers" ? "servers" : "apps";
  const picked = servers.find((s) => s.id === params.get("server")) ?? servers[0];
  const set = (k: string, v: string) => {
    params.set(k, v);
    setParams(params, { replace: true });
  };
  return (
    <>
      <PageHeader title="Docker" sub="Apps you deploy from GitHub, and every container on your servers with its ports, however it was started." />
      <Gate>
        <div className="space-y-5">
          <Segmented value={tab} onChange={(v) => set("tab", v)} options={[
            { value: "apps", label: <span className="inline-flex items-center gap-1.5"><Container size={13} /> Apps from GitHub</span> },
            { value: "servers", label: <span className="inline-flex items-center gap-1.5"><ServerIcon size={13} /> Servers ({servers.length})</span> },
          ]} />
          {tab === "apps" && <Card><Sites docker /></Card>}
          {tab === "servers" && (servers.length === 0 || !picked ? (
            <EmptyState icon={<ServerIcon size={20} />} title="No servers yet" desc="Add a server first." />
          ) : (
            <Card>
              <ServerTabs value={picked.id} onChange={(id) => set("server", id)} />
              <ServerDocker key={picked.id} serverId={picked.id} />
            </Card>
          ))}
        </div>
      </Gate>
    </>
  );
}

/** Picks one server: a row of names with online dots, or a list when there are many. */
export function ServerTabs({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const { servers } = useStore();
  return (
    <div className="mb-4 flex flex-wrap items-center gap-2 border-b border-line pb-4">
      {servers.length <= 8 ? servers.map((s) => (
        <button key={s.id} onClick={() => onChange(s.id)}
          className={cn("inline-flex items-center gap-2 rounded-full border px-3 py-1.5 text-sm", s.id === value ? "border-accent bg-accent/10 font-medium" : "border-line text-muted hover:text-fg")}>
          <span className={cn("h-2 w-2 rounded-full", s.status === "disconnected" ? "bg-crit" : "bg-ok")} />{s.name}
        </button>
      )) : (
        <Select aria-label="Server" className="max-w-xs" value={value} onChange={(e) => onChange(e.target.value)}>
          {servers.map((s) => <option key={s.id} value={s.id}>{s.name}{s.status === "disconnected" ? " (offline)" : ""}</option>)}
        </Select>
      )}
    </div>
  );
}
