package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/github"
)

// linkKey is whose GitHub link a session uses: the owner's or the member's.
func linkKey(sess auth.Session) string {
	if sess.Owner() {
		return github.OwnerKey
	}
	return sess.MemberID
}

// myGitHubRoutes: everyone's own GitHub account, for deploying their sites. The owner
// connects the app once (Integrations); each person then signs in with their
// own GitHub account and installs the app on it.
func (s *server) myGitHubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/github", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		st := s.GitHub.Status()
		out := map[string]any{"connected": st.Connected, "canDeploy": sess.Owner() || sess.Role == auth.RoleOperator, "owner": sess.Owner()}
		if st.App != nil {
			out["app"] = st.App.Name
			out["installUrl"] = "https://github.com/apps/" + st.App.Slug + "/installations/new"
		}
		if l, ok := s.GitHub.LinkOf(linkKey(sess)); ok {
			out["link"] = l
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /api/me/github/connect", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		u, err := s.GitHub.SignInURL(r.Context(), s.publicURL(r), linkKey(sess))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": u})
	}))
	mux.HandleFunc("POST /api/me/github/install", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		u, err := s.GitHub.InstallURL(linkKey(sess))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": u})
	}))
	mux.HandleFunc("DELETE /api/me/github", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if err := s.GitHub.Unlink(linkKey(sess)); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "github.unlink", "", "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/me/github/repos", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		repos, err := s.GitHub.ReposOf(r.Context(), linkKey(sess))
		if err != nil {
			fail(w, err)
			return
		}
		type item struct {
			Repo github.Repo `json:"repo"`
		}
		out := []item{}
		for _, rp := range repos {
			out = append(out, item{rp})
		}
		writeJSON(w, http.StatusOK, out)
	}))
	// GitHub sends the browser back here after the sign-in; the one-time state proves
	// who started it (the cookie may not come along on a redirect from github.com).
	mux.HandleFunc("GET "+github.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		key, l, err := s.GitHub.FinishSignIn(ctx, r.URL.Query().Get("code"), r.URL.Query().Get("state"))
		if err != nil {
			http.Redirect(w, r, "/#/sites?github=error&message="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		if len(l.Installations) == 0 { // not installed on their account yet: do that, then come back
			if u, err := s.GitHub.InstallURL(key); err == nil {
				http.Redirect(w, r, u, http.StatusSeeOther)
				return
			}
		}
		http.Redirect(w, r, "/#/sites?github=linked", http.StatusSeeOther)
	})
}
