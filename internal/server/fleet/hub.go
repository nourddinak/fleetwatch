package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/metrics"
)

const (
	helloTimeout = 10 * time.Second
	idleTimeout  = 45 * time.Second // no frame for this long = agent gone
	pingEvery    = 15 * time.Second
	keepPings    = 8
)

type Ping struct {
	TS int64 `json:"ts"`
	MS int64 `json:"ms"`
}

type agentConn struct {
	ws        *websocket.Conn
	cancel    context.CancelFunc
	transport string
	mu        sync.Mutex
	pings     []Ping
}

// Hub tracks live agent connections and turns their frames into stored state.
type Hub struct {
	Reg     *Registry
	Metrics *metrics.Store
	Bus     *live.Bus
	Version string                             // server version = latest agent version
	Config  func(serverID string) proto.Config // current agent config for one server

	// Optional hooks, set before serving.
	OnInventory   func(serverID string, inv proto.Inventory)
	OnLogs        func(serverID string, lines []proto.LogLine)
	OnMetrics     func(serverID string, m proto.Metrics)
	OnStateChange func(serverID string) // agent connected or disconnected
	OnConnect     func(serverID, agentVersion string)
	// Handlers receive other agent message types, keyed by Envelope.Type.
	Handlers map[string]func(serverID string, data json.RawMessage)

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	conns  map[string]*agentConn
}

func NewHub(reg *Registry, m *metrics.Store, bus *live.Bus, version string, config func(serverID string) proto.Config) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{Reg: reg, Metrics: m, Bus: bus, Version: version, Config: config, conns: map[string]*agentConn{}, ctx: ctx, cancel: cancel, Handlers: map[string]func(string, json.RawMessage){}}
}

// Context lives until Shutdown; agent connections run under it.
func (h *Hub) Context() context.Context { return h.ctx }

// Shutdown closes every agent connection (they reconnect to the restarted server).
func (h *Hub) Shutdown() {
	h.mu.Lock()
	list := make([]*agentConn, 0, len(h.conns))
	for _, c := range h.conns {
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		c.ws.Close(websocket.StatusGoingAway, "server restarting")
	}
	h.cancel()
}

// Connected reports whether the server's agent is connected right now.
func (h *Hub) Connected(serverID string) bool { return h.conn(serverID) != nil }

func (h *Hub) stateChanged(serverID string) {
	if h.OnStateChange != nil {
		h.OnStateChange(serverID)
	}
}

func (h *Hub) conn(serverID string) *agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[serverID]
}

