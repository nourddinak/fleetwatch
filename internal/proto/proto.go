// Package proto defines the messages exchanged between ops-agent and ops-server.
// Every WebSocket frame is one JSON Envelope.
package proto

import "encoding/json"

// Protocol is bumped on incompatible changes; the server rejects agents it cannot talk to.
const Protocol = 1

type Envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"` // correlates requests and replies
	Data json.RawMessage `json:"data,omitempty"`
}

// Message types.
const (
	TypeHello   = "hello"   // agent → server, first frame after connecting
	TypeMetrics = "metrics" // agent → server, periodic host sample
	TypeConfig  = "config"  // server → agent, sent after hello and on change
	TypeRestart = "restart" // server → agent, exit so systemd restarts the agent
)

// Encode builds one frame.
func Encode(typ, id string, v any) ([]byte, error) {
	var raw json.RawMessage
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Envelope{Type: typ, ID: id, Data: raw})
}

// Hello describes the host and agent.
type Hello struct {
	Protocol     int      `json:"protocol"`
	AgentVersion string   `json:"agentVersion"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"` // e.g. "Ubuntu 24.04.1 LTS"
	Kernel       string   `json:"kernel"`
	Arch         string   `json:"arch"`
	CPUCores     int      `json:"cpuCores"`
	MemTotal     uint64   `json:"memTotal"`  // bytes
	SwapTotal    uint64   `json:"swapTotal"` // bytes
	DiskTotal    uint64   `json:"diskTotal"` // bytes, root filesystem
	IPs          []string `json:"ips"`       // non-loopback interface addresses
	BootTime     int64    `json:"bootTime"`  // unix seconds
	Capabilities []string `json:"capabilities"`
}

