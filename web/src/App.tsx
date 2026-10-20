import { lazy, type ComponentType } from "react";
import { HashRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { StoreProvider } from "./store/store";
import { RequireAuth } from "./components/Layout";
import { Login, TwoFA } from "./pages/Login";
import { Setup } from "./pages/Setup";


// Pages load on first use, so a phone only downloads the screen it opens. Pages from
// the same file share one chunk.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function page<M extends Record<string, any>>(load: () => Promise<M>, name: keyof M) {
  return lazy(() =>
    load().then(
      (m) => {
        try {
          sessionStorage.removeItem("fw.chunk-reload"); // this build loads: a later update may reload again
        } catch {
          /* private mode */
        }
        return { default: m[name] as ComponentType };
      },
      (err) => {
        // A tab left open across a platform update asks for chunks the new server no
        // longer has: reload once to get the new build instead of showing a broken page.
        try {
          if (!sessionStorage.getItem("fw.chunk-reload")) {
            sessionStorage.setItem("fw.chunk-reload", "1");
            location.reload();
            return new Promise<never>(() => {});
          }
        } catch {
          /* private mode: fall through to the error */
        }
        throw err;
      },
    ),
  );
}
const Overview = page(() => import("./pages/Overview"), "Overview");
const Servers = page(() => import("./pages/Servers"), "Servers");
const AddServer = page(() => import("./pages/AddServer"), "AddServer");
const ServerAgent = page(() => import("./pages/ServerDetail"), "ServerAgent");
const ServerApplications = page(() => import("./pages/ServerDetail"), "ServerApplications");
const ServerDetail = page(() => import("./pages/ServerDetail"), "ServerDetail");
const ServerIncidents = page(() => import("./pages/ServerDetail"), "ServerIncidents");
const ServerMetrics = page(() => import("./pages/ServerDetail"), "ServerMetrics");
const ServerOverview = page(() => import("./pages/ServerDetail"), "ServerOverview");
const ServerPorts = page(() => import("./pages/ServerDetail"), "ServerPorts");
const ServerSettings = page(() => import("./pages/ServerDetail"), "ServerSettings");
const ServerStorage = page(() => import("./pages/ServerDetail"), "ServerStorage");
const ServerDocker = page(() => import("./pages/ServerDocker"), "ServerDocker");
const ServerFirewall = page(() => import("./pages/SecurityAdmin"), "ServerFirewall");
const Applications = page(() => import("./pages/Applications"), "Applications");
const AppDetail = page(() => import("./pages/AppDetail"), "AppDetail");
const AppCommands = page(() => import("./pages/AppTabs"), "AppCommands");
const AppGit = page(() => import("./pages/AppTabs"), "AppGit");
const AppEnv = page(() => import("./pages/AppTabs"), "AppEnv");
const AppWeb = page(() => import("./pages/AppTabs"), "AppWeb");
const ServerWeb = page(() => import("./pages/ServerDetail"), "ServerWeb");
const AppHealth = page(() => import("./pages/AppTabs"), "AppHealth");
const AppIncidents = page(() => import("./pages/AppTabs"), "AppIncidents");
const AppLogs = page(() => import("./pages/AppTabs"), "AppLogs");
const AppMetrics = page(() => import("./pages/AppTabs"), "AppMetrics");
const AppOverview = page(() => import("./pages/AppTabs"), "AppOverview");
const AppSettings = page(() => import("./pages/AppTabs"), "AppSettings");
const AppStorage = page(() => import("./pages/AppTabs"), "AppStorage");
const GlobalLogs = page(() => import("./pages/GlobalLogs"), "GlobalLogs");
const Incidents = page(() => import("./pages/Incidents"), "Incidents");
const Postmortem = page(() => import("./pages/Postmortem"), "Postmortem");
const TeamPage = page(() => import("./pages/TeamPage"), "TeamPage");
const Domains = page(() => import("./pages/Domains"), "Domains");
const Uptime = page(() => import("./pages/Uptime"), "Uptime");
const CronJobs = page(() => import("./pages/CronJobs"), "CronJobs");
const Backups = page(() => import("./pages/Backups"), "Backups");
const Sites = page(() => import("./pages/Sites"), "Sites");
const ServerSites = page(() => import("./pages/Sites"), "ServerSites");
const SitePage = page(() => import("./pages/Sites"), "SitePage");
const StatusEditor = page(() => import("./pages/StatusEditor"), "StatusEditor");
const StatusPages = page(() => import("./pages/StatusEditor"), "StatusPages");
const Security = page(() => import("./pages/Security"), "Security");
const Discoveries = page(() => import("./pages/Discoveries"), "Discoveries");
const MetricsExplorer = page(() => import("./pages/MetricsExplorer"), "MetricsExplorer");
const Notifications = page(() => import("./pages/Notifications"), "Notifications");
const AccountSettings = page(() => import("./pages/Settings"), "AccountSettings");
const AgentsSettings = page(() => import("./pages/Settings"), "AgentsSettings");
const IncidentRulesSettings = page(() => import("./pages/Settings"), "IncidentRulesSettings");
const LogSettings = page(() => import("./pages/Settings"), "LogSettings");
const MetricsSettings = page(() => import("./pages/Settings"), "MetricsSettings");
const NotificationSettings = page(() => import("./pages/Settings"), "NotificationSettings");
const SecuritySettings = page(() => import("./pages/Settings"), "SecuritySettings");
const ServersSettings = page(() => import("./pages/Settings"), "ServersSettings");
const SessionSettings = page(() => import("./pages/Settings"), "SessionSettings");
const SettingsIndex = page(() => import("./pages/Settings"), "SettingsIndex");
const SettingsLayout = page(() => import("./pages/Settings"), "SettingsLayout");
const SystemSettings = page(() => import("./pages/Settings"), "SystemSettings");
const OnCallPage = page(() => import("./pages/OnCall"), "OnCallPage");
const TeamSettings = page(() => import("./pages/Team"), "TeamSettings");
const IntegrationsPage = page(() => import("./pages/Integrations"), "IntegrationsPage");