// Serve runs one agent connection until it ends. It blocks.
func (h *Hub) Serve(ctx context.Context, ws *websocket.Conn, serverID, remoteIP, transport string) {
	ws.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	log := slog.With("server", serverID, "ip", remoteIP)

	hello, err := readHello(ctx, ws)
	if err != nil {
		log.Warn("agent handshake failed", "err", err)
		ws.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	rebooted, err := h.Reg.ApplyHello(serverID, hello, remoteIP)
	if err != nil {
		log.Error("store hello", "err", err)
		ws.Close(websocket.StatusInternalError, "")
		return
	}
	if rebooted {
		h.Reg.AddEvent(serverID, "reboot", "Server rebooted")
	}
	h.Reg.AddEvent(serverID, "agent", fmt.Sprintf("Agent v%s connected from %s", hello.AgentVersion, remoteIP))

	c := &agentConn{ws: ws, cancel: cancel, transport: transport}
	h.mu.Lock()
	old := h.conns[serverID]
	h.conns[serverID] = c
	h.mu.Unlock()
	if old != nil {
		old.cancel() // a second connection for the same server replaces the first
		old.ws.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
	log.Info("agent connected", "version", hello.AgentVersion)
	if h.OnConnect != nil {
		h.OnConnect(serverID, hello.AgentVersion)
	}

	defer func() {
		h.mu.Lock()
		current := h.conns[serverID] == c
		if current {
			delete(h.conns, serverID)
		}
		h.mu.Unlock()
		ws.CloseNow()
		if !current {
			return
		}
		h.Metrics.Flush(serverID)
		h.Reg.MarkDisconnected(serverID)
		h.Reg.AddEvent(serverID, "agent", "Agent disconnected")
		h.Publish(serverID)
		h.stateChanged(serverID)
		log.Info("agent disconnected")
	}()

	h.sendConfig(ctx, serverID, ws)
	h.Publish(serverID)
	h.stateChanged(serverID)
	go h.pinger(ctx, serverID, c)

	for {
		readCtx, cancelRead := context.WithTimeout(ctx, idleTimeout)
		_, data, err := ws.Read(readCtx)
		cancelRead()
		if err != nil {
			return
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			log.Warn("bad frame from agent", "err", err)
			continue
		}
		switch env.Type {
		case proto.TypeMetrics:
			var m proto.Metrics
			if err := json.Unmarshal(env.Data, &m); err != nil {
				continue
			}
			// Trust the server clock for storage: agent clocks may drift.
			m.TS = time.Now().UnixMilli()
			h.Metrics.Add(serverID, m.TS, metrics.FromProto(m).Values())
			h.Reg.RecordSample(serverID, m)
			h.Publish(serverID)
			if h.OnMetrics != nil {
				h.OnMetrics(serverID, m)
			}
		case proto.TypeInventory:
			var inv proto.Inventory
			if err := json.Unmarshal(env.Data, &inv); err != nil {
				log.Warn("bad inventory from agent", "err", err)
				continue
			}
			if h.OnInventory != nil {
				h.OnInventory(serverID, inv)
			}
		case proto.TypeLogs:
			var lines []proto.LogLine
			if err := json.Unmarshal(env.Data, &lines); err != nil {
				log.Warn("bad log batch from agent", "err", err)
				continue
			}
			if h.OnLogs != nil {
				h.OnLogs(serverID, lines)
			}
		default:
			if fn := h.Handlers[env.Type]; fn != nil {
				fn(serverID, env.Data)
			}
		}
	}
}

func readHello(ctx context.Context, ws *websocket.Conn) (proto.Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	var hello proto.Hello
	_, data, err := ws.Read(ctx)
	if err != nil {
		return hello, err
	}
	var env proto.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != proto.TypeHello {
		return hello, errors.New("first frame is not hello")
	}
	if err := json.Unmarshal(env.Data, &hello); err != nil {
		return hello, err
	}
	if hello.Protocol != proto.Protocol {
		return hello, fmt.Errorf("unsupported protocol %d", hello.Protocol)
	}
	return hello, nil
}

func (h *Hub) pinger(ctx context.Context, serverID string, c *agentConn) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		start := time.Now()
		err := c.ws.Ping(pctx)
		cancel()
		if err != nil {
			c.cancel()
			return
		}
		c.mu.Lock()
		c.pings = append(c.pings, Ping{TS: start.UnixMilli(), MS: time.Since(start).Milliseconds()})
		if len(c.pings) > keepPings {
			c.pings = c.pings[len(c.pings)-keepPings:]
		}
		c.mu.Unlock()
	}
}

func (h *Hub) sendConfig(ctx context.Context, serverID string, ws *websocket.Conn) {
	frame, _ := proto.Encode(proto.TypeConfig, "", h.Config(serverID))
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws.Write(wctx, websocket.MessageText, frame)
}

// BroadcastConfig pushes the current config to every connected agent.
func (h *Hub) BroadcastConfig() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.SendConfig(id)
	}
}

// SendConfig pushes the current config to one server's agent, if connected.
func (h *Hub) SendConfig(serverID string) {
	if c := h.conn(serverID); c != nil {
		h.sendConfig(context.Background(), serverID, c.ws)
	}
}

var ErrOffline = errors.New("agent is not connected")

// Send writes one frame to a connected agent.
func (h *Hub) Send(serverID, typ string, v any) error {
	c := h.conn(serverID)
	if c == nil {
		return ErrOffline
	}
	frame, err := proto.Encode(typ, "", v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, frame)
}

// Kick closes the server's agent connection, if any.
func (h *Hub) Kick(serverID string) {
	if c := h.conn(serverID); c != nil {
		c.ws.Close(websocket.StatusPolicyViolation, "credential revoked")
		c.cancel()
	}
}

// Publish sends the server's current view to browsers.
func (h *Hub) Publish(serverID string) {
	rec, err := h.Reg.Get(serverID)
	if err != nil {
		return
	}
	h.Bus.Publish("server", h.View(rec))
}

// Views returns every server as the UI sees it.
func (h *Hub) Views() ([]View, error) {
	recs, err := h.Reg.List()
	if err != nil {
		return nil, err
	}
	out := make([]View, len(recs))
	for i, r := range recs {
		out[i] = h.View(r)
	}
	return out, nil
}
