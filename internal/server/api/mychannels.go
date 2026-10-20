package api

import (
	"net/http"

	"fleetwatch/internal/server/auth"
)

// myChannelRoutes: everyone's own alert channels (Integrations, for team members).
func (s *server) myChannelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/channels", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		writeJSON(w, http.StatusOK, s.Notify.PersonalChannels(sess.MemberID))
	}))
	mux.HandleFunc("PUT /api/me/channels/{provider}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct {
			Enabled bool
			Fields  map[string]string
		}
		if !decode(w, r, &in) {
			return
		}
		if err := s.Notify.SavePersonal(sess.MemberID, r.PathValue("provider"), in.Enabled, in.Fields); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.Notify.PersonalChannels(sess.MemberID))
	}))
	mux.HandleFunc("POST /api/me/channels/{provider}/test", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if err := s.Notify.TestPersonal(r.Context(), sess.MemberID, r.PathValue("provider")); err != nil {
			writeError(w, http.StatusBadRequest, "failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	}))
}
