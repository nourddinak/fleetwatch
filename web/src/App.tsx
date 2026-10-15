import { HashRouter, Navigate, Route, Routes } from "react-router-dom";
import { StoreProvider } from "./store/store";
import { RequireAuth } from "./components/Layout";
import { Login, TwoFA } from "./pages/Login";
import { Setup } from "./pages/Setup";
import { Overview } from "./pages/Overview";
import { Servers } from "./pages/Servers";
import { AddServer } from "./pages/AddServer";
import { ServerAgent, ServerApplications, ServerDetail, ServerIncidents, ServerMetrics, ServerOverview, ServerPorts, ServerSettings, ServerStorage } from "./pages/ServerDetail";
import { Applications } from "./pages/Applications";
import { AppDetail } from "./pages/AppDetail";
import { AppCommands, AppGit, AppHealth, AppIncidents, AppLogs, AppMetrics, AppOverview, AppSettings, AppStorage } from "./pages/AppTabs";
import { GlobalLogs } from "./pages/GlobalLogs";
import { Incidents } from "./pages/Incidents";
import { Discoveries } from "./pages/Discoveries";
import { MetricsExplorer } from "./pages/MetricsExplorer";
import { Notifications } from "./pages/Notifications";
import { AccountSettings, AgentsSettings, IncidentRulesSettings, LogSettings, MetricsSettings, NotificationSettings, SecuritySettings, ServersSettings, SessionSettings, SettingsIndex, SettingsLayout, SystemSettings, TwoFASettings } from "./pages/Settings";

export default function App() {
  return (
    <StoreProvider>
      <HashRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/2fa" element={<TwoFA />} />
          <Route path="/setup" element={<Setup />} />
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
              <Route path="incidents" element={<ServerIncidents />} />
              <Route path="agent" element={<ServerAgent />} />
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
              <Route path="settings" element={<AppSettings />} />
            </Route>
            <Route path="logs" element={<GlobalLogs />} />
            <Route path="incidents" element={<Incidents />} />
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
              <Route path="system" element={<SystemSettings />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        </Routes>
      </HashRouter>
    </StoreProvider>
  );
}
