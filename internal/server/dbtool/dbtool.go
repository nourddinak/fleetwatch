// Package dbtool asks a server's agent about its databases and waits for the answer.
package dbtool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"sync"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/valid"
)

// Agent is the first agent version with the database tool.
const Agent = "v0.9.156"

var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type Service struct {
	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string

	mu      sync.Mutex
	waiting map[string]chan proto.DBResult
}

func New() *Service { return &Service{waiting: map[string]chan proto.DBResult{}} }

// Ask sends a request and waits for the agent's answer (the agent stops at 30 seconds).
func (s *Service) Ask(ctx context.Context, serverID string, req proto.DBRequest) (proto.DBResult, error) {
	if s.AgentVersion != nil && release.Newer(Agent, s.AgentVersion(serverID)) {
		return proto.DBResult{}, valid.Error("this server's agent is too old for the database tool; update the agent first")
	}
	if s.Send == nil {
		return proto.DBResult{}, valid.Error("the server is not connected")
	}
	b := make([]byte, 8)
	rand.Read(b)
	req.RunID = hex.EncodeToString(b)
	ch := make(chan proto.DBResult, 1)
	s.mu.Lock()
	s.waiting[req.RunID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, req.RunID)
		s.mu.Unlock()
	}()
	if err := s.Send(serverID, proto.TypeDBRequest, req); err != nil {
		return proto.DBResult{}, valid.Error("the server is not connected")
	}
	select {
	case res := <-ch:
		if res.Error != "" {
			return res, valid.Error(res.Error)
		}
		return res, nil
	case <-ctx.Done():
		return proto.DBResult{}, errors.New("the server did not answer in time")
	}
}

func (s *Service) OnResult(serverID string, data json.RawMessage) {
	var res proto.DBResult
	if json.Unmarshal(data, &res) != nil {
		return
	}
	s.mu.Lock()
	ch, ok := s.waiting[res.RunID]
	s.mu.Unlock()
	if ok {
		ch <- res
	}
}

// Created is a new database and the login for it, shown once.
type Created struct {
	Engine   string `json:"engine"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	URL      string `json:"url"` // as apps on the same server connect
}

// Create makes a database and a user of the same name that owns it, with a new password.
func (s *Service) Create(ctx context.Context, serverID, engine, name string) (Created, error) {
	if !nameRe.MatchString(name) {
		return Created{}, valid.Error("the name is lowercase letters, digits and _, starting with a letter (like shop or shop_db)")
	}
	if engine != "postgres" && engine != "mysql" {
		return Created{}, valid.Error("pick PostgreSQL or MySQL")
	}
	pw := password(24)
	if _, err := s.Ask(ctx, serverID, proto.DBRequest{Op: "create", Engine: engine, Database: name, User: name, Password: pw}); err != nil {
		return Created{}, err
	}
	c := Created{Engine: engine, Database: name, User: name, Password: pw}
	if engine == "postgres" {
		c.URL = "postgresql://" + name + ":" + pw + "@localhost:5432/" + name
	} else {
		c.URL = "mysql://" + name + ":" + pw + "@localhost:3306/" + name
	}
	return c, nil
}

// password is letters and digits only: nothing to quote in SQL or URLs.
func password(n int) string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[k.Int64()]
	}
	return string(b)
}
