package api

import (
	"context"
	"net/http"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
)

// The database tool: owner only (dbPath in access.go), reads included.
func (s *server) databaseRoutes(mux *http.ServeMux) {
	ask := func(w http.ResponseWriter, r *http.Request, req proto.DBRequest) {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		res, err := s.Databases.Ask(ctx, r.PathValue("id"), req)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
	mux.HandleFunc("GET /api/servers/{id}/db", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ask(w, r, proto.DBRequest{Op: "list"})
	}))
	mux.HandleFunc("GET /api/servers/{id}/db/tables", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ask(w, r, proto.DBRequest{Op: "tables", Engine: r.URL.Query().Get("engine"), Database: r.URL.Query().Get("db")})
	}))
	mux.HandleFunc("POST /api/servers/{id}/db/query", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Engine, Database, Query string }
		if !decode(w, r, &in) {
			return
		}
		q := in.Query
		if len(q) > 300 {
			q = q[:300] + "…"
		}
		s.audit(r, "db.query", r.PathValue("id"), in.Engine+" "+in.Database+": "+q)
		ask(w, r, proto.DBRequest{Op: "query", Engine: in.Engine, Database: in.Database, Query: in.Query})
	}))
	mux.HandleFunc("POST /api/servers/{id}/db/create", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Engine, Name string }
		if !decode(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		c, err := s.Databases.Create(ctx, r.PathValue("id"), in.Engine, in.Name)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "db.create", r.PathValue("id"), in.Engine+" "+in.Name)
		writeJSON(w, http.StatusOK, c)
	}))
}
