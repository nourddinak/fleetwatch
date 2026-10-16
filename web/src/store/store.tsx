import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import type { App, Discovery, Incident, NotificationRec, Server } from "../mock/types";
import { api, ApiError, UNAUTHENTICATED_EVENT, type Profile } from "../api/client";
import { uid } from "../mock/util";

export interface Toast {
  id: string;
  msg: string;
  kind: "ok" | "err" | "info";
}

const THEME_KEY = "fw.theme";

interface Store {
  theme: "light" | "dark";
  toggleTheme: () => void;
  auth: "loading" | "setup" | "out" | "needs2fa" | "in";
  me: Profile | null;
  refreshMe: () => Promise<boolean>;
  login: (u: string, p: string) => Promise<{ ok: boolean; error?: string }>;
  verify2fa: (code: string, recovery: boolean) => Promise<{ ok: boolean; error?: string }>;
  backToLogin: () => void;
  logout: () => void;
  live: "connecting" | "open" | "closed";
  servers: Server[];
  serversLoaded: boolean;
  upsertServer: (s: Server) => void;
  removeServer: (id: string) => void;
  apps: App[];
  appsLoaded: boolean;
  upsertApp: (a: App) => void;
  removeApp: (id: string) => void;
  discoveries: Discovery[]; // pending
  reloadDiscoveries: () => void;
  incidents: Incident[];
  /** Active maintenance windows: id → end time (unix ms). */
  maintenance: { servers: Record<string, number>; apps: Record<string, number> };
  notifications: NotificationRec[];
  getServer: (id: string) => Server | undefined;
  getApp: (id: string) => App | undefined;
  /** acknowledge | mute | resolve | reopen; throws ApiError. */
  actIncident: (id: string, action: string, note?: string) => Promise<Incident>;
  markAllRead: () => void;
  markRead: (id: number) => void;
  toast: (msg: string, kind?: Toast["kind"]) => void;
  toasts: Toast[];
  addAppOpen: { open: boolean; serverId?: string };
  openAddApp: (serverId?: string) => void;
  closeAddApp: () => void;
  tickCount: number;
  /** Receive every live message (run output, storage results, …); returns an unsubscribe function. */
  subscribeLive: (fn: (m: LiveMessage) => void) => () => void;
}

export interface LiveMessage {
  type: string;
  data: any;
}

const Ctx = createContext<Store | null>(null);
export const useStore = () => {
  const c = useContext(Ctx);
  if (!c) throw new Error("StoreProvider missing");
  return c;
};

const upsert = <T extends { id: string }>(list: T[], x: T) => (list.some((y) => y.id === x.id) ? list.map((y) => (y.id === x.id ? x : y)) : [...list, x]);

