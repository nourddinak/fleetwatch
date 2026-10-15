import { Suspense, useEffect, useRef, useState } from "react";
import { Link, NavLink, Navigate, Outlet, useLocation, useNavigate } from "react-router-dom";
import { Activity, AlertOctagon, Globe, Bell, Boxes, Check, LayoutDashboard, LogOut, Menu, Moon, Plus, Radar, ScrollText, Search, Server, Settings, Sun, X } from "lucide-react";
import { useStore } from "../store/store";
import { cn } from "../utils/cn";
import { SearchPalette } from "./SearchPalette";
import { AddAppDialog } from "./AddAppDialog";
import { LoadingState, Spinner } from "./ui";
import { useApi } from "../api/client";
import { UpdateBanner } from "./PlatformUpdate";

export function Logo({ className }: { className?: string }) {
  return (
    <div className={cn("flex items-center gap-2.5", className)}>
      <div className="flex h-9 w-9 items-center justify-center rounded-2xl bg-accent text-accent-fg">
        <Activity size={18} strokeWidth={2.6} />
      </div>
      <span className="text-[17px] font-semibold tracking-tight">Fleetwatch</span>
    </div>
  );
}

export function ThemeToggle({ className }: { className?: string }) {
  const { theme, toggleTheme } = useStore();
  return (
    <button onClick={toggleTheme} aria-label="Toggle theme" title={theme === "dark" ? "Switch to light mode" : "Switch to dark mode"} className={cn("flex h-10 w-10 items-center justify-center rounded-full border border-line bg-surface text-muted transition hover:text-fg", className)}>
      {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
    </button>
  );
}

export function RequireAuth() {
  const { auth } = useStore();
  if (auth === "loading")
    return (
      <div className="flex min-h-screen items-center justify-center bg-bg text-muted">
        <Spinner size={20} />
      </div>
    );
  if (auth === "setup") return <Navigate to="/setup" replace />;
  if (auth === "needs2fa") return <Navigate to="/2fa" replace />;
  if (auth !== "in") return <Navigate to="/login" replace />;
  return <Shell />;
}

function Shell() {
  const { incidents, discoveries, notifications, live, logout, me, toasts, openAddApp, servers } = useStore();
  const nav = useNavigate();
  const loc = useLocation();
  const [mobile, setMobile] = useState(false);
  const [palette, setPalette] = useState(false);
  const [addMenu, setAddMenu] = useState(false);
  const [userMenu, setUserMenu] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => setMobile(false), [loc.pathname]);
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, [loc.pathname]);
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((p) => !p);
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, []);
  useEffect(() => {
    const h = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setAddMenu(false);
        setUserMenu(false);
      }
    };
    document.addEventListener("mousedown", h);
    return () => document.removeEventListener("mousedown", h);
  }, []);

  const active = incidents.filter((i) => i.status === "active").length;
  const unread = notifications.filter((n) => !n.read).length;
  const nodes = [
    { to: "/", label: "Overview", icon: LayoutDashboard, end: true },
    { to: "/servers", label: "Servers", icon: Server },
    { to: "/applications", label: "Applications", icon: Boxes },
    { to: "/logs", label: "Logs", icon: ScrollText },
    { to: "/incidents", label: "Incidents", icon: AlertOctagon, badge: active, crit: true },
    { to: "/domains", label: "Domains", icon: Globe },
    { to: "/discoveries", label: "Discoveries", icon: Radar, badge: discoveries.length },
    { to: "/metrics", label: "Metrics", icon: Activity },
    { to: "/notifications", label: "Notifications", icon: Bell, badge: unread },
    { to: "/settings", label: "Settings", icon: Settings },
  ];
  const offline = servers.filter((s) => s.status === "disconnected").length;
  const version = useApi<{ version: string }>("/api/health").data?.version;

  const sidebar = (
    <div className="flex h-full flex-col gap-6 p-5">
      <div className="flex items-center justify-between">
        <Logo />
        <button className="text-muted lg:hidden" onClick={() => setMobile(false)} aria-label="Close menu">
          <X size={20} />
        </button>
      </div>
      <nav className="flex flex-1 flex-col gap-1">
        {nodes.map((n) => (
          <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => cn("flex items-center gap-3 rounded-full px-4 py-2.5 text-sm transition", isActive ? "bg-accent font-medium text-accent-fg" : "text-muted hover:bg-surface2 hover:text-fg")}>
            <n.icon size={17} />
            <span className="flex-1">{n.label}</span>
            {!!n.badge && <span className={cn("rounded-full px-2 py-0.5 text-[10px] font-semibold tabular-nums", n.crit ? "bg-crit text-white dark:text-black" : "bg-neutral-soft text-fg")}>{n.badge}</span>}
          </NavLink>
        ))}
      </nav>
      <div className="rounded-2xl bg-surface2 p-4 text-xs text-muted">
        <div className="mb-1 flex items-center gap-2 font-medium text-fg">
          <span className={cn("h-2 w-2 rounded-full", offline ? "bg-warn" : "bg-ok")} /> {offline ? `${offline} agent offline` : "All agents connected"}
        </div>
        Central server {version ? `v${version}` : "…"}
      </div>
    </div>
  );

  return (
    <div className="min-h-screen bg-bg lg:flex">
      <aside className="sticky top-0 hidden h-screen w-64 shrink-0 border-r border-line bg-surface lg:block">{sidebar}</aside>
      {mobile && (
        <div className="fixed inset-0 z-50 lg:hidden">
          <div className="absolute inset-0 bg-black/50" onClick={() => setMobile(false)} />
          <div className="fade-in absolute left-0 top-0 h-full w-72 bg-surface">{sidebar}</div>
        </div>
      )}
      <div className="min-w-0 flex-1">
        <header className="sticky top-0 z-30 border-b border-line bg-bg/85 backdrop-blur">
          <div ref={menuRef} className="flex items-center gap-2 px-4 py-3 sm:px-8">
            <button className="flex h-10 w-10 items-center justify-center rounded-full border border-line bg-surface lg:hidden" onClick={() => setMobile(true)} aria-label="Open menu">
              <Menu size={18} />
            </button>
            <button onClick={() => setPalette(true)} className="flex h-10 min-w-0 flex-1 items-center gap-3 rounded-full border border-line bg-surface px-4 text-left text-sm text-muted transition hover:border-fg/30 sm:max-w-md">
              <Search size={16} />
              <span className="flex-1 truncate">Search servers, apps, ports, logs…</span>
              <kbd className="hidden rounded-md border border-line px-1.5 py-0.5 text-[10px] sm:block">Ctrl K</kbd>
            </button>
            <div className="ml-auto flex items-center gap-2">
              <ThemeToggle />
              <div className="relative">
                <button onClick={() => { setAddMenu(!addMenu); setUserMenu(false); }} className="flex h-10 w-10 items-center justify-center rounded-full bg-accent text-accent-fg" aria-label="Add">
                  <Plus size={18} />
                </button>
                {addMenu && (
                  <div className="fade-in absolute right-0 top-12 z-40 w-52 rounded-2xl border border-line bg-surface p-1.5 shadow-xl">
                    <button className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-sm hover:bg-surface2" onClick={() => { setAddMenu(false); nav("/servers/new"); }}>
                      <Server size={15} /> Add server
                    </button>
                    <button className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-sm hover:bg-surface2" onClick={() => { setAddMenu(false); openAddApp(); }}>
                      <Boxes size={15} /> Add application
                    </button>
                  </div>
                )}
              </div>
              <Link to="/notifications" className="relative hidden h-10 w-10 items-center justify-center rounded-full border border-line bg-surface text-muted hover:text-fg sm:flex" aria-label="Notifications">
                <Bell size={17} />
                {unread > 0 && <span className="absolute right-2 top-2 h-2 w-2 rounded-full bg-crit" />}
              </Link>
              <div className="relative">
                <button onClick={() => { setUserMenu(!userMenu); setAddMenu(false); }} className="flex h-10 w-10 items-center justify-center rounded-full bg-accent text-sm font-semibold text-accent-fg" aria-label="Account">
                  {(me?.displayName || me?.username || "?").slice(0, 1).toUpperCase()}
                </button>
                {userMenu && (
                  <div className="fade-in absolute right-0 top-12 z-40 w-56 rounded-2xl border border-line bg-surface p-1.5 shadow-xl">
                    <div className="px-3 py-2">
                      <div className="text-sm font-medium">{me?.displayName || me?.username}</div>
                      <div className="text-xs text-muted">@{me?.username}</div>
                    </div>
                    <Link to="/settings/account" className="flex items-center gap-2 rounded-xl px-3 py-2 text-sm hover:bg-surface2" onClick={() => setUserMenu(false)}>
                      <Settings size={15} /> Account settings
                    </Link>
                    <button className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-sm text-crit hover:bg-surface2" onClick={logout}>
                      <LogOut size={15} /> Sign out
                    </button>
                  </div>
                )}
              </div>
            </div>
          </div>
          {live === "closed" && (
            <div role="status" className="flex items-center justify-center gap-2 bg-warn-soft px-4 py-1.5 text-xs text-warn">
              <Spinner size={12} /> Connection to the central server lost — reconnecting. Showing last known data.
            </div>
          )}
        </header>
        <main className="mx-auto w-full max-w-[1400px] px-4 py-6 sm:px-8 sm:py-8">
          <UpdateBanner />
          <Suspense fallback={<LoadingState />}>
            <Outlet />
          </Suspense>
        </main>
      </div>

      <SearchPalette open={palette} onClose={() => setPalette(false)} />
      <AddAppDialog />
      <div className="pointer-events-none fixed bottom-4 right-4 z-[90] flex flex-col gap-2">
        {toasts.map((t) => (
          <div key={t.id} className={cn("fade-in pointer-events-auto flex max-w-sm items-center gap-2 rounded-2xl border px-4 py-3 text-sm shadow-xl", t.kind === "err" ? "border-crit/40 bg-crit-soft text-crit" : "border-line bg-surface")}>
            {t.kind === "ok" && <Check size={15} className="shrink-0 text-ok" />}
            <span>{t.msg}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
