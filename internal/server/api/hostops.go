package api

import (
	"errors"
	"net/http"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/disk"
	"fleetwatch/internal/server/fleet"
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
