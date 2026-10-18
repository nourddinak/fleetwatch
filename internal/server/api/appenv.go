package api

import (
	"net/http"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/valid"
)

// appEnvRoutes: an app's .env files, owner only, with the password asked again for
// every read and write (they hold the app's secrets).
func (s *server) appEnvRoutes(mux *http.ServeMux) {
	handle := func(write bool) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			var in struct{ Password, Name, Content string }
			if !decode(w, r, &in) {
				return
			}
			if in.Password == "" {
				writeError(w, http.StatusForbidden, "password_required", "Enter your password to open .env files.")
				return
			}
			if err := s.Auth.ConfirmPassword(client(r), in.Password, "app.env"); err != nil {
				fail(w, err)
				return
			}
			a, err := s.Apps.Get(r.PathValue("id"))
			if err != nil {
				fail(w, err)
				return
			}
			if a.Path == "" {
				fail(w, valid.Error("this app has no folder on the server"))
				return
			}
			if in.Name == "" && !write {
				in.Name = ".env"
			}
			if s.EnvFiles == nil {
				fail(w, valid.Error(".env files are not available"))
				return
			}
			res, err := s.EnvFiles.Do(r.Context(), a.ServerID, proto.EnvFile{Dir: a.Path, Name: in.Name, Write: write, Content: in.Content})
			if err != nil {
				fail(w, err)
				return
			}
			action := "app.env.read"
			if write {
				action = "app.env.write"
			}
			s.audit(r, action, a.ID, a.Name+": "+in.Name) // never the content
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, res)
		})
	}
	mux.HandleFunc("POST /api/apps/{id}/env", handle(false)) // POST: the password goes in the body
	mux.HandleFunc("PUT /api/apps/{id}/env", handle(true))
}