// Metrics is one host sample.
type Metrics struct {
	TS        int64   `json:"ts"`  // unix ms
	CPU       float64 `json:"cpu"` // percent of all cores
	MemUsed   uint64  `json:"memUsed"`
	MemTotal  uint64  `json:"memTotal"`
	SwapUsed  uint64  `json:"swapUsed"`
	SwapTotal uint64  `json:"swapTotal"`
	DiskUsed  uint64  `json:"diskUsed"`
	DiskTotal uint64  `json:"diskTotal"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	NetRxBps  float64 `json:"netRxBps"` // bytes/s, all non-loopback interfaces
	NetTxBps  float64 `json:"netTxBps"`
	Uptime    uint64  `json:"uptime"`         // seconds
	GPUs      []GPU   `json:"gpus,omitempty"` // NVIDIA GPUs (agent 0.9.38+)
}

// GPU is one graphics card's reading.
type GPU struct {
	Name     string  `json:"name"`
	Util     float64 `json:"util"`     // percent
	MemUsed  uint64  `json:"memUsed"`  // bytes
	MemTotal uint64  `json:"memTotal"` // bytes
	TempC    float64 `json:"tempC"`
	PowerW   float64 `json:"powerW"`
}

type Config struct {
	MetricsIntervalSec   int             `json:"metricsIntervalSec"`
	InventoryIntervalSec int             `json:"inventoryIntervalSec"`
	DiscoveryRoots       []string        `json:"discoveryRoots"`
	LogTargets           []LogTarget     `json:"logTargets"`      // monitored apps whose logs to collect
	MaxLogLineBytes      int             `json:"maxLogLineBytes"` // longer lines are cut
	HealthChecks         []HealthCheck   `json:"healthChecks"`
	StorageTargets       []StorageTarget `json:"storageTargets"` // app folders to measure periodically
	Blocking             bool            `json:"blocking"`       // keep BlockedIPs blocked (agent 0.9.38+)
	BlockedIPs           []string        `json:"blockedIps"`     // addresses and CIDR ranges blocked fleet-wide
}

// LogTarget asks the agent to collect one application's logs. The agent resolves
// PM2 log files and systemd units from its own inventory.
type LogTarget struct {
	AppID   string `json:"appId"`
	Manager string `json:"manager"` // pm2 | systemd
	Key     string `json:"key"`     // PM2 name or systemd unit
}

type EnrollRequest struct {
	Token string `json:"token"`
}

type EnrollResponse struct {
	ServerID   string `json:"serverId"`
	Credential string `json:"credential"`
}

// Inventory messages: the agent reports what runs on the host; the server decides
// which entries are monitored apps and which are new discoveries.
const (
	TypeInventory = "inventory" // agent → server, periodic and after TypeScan
	TypeScan      = "scan"      // server → agent, send an inventory now
)

type Inventory struct {
	TS        int64      `json:"ts"`
	Processes []Process  `json:"processes"`
	Ports     []Port     `json:"ports"`
	Projects  []Project  `json:"projects"`
	Routes    []Route    `json:"routes"`          // reverse-proxy routes found in Caddy
	Certs     []CertInfo `json:"certs,omitempty"` // certificates the local web server presents
	Errors    []string   `json:"errors"`          // probes that failed (e.g. pm2 not runnable)
}

// Process is one PM2 app (all its instances) or one systemd service.
type Process struct {
	Manager       string  `json:"manager"` // "pm2" | "systemd"
	Key           string  `json:"key"`     // PM2 app name or systemd unit name
	Description   string  `json:"description,omitempty"`
	PM2IDs        []int   `json:"pm2Ids,omitempty"`
	PM2Home       string  `json:"pm2Home,omitempty"`
	User          string  `json:"user,omitempty"`
	Status        string  `json:"status"`    // online | starting | stopped | errored | failed | unknown
	RawStatus     string  `json:"rawStatus"` // PM2 status or "ActiveState/SubState"
	PID           int     `json:"pid"`
	StartedAt     int64   `json:"startedAt"` // unix ms, 0 = unknown
	Restarts      int     `json:"restarts"`
	Unstable      bool    `json:"unstable,omitempty"` // PM2 unstable restarts / systemd auto-restart loop
	CPU           float64 `json:"cpu"`                // percent of one core, whole process tree
	MemBytes      uint64  `json:"memBytes"`           // RSS of the process tree
	Instances     int     `json:"instances"`
	Script        string  `json:"script,omitempty"`
	Interpreter   string  `json:"interpreter,omitempty"`
	ExecStart     string  `json:"execStart,omitempty"`
	Cwd           string  `json:"cwd,omitempty"`
	OutLog        string  `json:"outLog,omitempty"`
	ErrLog        string  `json:"errLog,omitempty"`
	RestartPolicy string  `json:"restartPolicy,omitempty"`
	Likely        bool    `json:"likely"` // looks like a user application, not an OS service
	Reason        string  `json:"reason,omitempty"`
	Runtime       string  `json:"runtime,omitempty"`
	Project       string  `json:"project,omitempty"` // project root directory
	Ports         []Port  `json:"ports,omitempty"`
}

type Port struct {
	Proto   string `json:"proto"` // TCP | UDP
	Addr    string `json:"addr"`  // bind address
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Process string `json:"process"`
}

type Project struct {
	Path    string   `json:"path"`
	Markers []string `json:"markers"`
	Runtime string   `json:"runtime"`
	Files   []string `json:"files"`
	Owner   string   `json:"owner"`
	Git     *Git     `json:"git,omitempty"`
}

type Git struct {
	Remote  string      `json:"remote"` // credentials stripped
	Branch  string      `json:"branch"`
	Commit  string      `json:"commit"`
	Message string      `json:"message"`
	Author  string      `json:"author"`
	Date    int64       `json:"date"` // unix ms
	Dirty   bool        `json:"dirty"`
	Ahead   int         `json:"ahead"`
	Behind  int         `json:"behind"`
	Changes []GitChange `json:"changes"`
}

type GitChange struct {
	State string `json:"state"` // Modified | Added | Deleted | Renamed | Untracked
	File  string `json:"file"`
}

type Route struct {
	Host     string `json:"host"`
	Upstream string `json:"upstream"`
	Port     int    `json:"port"`
	Root     string `json:"root,omitempty"`   // static site directory (root / file_server)
	Live     bool   `json:"live,omitempty"`   // read from the running Caddy, not just its config file
	Server   string `json:"server,omitempty"` // the web server it comes from: "" (Caddy) or "nginx" (agent 0.9.71+)
}

// TypeLogs carries a batch of []LogLine from agent to server.
const TypeLogs = "logs"

// LogLine is one raw line. The server classifies severity and sanitizes the text.
type LogLine struct {
	TS       int64  `json:"ts"`              // unix ms
	AppID    string `json:"appId,omitempty"` // empty for agent notices
	Source   string `json:"source"`          // pm2 | systemd | agent
	Stream   string `json:"stream"`          // stdout | stderr | journal
	Priority int    `json:"priority"`        // syslog priority 0-7 from journald, -1 when unknown
	Message  string `json:"message"`
}

// Commands, health checks and storage scans.
const (
	TypeExec          = "exec"           // server → agent: run a built-in action or custom command
	TypeExecOutput    = "exec.output"    // agent → server: output chunk of a running command
	TypeExecResult    = "exec.result"    // agent → server: command finished
	TypeHealth        = "health"         // agent → server: []HealthResult
	TypeHealthNow     = "health.now"     // server → agent: run one app's check now (data: app id)
	TypeStorageScan   = "storage.scan"   // server → agent: measure one folder now (StorageTarget)
	TypeStorageResult = "storage.result" // agent → server: StorageResult
)

// Exec asks the agent to run something. Built-ins (start/stop/restart/reload) go
// through the process manager; custom commands are admin-defined shell commands run
// as the app's own user in the app directory.
type Exec struct {
	RunID      string   `json:"runId"`
	AppID      string   `json:"appId"`
	Kind       string   `json:"kind"`   // builtin | custom
	Action     string   `json:"action"` // builtin: start | stop | restart | reload
	Manager    string   `json:"manager"`
	Key        string   `json:"key"`
	AppPath    string   `json:"appPath"`
	Command    string   `json:"command"` // custom only
	Cwd        string   `json:"cwd"`     // custom only; inside AppPath
	Env        []string `json:"env"`     // custom only; KEY=VALUE
	TimeoutSec int      `json:"timeoutSec"`
}

type ExecOutput struct {
	RunID  string `json:"runId"`
	Stream string `json:"stream"` // stdout | stderr
	Data   string `json:"data"`
}

type ExecResult struct {
	RunID    string `json:"runId"`
	Status   string `json:"status"` // succeeded | failed | timeout | refused | error
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
	RunAs    string `json:"runAs,omitempty"`
	Preview  string `json:"preview,omitempty"` // what was actually executed
}

type HealthCheck struct {
	AppID          string `json:"appId"`
	URL            string `json:"url"`
	IntervalSec    int    `json:"intervalSec"`
	TimeoutMs      int    `json:"timeoutMs"`
	ExpectedStatus int    `json:"expectedStatus"`
}

type HealthResult struct {
	AppID     string `json:"appId"`
	TS        int64  `json:"ts"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status"` // HTTP status, 0 when no response
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

type StorageTarget struct {
	AppID string `json:"appId"`
	Path  string `json:"path"`
}

type StorageItem struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Dir   bool   `json:"dir"`
}

