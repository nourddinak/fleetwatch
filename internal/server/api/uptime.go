package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"fleetwatch/internal/agent/caddy"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/uptime"
)

func (s *server) uptimeRoutes(mux *http.ServeMux) {
	failUp := func(w http.ResponseWriter, err error) {
		if errors.Is(err, uptime.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Uptime check not found.")
			return
		}
		fail(w, err)
	}
	mux.HandleFunc("GET /api/uptime", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		list, err := s.Uptime.Views()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("POST /api/uptime", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in uptime.Check
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Uptime.Create(in)
		if err != nil {
			failUp(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	}))
	mux.HandleFunc("PUT /api/uptime/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in uptime.Check
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Uptime.Update(r.PathValue("id"), in)
		if err != nil {
			failUp(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}))
	mux.HandleFunc("DELETE /api/uptime/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Uptime.Delete(r.PathValue("id")); err != nil {
			failUp(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/uptime/{id}/check", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Uptime.CheckNow(r.PathValue("id")); err != nil {
			failUp(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "checking"})
	}))
	mux.HandleFunc("GET /api/uptime/{id}/history", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		h, err := s.Uptime.History(r.PathValue("id"), r.URL.Query().Get("range"), r.URL.Query().Get("location"))
		if err != nil {
			failUp(w, err)
			return
		}
		writeJSON(w, http.StatusOK, h)
	}))
	// suggestions: the sites the dashboard knows about (Caddy, app domains) that have no check yet.
	mux.HandleFunc("GET /api/uptime/suggestions", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		type suggestion struct {
			Name     string `json:"name"`
			URL      string `json:"url"`
			ServerID string `json:"serverId"`
		}
		out := []suggestion{}
		views, _ := s.Uptime.Views()
		have := func(host string) bool {
			return slices.ContainsFunc(views, func(v uptime.View) bool { return strings.Contains(v.URL, "//"+host) })
		}
		if s.Apps != nil {
			for _, d := range s.Apps.Domains() {
				if caddy.PublicName(d.Host) && !have(d.Host) {
					out = append(out, suggestion{Name: d.Host, URL: "https://" + d.Host, ServerID: d.ServerID})
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))
}
