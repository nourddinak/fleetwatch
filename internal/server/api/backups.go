package api

import (
	"errors"
	"net/http"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/backups"
)

// Backups: owner only (/api/backups is in ownerOnly).
func (s *server) backupRoutes(mux *http.ServeMux) {
	failB := func(w http.ResponseWriter, err error) {
		if errors.Is(err, backups.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Not found.")
			return
		}
		fail(w, err)
	}
	mux.HandleFunc("GET /api/backups", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, map[string]any{"dests": s.Backups.Dests(), "plans": s.Backups.Plans(), "runs": s.Backups.Runs(nil),
			"snapshots": s.Backups.Snapshots(), "found": s.Backups.Found()})
	}))
	saveDest := func(w http.ResponseWriter, r *http.Request, id string) {
		var in backups.Dest
		if !decode(w, r, &in) {
			return
		}
		in.ID = id
		d, err := s.Backups.SaveDest(in)
		if err != nil {
			failB(w, err)
			return
		}
		s.audit(r, "backups.dest_save", d.ID, d.Name)
		writeJSON(w, http.StatusOK, d)
	}
	mux.HandleFunc("POST /api/backups/dests", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { saveDest(w, r, "") }))
	mux.HandleFunc("PUT /api/backups/dests/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { saveDest(w, r, r.PathValue("id")) }))
	mux.HandleFunc("DELETE /api/backups/dests/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Backups.DeleteDest(r.PathValue("id")); err != nil {
			failB(w, err)
			return
		}
		s.audit(r, "backups.dest_delete", r.PathValue("id"), "")
		w.WriteHeader(http.StatusNoContent)
	}))
	// The repositories' password: with it (and the storage's keys) the copies can be
	// restored with plain restic, without Fleetwatch.
	mux.HandleFunc("GET /api/backups/dests/{id}/password", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		pw, err := s.Backups.Password(r.PathValue("id"))
		if err != nil {
			failB(w, err)
			return
		}
		s.audit(r, "backups.password_shown", r.PathValue("id"), "")
		writeJSON(w, http.StatusOK, map[string]string{"password": pw})
	}))
	savePlan := func(w http.ResponseWriter, r *http.Request, id string) {
		var in backups.Plan
		if !decode(w, r, &in) {
			return
		}
		in.ID = id
		if id == "" {
			if _, err := s.Fleet.Reg.Get(in.ServerID); err != nil {
				writeError(w, http.StatusNotFound, "not_found", "Server not found.")
				return
			}
		}
		p, err := s.Backups.SavePlan(in)
		if err != nil {
			failB(w, err)
			return
		}
		s.audit(r, "backups.plan_save", p.ServerID, p.Name)
		writeJSON(w, http.StatusOK, p)
	}
	mux.HandleFunc("POST /api/backups/plans", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { savePlan(w, r, "") }))
	mux.HandleFunc("PUT /api/backups/plans/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { savePlan(w, r, r.PathValue("id")) }))
	mux.HandleFunc("DELETE /api/backups/plans/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Backups.DeletePlan(r.PathValue("id")); err != nil {
			failB(w, err)
			return
		}
		s.audit(r, "backups.plan_delete", r.PathValue("id"), "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/backups/servers/{server}/scan", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Backups.Scan(r.PathValue("server")); err != nil {
			failB(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "scanning"})
	}))
	mux.HandleFunc("POST /api/backups/plans/{id}/run", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		id, err := s.Backups.RunNow(r.PathValue("id"), true)
		if err != nil {
			failB(w, err)
			return
		}
		s.audit(r, "backups.run", r.PathValue("id"), "")
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
	}))
	mux.HandleFunc("POST /api/backups/plans/{id}/refresh", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Backups.Refresh(r.PathValue("id")); err != nil {
			failB(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "listing"})
	}))
	mux.HandleFunc("POST /api/backups/plans/{id}/restore", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct {
			Snapshot string `json:"snapshot"`
		}
		if !decode(w, r, &in) {
			return
		}
		id, err := s.Backups.Restore(r.PathValue("id"), in.Snapshot)
		if err != nil {
			failB(w, err)
			return
		}
		s.audit(r, "backups.restore", r.PathValue("id"), in.Snapshot)
		writeJSON(w, http.StatusAccepted, map[string]string{"runId": id})
	}))
}