type StorageResult struct {
	AppID      string        `json:"appId"`
	Path       string        `json:"path"`
	TotalBytes int64         `json:"totalBytes"`
	Files      int64         `json:"files"`
	Items      []StorageItem `json:"items"` // largest top-level entries
	ScannedAt  int64         `json:"scannedAt"`
	DurationMs int64         `json:"durationMs"`
	Truncated  bool          `json:"truncated"` // stopped at the entry or time limit
	Error      string        `json:"error,omitempty"`
}

// Self-update: the agent downloads the server's build of itself and restarts.
const (
	TypeUpdate       = "update"        // server → agent: Update
	TypeUpdateResult = "update.result" // agent → server: why the update failed (string)
)

// Update names the version the server ships and the sha256 of each platform's binary
// ("linux-amd64" → hex). The agent only installs a download that matches.
type Update struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"`
}

// CertInfo is the certificate a web server presents for Host. The agent checks its own
// server (127.0.0.1:443); behind a CDN this is the origin certificate, which the public
// one hides.
type CertInfo struct {
	Host      string `json:"host"`
	NotAfter  int64  `json:"notAfter,omitempty"`  // unix ms
	NotBefore int64  `json:"notBefore,omitempty"` // unix ms; with NotAfter, tells short-lived certificates apart
	Issuer    string `json:"issuer,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Disk rescue: what fills a server's disk, and safe cleanups (agent 0.9.31+).
const (
	TypeDiskScan    = "disk.scan"    // server → agent: measure (no data)
	TypeDiskReport  = "disk.report"  // agent → server: DiskReport
	TypeDiskClean   = "disk.clean"   // server → agent: DiskClean
	TypeDiskCleaned = "disk.cleaned" // agent → server: DiskCleaned
)

// DiskItem is one kind of space the agent can give back.
type DiskItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

type DiskDir struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type DiskReport struct {
	At         int64      `json:"at"`
	Items      []DiskItem `json:"items"`
	Top        []DiskDir  `json:"top"` // biggest folders on the root filesystem
	DurationMs int64      `json:"durationMs"`
}

type DiskClean struct {
	RunID  string `json:"runId"`
	Action string `json:"action"` // a DiskItem ID
}

type DiskCleaned struct {
	RunID  string `json:"runId"`
	Action string `json:"action"`
	Freed  int64  `json:"freed"`
	Error  string `json:"error,omitempty"`
}

// Security: host checks and SSH logins (agent 0.9.32+).
const (
	TypeSecurityScan   = "security.scan"   // server → agent: SecurityScan
	TypeSecurityReport = "security.report" // agent → server: SecurityReport
	TypeSSHLogins      = "ssh.logins"      // agent → server: []SSHLogin, new successful logins
)

type SecurityScan struct {
	AppPaths []string `json:"appPaths"` // folders to check for readable .env files
}

// Finding is one security issue with a plain-language fix.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // info | warning | critical
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Fix      string `json:"fix,omitempty"`
}

type SecurityReport struct {
	At       int64     `json:"at"`
	Findings []Finding `json:"findings"`
}

// Host administration (agent 0.9.38+): blocking repeat SSH guessers fleet-wide, the
// firewall, package updates and app dependencies (for known vulnerabilities).
const (
	TypeSSHFailures    = "ssh.failures"    // agent → server: []SSHFailure since the last report
	TypeBlockStatus    = "block.status"    // agent → server: BlockStatus after applying Config.BlockedIPs
	TypeFirewallScan   = "firewall.scan"   // server → agent (no data)
	TypeFirewallReport = "firewall.report" // agent → server: FirewallReport
	TypeFirewallChange = "firewall.change" // server → agent: FirewallChange
	TypeFirewallResult = "firewall.result" // agent → server: HostActionResult
	TypeUpdatesScan    = "updates.scan"    // server → agent: UpdatesScan
	TypeUpdatesReport  = "updates.report"  // agent → server: UpdatesReport
	TypeUpdatesInstall = "updates.install" // server → agent: UpdatesInstall
	TypeUpdatesResult  = "updates.result"  // agent → server: HostActionResult
	TypeDepsScan       = "deps.scan"       // server → agent: DepsScan
	TypeDepsReport     = "deps.report"     // agent → server: DepsReport
)

// SSHFailure is the failed SSH logins from one address.
type SSHFailure struct {
	IP    string   `json:"ip"`
	Count int      `json:"count"`
	Users []string `json:"users,omitempty"` // a few of the user names tried
	Last  int64    `json:"last"`
}

type BlockStatus struct {
	At      int64  `json:"at"`
	Applied int    `json:"applied"`
	Error   string `json:"error,omitempty"`
}

type FirewallRule struct {
	Num       int    `json:"num"`
	To        string `json:"to"`
	Action    string `json:"action"` // ALLOW | DENY | REJECT | LIMIT
	Direction string `json:"direction,omitempty"`
	From      string `json:"from"`
	V6        bool   `json:"v6"`
	Raw       string `json:"raw"` // the rule as ufw prints it; identifies it for deletion
}

type FirewallReport struct {
	At              int64          `json:"at"`
	Tool            string         `json:"tool"` // ufw | firewalld | iptables | none
	Active          bool           `json:"active"`
	DefaultIncoming string         `json:"defaultIncoming,omitempty"` // ufw: deny | allow | reject
	Rules           []FirewallRule `json:"rules"`
	Raw             string         `json:"raw,omitempty"` // firewalld or iptables configuration, shown as is
	SSHPort         int            `json:"sshPort"`
}

// FirewallChange: Action allow | deny | limit (Port, Proto, From), delete (Rule), enable or disable (ufw only).
type FirewallChange struct {
	RunID  string `json:"runId"`
	Action string `json:"action"`
	Port   string `json:"port,omitempty"`
	Proto  string `json:"proto,omitempty"`
	From   string `json:"from,omitempty"`
	Rule   string `json:"rule,omitempty"`
}

type HostActionResult struct {
	RunID  string `json:"runId"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Package struct {
	Name     string `json:"name"`
	Current  string `json:"current,omitempty"`
	New      string `json:"new"`
	Security bool   `json:"security"`
}

type UpdatesScan struct {
	Refresh bool `json:"refresh"` // download fresh package lists first
}

type UpdatesReport struct {
	At             int64     `json:"at"`
	Manager        string    `json:"manager"` // apt | dnf
	Packages       []Package `json:"packages"`
	RebootRequired bool      `json:"rebootRequired"`
	AutoSecurity   bool      `json:"autoSecurity"` // unattended-upgrades installed
	ListsAt        int64     `json:"listsAt,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type UpdatesInstall struct {
	RunID        string `json:"runId"`
	SecurityOnly bool   `json:"securityOnly"`
}

type DepsScan struct {
	Apps map[string]string `json:"apps"` // app ID → folder
}

type Dep struct {
	Ecosystem string `json:"ecosystem"` // npm | PyPI | Go | Packagist (OSV names)
	Name      string `json:"name"`
	Version   string `json:"version"`
}

type DepsReport struct {
	At   int64            `json:"at"`
	Apps map[string][]Dep `json:"apps"`
}

// Static sites (agent 0.9.43+): publish a repository snapshot under /var/www/<name>
// with a Caddy site for its addresses.
const (
	TypeSiteDeploy = "site.deploy" // server → agent: SiteDeploy
	TypeSiteRemove = "site.remove" // server → agent: SiteRemove
	TypeSiteResult = "site.result" // agent → server: SiteResult
)

type SiteDeploy struct {
	RunID   string   `json:"runId"`
	Name    string   `json:"name"`    // folder under /var/www and Caddy site name
	Archive string   `json:"archive"` // https URL of a tar.gz snapshot (GitHub tarball)
	Token   string   `json:"token"`   // short-lived token for the download
	Dir     string   `json:"dir"`     // folder of the snapshot to publish ("" = all)
	Domains []string `json:"domains"` // addresses Caddy serves the site on
	SPA     bool     `json:"spa"`     // unknown paths fall back to /index.html
}

type SiteRemove struct {
	RunID string `json:"runId"`
	Name  string `json:"name"`
}

type SiteResult struct {
	RunID   string `json:"runId"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Log     string `json:"log,omitempty"`
	Release string `json:"release,omitempty"` // the release now live
	Caddy   string `json:"caddy,omitempty"`   // generated | repository | "" (files only)
}

// An app's .env files (agent 0.9.64+): read or write one, inside the app's folder.
const (
	TypeEnvFile       = "env.file"        // server → agent: EnvFile
	TypeEnvFileResult = "env.file.result" // agent → server: EnvFileResult
)

type EnvFile struct {
	RunID   string `json:"runId"`
	Dir     string `json:"dir"`             // the app's working directory
	Name    string `json:"name"`            // .env, .env.<something> or Caddyfile; "" = only list the files
	Caddy   bool   `json:"caddy,omitempty"` // the server's main Caddyfile instead (agent 0.9.68+): checked and reloaded on save
	Web     string `json:"web,omitempty"`   // or a web server's own files (agent 0.9.70+): "caddy" | "nginx"
	Write   bool   `json:"write,omitempty"`
	Content string `json:"content,omitempty"`
}

type EnvFileResult struct {
	RunID   string   `json:"runId"`
	Name    string   `json:"name"`
	Files   []string `json:"files"`  // the .env files in the folder
	Exists  bool     `json:"exists"` // Name is there (else it can be created)
	Content string   `json:"content"`
	Error   string   `json:"error,omitempty"`
}

// Docker: containers, compose stacks, images and volumes (agent 0.9.38+).
const (
	TypeDockerList   = "docker.list"   // server → agent: send a DockerReport (no data)
	TypeDockerReport = "docker.report" // agent → server: DockerReport
	TypeDockerAction = "docker.action" // server → agent: DockerAction
	TypeDockerResult = "docker.result" // agent → server: DockerResult
)

type DockerContainer struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Image    string  `json:"image"`
	State    string  `json:"state"`  // running | exited | paused | restarting | created | dead
	Status   string  `json:"status"` // "Up 3 hours", "Exited (1) 2 minutes ago"
	Ports    string  `json:"ports"`
	Created  string  `json:"created"`
	Project  string  `json:"project,omitempty"` // docker compose project
	Service  string  `json:"service,omitempty"`
	CPU      float64 `json:"cpu"`    // percent of one core
	MemPct   float64 `json:"memPct"` // of the container's limit
	MemUsed  int64   `json:"memUsed"`
	MemLimit int64   `json:"memLimit"`
}

