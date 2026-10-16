package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/commands"
)

func (s *server) commandRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/apps/{id}/commands", s.authed(s.listCommands))
	mux.HandleFunc("POST /api/apps/{id}/commands", s.authed(s.createCommand))
	mux.HandleFunc("GET /api/commands/{cmd}", s.authed(s.getCommand))
	mux.HandleFunc("PUT /api/commands/{cmd}", s.authed(s.updateCommand))
	mux.HandleFunc("DELETE /api/commands/{cmd}", s.authed(s.deleteCommand))
	mux.HandleFunc("POST /api/apps/{id}/commands/{cmd}/run", s.authed(s.runCommand))
	mux.HandleFunc("GET /api/apps/{id}/runs", s.authed(s.listRuns))
	mux.HandleFunc("GET /api/runs/{run}", s.authed(s.getRun))
	mux.HandleFunc("PUT /api/apps/{id}/health", s.authed(s.putHealth))
	mux.HandleFunc("POST /api/apps/{id}/health/check", s.authed(s.checkHealthNow))
	mux.HandleFunc("GET /api/apps/{id}/health/history", s.authed(s.healthHistory))
	mux.HandleFunc("GET /api/apps/{id}/storage", s.authed(s.getStorage))
	mux.HandleFunc("POST /api/apps/{id}/storage/scan", s.authed(s.scanStorage))
}

const masked = "••••••"

// maskEnv hides values: command environments often hold tokens.
func maskEnv(env []string) []string {
	out := make([]string, len(env))
	for i, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		out[i] = k + "=" + masked
	}
	return out
}

func (s *server) listCommands(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	list, err := s.Commands.List(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type commandBody struct {
	commands.Input
	Password string `json:"password"`
}

func (s *server) createCommand(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in commandBody
	if !decode(w, r, &in) {
		return
	}
	// Defining what may run on a server is the sensitive step: confirm the password.
	if err := s.Auth.ConfirmPassword(client(r), in.Password, "command.create"); err != nil {
		fail(w, err)
		return
	}
	c, err := s.Commands.Create(r.PathValue("id"), in.Input)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "command.create", c.ID, c.AppID+": "+c.Name+" → "+c.Command)
	c.Env = maskEnv(c.Env)
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) getCommand(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	c, err := s.Commands.Get(r.PathValue("cmd"))
	if err != nil {
		fail(w, err)
		return
	}
	c.Env = maskEnv(c.Env)
	writeJSON(w, http.StatusOK, c)
}

func (s *server) updateCommand(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in commandBody
	if !decode(w, r, &in) {
		return
	}
	if err := s.Auth.ConfirmPassword(client(r), in.Password, "command.update"); err != nil {
		fail(w, err)
		return
	}
	old, err := s.Commands.Get(r.PathValue("cmd"))
	if err != nil {
		fail(w, err)
		return
	}
	// "KEY=••••••" means "keep the stored value".
	prev := map[string]string{}
	for _, kv := range old.Env {
		k, _, _ := strings.Cut(kv, "=")
		prev[k] = kv
	}
	for i, kv := range in.Env {
		if k, v, _ := strings.Cut(kv, "="); v == masked && prev[k] != "" {
			in.Env[i] = prev[k]
		}
	}
	c, err := s.Commands.Update(old.ID, in.Input)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "command.update", c.ID, c.AppID+": "+c.Name+" → "+c.Command)
	c.Env = maskEnv(c.Env)
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteCommand(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	c, err := s.Commands.Delete(r.PathValue("cmd"))
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "command.delete", c.ID, c.AppID+": "+c.Name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) runCommand(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ Password string }
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	if s.Auth.Security().ReauthForCommands {
		if in.Password == "" {
			writeError(w, http.StatusForbidden, "password_required", "Enter your password to run commands.")
			return
		}
		if err := s.Auth.ConfirmPassword(client(r), in.Password, "command.run"); err != nil {
			fail(w, err)
			return
		}
	}
	p, _ := s.Auth.Profile()
	run, err := s.Commands.Start(r.PathValue("id"), r.PathValue("cmd"), p.Username+" from "+clientIP(r))
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "command.run", run.AppID, run.Name+": "+run.Preview)
	if s.Detector != nil {
		s.Detector.Intended(run.AppID, time.Now())
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *server) listRuns(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	runs, err := s.Commands.History(r.PathValue("id"), 50)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *server) getRun(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	run, err := s.Commands.GetRun(r.PathValue("run"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *server) putHealth(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in apps.HealthView
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Apps.SetHealth(r.PathValue("id"), in)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "app.health", v.ID, in.URL)
	writeJSON(w, http.StatusOK, v)
}

func (s *server) checkHealthNow(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	v, err := s.Apps.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.Fleet.Send(v.ServerID, proto.TypeHealthNow, v.ID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}

func (s *server) healthHistory(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 120
	}
	h, err := s.Apps.HealthHistory(r.PathValue("id"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *server) getStorage(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	if _, err := s.Apps.Get(id); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scan": s.Apps.Storage(id)})
}

func (s *server) scanStorage(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	serverID, target, err := s.Apps.StorageTarget(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if target.Path == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "The application has no working directory to measure.")
		return
	}
	if err := s.Fleet.Send(serverID, proto.TypeStorageScan, target); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}
