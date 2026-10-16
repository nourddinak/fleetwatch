package hostadmin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// FirewallState is what the dashboard shows of one server's firewall.
type FirewallState struct {
	Report   *proto.FirewallReport   `json:"report"`
	Scanning bool                    `json:"scanning"`
	Changing string                  `json:"changing,omitempty"` // run ID of a change on its way
	Last     *proto.HostActionResult `json:"last,omitempty"`
	since    time.Time
}

// UpdatesState is one server's waiting package updates.
type UpdatesState struct {
	Report     *proto.UpdatesReport    `json:"report"`
	Scanning   bool                    `json:"scanning"`
	Installing string                  `json:"installing,omitempty"` // "security" | "all" while running
	Last       *proto.HostActionResult `json:"last,omitempty"`
	LastBy     string                  `json:"lastBy,omitempty"`
	runID      string
	since      time.Time
}

func runID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) fw(id string) *FirewallState {
	st := s.firewall[id]
	if st == nil {
		st = &FirewallState{}
		s.firewall[id] = st
	}
	if (st.Scanning || st.Changing != "") && s.Now().Sub(st.since) > 3*time.Minute {
		st.Scanning, st.Changing = false, "" // the agent went away
	}
	return st
}

func (s *Service) up(id string) *UpdatesState {
	st := s.updates[id]
	if st == nil {
		st = &UpdatesState{}
		s.updates[id] = st
	}
	if st.Scanning && s.Now().Sub(st.since) > 10*time.Minute || st.Installing != "" && s.Now().Sub(st.since) > time.Hour {
		st.Scanning, st.Installing = false, ""
	}
	return st
}

func (s *Service) publishFW(id string, st FirewallState) {
	s.Bus.Publish("firewall", map[string]any{"serverId": id, "state": st})
}

func (s *Service) publishUp(id string, st UpdatesState) {
	s.Bus.Publish("updates", map[string]any{"serverId": id, "state": st})
}

// ---------- firewall ----------

func (s *Service) Firewall(serverID string) FirewallState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.fw(serverID)
}

func (s *Service) ScanFirewall(serverID string) error {
	if err := s.supported(serverID); err != nil {
		return err
	}
	s.mu.Lock()
	st := s.fw(serverID)
	if !st.Scanning {
		if err := s.Send(serverID, proto.TypeFirewallScan, nil); err != nil {
			s.mu.Unlock()
			return err
		}
		st.Scanning, st.since = true, s.Now()
	}
	cp := *st
	s.mu.Unlock()
	s.publishFW(serverID, cp)
	return nil
}

func (s *Service) ChangeFirewall(serverID string, c proto.FirewallChange) error {
	if err := s.supported(serverID); err != nil {
		return err
	}
	if !slices.Contains([]string{"allow", "deny", "limit", "delete", "enable", "disable"}, c.Action) {
		return valid.Error("unknown firewall change")
	}
	s.mu.Lock()
	st := s.fw(serverID)
	if st.Changing != "" {
		s.mu.Unlock()
		return valid.Error("another firewall change is still running on this server")
	}
	c.RunID = runID()
	if err := s.Send(serverID, proto.TypeFirewallChange, c); err != nil {
		s.mu.Unlock()
		return err
	}
	st.Changing, st.since = c.RunID, s.Now()
	cp := *st
	s.mu.Unlock()
	s.publishFW(serverID, cp)
	return nil
}

func (s *Service) OnFirewallReport(serverID string, data json.RawMessage) {
	var rep proto.FirewallReport
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	s.mu.Lock()
	st := s.fw(serverID)
	st.Report, st.Scanning = &rep, false
	cp := *st
	s.mu.Unlock()
	s.publishFW(serverID, cp)
}

func (s *Service) OnFirewallResult(serverID string, data json.RawMessage) {
	var res proto.HostActionResult
	if json.Unmarshal(data, &res) != nil {
		return
	}
	s.mu.Lock()
	st := s.fw(serverID)
	st.Changing, st.Last = "", &res
	cp := *st
	s.mu.Unlock()
	s.publishFW(serverID, cp)
}

// ---------- package updates ----------

// AutoUpdates is the "autoUpdates" settings section: install updates on every server in
// a weekly window.
type AutoUpdates struct {
	Enabled      bool   `json:"enabled"`
	SecurityOnly bool   `json:"securityOnly"`
	Days         []int  `json:"days"` // 0 = Sunday … 6 = Saturday; empty = every day
	Hour         int    `json:"hour"` // 0-23 in TimeZone
	TimeZone     string `json:"timeZone"`
	LastRun      string `json:"lastRun,omitempty"` // date (in TimeZone) of the last automatic run
}

func DefaultAutoUpdates() AutoUpdates {
	return AutoUpdates{SecurityOnly: true, Days: []int{}, Hour: 4, TimeZone: "UTC"}
}

