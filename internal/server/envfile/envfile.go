// Package envfile asks an app's agent for its .env files and waits for the answer.
package envfile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/valid"
)

// MinAgent is the first agent that answers TypeEnvFile; CaddyAgent the first that
// opens Caddyfiles.
const (
	MinAgent   = "v0.9.64"
	CaddyAgent = "v0.9.68"
	WebAgent   = "v0.9.70" // Caddy and Nginx configuration by name
)

type Service struct {
	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string

	mu      sync.Mutex
	waiting map[string]chan proto.EnvFileResult
}

func New() *Service { return &Service{waiting: map[string]chan proto.EnvFileResult{}} }

// Do sends the request to the server's agent and waits up to 15 seconds.
func (s *Service) Do(ctx context.Context, serverID string, req proto.EnvFile) (proto.EnvFileResult, error) {
	if min := MinAgent; s.AgentVersion != nil {
		if req.Caddy || req.Name == "Caddyfile" {
			min = CaddyAgent
		}
		if req.Web != "" || req.Name == "nginx.conf" {
			min = WebAgent
		}
		if release.Newer(min, s.AgentVersion(serverID)) {
			return proto.EnvFileResult{}, valid.Error("this server's agent is too old for this; update the agent first")
		}
	}
	b := make([]byte, 8)
	rand.Read(b)
	req.RunID = hex.EncodeToString(b)
	ch := make(chan proto.EnvFileResult, 1)
	s.mu.Lock()
	s.waiting[req.RunID] = ch // before sending: a fast agent may answer first
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, req.RunID)
		s.mu.Unlock()
	}()
	if err := s.Send(serverID, proto.TypeEnvFile, req); err != nil {
		return proto.EnvFileResult{}, valid.Error("the server is offline")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case res := <-ch:
		if res.Error != "" {
			return res, valid.Error(res.Error)
		}
		if res.Files == nil {
			res.Files = []string{}
		}
		return res, nil
	case <-ctx.Done():
		return proto.EnvFileResult{}, valid.Error("the agent did not answer in time")
	}
}

// OnResult is the hub handler for TypeEnvFileResult.
func (s *Service) OnResult(_ string, data json.RawMessage) {
	var res proto.EnvFileResult
	if json.Unmarshal(data, &res) != nil {
		return
	}
	s.mu.Lock()
	ch := s.waiting[res.RunID]
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- res:
		default:
		}
	}
}
