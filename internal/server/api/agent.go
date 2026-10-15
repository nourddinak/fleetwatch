package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/agentbin"
	"fleetwatch/internal/server/fleet"
)

// Agent-facing routes. They are outside /api: agents authenticate with their own
// credential, not a browser session, and send no Origin header.
func (s *server) agentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /install.sh", s.installScript)
	mux.HandleFunc("GET /uninstall.sh", s.uninstallScript)
	mux.HandleFunc("GET /agent/download/{file}", s.downloadAgent)
	mux.HandleFunc("POST /agent/enroll", s.enrollAgent)
	mux.HandleFunc("GET /agent/ws", s.agentWS)
}

func (s *server) installScript(w http.ResponseWriter, r *http.Request) {
	if s.PublicURL == "" && !validHost(r.Host) {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	body, err := agentbin.InstallScript(s.publicURL(r))
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

func (s *server) uninstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Write(agentbin.UninstallScript())
}

func (s *server) downloadAgent(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	platform, wantSum := strings.CutSuffix(file, ".sha256")
	if wantSum {
		sum, ok := agentbin.SHA256(platform)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(sum + "\n"))
		return
	}
	bin, ok := agentbin.Binary(platform)
	if !ok {
		http.Error(w, "no agent binary for "+platform+" in this build", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="ops-agent"`)
	w.Write(bin)
}

func (s *server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var in proto.EnrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	serverID, cred, err := s.Fleet.Reg.Enroll(in.Token)
	if errors.Is(err, fleet.ErrBadToken) {
		writeError(w, http.StatusUnauthorized, "bad_token", "Enrollment token is invalid, used or expired.")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	s.Fleet.Kick(serverID) // an older agent for this server is now revoked
	s.Fleet.Publish(serverID)
	writeJSON(w, http.StatusOK, proto.EnrollResponse{ServerID: serverID, Credential: cred})
}

func (s *server) agentWS(w http.ResponseWriter, r *http.Request) {
	cred, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "missing agent credential")
		return
	}
	serverID, err := s.Fleet.Reg.Authenticate(cred)
	if errors.Is(err, fleet.ErrBadCred) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "agent credential is invalid or revoked")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	transport := "ws"
	if isTLS(r) {
		transport = "wss"
	}
	// Not r.Context(): hijacked connections outlive it; the hub closes them on shutdown.
	s.Fleet.Serve(s.Fleet.Context(), c, serverID, clientIP(r), transport)
}

func wsWriteJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, b)
}
