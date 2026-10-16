package api

import (
	"errors"
	"net/http"
	"strconv"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/disk"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/security"
)

// hostRoutes: disk rescue and forecasts.
func (s *server) hostRoutes(mux *http.ServeMux) {
	known := func(w http.ResponseWriter, id string) bool {
		if _, err := s.Fleet.Reg.Get(id); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Server not found.")
			return false
		}
		return true
	}
	diskFail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, disk.ErrOldAgent), errors.Is(err, disk.ErrBusy):
			writeError(w, http.StatusConflict, "conflict", capitalize(err.Error())+".")
		case errors.Is(err, fleet.ErrOffline):
			writeError(w, http.StatusConflict, "offline", "The agent is not connected.")
		default:
			fail(w, err)
		}
	}
	if s.Disk != nil {
		mux.HandleFunc("GET /api/servers/{id}/disk", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			if known(w, r.PathValue("id")) {
				writeJSON(w, http.StatusOK, s.Disk.State(r.PathValue("id")))
			}
		}))
		mux.HandleFunc("POST /api/servers/{id}/disk/scan", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			id := r.PathValue("id")
			if !known(w, id) {
				return
			}
			if err := s.Disk.Scan(id); err != nil {
				diskFail(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, s.Disk.State(id))
		}))
		mux.HandleFunc("POST /api/servers/{id}/disk/clean", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			id := r.PathValue("id")
			var in struct{ Action string }
			if !known(w, id) || !decode(w, r, &in) {
				return
			}
			if err := s.Disk.Clean(id, in.Action); err != nil {
				diskFail(w, err)
				return
			}
			s.audit(r, "disk.clean", id, in.Action)
			writeJSON(w, http.StatusAccepted, s.Disk.State(id))
		}))
	}
	if s.Forecast != nil {
		mux.HandleFunc("GET /api/forecasts", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			writeJSON(w, http.StatusOK, s.Forecast.Last())
		}))
	}
}

// securityRoutes: findings, scans, expected ports and SSH logins.
func (s *server) securityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/security", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, s.Security.Reports())
	}))
	mux.HandleFunc("POST /api/servers/{id}/security/scan", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		id := r.PathValue("id")
		if _, err := s.Fleet.Reg.Get(id); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Server not found.")
			return
		}
		if err := s.Security.Scan(id); err != nil {
			switch {
			case errors.Is(err, security.ErrOldAgent):
				writeError(w, http.StatusConflict, "conflict", capitalize(err.Error())+".")
			case errors.Is(err, fleet.ErrOffline):
				writeError(w, http.StatusConflict, "offline", "The agent is not connected.")
			default:
				fail(w, err)
			}
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "scanning"})
	}))
	mux.HandleFunc("PUT /api/security/expected", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct {
			ServerID string
			Port     int
			Expected bool
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Port <= 0 || in.Port > 65535 {
			writeError(w, http.StatusBadRequest, "invalid_input", "Port must be 1-65535.")
			return
		}
		if err := s.Security.SetExpected(in.ServerID, in.Port, in.Expected); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "security.expected_port", in.ServerID, strconv.Itoa(in.Port))
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/security/logins", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 1000 {
			limit = 300
		}
		list, err := s.Security.Logins(q.Get("server"), q.Get("new") == "1", limit)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
}
