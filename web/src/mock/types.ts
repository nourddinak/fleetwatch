export type Manager = "PM2" | "systemd" | "standalone" | "Docker";
export type AppStatusT = "online" | "offline" | "warning" | "starting" | "stopped" | "crash-loop" | "unknown";
export type ServerStatusT = "pending" | "online" | "warning" | "critical" | "disconnected";
export type Severity = "info" | "warning" | "critical";
export type IncidentStatus = "active" | "acknowledged" | "resolved" | "muted";
export type HealthStatus = "healthy" | "failed" | "unknown" | "disabled";
/** Detected by the agent: Node.js, Next.js, Bun, Deno, Python, Go, Rust, PHP, Ruby, Java, Static, Binary, … */
export type Runtime = string;

/** Display label from the server, e.g. "App Offline", "High CPU". */
export type IncidentType = string;

export interface Ping {
  ts: number;
  ms: number;
}

/** A monitored VPS, as served by GET /api/servers (internal/server/fleet/view.go). */
export interface Server {
  id: string;
  name: string;
  hostname: string;
  ip: string;
  privateIps: string[];
  provider: string;
  os: string;
  kernel: string;
  arch: string;
  cpuCores: number;
  ramGb: number;
  swapGb: number;
  diskGb: number;
  cpu: number;
  ram: number;
  swap: number;
  disk: number;
  netRx: number; // Mbps
  netTx: number;
  load: [number, number, number];
  uptimeSec: number;
  status: ServerStatusT;
  agentVersion: string;
  latestAgentVersion: string;
  lastSeen: number;
  connectedSince: number;
  createdAt: number;
  latencyMs: number;
  pings: Ping[];
  notes: string;
  notifyOverride: "default" | "all" | "critical-only" | "muted";
  capabilities: string[];
  enrolled: boolean;
  transport: string;
  /** NVIDIA GPUs (agent 0.9.38+); empty without one. */
  gpus?: GPU[];
}

export interface GPU {
  name: string;
  util: number;
  memUsedGb: number;
  memTotalGb: number;
  tempC: number;
  powerW: number;
}

export interface PortInfo {
  port: number;
  bind: string;
  proto: "TCP" | "UDP";
}

export interface HealthCheck {
  enabled: boolean;
  url: string;
  intervalSec: number;
  timeoutMs: number;
  expectedStatus: number;
}

export interface GitInfo {
  repo: string;
  remote: string;
  branch: string;
  commit: string;
  message: string;
  author: string;
  date: number;
  dirty: boolean;
  ahead: number;
  behind: number;
  changes: { state: string; file: string }[];
}

/** An application as served by GET /api/apps (internal/server/apps/view.go). */
export interface App {
  id: string;
  name: string;
  slug: string;
  serverId: string;
  manager: Manager;
  pm2Id: number | null;
  pm2Name: string;
  unit: string;
  path: string;
  runtime: Runtime;
  status: AppStatusT;
  pid: number | null;
  cpu: number;
  ram: number;
  uptimeSec: number;
  restarts: number;
  ports: PortInfo[];
  folderMB: number;
  domain: string | null;
  health: HealthCheck;
  healthStatus: HealthStatus;
  latency: number | null;
  git: GitInfo | null;
  logRate: number;
  errors24h: number;
  discoveredAt: number;
  notes: string;
  docker: { image: string; container: string } | null;
  flags: AppFlags;
  processKey: string;
  rawStatus: string;
  instances: number;
  suggestedDomain: string;
  lastSeen: number;
  source: string;
  user: string;
  script: string;
  interpreter: string;
  execStart: string;
  outLog: string;
  errLog: string;
  processFound: boolean;
  markers: string[];
  healthError: string;
  healthFailures: number;
  healthLastOk: number;
  healthLastFail: number;
  storageScannedAt: number;
}

export interface AppFlags {
  monitoring: boolean;
  logs: boolean;
  metrics: boolean;
  incidents: boolean;
  notifications: boolean;
}

export interface HealthResult {
  ts: number;
  ok: boolean;
  status: number;
  latency: number;
  error?: string;
}

export interface Incident {
  id: string;
  typeKey: string;
  type: IncidentType;
  severity: Severity;
  status: IncidentStatus;
  title: string;
  serverId: string;
  appId: string | null;
  occurrences: number;
  firstAt: number;
  lastAt: number;
  resolvedAt?: number;
  resolvedBy: string; // "" | auto | admin
  mutedUntil?: number;
  resolution: string;
  timeline: { ts: number; text: string }[];
  logSamples: string[];
}

/** Served by GET /api/discoveries (internal/server/apps/discovery.go). */
export interface Discovery {
  id: string;
  name: string;
  serverId: string;
  kind: "pm2" | "systemd" | "folder";
  headline: string;
  manager: Manager;
  unit: string;
  path: string;
  runtime: Runtime;
  port: number | null;
  ports: PortInfo[];
  public: boolean;
  git: boolean;
  gitRemote: string;
  discoveredAt: number;
  lastSeen: number;
  pid: number | null;
  status: string;
  cmdline: string;
  user: string;
  files: string[];
  markers: string[];
  state: "pending" | "ignored" | "added";
  reason: string;
  suggestedDomain: string;
}

export interface CommandDef {
  id: string;
  name: string;
  description: string;
  kind: "builtin" | "custom";
  requiresConfirm: boolean;
  danger?: boolean;
}

export interface CommandRun {
  id: string;
  appId: string;
  commandId: string;
  name: string;
  preview: string;
  cwd: string;
  startedAt: number;
  finishedAt?: number;
  status: "running" | "completed" | "failed";
  exitCode?: number;
  stdout: string;
  stderr: string;
}

export interface NotificationRec {
  id: number;
  ts: number;
  event: string;
  serverId: string; // "" when not tied to a server (tests)
  appId: string;
  incidentId: string;
  provider: "ntfy" | "telegram" | "discord" | "webhook" | "push";
  title: string;
  message: string;
  result: "delivered" | "failed";
  error?: string;
  latencyMs: number;
  read: boolean;
}

export interface AppEvent {
  id: string;
  ts: number;
  appId?: string;
  serverId: string;
  kind: "restart" | "health_failed" | "health_recovered" | "high_ram" | "high_cpu" | "git_commit" | "started" | "stopped" | "deploy" | "agent" | "disk" | "reboot" | "discovery";
  text: string;
}

export type Metric = "cpu" | "ram" | "disk" | "netRx" | "netTx" | "load" | "latency" | "restarts" | "errors" | "requests";
export type Range = "1h" | "6h" | "24h" | "7d" | "30d";
export interface Point {
  t: number;
  v: number;
}
