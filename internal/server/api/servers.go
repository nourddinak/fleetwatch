package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/agentbin"
	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/metrics"
)

func (s *server) serverRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/servers", s.authed(s.listServers))
	mux.HandleFunc("POST /api/servers", s.authed(s.createServer))
	mux.HandleFunc("GET /api/servers/{id}", s.authed(s.getServer))
	mux.HandleFunc("PATCH /api/servers/{id}", s.authed(s.patchServer))
	mux.HandleFunc("DELETE /api/servers/{id}", s.authed(s.deleteServer))
	mux.HandleFunc("POST /api/servers/{id}/enroll-token", s.authed(s.newEnrollToken))
	mux.HandleFunc("POST /api/servers/{id}/revoke", s.authed(s.revokeAgent))
	mux.HandleFunc("POST /api/servers/{id}/restart-agent", s.authed(s.restartAgent))
	mux.HandleFunc("POST /api/servers/{id}/update-agent", s.authed(s.updateAgent))
	mux.HandleFunc("GET /api/settings/agents", s.authed(s.getAgentSettings))
	mux.HandleFunc("PUT /api/settings/agents", s.authed(s.putAgentSettings))
	mux.HandleFunc("GET /api/servers/{id}/events", s.authed(s.serverEvents))
	mux.HandleFunc("GET /api/metrics", s.authed(s.queryMetrics))
	mux.HandleFunc("GET /api/settings/metrics", s.authed(s.getMetricsSettings))
	mux.HandleFunc("PUT /api/settings/metrics", s.authed(s.putMetricsSettings))
	mux.HandleFunc("GET /ws", s.authed(s.browserWS))
}

func (s *server) audit(r *http.Request, action, target, detail string) {
	audit.Record(s.DB, audit.Event{Action: action, OK: true, IP: clientIP(r), UserAgent: r.UserAgent(), Target: target, Detail: detail})
}

// publicURL is the base URL agents use to reach this server.
func (s *server) publicURL(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "http"
	if isTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// validHost guards the Host header before it is written into install.sh.
func validHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c)) {
			return false
		}
	}
	return true
}

type enrollInfo struct {
	Token          string `json:"token"`
	InstallCommand string `json:"installCommand"`
	ExpiresAt      int64  `json:"expiresAt"`
}

func (s *server) enrollInfo(r *http.Request, token string) enrollInfo {
	return enrollInfo{
		Token:          token,
		InstallCommand: "curl -fsSL " + s.publicURL(r) + "/install.sh | sudo bash -s -- " + token,
		ExpiresAt:      time.Now().Add(fleet.EnrollTokenTTL).UnixMilli(),
	}
}

func (s *server) listServers(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	views, err := s.Fleet.Views()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *server) getServer(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	rec, err := s.Fleet.Reg.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Fleet.View(rec))
}

func (s *server) createServer(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ Name, Provider string }
	if !decode(w, r, &in) {
		return
	}
	if s.PublicURL == "" && !validHost(r.Host) {
		writeError(w, http.StatusBadRequest, "invalid_input", "Invalid Host header; set OPS_PUBLIC_URL.")
		return
	}
	id, token, err := s.Fleet.Reg.Create(in.Name, in.Provider)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "server.create", id, in.Name)
	s.Fleet.Publish(id)
	rec, _ := s.Fleet.Reg.Get(id)
	writeJSON(w, http.StatusCreated, map[string]any{"server": s.Fleet.View(rec), "enroll": s.enrollInfo(r, token)})
}

func (s *server) patchServer(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var p fleet.Patch
	if !decode(w, r, &p) {
		return
	}
	id := r.PathValue("id")
	if err := s.Fleet.Reg.Update(id, p); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "server.update", id, "")
	s.Fleet.Publish(id)
	s.getServer(w, r, auth.Session{})
}

