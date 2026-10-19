package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	mux.HandleFunc("POST /api/members/{id}/reset-2fa", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Auth.ResetMember2FA(client(r), r.PathValue("id")); err != nil {
			failMember(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	// Sends a member where and how to sign in, on WhatsApp (and saves their number).
	mux.HandleFunc("POST /api/members/{id}/invite", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Phone, Password string }
		if !decode(w, r, &in) {
			return
		}
		m, err := s.Auth.GetMember(r.PathValue("id"))
		if err != nil {
			failMember(w, err)
			return
		}
		if s.WhatsApp == nil || !s.WhatsApp.Linked() {
			writeError(w, http.StatusConflict, "not_linked", "Link WhatsApp first (Integrations → WhatsApp).")
			return
		}
		p, _ := s.WhatsApp.PersonOf(m.ID)
		if strings.TrimSpace(in.Phone) != "" {
			p.Phone = in.Phone
			if p, err = s.WhatsApp.SetPerson(m.ID, p); err != nil {
				fail(w, err)
				return
			}
		}
		if p.Phone == "" {
			writeError(w, http.StatusBadRequest, "no_phone", "Their WhatsApp number, with the country code.")
			return
		}
		owner := "The owner"
		if pr, err := s.Auth.Profile(); err == nil {
			owner = cmp.Or(pr.DisplayName, pr.Username)
		}
		text := fmt.Sprintf("👋 %s added you to Fleetwatch, the team's server dashboard.\nSign in: %s\nUsername: %s", owner, s.publicURL(r), m.Username)
		if in.Password != "" {
			text += "\nPassword: " + in.Password + "\n(Change it after signing in: Settings → Account.)"
		} else {
			text += "\nYour password: ask " + owner + "."
		}
		text += "\nYou can also call or write to this number to ask the AI about the servers."
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.WhatsApp.Send(ctx, p.Phone, text); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "member.invite", m.ID, "sign-in details sent on WhatsApp")
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
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
