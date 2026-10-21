package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/docker"
)

func (s *server) dockerRoutes(mux *http.ServeMux) {
	failD := func(w http.ResponseWriter, err error) {
		if errors.Is(err, docker.ErrOldAgent) {
			writeError(w, http.StatusConflict, "old_agent", capitalize(err.Error())+".")
			return
		}
		fail(w, err)
	}
	// Private registry logins (owner only: under /api/integrations).
	mux.HandleFunc("GET /api/integrations/docker-registries", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, s.Docker.Registries.List())
	}))
	mux.HandleFunc("POST /api/integrations/docker-registries", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in docker.Registry
		if !decode(w, r, &in) {
			return
		}
		out, err := s.Docker.Registries.Save(in)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "docker.registry_save", out.Host, out.Username)
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("DELETE /api/integrations/docker-registries/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Docker.Registries.Delete(r.PathValue("id")); err != nil {
			if errors.Is(err, docker.ErrNoRegistry) {
				writeError(w, http.StatusNotFound, "not_found", "Registry login not found.")
				return
			}
			fail(w, err)
			return
		}
		s.audit(r, "docker.registry_delete", r.PathValue("id"), "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/servers/{id}/docker", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, s.Docker.State(r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/servers/{id}/docker/refresh", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Docker.Refresh(r.PathValue("id")); err != nil {
			failD(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "loading"})
	}))
	// Ask the registries again now whether the images have newer versions.
	mux.HandleFunc("POST /api/servers/{id}/docker/updates", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			s.Docker.CheckUpdates(ctx, true)
		}()
		w.WriteHeader(http.StatusAccepted)
	}))
	// action: start, stop, restart and update for operators; delete removes containers,
	// images and volumes, owner only (see access.go).
	act := func(destructive bool) func(http.ResponseWriter, *http.Request, auth.Session) {
		return func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			var in struct{ Kind, Target, Action string }
			if !decode(w, r, &in) {
				return
			}
			if docker.Destructive(in.Kind, in.Action) != destructive || in.Action == "logs" {
				writeError(w, http.StatusBadRequest, "invalid_input", "Use the other docker endpoint for this action.")
				return
			}
			id := r.PathValue("id")
			runID, err := s.Docker.Act(id, proto.DockerAction{Kind: in.Kind, Target: in.Target, Action: in.Action})
			if err != nil {
				failD(w, err)
				return
			}
			s.audit(r, "docker."+in.Action, id, in.Kind+" "+in.Target)
			writeJSON(w, http.StatusAccepted, map[string]string{"runId": runID})
		}
	}
	mux.HandleFunc("POST /api/servers/{id}/docker/action", s.authed(act(false)))
	mux.HandleFunc("POST /api/servers/{id}/docker/delete", s.authed(act(true)))
	mux.HandleFunc("GET /api/servers/{id}/docker/logs", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		res, err := s.Docker.Logs(ctx, r.PathValue("id"), r.URL.Query().Get("container"))
		if err != nil {
			failD(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}))
}
