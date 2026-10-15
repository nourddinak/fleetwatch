package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/maintenance"
)

func (s *server) maintenanceList(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, s.Maint.Snapshot())
}

// maintenanceSet starts (minutes > 0, or -1 for "until turned off") or ends (0) a
// maintenance window for a server or an app.
func (s *server) maintenanceSet(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct {
		Kind    string
		ID      string
		Minutes int
	}
	if !decode(w, r, &in) {
		return
	}
	until, err := s.Maint.Set(in.Kind, in.ID, in.Minutes)
	switch {
	case errors.Is(err, maintenance.ErrKind):
		writeError(w, http.StatusBadRequest, "invalid_input", "Kind must be server or app.")
		return
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "No such "+in.Kind+".")
		return
	case err != nil:
		fail(w, err)
		return
	}
	detail := "ended"
	if in.Minutes == -1 {
		detail = "until turned off"
	} else if in.Minutes > 0 {
		detail = fmt.Sprintf("%d minutes", in.Minutes)
	}
	s.audit(r, "maintenance."+in.Kind, in.ID, detail)
	writeJSON(w, http.StatusOK, map[string]int64{"until": until})
}
