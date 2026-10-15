import { useState } from "react";
import { Wrench } from "lucide-react";
import { api, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Select, Spinner } from "./ui";

const FOREVER = 2 ** 62;
const CHOICES: [number, string][] = [
  [30, "Pause alerts for 30 minutes"],
  [60, "Pause alerts for 1 hour"],
  [240, "Pause alerts for 4 hours"],
  [1440, "Pause alerts for 24 hours"],
  [-1, "Pause alerts until I end it"],
];

/** When the maintenance window of this server or app (or its server) ends; 0 = none. */
export function useMaintenance(kind: "server" | "app", id: string, serverId?: string): number {
  const { maintenance } = useStore();
  const own = (kind === "server" ? maintenance.servers : maintenance.apps)[id] ?? 0;
  const inherited = kind === "app" && serverId ? maintenance.servers[serverId] ?? 0 : 0;
  const until = Math.max(own, inherited);
  return until > Date.now() ? until : 0;
}

function endsLabel(until: number) {
  if (until >= FOREVER) return "until ended";
  const d = new Date(until);
  return "until " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) + (d.toDateString() === new Date().toDateString() ? "" : " " + d.toLocaleDateString());
}

/** Small badge for tables and headers. */
export function MaintenanceBadge({ until }: { until: number }) {
  if (!until) return null;
  return (
    <span title={`Alerts paused ${endsLabel(until)}`}>
      <Badge tone="info" className="whitespace-nowrap">
        <Wrench size={11} className="mr-1 inline" />
        Maintenance
      </Badge>
    </span>
  );
}

/** Start or end maintenance for a server or app: alerts pause, incidents are still recorded. */
export function MaintenanceControl({ kind, id, serverId }: { kind: "server" | "app"; id: string; serverId?: string }) {
  const { maintenance, toast } = useStore();
  const own = (kind === "server" ? maintenance.servers : maintenance.apps)[id] ?? 0;
  const until = useMaintenance(kind, id, serverId);
  const viaServer = kind === "app" && until > 0 && !(own > Date.now());
  const [busy, setBusy] = useState(false);
  const set = async (minutes: number) => {
    setBusy(true);
    try {
      await api("PUT", "/api/maintenance", { kind, id, minutes });
      toast(minutes === 0 ? "Maintenance ended" : "Maintenance started: alerts are paused", "info");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  if (until > 0) {
    return (
      <span className="flex items-center gap-2">
        <Badge tone="info" className="whitespace-nowrap">
          <Wrench size={12} className="mr-1 inline" />
          Maintenance {endsLabel(until)}
          {viaServer ? " (server)" : ""}
        </Badge>
        {!viaServer && (
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => set(0)}>
            {busy && <Spinner size={13} />} End now
          </Button>
        )}
      </span>
    );
  }
  return (
    <Select
      aria-label="Start maintenance"
      title="Pause alerts while you deploy or reboot. Incidents are still recorded, and one summary is sent when maintenance ends if something is still broken."
      value=""
      disabled={busy}
      onChange={(e) => e.target.value && set(Number(e.target.value))}
      className="!w-auto !py-1.5 text-sm"
    >
      <option value="">Maintenance…</option>
      {CHOICES.map(([m, label]) => (
        <option key={m} value={m}>
          {label}
        </option>
      ))}
    </Select>
  );
}
