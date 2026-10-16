// Package disk asks agents what fills their server's disk and runs the safe cleanups
// they offer. A report is requested by hand, or by itself when a disk passes 90%.
package disk

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/release"
)

// MinAgent is the first agent version that can do this.
const MinAgent = "v0.9.31"

var (
	ErrOldAgent = errors.New("this server's agent is too old for disk cleanup; update the agent first")
	ErrBusy     = errors.New("a disk scan or cleanup is already running on this server")
)

// State is what the dashboard shows for one server.
type State struct {
	Report   *proto.DiskReport  `json:"report"`
	Scanning bool               `json:"scanning"`
	Cleaning string             `json:"cleaning,omitempty"` // action running
	Last     *proto.DiskCleaned `json:"last,omitempty"`     // last cleanup result
	since    time.Time          // when the running scan or cleanup started
}

type Service struct {
	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string
	Bus          *live.Bus
	Now          func() time.Time

	mu    sync.Mutex
	state map[string]*State
}

func New(bus *live.Bus) *Service {
	return &Service{Bus: bus, Now: time.Now, state: map[string]*State{}}
}

func (s *Service) get(id string) *State {
	if s.state[id] == nil {
		s.state[id] = &State{}
	}
	st := s.state[id]
	if (st.Scanning || st.Cleaning != "") && s.Now().Sub(st.since) > 15*time.Minute {
		st.Scanning, st.Cleaning = false, "" // the agent never answered (it went away)
	}
	return st
}

func (s *Service) State(serverID string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.get(serverID)
}

func (s *Service) publish(serverID string, st State) {
	if s.Bus != nil {
		s.Bus.Publish("disk", map[string]any{"serverId": serverID, "state": st})
	}
}

func (s *Service) supported(serverID string) error {
	if s.AgentVersion != nil && release.Newer(MinAgent, s.AgentVersion(serverID)) {
		return ErrOldAgent
	}
	return nil
}

// Scan asks the agent for a fresh report.
func (s *Service) Scan(serverID string) error {
	if err := s.supported(serverID); err != nil {
		return err
	}
	s.mu.Lock()
	st := s.get(serverID)
	if st.Scanning || st.Cleaning != "" {
		s.mu.Unlock()
		return ErrBusy
	}
	if err := s.Send(serverID, proto.TypeDiskScan, nil); err != nil {
		s.mu.Unlock()
		return err
	}
	st.Scanning, st.since = true, s.Now()
	cp := *st
	s.mu.Unlock()
	s.publish(serverID, cp)
	return nil
}

// Clean runs one of the cleanups from the report.
func (s *Service) Clean(serverID, action string) error {
	if err := s.supported(serverID); err != nil {
		return err
	}
	s.mu.Lock()
	st := s.get(serverID)
	if st.Scanning || st.Cleaning != "" {
		s.mu.Unlock()
		return ErrBusy
	}
	b := make([]byte, 8)
	rand.Read(b)
	if err := s.Send(serverID, proto.TypeDiskClean, proto.DiskClean{RunID: hex.EncodeToString(b), Action: action}); err != nil {
		s.mu.Unlock()
		return err
	}
	st.Cleaning, st.since = action, s.Now()
	cp := *st
	s.mu.Unlock()
	s.publish(serverID, cp)
	return nil
}

// OnReport and OnCleaned take the agent's answers.
func (s *Service) OnReport(serverID string, data json.RawMessage) {
	var rep proto.DiskReport
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	s.mu.Lock()
	st := s.get(serverID)
	st.Report, st.Scanning = &rep, false
	cp := *st
	s.mu.Unlock()
	s.publish(serverID, cp)
}

func (s *Service) OnCleaned(serverID string, data json.RawMessage) {
	var res proto.DiskCleaned
	if json.Unmarshal(data, &res) != nil {
		return
	}
	s.mu.Lock()
	st := s.get(serverID)
	st.Cleaning, st.Last = "", &res
	if st.Report != nil { // show the item as emptied until the next scan
		rep := *st.Report
		rep.Items = append([]proto.DiskItem(nil), rep.Items...)
		for i := range rep.Items {
			if rep.Items[i].ID == res.Action {
				rep.Items[i].Bytes = max(rep.Items[i].Bytes-res.Freed, 0)
			}
		}
		st.Report = &rep
	}
	cp := *st
	s.mu.Unlock()
	s.publish(serverID, cp)
}

// Watch starts a scan by itself when a disk passes 90% and the last report is older
// than six hours, so the report is ready when someone looks.
func (s *Service) Watch(serverID string, diskPct float64) {
	if diskPct < 90 || s.supported(serverID) != nil {
		return
	}
	s.mu.Lock()
	st := s.get(serverID)
	fresh := st.Report != nil && s.Now().Sub(time.UnixMilli(st.Report.At)) < 6*time.Hour
	busy := st.Scanning || st.Cleaning != ""
	s.mu.Unlock()
	if !fresh && !busy {
		s.Scan(serverID)
	}
}
