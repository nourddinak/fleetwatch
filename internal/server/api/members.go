package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/whatsapp"
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
		id := r.PathValue("id")
		m, _ := s.Auth.GetMember(id)
		var wp whatsapp.Person
		reach := false
		if s.WhatsApp != nil {
			wp, reach = s.WhatsApp.PersonOf(id) // before they are gone
		}
		if err := s.Auth.DeleteMember(client(r), id); err != nil {
			failMember(w, err)
			return
		}
		if s.Mobile != nil {
			s.Mobile.RemoveMember(id) // their phones stop getting alerts
		}
		if s.WhatsApp != nil {
			s.WhatsApp.Forget(id) // the AI stops answering them; their waiting messages and reminders end
			if reach && m.Role != auth.RoleClient {
				owner := "The owner"
				if pr, err := s.Auth.Profile(); err == nil {
					owner = cmp.Or(pr.DisplayName, pr.Username)
				}
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := s.WhatsApp.Send(ctx, wp.Phone, "👋 "+owner+" removed you from the team on Fleetwatch. Your dashboard account is closed, and this number will not answer you or send you alerts anymore."); err != nil {
						slog.Warn("whatsapp: removed member", "err", err)
					}
				}()
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
