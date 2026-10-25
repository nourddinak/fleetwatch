/** One port of a container, from docker ps: published on the server (host) or only inside Docker. */
export interface PortMap {
  host?: number; // the server's port; none = only reachable inside Docker
  container: string; // "80", or a range "8000-8002"
  proto: string;
  local: boolean; // published on 127.0.0.1 only: not reachable from outside
}

/** Reads docker ps's Ports ("0.0.0.0:8080->80/tcp, [::]:8080->80/tcp, 5432/tcp"); the
 * IPv4 and IPv6 lines of one port become one. */
export function parsePorts(s: string): PortMap[] {
  const out: PortMap[] = [];
  for (const part of s.split(",").map((x) => x.trim()).filter(Boolean)) {
    const m = part.match(/^(?:(.*):(\d+)(?:-\d+)?->)?(\d+(?:-\d+)?)\/(\w+)$/);
    if (!m) continue;
    const [, ip, host, container, proto] = m;
    const p: PortMap = { host: host ? Number(host) : undefined, container, proto, local: !!ip && /^(127\.|\[?::1\]?$|localhost$)/.test(ip) };
    const same = out.find((o) => o.host === p.host && o.container === p.container && o.proto === p.proto);
    if (same) same.local = same.local && p.local;
    else out.push(p);
  }
  return out;
}
