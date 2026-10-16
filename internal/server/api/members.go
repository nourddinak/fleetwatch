package api

import (
	"errors"
	"net/http"

	"fleetwatch/internal/server/auth"
)

// memberRoutes: the owner manages the team (the /api/members prefix is owner-only).
func (s *server) memberRoutes(mux *http.ServeMux) {
	failMember := func(w http.ResponseWriter, err error) {
		if errors.Is(err, auth.ErrNoMember) {
			writeError(w, http.StatusNotFound, "not_found", "Team member not found.")
			return
		}
		fail(w, err)
	}
	mux.HandleFunc("GET /api/members", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		list, err := s.Auth.ListMembers()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("POST /api/members", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in auth.MemberInput
		if !decode(w, r, &in) {
			return
		}
		m, err := s.Auth.CreateMember(client(r), in)
		if err != nil {
			failMember(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, m)
	}))
	mux.HandleFunc("PUT /api/members/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in auth.MemberInput
		if !decode(w, r, &in) {
			return
		}
		m, err := s.Auth.UpdateMember(client(r), r.PathValue("id"), in)
		if err != nil {
			failMember(w, err)
			return
		}
		writeJSON(w, http.StatusOK, m)
	}))
	mux.HandleFunc("DELETE /api/members/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Auth.DeleteMember(client(r), r.PathValue("id")); err != nil {
			failMember(w, err)
			return
		}
		if s.Mobile != nil {
			s.Mobile.RemoveMember(r.PathValue("id")) // their phones stop getting alerts
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
