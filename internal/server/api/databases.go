package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
)

// dbUnlocked: sessions that entered their password for the databases, until when. Each
// use moves the time on, so it closes after unlockFor without use.
var (
	dbUnlockMu sync.Mutex
	dbUnlocked = map[string]time.Time{}
)

const unlockFor = 15 * time.Minute

func dbOpen(sess auth.Session) bool {
	dbUnlockMu.Lock()
	defer dbUnlockMu.Unlock()
	if until, ok := dbUnlocked[sess.ID]; ok && time.Now().Before(until) {
		dbUnlocked[sess.ID] = time.Now().Add(unlockFor)
		return true
	}
	delete(dbUnlocked, sess.ID)
	return false
}

// The database tool: owner only (dbPath in access.go), reads included, and only after
// the password is entered again (unlock).
func (s *server) databaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/db/unlock", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ Password string }
		if !decode(w, r, &in) {
			return
		}
		if in.Password == "" {
			writeError(w, http.StatusForbidden, "password_required", "Enter your password to open the databases.")
			return
		}
		if err := s.Auth.ConfirmPassword(client(r), in.Password, "db.open"); err != nil {
			fail(w, err)
			return
		}
		dbUnlockMu.Lock()
		dbUnlocked[sess.ID] = time.Now().Add(unlockFor)
		dbUnlockMu.Unlock()
		s.audit(r, "db.unlock", "", "")
		writeJSON(w, http.StatusOK, map[string]int{"minutes": int(unlockFor / time.Minute)})
	}))
	authed := func(h authedHandler) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
			if !dbOpen(sess) {
				writeError(w, http.StatusForbidden, "password_required", "Enter your password to open the databases.")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			h(w, r, sess)
		})
	}
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
	mux.HandleFunc("GET /api/servers/{id}/db", authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ask(w, r, proto.DBRequest{Op: "list"})
	}))
	mux.HandleFunc("GET /api/servers/{id}/db/tables", authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ask(w, r, proto.DBRequest{Op: "tables", Engine: r.URL.Query().Get("engine"), Database: r.URL.Query().Get("db")})
	}))
	mux.HandleFunc("POST /api/servers/{id}/db/query", authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
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
	mux.HandleFunc("POST /api/servers/{id}/db/create", authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
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