type DockerImage struct {
	ID       string `json:"id"`
	Repo     string `json:"repo"`
	Tag      string `json:"tag"`
	Size     int64  `json:"size"`
	Created  string `json:"created"`
	Dangling bool   `json:"dangling"`
	InUse    bool   `json:"inUse"`
}

type DockerVolume struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
	InUse  bool   `json:"inUse"`
}

type DockerProject struct {
	Name        string `json:"name"`
	Status      string `json:"status"` // "running(3)", "exited(1)"
	ConfigFiles string `json:"configFiles"`
}

type DockerReport struct {
	At         int64             `json:"at"`
	Available  bool              `json:"available"` // the docker CLI is installed
	Version    string            `json:"version,omitempty"`
	Error      string            `json:"error,omitempty"`
	Containers []DockerContainer `json:"containers"`
	Images     []DockerImage     `json:"images"`
	Volumes    []DockerVolume    `json:"volumes"`
	Projects   []DockerProject   `json:"projects"`
}

// DockerAction: Kind container (start | stop | restart | remove | logs | update),
// project (start | stop | restart | update), image (remove | prune) or volume (remove).
type DockerAction struct {
	RunID  string `json:"runId"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Action string `json:"action"`
}

type DockerResult struct {
	RunID  string `json:"runId"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type SSHLogin struct {
	TS     int64  `json:"ts"`
	User   string `json:"user"`
	IP     string `json:"ip"`
	Method string `json:"method"` // publickey | password | keyboard-interactive
	Key    string `json:"key,omitempty"`
}
