package api

import (
	"errors"
	"net/http"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/sites"
)

func (s *server) siteRoutes(mux *http.ServeMux) {
	failSite := func(w http.ResponseWriter, err error) {
		if errors.Is(err, sites.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Site not found.")
			return
		}
		fail(w, err)
	}
	mux.HandleFunc("GET /api/sites", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, s.Sites.List())
	}))
	mux.HandleFunc("GET /api/sites/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		st, err := s.Sites.Get(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"site": st, "history": s.Sites.History(st.ID)})
	}))
	mux.HandleFunc("POST /api/sites/detect", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Repo, Branch string }
		if !decode(w, r, &in) {
			return
		}
		branch := in.Branch
		if branch == "" {
			def, _, err := s.GitHub.RepoInfo(r.Context(), in.Repo)
			if err != nil {
				fail(w, err)
				return
			}
			branch = def
		}
		p, err := s.Sites.Detect(r.Context(), in.Repo, branch)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input", capitalize(err.Error())+".")
			return
		}
		file, example := s.Sites.EnvExample(r.Context(), in.Repo, branch)
		writeJSON(w, http.StatusOK, map[string]any{"plan": p, "branch": branch, "exampleFile": file, "example": example})
	}))
	mux.HandleFunc("POST /api/sites", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in sites.Input
		if !decode(w, r, &in) {
			return
		}
		if _, err := s.Fleet.Reg.Get(in.ServerID); err != nil {
			fail(w, err)
			return
		}
		st, err := s.Sites.Create(r.Context(), in, s.actor(r))
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.create", in.ServerID, st.Name+" ("+st.Repo+")")
		writeJSON(w, http.StatusCreated, st)
	}))
	mux.HandleFunc("PUT /api/sites/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in sites.Change
		if !decode(w, r, &in) {
			return
		}
		st, err := s.Sites.Update(r.Context(), r.PathValue("id"), in, s.actor(r))
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.update", st.ServerID, st.Name)
		writeJSON(w, http.StatusOK, st)
	}))
	mux.HandleFunc("GET /api/sites/{id}/env", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		env, err := s.Sites.Env(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		st, _ := s.Sites.Get(r.PathValue("id"))
		file, example := s.Sites.EnvExample(r.Context(), st.Repo, st.Branch)
		writeJSON(w, http.StatusOK, map[string]string{"env": env, "exampleFile": file, "example": example})
	}))
	mux.HandleFunc("PUT /api/sites/{id}/env", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Env string }
		if !decode(w, r, &in) {
			return
		}
		st, err := s.Sites.SetEnv(r.Context(), r.PathValue("id"), in.Env)
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.env", st.ServerID, st.Name)
		writeJSON(w, http.StatusOK, st)
	}))
	mux.HandleFunc("POST /api/sites/{id}/redeploy", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Sites.Redeploy(r.Context(), r.PathValue("id"), s.actor(r)); err != nil {
			failSite(w, err)
			return
		}
		st, _ := s.Sites.Get(r.PathValue("id"))
		s.audit(r, "site.redeploy", st.ServerID, st.Name)
		writeJSON(w, http.StatusAccepted, st)
	}))
	mux.HandleFunc("DELETE /api/sites/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		st, err := s.Sites.Delete(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.delete", st.ServerID, st.Name)
		w.WriteHeader(http.StatusNoContent)
	}))
}