func (s *server) deleteServer(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	rec, err := s.Fleet.Reg.Get(id)
	if err != nil {
		fail(w, err)
		return
	}
	s.Fleet.Reg.RevokeCredentials(id)
	s.Fleet.Kick(id)
	s.Metrics.Forget(id)
	if err := s.Fleet.Reg.Delete(id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "server.delete", id, rec.Name)
	s.Bus.Publish("server.removed", map[string]string{"id": id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) newEnrollToken(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	if s.PublicURL == "" && !validHost(r.Host) {
		writeError(w, http.StatusBadRequest, "invalid_input", "Invalid Host header; set OPS_PUBLIC_URL.")
		return
	}
	token, err := s.Fleet.Reg.NewEnrollToken(id)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "server.enroll_token", id, "")
	writeJSON(w, http.StatusOK, s.enrollInfo(r, token))
}

func (s *server) revokeAgent(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	if _, err := s.Fleet.Reg.Get(id); err != nil {
		fail(w, err)
		return
	}
	if err := s.Fleet.Reg.RevokeCredentials(id); err != nil {
		fail(w, err)
		return
	}
	s.Fleet.Kick(id)
	s.audit(r, "server.revoke", id, "")
	s.Fleet.Publish(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) restartAgent(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	if err := s.Fleet.Send(id, proto.TypeRestart, nil); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "server.restart_agent", id, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// updateAgent asks the agent to install the agent build embedded in this server.
func (s *server) updateAgent(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	if err := s.sendAgentUpdate(id, "requested"); err != nil {
		if errors.Is(err, errNoAgentBuild) {
			writeError(w, http.StatusConflict, "no_agent_build", "This server build contains no agent binaries.")
			return
		}
		fail(w, err)
		return
	}
	s.audit(r, "server.update_agent", id, s.Version)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

var errNoAgentBuild = errors.New("no agent binaries in this build")

func (s *server) sendAgentUpdate(id, why string) error {
	u, ok := agentbin.UpdateMessage(s.Version)
	if !ok {
		return errNoAgentBuild
	}
	if err := s.Fleet.Send(id, proto.TypeUpdate, u); err != nil {
		return err
	}
	s.Fleet.Reg.AddEvent(id, "agent", "Update to v"+s.Version+" "+why)
	return nil
}

func (s *server) getAgentSettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, fleet.LoadAgentSettings(s.DB))
}

// putAgentSettings saves the agent settings; turning auto-update on also updates the
// outdated agents that are connected right now.
func (s *server) putAgentSettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in fleet.AgentSettings
	if !decode(w, r, &in) {
		return
	}
	if err := fleet.SaveAgentSettings(s.DB, in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "settings.agents", "", fmt.Sprintf("autoUpdate=%v", in.AutoUpdate))
	if in.AutoUpdate {
		if views, err := s.Fleet.Views(); err == nil {
			for _, v := range views {
				if s.Fleet.Connected(v.ID) && v.AgentVersion != "" && v.AgentVersion != s.Version {
					s.sendAgentUpdate(v.ID, "started automatically (auto-update turned on)")
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *server) serverEvents(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	ev, err := s.Fleet.Reg.Events(r.PathValue("id"), 30)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) queryMetrics(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	q := r.URL.Query()
	rows, err := s.Metrics.Rows(q.Get("server"), q.Get("range"), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics.ServerPoints(rows))
}

func (s *server) getMetricsSettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, metrics.LoadSettings(s.DB))
}

func (s *server) putMetricsSettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in metrics.Settings
	if !decode(w, r, &in) {
		return
	}
	if err := metrics.SaveSettings(s.DB, in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "settings.metrics", "", "")
	s.Fleet.BroadcastConfig()
	writeJSON(w, http.StatusOK, in)
}

// browserWS streams live updates. It starts with a snapshot of every server, then
// forwards bus messages. The session is re-checked every minute so a revoked or
// expired session stops receiving data.
func (s *server) browserWS(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	token := cookieToken(r)
	c, err := websocket.Accept(w, r, nil) // nil options = same-origin only
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	sub := s.Bus.Subscribe()
	defer s.Bus.Unsubscribe(sub)

	views, err := s.Fleet.Views()
	if err != nil {
		return
	}
	if err := wsWriteJSON(ctx, c, map[string]any{"type": "servers", "data": views}); err != nil {
		return
	}
	check := time.NewTicker(time.Minute)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			if sess, err := s.Auth.Authenticate(token); err != nil || sess.MFAPending {
				c.Close(websocket.StatusPolicyViolation, "session ended")
				return
			}
		case msg := <-sub.C:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