export function StoreProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<"light" | "dark">(() => {
    const t = localStorage.getItem(THEME_KEY);
    if (t === "light" || t === "dark") return t;
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  });
  const [auth, setAuth] = useState<Store["auth"]>("loading");
  const [me, setMe] = useState<Profile | null>(null);
  const [live, setLive] = useState<Store["live"]>("connecting");
  const [servers, setServers] = useState<Server[]>([]);
  const [serversLoaded, setServersLoaded] = useState(false);
  const [apps, setApps] = useState<App[]>([]);
  const [appsLoaded, setAppsLoaded] = useState(false);
  const [discoveries, setDiscoveries] = useState<Discovery[]>([]);
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [notifications, setNotifications] = useState<NotificationRec[]>([]);
  const [maintenance, setMaintenance] = useState<Store["maintenance"]>({ servers: {}, apps: {} });
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [addAppOpen, setAddAppOpen] = useState<{ open: boolean; serverId?: string }>({ open: false });
  const [tickCount, setTick] = useState(0);
  const listeners = useRef(new Set<(m: LiveMessage) => void>());
  const subscribeLive = useCallback((fn: (m: LiveMessage) => void) => {
    listeners.current.add(fn);
    return () => {
      listeners.current.delete(fn);
    };
  }, []);

  // theme
  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem(THEME_KEY, theme);
  }, [theme]);

  // auth: the server session is the source of truth.
  // Returns false when the server was unreachable; that never signs the user out.
  const refreshMe = useCallback(async () => {
    try {
      setMe(await api<Profile>("GET", "/api/auth/me"));
      setAuth("in");
    } catch (e) {
      if ((e as ApiError).status === 0) return false;
      setMe(null);
      const code = (e as ApiError).code;
      setAuth(code === "mfa_required" ? "needs2fa" : code === "setup_required" ? "setup" : "out");
    }
    return true;
  }, []);
  useEffect(() => {
    let t = 0;
    const boot = async () => {
      if (!(await refreshMe())) t = window.setTimeout(boot, 3000);
    };
    boot();
    const out = () => {
      setMe(null);
      setAuth("out");
    };
    window.addEventListener(UNAUTHENTICATED_EVENT, out);
    return () => {
      clearTimeout(t);
      window.removeEventListener(UNAUTHENTICATED_EVENT, out);
    };
  }, [refreshMe]);
  // Notice idle-timeout / remote sign-out even on pages that make no API calls.
  useEffect(() => {
    if (auth !== "in") return;
    const t = setInterval(refreshMe, 60_000);
    window.addEventListener("focus", refreshMe);
    return () => {
      clearInterval(t);
      window.removeEventListener("focus", refreshMe);
    };
  }, [auth, refreshMe]);

  const toast = useCallback((msg: string, kind: Toast["kind"] = "ok") => {
    const id = uid("t");
    setToasts((t) => [...t.slice(-3), { id, msg, kind }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 3600);
  }, []);

  const upsertServer = useCallback((s: Server) => setServers((l) => upsert(l, s)), []);
  const removeServer = useCallback((id: string) => {
    setServers((l) => l.filter((x) => x.id !== id));
    setApps((l) => l.filter((a) => a.serverId !== id));
    setDiscoveries((l) => l.filter((d) => d.serverId !== id));
  }, []);
  const upsertApp = useCallback((a: App) => setApps((l) => upsert(l, a)), []);
  const removeApp = useCallback((id: string) => setApps((l) => l.filter((a) => a.id !== id)), []);
  const reloadDiscoveries = useCallback(() => {
    api<Discovery[]>("GET", "/api/discoveries?state=pending").then(setDiscoveries).catch(() => {});
  }, []);

  // Live feed: REST snapshots, then WebSocket pushes. Reconnects with backoff and
  // re-fetches on reconnect, since messages sent while disconnected are lost.
  const blocked = !!me?.twofaRequired; // must set up 2FA first: every other call is refused
  useEffect(() => {
    if (auth !== "in" || blocked) return;
    let ws: WebSocket | null = null;
    let timer = 0;
    let stopped = false;
    let backoff = 1000;
    const snapshot = () => {
      api<Server[]>("GET", "/api/servers").then((l) => !stopped && (setServers(l), setServersLoaded(true))).catch(() => {});
      api<App[]>("GET", "/api/apps").then((l) => !stopped && (setApps(l), setAppsLoaded(true))).catch(() => {});
      reloadDiscoveries();
      api<Incident[]>("GET", "/api/incidents?limit=1000").then((l) => !stopped && setIncidents(l)).catch(() => {});
      api<NotificationRec[]>("GET", "/api/notifications?limit=200").then((l) => !stopped && setNotifications(l)).catch(() => {});
      api<Store["maintenance"]>("GET", "/api/maintenance").then((m) => !stopped && setMaintenance(m)).catch(() => {});
    };
    const connect = () => {
      ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
      ws.onopen = () => {
        if (backoff > 1000) snapshot();
        backoff = 1000;
        setLive("open");
      };
      ws.onmessage = (e) => {
        const m = JSON.parse(e.data) as LiveMessage;
        listeners.current.forEach((fn) => fn(m));
        switch (m.type) {
          case "servers":
            setServers(m.data);
            setServersLoaded(true);
            break;
          case "server":
            upsertServer(m.data);
            break;
          case "server.removed":
            removeServer(m.data.id);
            break;
          case "apps": {
            const { serverId, apps: list } = m.data as { serverId: string; apps: App[] };
            setApps((l) => [...l.filter((a) => a.serverId !== serverId), ...list]);
            break;
          }
          case "maintenance":
            setMaintenance(m.data);
            break;
          case "discoveries":
            reloadDiscoveries();
            break;
          case "incident":
            setIncidents((l) => [m.data, ...l.filter((i) => i.id !== m.data.id)]);
            break;
          case "notification":
            setNotifications((l) => [m.data, ...l.filter((n) => n.id !== m.data.id)].slice(0, 200));
            break;
        }
      };
      ws.onclose = () => {
        setLive("closed");
        if (stopped) return;
        timer = window.setTimeout(connect, backoff);
        backoff = Math.min(backoff * 2, 30_000);
        refreshMe(); // a closed socket may mean the session ended
      };
    };
    snapshot();
    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
      ws?.close();
    };
  }, [auth, blocked, upsertServer, removeServer, reloadDiscoveries, refreshMe]);

  // Re-render every 5s so relative times ("seen 10s ago") stay current.
  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 5000);
    return () => clearInterval(t);
  }, []);

  const value: Store = {
    theme,
    toggleTheme: () => setTheme((t) => (t === "dark" ? "light" : "dark")),
    auth,
    me,
    refreshMe,
    login: async (u, p) => {
      if (!u.trim() || !p) return { ok: false, error: "Enter your username and password." };
      try {
        const r = await api<{ status: "ok" | "2fa" }>("POST", "/api/auth/login", { username: u, password: p });
        if (r.status === "2fa") setAuth("needs2fa");
        else await refreshMe();
        return { ok: true };
      } catch (e) {
        return { ok: false, error: (e as ApiError).message };
      }
    },
    verify2fa: async (code, recovery) => {
      try {
        await api("POST", "/api/auth/2fa", { code: code.trim(), recovery });
        await refreshMe();
        return { ok: true };
      } catch (e) {
        // The 5-minute pending session expired: start over from the password step.
        if ((e as ApiError).code === "unauthenticated") setAuth("out");
        return { ok: false, error: (e as ApiError).message };
      }
    },
    backToLogin: () => {
      api("POST", "/api/auth/logout").catch(() => {});
      setAuth("out");
    },
    logout: () => {
      api("POST", "/api/auth/logout").catch(() => {});
      setMe(null);
      setAuth("out");
    },
    live,
    servers,
    serversLoaded,
    upsertServer,
    removeServer,
    apps,
    appsLoaded,
    upsertApp,
    removeApp,
    discoveries,
    reloadDiscoveries,
    incidents,
    notifications,
    maintenance,
    getServer: (id) => servers.find((s) => s.id === id),
    getApp: (id) => apps.find((a) => a.id === id),
    actIncident: async (id, action, note) => {
      const x = await api<Incident>("POST", `/api/incidents/${id}/${action}`, note ? { note } : undefined);
      setIncidents((l) => l.map((i) => (i.id === x.id ? x : i)));
      return x;
    },
    markAllRead: () => {
      setNotifications((l) => l.map((n) => ({ ...n, read: true })));
      api("POST", "/api/notifications/read", { id: 0 }).catch(() => {});
    },
    markRead: (id) => {
      setNotifications((l) => l.map((n) => (n.id === id ? { ...n, read: true } : n)));
      api("POST", "/api/notifications/read", { id }).catch(() => {});
    },
    toast,
    toasts,
    addAppOpen,
    openAddApp: (serverId) => setAddAppOpen({ open: true, serverId }),
    closeAddApp: () => setAddAppOpen({ open: false }),
    tickCount,
    subscribeLive,
  };

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

// ---------- derived helpers ----------
export const isOpenIncident = (i: Incident) => i.status === "active" || i.status === "acknowledged";

export function serverCounts(serverId: string, apps: App[], incidents: Incident[]) {
  const a = apps.filter((x) => x.serverId === serverId);
  const inc = incidents.filter((i) => i.serverId === serverId && i.status === "active");
  const warnings = a.filter((x) => ["warning", "crash-loop", "offline"].includes(x.status)).length;
  return { apps: a.length, incidents: inc.length, warnings };
}
