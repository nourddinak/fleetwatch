// Package docker shows each server's containers, compose stacks, images and volumes,
// and passes the actions people take on them to the server's agent.
package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/valid"
)

// MinAgent is the first agent version with Docker support.
const MinAgent = "v0.9.38"

var ErrOldAgent = errors.New("this server's agent is too old for Docker; update the agent first")

// State is what the dashboard shows for one server.
type State struct {
	Report  *proto.DockerReport  `json:"report"`
	Loading bool                 `json:"loading"`
	Running []proto.DockerAction `json:"running"` // actions not finished yet
	since   time.Time
}

type Service struct {
	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string
	Bus          *live.Bus
	Now          func() time.Time

	mu      sync.Mutex
	state   map[string]*State
	waiting map[string]chan proto.DockerResult // by run ID
	started map[string]time.Time               // run ID → when it was sent
}

func New(bus *live.Bus) *Service {
	return &Service{Bus: bus, Now: time.Now, state: map[string]*State{}, waiting: map[string]chan proto.DockerResult{}, started: map[string]time.Time{}}
}

func (s *Service) get(id string) *State {
	if s.state[id] == nil {
		s.state[id] = &State{Running: []proto.DockerAction{}}
	}
	st := s.state[id]
	if st.Loading && s.Now().Sub(st.since) > 2*time.Minute {
		st.Loading = false // the agent never answered
	}
	if len(st.Running) > 0 { // nor for an action (it went away): updates take 15 minutes at most
		kept := []proto.DockerAction{}
		for _, a := range st.Running {
			if s.Now().Sub(s.started[a.RunID]) < 20*time.Minute {
				kept = append(kept, a)
			} else {
				delete(s.started, a.RunID)
			}
		}
		st.Running = kept
	}
	return st
}

func (s *Service) State(serverID string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.get(serverID)
}

func (s *Service) publish(serverID string, st State) {
	s.Bus.Publish("docker", map[string]any{"serverId": serverID, "state": st})
}

func (s *Service) supported(serverID string) error {
	if s.AgentVersion != nil && release.Newer(MinAgent, s.AgentVersion(serverID)) {
		return ErrOldAgent
	}
	return nil
}

// Refresh asks the agent for a new list, unless one is on its way.
func (s *Service) Refresh(serverID string) error {
	if err := s.supported(serverID); err != nil {
		return err
	}
	s.mu.Lock()
	st := s.get(serverID)
	if st.Loading {
		s.mu.Unlock()
		return nil
	}
	if err := s.Send(serverID, proto.TypeDockerList, nil); err != nil {
		s.mu.Unlock()
		return err
	}
	st.Loading, st.since = true, s.Now()
	cp := *st
	s.mu.Unlock()
	s.publish(serverID, cp)
	return nil
}

var actions = map[string][]string{
	"container": {"start", "stop", "restart", "remove", "logs", "update"},
	"project":   {"start", "stop", "restart", "update"},
	"image":     {"remove", "prune"},
	"volume":    {"remove"},
}

// Destructive reports whether an action deletes something (only the owner may).
func Destructive(kind, action string) bool {
	return action == "remove" || action == "prune"
}

// Act sends an action to the agent and returns its run ID; the result arrives on the
// live socket ("docker.result").
func (s *Service) Act(serverID string, a proto.DockerAction) (string, error) {
	if err := s.supported(serverID); err != nil {
		return "", err
	}
	ok := false
	for _, x := range actions[a.Kind] {
		ok = ok || x == a.Action
	}
	if !ok || (a.Target == "" && a.Action != "prune") || len(a.Target) > 255 {
		return "", valid.Error("unknown docker action")
	}
	b := make([]byte, 8)
	rand.Read(b)
	a.RunID = hex.EncodeToString(b)
	if err := s.Send(serverID, proto.TypeDockerAction, a); err != nil {
		return "", err
	}
	if a.Action != "logs" {
		s.mu.Lock()
		st := s.get(serverID)
		st.Running = append(st.Running, a)
		s.started[a.RunID] = s.Now()
		cp := *st
		s.mu.Unlock()
		s.publish(serverID, cp)
	}
	return a.RunID, nil
}

// Logs asks for a container's last lines and waits for them.
func (s *Service) Logs(ctx context.Context, serverID, container string) (proto.DockerResult, error) {
	ch := make(chan proto.DockerResult, 1)
	runID, err := s.actWaiting(serverID, proto.DockerAction{Kind: "container", Target: container, Action: "logs"}, ch)
	defer func() {
		s.mu.Lock()
		delete(s.waiting, runID)
		s.mu.Unlock()
	}()
	if err != nil {
		return proto.DockerResult{}, err
	}
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return proto.DockerResult{}, valid.Error("the agent did not answer in time")
	}
}

func (s *Service) actWaiting(serverID string, a proto.DockerAction, ch chan proto.DockerResult) (string, error) {
	// Register before sending: a fast agent may answer before Act returns.
	b := make([]byte, 8)
	rand.Read(b)
	pre := hex.EncodeToString(b)
	s.mu.Lock()
	s.waiting[pre] = ch
	s.mu.Unlock()
	if err := s.supported(serverID); err != nil {
		return pre, err
	}
	a.RunID = pre
	return pre, s.Send(serverID, proto.TypeDockerAction, a)
}

// OnReport and OnResult take the agent's answers.
func (s *Service) OnReport(serverID string, data json.RawMessage) {
	var rep proto.DockerReport
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	s.mu.Lock()
	st := s.get(serverID)
	st.Report, st.Loading = &rep, false
	cp := *st
	s.mu.Unlock()
	s.publish(serverID, cp)
}

func (s *Service) OnResult(serverID string, data json.RawMessage) {
	var res proto.DockerResult
	if json.Unmarshal(data, &res) != nil {
		return
	}
	s.mu.Lock()
	if ch, ok := s.waiting[res.RunID]; ok {
		ch <- res
		s.mu.Unlock()
		return
	}
	st := s.get(serverID)
	running := st.Running[:0]
	for _, a := range st.Running {
		if a.RunID != res.RunID {
			running = append(running, a)
		}
	}
	st.Running = running
	delete(s.started, res.RunID)
	cp := *st
	s.mu.Unlock()
	s.publish(serverID, cp)
	s.Bus.Publish("docker.result", map[string]any{"serverId": serverID, "result": res})
}
