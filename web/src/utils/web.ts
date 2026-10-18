import type { Server } from "../mock/types";

/** v is at least min (numeric parts of x.y.z; anything unreadable counts as older). */
function atLeast(v: string, min: string) {
  const a = v.replace(/^v/, "").split(/[.-]/).map(Number), b = min.split(".").map(Number);
  for (let i = 0; i < 3; i++) if ((a[i] || 0) !== b[i]) return (a[i] || 0) > b[i];
  return true;
}

/** The web servers a server runs, as its agent reports them; null when the agent is too old to say. */
export function webServers(s: Server): ("Caddy" | "Nginx")[] | null {
  const found = (["Caddy", "Nginx"] as const).filter((w) => s.capabilities.includes(w));
  if (found.length) return [...found];
  return atLeast(s.agentVersion || "0", "0.9.70") ? [] : null;
}
