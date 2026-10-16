import { useEffect, useState } from "react";
import { BellRing } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Button, Card, Field, Input, SectionTitle, Select } from "./ui";
import { cn } from "../utils/cn";

interface Rules {
  minSeverity: "info" | "warning" | "critical";
  servers: string[];
  quietFrom: string;
  quietTo: string;
  timeZone: string;
}

/** Your own rules for alerts on your phones: severity, servers and quiet hours. On-call pages always ring. */
export function AlertRulesCard() {
  const { servers, toast } = useStore();
  const data = useApi<Rules>("/api/account/alert-rules");
  const [f, setF] = useState<Rules | null>(null);
  useEffect(() => {
    if (data.data) setF(data.data.timeZone === "UTC" && !data.data.quietFrom ? { ...data.data, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" } : data.data);
  }, [data.data]);
  if (!f || !data.data) return null;
  const save = async () => {
    try {
      data.setData(await api<Rules>("PUT", "/api/account/alert-rules", f));
      toast("Alert rules saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const quiet = !!f.quietFrom;
  return (
    <Card>
      <SectionTitle title={<span className="flex items-center gap-2"><BellRing size={16} /> My phone alerts</span>} sub="Which dashboard alerts reach your phones. On-call pages always come through." />
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="At least">
          <Select value={f.minSeverity} onChange={(e) => setF({ ...f, minSeverity: e.target.value as Rules["minSeverity"] })}>
            <option value="info">Everything</option>
            <option value="warning">Warnings and critical</option>
            <option value="critical">Only critical</option>
          </Select>
        </Field>
        <Field label="Quiet hours" hint="Nothing but on-call pages in this window.">
          <div className="flex items-center gap-2">
            <Input type="time" value={f.quietFrom} onChange={(e) => setF({ ...f, quietFrom: e.target.value, quietTo: f.quietTo || "07:00" })} />
            <span className="text-muted">→</span>
            <Input type="time" value={f.quietTo} disabled={!quiet} onChange={(e) => setF({ ...f, quietTo: e.target.value })} />
            {quiet && <Button size="sm" variant="ghost" onClick={() => setF({ ...f, quietFrom: "", quietTo: "" })}>Off</Button>}
          </div>
        </Field>
        <Field label="Time zone"><Input value={f.timeZone} onChange={(e) => setF({ ...f, timeZone: e.target.value })} /></Field>
      </div>
      <Field label="Only these servers" hint="None picked = every server you can see." className="mt-4">
        <div className="flex flex-wrap gap-2">
          {servers.map((s) => (
            <button key={s.id} type="button" onClick={() => setF({ ...f, servers: f.servers.includes(s.id) ? f.servers.filter((x) => x !== s.id) : [...f.servers, s.id] })}
              className={cn("rounded-full border px-3 py-1.5 text-xs", f.servers.includes(s.id) ? "border-transparent bg-accent text-accent-fg" : "border-line text-muted hover:text-fg")}>{s.name}</button>
          ))}
        </div>
      </Field>
      <div className="mt-4"><Button size="sm" disabled={JSON.stringify(f) === JSON.stringify(data.data)} onClick={save}>Save</Button></div>
    </Card>
  );
}
