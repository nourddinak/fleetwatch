// Shapes of /api/status-pages, shared by the editor and its tabs.

export interface Component {
  kind: "app" | "server";
  id: string;
  name: string;
  description: string;
}
export interface Group {
  name: string;
  components: Component[];
}
export type Stage = "investigating" | "identified" | "monitoring" | "resolved" | "update";
export interface NoticeUpdate {
  at: number;
  stage: Stage;
  body: string;
}
export interface Notice {
  id: string;
  title: string;
  body: string;
  status: "info" | "maintenance" | "degraded" | "outage" | "resolved";
  start: number;
  end: number;
  createdAt: number;
  updates: NoticeUpdate[];
  draft: boolean;
  auto?: string;
  maint?: string;
}
export interface Config {
  id: string;
  slug: string;
  enabled: boolean;
  title: string;
  description: string;
  logo: string;
  accent: string;
  theme: "auto" | "light" | "dark";
  homeUrl: string;
  homeLabel: string;
  footer: string;
  poweredBy: boolean;
  domain: string;
  historyDays: number;
  showUptime: boolean;
  groups: Group[];
  notices: Notice[];
  updatedAt?: number;
  showResponseTime: boolean;
  subscribe: boolean;
  subscribeHooks: boolean;
  autoNotices: "off" | "draft" | "publish";
  skipMaintNotices: boolean;
  access: "public" | "password" | "team";
  password?: string;
  hasPassword: boolean;
  customCss: string;
  headerHtml: string;
  report: { monthly: boolean; to: string; lastSent: string };
}

export const STAGE_LABEL: Record<Stage, string> = { investigating: "Investigating", identified: "Identified", monitoring: "Monitoring", update: "Update", resolved: "Resolved" };
