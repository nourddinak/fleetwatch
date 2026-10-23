package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/geo"
)

// Country rules: owner only (/api/geo is in ownerOnly).
func (s *server) geoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/geo", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		me := map[string]string{"ip": clientIP(r)}
		if s.Security != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			me["country"] = s.Security.CountryOf(ctx, me["ip"])
			cancel()
		}
		writeJSON(w, http.StatusOK, map[string]any{"rules": s.Geo.Rules(), "zones": s.Geo.Zones(), "status": s.Geo.Status(), "me": me})
	}))
	mux.HandleFunc("POST /api/geo/rules", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in geo.Rule
		if !decode(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		out, err := s.Geo.SaveRule(ctx, in)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "geo.rule_add", "", out.Mode+" "+strings.Join(out.Countries, ","))
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("DELETE /api/geo/rules/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Geo.DeleteRule(r.PathValue("id")); err != nil {
			if errors.Is(err, geo.ErrNone) {
				writeError(w, http.StatusNotFound, "not_found", "Rule not found.")
				return
			}
			fail(w, err)
			return
		}
		s.audit(r, "geo.rule_delete", r.PathValue("id"), "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/geo/zones", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in geo.Zone
		if !decode(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.Geo.SaveZone(ctx, in); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "geo.zone", in.Zone, in.Mode+" "+strings.Join(in.Countries, ","))
		writeJSON(w, http.StatusOK, s.Geo.Zones())
	}))
}
