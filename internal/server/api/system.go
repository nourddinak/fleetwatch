package api

import (
	"errors"
	"net/http"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/release"
)

// updateStatus answers from the last check and, if that is a few minutes old, starts
// a new one in the background; a newer release then arrives over the live socket.
func (s *server) updateStatus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	s.Release.Refresh()
	writeJSON(w, http.StatusOK, s.Release.Status())
}

func (s *server) updateCheck(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	s.Release.Check(r.Context()) // a failure is reported in the status
	writeJSON(w, http.StatusOK, s.Release.Status())
}

// updateApply asks the root helper to install a newer release; ops-server restarts
// when it is done and the UI reloads once /api/health reports the new version.
func (s *server) updateApply(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ Version string }
	if !decode(w, r, &in) {
		return
	}
	err := s.Release.Request(in.Version)
	switch {
	case errors.Is(err, release.ErrNoHelper), errors.Is(err, release.ErrNotNewer), errors.Is(err, release.ErrUnavailable):
		writeError(w, http.StatusConflict, "cannot_update", capitalize(err.Error())+".")
		return
	case err != nil:
		fail(w, err)
		return
	}
	s.audit(r, "system.update", in.Version, "from "+s.Version)
	writeJSON(w, http.StatusAccepted, s.Release.Status())
}
