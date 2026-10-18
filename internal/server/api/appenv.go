package api

import (
	"log/slog"
	"net/http"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/envfile"
	"fleetwatch/internal/server/valid"
)

type envIn struct {
	Password, Name, Content string
	ID                      int64
}

// fileTarget is whose file it is: an app (.env files, its Caddyfile) or a server (its
// main Caddyfile).
type fileTarget struct {
	id, label, serverID string
	req                 proto.EnvFile
	hist                *envfile.History
}

// appEnvRoutes: secret and config files, owner only, with the password asked again
// for every request: an app's .env files and Caddyfile, a server's Caddyfile.
func (s *server) appEnvRoutes(mux *http.ServeMux) {
	app := func(r *http.Request, in *envIn) (fileTarget, error) {
		a, err := s.Apps.Get(r.PathValue("id"))
		if err != nil {
			return fileTarget{}, err
		}
		if a.Path == "" {
			return fileTarget{}, valid.Error("this app has no folder on the server")
		}
		return fileTarget{id: a.ID, label: a.Name, serverID: a.ServerID, req: proto.EnvFile{Dir: a.Path, Name: in.Name}, hist: s.EnvHistory}, nil
	}
	srv := func(r *http.Request, in *envIn) (fileTarget, error) {
		rec, err := s.Fleet.Reg.Get(r.PathValue("id"))
		if err != nil {
			return fileTarget{}, err
		}
		in.Name = "Caddyfile"
		return fileTarget{id: rec.ID, label: rec.Name, serverID: rec.ID, req: proto.EnvFile{Caddy: true, Name: in.Name}, hist: s.ServerFileHistory}, nil
	}
	// unlock checks the password and finds the target; it answers the request itself on failure.
	unlock := func(w http.ResponseWriter, r *http.Request, in *envIn, find func(*http.Request, *envIn) (fileTarget, error)) (fileTarget, bool) {
		if !decode(w, r, in) {
			return fileTarget{}, false
		}
		if in.Password == "" {
			writeError(w, http.StatusForbidden, "password_required", "Enter your password to open this file.")
			return fileTarget{}, false
		}
		if err := s.Auth.ConfirmPassword(client(r), in.Password, "file.open"); err != nil {
			fail(w, err)
			return fileTarget{}, false
		}
		t, err := find(r, in)
		if err == nil && s.EnvFiles == nil {
			err = valid.Error("files are not available")
		}
		if err != nil {
			fail(w, err)
			return t, false
		}
		w.Header().Set("Cache-Control", "no-store")
		return t, true
	}
	file := func(write bool, find func(*http.Request, *envIn) (fileTarget, error)) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			var in envIn
			t, ok := unlock(w, r, &in, find)
			if !ok {
				return
			}
			if t.req.Name == "" && !write {
				t.req.Name, in.Name = ".env", ".env"
			}
			if write && t.hist != nil {
				// What is on the server now, in case it was changed outside Fleetwatch.
				if cur, err := s.EnvFiles.Do(r.Context(), t.serverID, t.req); err == nil && cur.Exists {
					t.hist.Add(t.id, in.Name, envfile.OutsideFleetwatch, cur.Content)
				}
			}
			t.req.Write, t.req.Content = write, in.Content
			res, err := s.EnvFiles.Do(r.Context(), t.serverID, t.req)
			if err != nil {
				fail(w, err)
				return
			}
			action := "file.read"
			if write {
				action = "file.write"
				if t.hist != nil {
					if err := t.hist.Add(t.id, in.Name, s.actor(r), in.Content); err != nil {
						slog.Warn("file history", "target", t.id, "err", err)
					}
				}
			}
			s.audit(r, action, t.id, t.label+": "+in.Name) // never the content
			writeJSON(w, http.StatusOK, res)
		})
	}
	history := func(find func(*http.Request, *envIn) (fileTarget, error)) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			var in envIn
			t, ok := unlock(w, r, &in, find)
			if !ok || t.hist == nil {
				return
			}
			list, err := t.hist.List(t.id, in.Name)
			if err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, list)
		})
	}
	forget := func(find func(*http.Request, *envIn) (fileTarget, error)) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			var in envIn
			t, ok := unlock(w, r, &in, find)
			if !ok || t.hist == nil {
				return
			}
			n, err := t.hist.Delete(t.id, in.Name, in.ID)
			if err != nil {
				fail(w, err)
				return
			}
			what := "all versions"
			if in.ID > 0 {
				what = "one version"
			}
			s.audit(r, "file.history.delete", t.id, t.label+": "+in.Name+", "+what)
			writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
		})
	}
	// POST for reads: the password goes in the body.
	mux.HandleFunc("POST /api/apps/{id}/env", file(false, app))
	mux.HandleFunc("PUT /api/apps/{id}/env", file(true, app))
	mux.HandleFunc("POST /api/apps/{id}/env/history", history(app))
	mux.HandleFunc("DELETE /api/apps/{id}/env/history", forget(app))
	mux.HandleFunc("POST /api/servers/{id}/caddyfile", file(false, srv))
	mux.HandleFunc("PUT /api/servers/{id}/caddyfile", file(true, srv))
	mux.HandleFunc("POST /api/servers/{id}/caddyfile/history", history(srv))
	mux.HandleFunc("DELETE /api/servers/{id}/caddyfile/history", forget(srv))
}
