import { Check } from "lucide-react";
import type { EnrollInfo } from "../api/client";
import { CodeBlock } from "./ui";
import { dateTime } from "../mock/util";

/** The one-line agent install command plus what it does. */
export function InstallCommand({ info }: { info: EnrollInfo }) {
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted">Run this on the VPS (Ubuntu with systemd). The token is single-use and expires {dateTime(info.expiresAt)}.</p>
      <CodeBlock>{info.installCommand}</CodeBlock>
      <ul className="space-y-1.5 text-sm text-muted">
        {[
          "Downloads ops-agent from this server and verifies its SHA-256",
          "Exchanges the token for a permanent, revocable agent credential",
          "Installs and starts the ops-agent systemd service",
          "Connects outbound over WebSocket — no inbound ports needed",
        ].map((t) => (
          <li key={t} className="flex gap-2"><Check size={14} className="mt-0.5 shrink-0 text-ok" />{t}</li>
        ))}
      </ul>
    </div>
  );
}
