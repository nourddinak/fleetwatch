package api

import (
	"log/slog"
	"net/http"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/envfile"
	"fleetwatch/internal/server/valid"
)

type envIn struct {
	Password, Name, Content string
	ID                      int64
}

// appEnvRoutes: an app's .env files and their history, owner only, with the password
// asked again for every request (they hold the app's secrets).
func (s *server) appEnvRoutes(mux *http.ServeMux) {
	// unlock checks the password and finds the app; it answers the request itself on failure.
	unlock := func(w http.ResponseWriter, r *http.Request, in *envIn) (apps.View, bool) {
		if !decode(w, r, in) {
			return apps.View{}, false
		}
		if in.Password == "" {
			writeError(w, http.StatusForbidden, "password_required", "Enter your password to open .env files.")
			return apps.View{}, false
		}
		if err := s.Auth.ConfirmPassword(client(r), in.Password, "app.env"); err != nil {
			fail(w, err)
			return apps.View{}, false
		}
		a, err := s.Apps.Get(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return a, false
		}
		if a.Path == "" {
			fail(w, valid.Error("this app has no folder on the server"))
			return a, false
		}
		if s.EnvFiles == nil {
			fail(w, valid.Error(".env files are not available"))
			return a, false
		}
		w.Header().Set("Cache-Control", "no-store")
		return a, true
	}
	file := func(write bool) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			var in envIn
			a, ok := unlock(w, r, &in)
			if !ok {
				return
			}
			if in.Name == "" && !write {
				in.Name = ".env"
			}
			req := proto.EnvFile{Dir: a.Path, Name: in.Name}
			if write && s.EnvHistory != nil {
				// What is on the server now, in case it was changed outside Fleetwatch.
				if cur, err := s.EnvFiles.Do(r.Context(), a.ServerID, req); err == nil && cur.Exists {
					s.EnvHistory.Add(a.ID, in.Name, envfile.OutsideFleetwatch, cur.Content)
				}
			}
			req.Write, req.Content = write, in.Content
			res, err := s.EnvFiles.Do(r.Context(), a.ServerID, req)
			if err != nil {
				fail(w, err)
				return
			}
			action := "app.env.read"
			if write {
				action = "app.env.write"
				if s.EnvHistory != nil {
					if err := s.EnvHistory.Add(a.ID, in.Name, s.actor(r), in.Content); err != nil {
						slog.Warn("env history", "app", a.ID, "err", err)
					}
				}
			}
			s.audit(r, action, a.ID, a.Name+": "+in.Name) // never the content
			writeJSON(w, http.StatusOK, res)
		})
	}
	mux.HandleFunc("POST /api/apps/{id}/env", file(false)) // POST: the password goes in the body
	mux.HandleFunc("PUT /api/apps/{id}/env", file(true))

	mux.HandleFunc("POST /api/apps/{id}/env/history", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in envIn
		a, ok := unlock(w, r, &in)
		if !ok || s.EnvHistory == nil {
			return
		}
		list, err := s.EnvHistory.List(a.ID, in.Name)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("DELETE /api/apps/{id}/env/history", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in envIn
		a, ok := unlock(w, r, &in)
		if !ok || s.EnvHistory == nil {
			return
		}
		n, err := s.EnvHistory.Delete(a.ID, in.Name, in.ID)
		if err != nil {
			fail(w, err)
			return
		}
		what := "all versions"
		if in.ID > 0 {
			what = "one version"
		}
		s.audit(r, "app.env.history.delete", a.ID, a.Name+": "+in.Name+", "+what)
		writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
	}))
}
