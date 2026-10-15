package fleet

import (
	"math"

	"fleetwatch/internal/server/metrics"
)

// View is a server as the UI shows it (field names match web/src/mock/types.ts Server).
type View struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Hostname           string     `json:"hostname"`
	IP                 string     `json:"ip"`
	PrivateIPs         []string   `json:"privateIps"`
	Provider           string     `json:"provider"`
	OS                 string     `json:"os"`
	Kernel             string     `json:"kernel"`
	Arch               string     `json:"arch"`
	CPUCores           int        `json:"cpuCores"`
	RAMGB              float64    `json:"ramGb"`
	SwapGB             float64    `json:"swapGb"`
	DiskGB             float64    `json:"diskGb"`
	CPU                float64    `json:"cpu"`
	RAM                float64    `json:"ram"`
	Swap               float64    `json:"swap"`
	Disk               float64    `json:"disk"`
	NetRx              float64    `json:"netRx"` // Mbps
	NetTx              float64    `json:"netTx"`
	Load               [3]float64 `json:"load"`
	UptimeSec          uint64     `json:"uptimeSec"`
	Status             string     `json:"status"` // pending | online | warning | critical | disconnected
	AgentVersion       string     `json:"agentVersion"`
	LatestAgentVersion string     `json:"latestAgentVersion"`
	LastSeen           int64      `json:"lastSeen"`
	ConnectedSince     int64      `json:"connectedSince"`
	CreatedAt          int64      `json:"createdAt"`
	LatencyMs          int64      `json:"latencyMs"`
	Pings              []Ping     `json:"pings"`
	Notes              string     `json:"notes"`
	NotifyOverride     string     `json:"notifyOverride"`
	Capabilities       []string   `json:"capabilities"`
	Enrolled           bool       `json:"enrolled"`
	Transport          string     `json:"transport"`
}

const gb = 1 << 30

func r1(v float64) float64 { return math.Round(v*10) / 10 }
func r2(v float64) float64 { return math.Round(v*100) / 100 }

func (h *Hub) View(x Record) View {
	v := View{
		ID: x.ID, Name: x.Name, Hostname: x.Hostname, IP: x.PublicIP, PrivateIPs: x.IPs, Provider: x.Provider,
		OS: x.OS, Kernel: x.Kernel, Arch: x.Arch, CPUCores: x.CPUCores,
		RAMGB: r1(float64(x.MemTotal) / gb), SwapGB: r1(float64(x.SwapTotal) / gb), DiskGB: r1(float64(x.DiskTotal) / gb),
		AgentVersion: x.AgentVersion, LatestAgentVersion: h.Version, LastSeen: x.LastSeenAt, ConnectedSince: x.ConnectedSince,
		CreatedAt: x.CreatedAt, Notes: x.Notes, NotifyOverride: x.NotifyOverride, Capabilities: x.Capabilities,
		Enrolled: x.EnrolledAt != 0, Pings: []Ping{},
	}
	if v.PrivateIPs == nil {
		v.PrivateIPs = []string{}
	}
	if v.Capabilities == nil {
		v.Capabilities = []string{}
	}
	if v.IP == "" && len(x.IPs) > 0 {
		v.IP = x.IPs[0]
	}
	if m := x.LastMetrics; m != nil {
		s := metrics.FromProto(*m)
		v.CPU, v.RAM, v.Swap, v.Disk = r1(s.CPU), r1(s.Mem), r1(s.Swap), r1(s.Disk)
		v.NetRx, v.NetTx = r2(s.NetRx*8/1e6), r2(s.NetTx*8/1e6)
		v.Load = [3]float64{r2(m.Load1), r2(m.Load5), r2(m.Load15)}
		v.UptimeSec = m.Uptime
	}
	c := h.conn(x.ID)
	switch {
	case !v.Enrolled:
		v.Status = "pending"
	case c == nil:
		v.Status = "disconnected"
	default:
		v.Status = healthStatus(v)
		v.Transport = c.transport
		c.mu.Lock()
		v.Pings = append(v.Pings, c.pings...)
		c.mu.Unlock()
		if n := len(v.Pings); n > 0 {
			v.LatencyMs = v.Pings[n-1].MS
		}
	}
	return v
}

// healthStatus grades a connected server by its latest resources.
// shortcut: fixed thresholds; M6 moves these to the configurable incident rules.
func healthStatus(v View) string {
	switch {
	case v.Disk >= 95 || v.RAM >= 95:
		return "critical"
	case v.CPU >= 90 || v.RAM >= 85 || v.Disk >= 85:
		return "warning"
	}
	return "online"
}
