import { useCallback, useEffect, useState } from "react";

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
  }
}

/** Fired when the server says the session is gone; the store listens and shows the login page. */
export const UNAUTHENTICATED_EVENT = "fw:unauthenticated";

/** JSON request to the central server. Throws ApiError with the server's message on failure. */
export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: body === undefined ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, "network", "Could not reach the central server.");
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (res.status === 401 && data.code === "unauthenticated" && !path.startsWith("/api/auth/")) {
      window.dispatchEvent(new Event(UNAUTHENTICATED_EVENT));
    }
    throw new ApiError(res.status, data.code ?? "error", data.error ?? `Request failed (${res.status})`);
  }
  return data as T;
}

/** Loads GET `path` on mount (and when it changes). `reload` refetches without clearing data. */
export function useApi<T>(path: string) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    setLoading(true);
    api<T>("GET", path)
      .then((d) => live && (setData(d), setError(null)))
      .catch((e: ApiError) => live && setError(e.message))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [path, n]);
  const reload = useCallback(() => setN((x) => x + 1), []);
  return { data, error, loading, reload, setData };
}

export interface Profile {
  role: "owner" | "operator" | "viewer";
  servers?: string[]; // a member limited to these servers
  username: string;
  displayName: string;
  email: string;
  passwordChangedAt: number;
  twofaEnabled: boolean;
  recoveryCodesLeft: number;
  /** The owner requires 2FA and this member has not turned it on yet. */
  twofaRequired?: boolean;
}

export interface SessionInfo {
  id: string;
  createdAt: number;
  lastActive: number;
  expiresAt: number;
  ip: string;
  userAgent: string;
  current: boolean;
}

export interface SecuritySettings {
  sessionTimeoutMin: number;
  lockoutAfter: number;
  reauthForCommands: boolean;
  require2FA: boolean;
}

export interface AuditRow {
  ts: number;
  action: string;
  ok: boolean;
  ip: string;
  userAgent: string;
  target: string;
  detail: string;
}

/** "Chrome · Windows" style label from a User-Agent string. */
export function deviceLabel(ua: string): string {
  if (!ua) return "Unknown device";
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : /curl\//.test(ua) ? "curl" : ua.split(/[ /]/)[0];
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Windows/.test(ua) ? "Windows" : /Mac OS X/.test(ua) ? "macOS" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} · ${os}` : browser;
}

/** One bucket from GET /api/metrics (percentages, Mbps). */
export interface MetricPoint {
  t: number;
  cpu: number;
  ram: number;
  swap: number;
  disk: number;
  load: number;
  netRx: number;
  netTx: number;
  gpu: number;
  gpuMem: number;
}
export type MetricKey = Exclude<keyof MetricPoint, "t">;
export const toSeries = (pts: MetricPoint[] | null, m: MetricKey) => (pts ?? []).map((p) => ({ t: p.t, v: p[m] }));

export interface ServerEvent {
  id: number;
  ts: number;
  serverId: string;
  appId?: string;
  kind: string;
  text: string;
}

export interface EnrollInfo {
  token: string;
  installCommand: string;
  expiresAt: number;
}