func (a AutoUpdates) Validate() error {
	if a.Hour < 0 || a.Hour > 23 {
		return valid.Error("the hour is 0-23")
	}
	for _, d := range a.Days {
		if d < 0 || d > 6 {
			return valid.Error("days are 0 (Sunday) to 6 (Saturday)")
		}
	}
	if _, err := time.LoadLocation(a.TimeZone); err != nil || a.TimeZone == "" {
		return valid.Error("unknown time zone")
	}
	return nil
}

func (s *Service) AutoUpdates() AutoUpdates {
	a := DefaultAutoUpdates()
	if settings.Load(s.db, "autoUpdates", &a) != nil || a.Validate() != nil {
		return DefaultAutoUpdates()
	}
	if a.Days == nil {
		a.Days = []int{}
	}
	return a
}

func (s *Service) SaveAutoUpdates(a AutoUpdates) error {
	if a.Days == nil {
		a.Days = []int{}
	}
	if err := a.Validate(); err != nil {
		return err
	}
	a.LastRun = s.AutoUpdates().LastRun
	return settings.Save(s.db, "autoUpdates", a)
}

// Updates is every server's update state (key: server ID).
func (s *Service) Updates(serverIDs []string) map[string]UpdatesState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]UpdatesState{}
	for _, id := range serverIDs {
		out[id] = *s.up(id)
	}
	return out
}

func (s *Service) ScanUpdates(serverID string, refresh bool) error {
	if err := s.supported(serverID); err != nil {
		return err
	}
	s.mu.Lock()
	st := s.up(serverID)
	if !st.Scanning && st.Installing == "" {
		if err := s.Send(serverID, proto.TypeUpdatesScan, proto.UpdatesScan{Refresh: refresh}); err != nil {
			s.mu.Unlock()
			return err
		}
		st.Scanning, st.since = true, s.Now()
	}
	cp := *st
	s.mu.Unlock()
	s.publishUp(serverID, cp)
	return nil
}

// InstallUpdates starts installing updates; by is who asked ("schedule" for the window).
func (s *Service) InstallUpdates(serverID string, securityOnly bool, by string) error {
	if err := s.supported(serverID); err != nil {
		return err
	}
	s.mu.Lock()
	st := s.up(serverID)
	if st.Installing != "" {
		s.mu.Unlock()
		return valid.Error("updates are already being installed on this server")
	}
	id := runID()
	if err := s.Send(serverID, proto.TypeUpdatesInstall, proto.UpdatesInstall{RunID: id, SecurityOnly: securityOnly}); err != nil {
		s.mu.Unlock()
		return err
	}
	st.Installing, st.runID, st.since, st.LastBy = "all", id, s.Now(), by
	if securityOnly {
		st.Installing = "security"
	}
	cp := *st
	s.mu.Unlock()
	s.publishUp(serverID, cp)
	return nil
}

func (s *Service) OnUpdatesReport(serverID string, data json.RawMessage) {
	var rep proto.UpdatesReport
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	if rep.Packages == nil {
		rep.Packages = []proto.Package{}
	}
	s.mu.Lock()
	st := s.up(serverID)
	st.Report, st.Scanning = &rep, false
	cp := *st
	s.mu.Unlock()
	s.publishUp(serverID, cp)
}

func (s *Service) OnUpdatesResult(serverID string, data json.RawMessage, name func(string) string) {
	var res proto.HostActionResult
	if json.Unmarshal(data, &res) != nil {
		return
	}
	s.mu.Lock()
	st := s.up(serverID)
	kind := st.Installing
	st.Installing, st.Last = "", &res
	cp := *st
	s.mu.Unlock()
	s.publishUp(serverID, cp)
	if !res.OK {
		s.Notify("Updates failed on "+name(serverID), res.Error, "warning")
	} else if cp.LastBy == "schedule" {
		s.Notify(fmt.Sprintf("Installed %s updates on %s", map[string]string{"security": "security", "all": "all"}[kind], name(serverID)), "Automatic update window.", "info")
	}
}

// Tick runs the automatic update window: once a day, at the chosen hour, on every
// connected server.
func (s *Service) Tick(connected []string) {
	a := s.AutoUpdates()
	if !a.Enabled {
		return
	}
	loc, err := time.LoadLocation(a.TimeZone)
	if err != nil {
		return
	}
	now := s.Now().In(loc)
	today := now.Format("2006-01-02")
	if now.Hour() != a.Hour || a.LastRun == today || (len(a.Days) > 0 && !slices.Contains(a.Days, int(now.Weekday()))) {
		return
	}
	a.LastRun = today
	if settings.Save(s.db, "autoUpdates", a) != nil {
		return
	}
	for _, id := range connected {
		if s.supported(id) == nil {
			s.InstallUpdates(id, a.SecurityOnly, "schedule")
		}
	}
}
