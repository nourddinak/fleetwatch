package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/dns"
)

// integrationRoutes: connecting GitHub (owner only, under /api/integrations), plus the
// three addresses GitHub itself calls: the two browser redirects after creating and
// installing the app, and the webhook. Those carry their own proof (a one-time state,
// an installation GitHub confirms, a signature) instead of a session cookie, which the
// browser does not send on a redirect coming from github.com.
func (s *server) integrationRoutes(mux *http.ServeMux) {
	back := func(w http.ResponseWriter, r *http.Request, query string) {
		http.Redirect(w, r, "/#/integrations?tab=github&"+query, http.StatusSeeOther)
	}
	mux.HandleFunc("GET /api/integrations", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if s.GitHub.Connected() {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			s.GitHub.Refresh(ctx) // permissions may have been granted on GitHub since
			cancel()
		}
		pub := s.publicURL(r)
		out := map[string]any{"github": s.GitHub.Status(), "publicUrl": pub, "links": s.GitHub.Links(),
			"webhooks": strings.HasPrefix(pub, "https://") && !strings.Contains(pub, "localhost") && !strings.Contains(pub, "127.0.0.1")}
		if s.DNS != nil {
			out["cloudflare"] = map[string]any{"clientId": s.DNS.AppClientID(), "redirectUri": pub + dns.CallbackPath,
				"scopes": append(dns.Scopes, dns.AnalyticsScope), "noAnalytics": s.DNS.AppNoAnalytics()}
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /api/integrations/github/manifest", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Org string }
		if !decode(w, r, &in) {
			return
		}
		action, manifest, err := s.GitHub.Manifest(s.publicURL(r), strings.TrimSpace(in.Org), s.actor(r))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"action": action, "manifest": manifest})
	}))
	mux.HandleFunc("GET /api/integrations/github/created", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		install, by, err := s.GitHub.Created(ctx, r.URL.Query().Get("code"), r.URL.Query().Get("state"))
		if err != nil {
			back(w, r, "github=error&message="+url.QueryEscape(err.Error()))
			return
		}
		audit.Record(s.DB, audit.Event{Action: "integration.github.connect", OK: true, IP: clientIP(r), UserAgent: r.UserAgent(), Actor: by,
			Detail: s.GitHub.Status().App.Name})
		http.Redirect(w, r, install, http.StatusSeeOther) // straight on to choosing repositories
	})
	mux.HandleFunc("GET /api/integrations/github/installed", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		id, _ := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
		err := s.GitHub.Installed(ctx, id)
		// A team member installing on their own account: on to the sign-in that records it.
		if key := s.GitHub.AfterInstall(r.URL.Query().Get("state")); key != "" {
			if u, e := s.GitHub.SignInURL(ctx, s.publicURL(r), key); e == nil {
				http.Redirect(w, r, u, http.StatusSeeOther)
				return
			}
		}
		if err != nil {
			back(w, r, "github=error&message="+url.QueryEscape(err.Error()))
			return
		}
		back(w, r, "github=installed")
	})
	mux.HandleFunc("POST /api/integrations/github/webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20)) // GitHub caps payloads at 25 MB
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "unreadable body")
			return
		}
		if !s.GitHub.Webhook(r.Header.Get("X-GitHub-Event"), r.Header.Get("X-Hub-Signature-256"), body) {
			writeError(w, http.StatusUnauthorized, "bad_signature", "signature does not match")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/integrations/github/refresh", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.GitHub.Refresh(r.Context()); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.GitHub.Status())
	}))
	mux.HandleFunc("GET /api/integrations/github/repos", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		repos, err := s.GitHub.Repos(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		// Which monitored apps are checked out from each repository (their git remote).
		type linked struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			ServerID string `json:"serverId"`
			Branch   string `json:"branch"`
			Commit   string `json:"commit"`
		}
		byRepo := map[string][]linked{}
		if s.Apps != nil {
			if views, err := s.Apps.List(""); err == nil {
				for _, v := range views {
					if v.Git != nil && v.Git.Repo != "" {
						k := strings.ToLower(v.Git.Repo)
						byRepo[k] = append(byRepo[k], linked{v.ID, v.Name, v.ServerID, v.Git.Branch, v.Git.Commit})
					}
				}
			}
		}
		type item struct {
			Repo any      `json:"repo"`
			Apps []linked `json:"apps"`
		}
		out := []item{}
		for _, rp := range repos {
			apps := byRepo[strings.ToLower(rp.FullName)]
			if apps == nil {
				apps = []linked{}
			}
			out = append(out, item{rp, apps})
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("DELETE /api/integrations/github", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		app, err := s.GitHub.Disconnect(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "integration.github.disconnect", "", app.Name)
		writeJSON(w, http.StatusOK, app)
	}))
}