/** The old Settings → Integrations address, kept for links and GitHub's redirects. */
function ToIntegrations() {
  return <Navigate to={"/integrations" + useLocation().search} replace />;
}
const TwoFASettings = page(() => import("./pages/Settings"), "TwoFASettings");
const UptimeReport = page(() => import("./pages/UptimeReport"), "UptimeReport");

export default function App() {
  return (
    <StoreProvider>
      <HashRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/2fa" element={<TwoFA />} />
          <Route path="/setup" element={<Setup />} />
          <Route path="/report/:id/:month" element={<UptimeReport />} />
          <Route element={<RequireAuth />}>
            <Route index element={<Overview />} />
            <Route path="servers" element={<Servers />} />
            <Route path="servers/new" element={<AddServer />} />
            <Route path="servers/:id" element={<ServerDetail />}>
              <Route index element={<ServerOverview />} />
              <Route path="applications" element={<ServerApplications />} />
              <Route path="metrics" element={<ServerMetrics />} />
              <Route path="ports" element={<ServerPorts />} />
              <Route path="storage" element={<ServerStorage />} />
              <Route path="docker" element={<ServerDocker />} />
              <Route path="firewall" element={<ServerFirewall />} />
              <Route path="incidents" element={<ServerIncidents />} />
              <Route path="agent" element={<ServerAgent />} />
              <Route path="sites" element={<ServerSites />} />
              <Route path="web" element={<ServerWeb />} />
              <Route path="settings" element={<ServerSettings />} />
            </Route>
            <Route path="applications" element={<Applications />} />
            <Route path="applications/:id" element={<AppDetail />}>
              <Route index element={<AppOverview />} />
              <Route path="logs" element={<AppLogs />} />
              <Route path="metrics" element={<AppMetrics />} />
              <Route path="commands" element={<AppCommands />} />
              <Route path="incidents" element={<AppIncidents />} />
              <Route path="health" element={<AppHealth />} />
              <Route path="storage" element={<AppStorage />} />
              <Route path="git" element={<AppGit />} />
              <Route path="env" element={<AppEnv />} />
              <Route path="web" element={<AppWeb />} />
              <Route path="settings" element={<AppSettings />} />
            </Route>
            <Route path="logs" element={<GlobalLogs />} />
            <Route path="incidents" element={<Incidents />} />
            <Route path="incidents/:id/postmortem" element={<Postmortem />} />
            <Route path="team" element={<TeamPage />} />
            <Route path="domains" element={<Domains />} />
            <Route path="uptime" element={<Uptime />} />
            <Route path="cron" element={<CronJobs />} />
            <Route path="backups" element={<Backups />} />
            <Route path="sites" element={<Sites />} />
            <Route path="sites/:id" element={<SitePage />} />
            <Route path="integrations" element={<IntegrationsPage />} />
            <Route path="oncall" element={<OnCallPage />} />
            <Route path="status-page" element={<StatusPages />} />
            <Route path="status-page/:id" element={<StatusEditor />} />
            <Route path="security" element={<Security />} />
            <Route path="discoveries" element={<Discoveries />} />
            <Route path="metrics" element={<MetricsExplorer />} />
            <Route path="notifications" element={<Notifications />} />
            <Route path="settings" element={<SettingsLayout />}>
              <Route index element={<SettingsIndex />} />
              <Route path="account" element={<AccountSettings />} />
              <Route path="security" element={<SecuritySettings />} />
              <Route path="2fa" element={<TwoFASettings />} />
              <Route path="sessions" element={<SessionSettings />} />
              <Route path="servers" element={<ServersSettings />} />
              <Route path="agents" element={<AgentsSettings />} />
              <Route path="logs" element={<LogSettings />} />
              <Route path="metrics" element={<MetricsSettings />} />
              <Route path="notifications" element={<NotificationSettings />} />
              <Route path="incidents" element={<IncidentRulesSettings />} />
              <Route path="team" element={<TeamSettings />} />
              <Route path="oncall" element={<Navigate to="/oncall" replace />} />
              <Route path="tokens" element={<Navigate to="/integrations?tab=api" replace />} />
              <Route path="integrations" element={<ToIntegrations />} />
              <Route path="system" element={<SystemSettings />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        </Routes>
      </HashRouter>
    </StoreProvider>
  );
}
