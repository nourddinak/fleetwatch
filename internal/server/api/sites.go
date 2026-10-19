package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/dns"
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
	// Members manage the sites they made; the owner manages every site.
	mark := func(sess auth.Session, st sites.Site) sites.Site {
		st.Mine = sess.Owner() || (st.OwnerKey != "" && st.OwnerKey == sess.MemberID)
		return st
	}
	// own loads a site the session may change, answering the request itself otherwise.
	own := func(w http.ResponseWriter, r *http.Request, sess auth.Session) (sites.Site, bool) {
		st, err := s.Sites.Get(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return st, false
		}
		if !mark(sess, st).Mine {
			writeError(w, http.StatusForbidden, "forbidden", "Only whoever added this site, or the owner, can change it.")
			return st, false
		}
		return st, true
	}
	// mayDeploy: a member deploys only from their own GitHub repositories.
	mayDeploy := func(w http.ResponseWriter, r *http.Request, sess auth.Session, repo string) bool {
		if sess.Owner() || (s.GitHub != nil && s.GitHub.CanDeploy(r.Context(), sess.MemberID, repo)) {
			return true
		}
		writeError(w, http.StatusForbidden, "forbidden", "Connect your GitHub account and give the app access to this repository first.")
		return false
	}
	mux.HandleFunc("GET /api/sites", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		list := s.Sites.List()
		for i := range list {
			list[i] = mark(sess, list[i])
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("GET /api/sites/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		st, err := s.Sites.Get(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"site": mark(sess, st), "history": s.Sites.History(st.ID)})
	}))
	mux.HandleFunc("POST /api/sites/detect", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ Repo, Branch string }
		if !decode(w, r, &in) || !mayDeploy(w, r, sess, in.Repo) {
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
	mux.HandleFunc("POST /api/sites", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in sites.Input
		if !decode(w, r, &in) || !mayDeploy(w, r, sess, in.Repo) {
			return
		}
		if _, err := s.Fleet.Reg.Get(in.ServerID); err != nil {
			fail(w, err)
			return
		}
		if !s.scopeOf(sess).server(in.ServerID) {
			writeError(w, http.StatusForbidden, "forbidden", "You do not have access to this server.")
			return
		}
		in.Owner = sess.MemberID
		st, err := s.Sites.Create(r.Context(), in, s.actor(r))
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.create", in.ServerID, st.Name+" ("+st.Repo+")")
		writeJSON(w, http.StatusCreated, mark(sess, st))
	}))
	mux.HandleFunc("PUT /api/sites/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in sites.Change
		if !decode(w, r, &in) {
			return
		}
		if _, ok := own(w, r, sess); !ok {
			return
		}
		st, err := s.Sites.Update(r.Context(), r.PathValue("id"), in, s.actor(r))
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.update", st.ServerID, st.Name)
		writeJSON(w, http.StatusOK, mark(sess, st))
	}))
	mux.HandleFunc("GET /api/sites/{id}/env", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if _, ok := own(w, r, sess); !ok { // the .env holds secrets
			return
		}
		env, err := s.Sites.Env(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		st, _ := s.Sites.Get(r.PathValue("id"))
		file, example := s.Sites.EnvExample(r.Context(), st.Repo, st.Branch)
		writeJSON(w, http.StatusOK, map[string]string{"env": env, "exampleFile": file, "example": example})
	}))
	mux.HandleFunc("PUT /api/sites/{id}/env", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ Env string }
		if !decode(w, r, &in) {
			return
		}
		if _, ok := own(w, r, sess); !ok {
			return
		}
		st, err := s.Sites.SetEnv(r.Context(), r.PathValue("id"), in.Env)
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.env", st.ServerID, st.Name)
		writeJSON(w, http.StatusOK, mark(sess, st))
	}))
	// The site's last 24 hours on Cloudflare, through the account of whoever added it.
	mux.HandleFunc("GET /api/sites/{id}/traffic", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		st, err := s.Sites.Get(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		if s.DNS == nil {
			fail(w, dns.ErrNoZone)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		t, err := s.DNS.Traffic(ctx, sites.DNSOwner(st), st.Domains)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	}))
	mux.HandleFunc("POST /api/sites/{id}/purge", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		st, err := s.Sites.Get(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		if s.DNS == nil {
			fail(w, dns.ErrNoZone)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		done, err := s.DNS.PurgeSite(ctx, sites.DNSOwner(st), st.Domains)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "site.purge", st.ID, strings.Join(done, ", "))
		writeJSON(w, http.StatusOK, map[string]any{"purged": done})
	}))
	attack := func(w http.ResponseWriter, r *http.Request, on *bool) {
		st, err := s.Sites.Get(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		if s.DNS == nil {
			fail(w, dns.ErrNoZone)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		v, err := s.DNS.SiteUnderAttack(ctx, sites.DNSOwner(st), st.Domains, on)
		if err != nil {
			fail(w, err)
			return
		}
		if on != nil {
			s.audit(r, "site.under_attack", st.ID, fmt.Sprintf("%v", *on))
		}
		writeJSON(w, http.StatusOK, map[string]bool{"on": v})
	}
	mux.HandleFunc("GET /api/sites/{id}/attack", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { attack(w, r, nil) }))
	mux.HandleFunc("PUT /api/sites/{id}/attack", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ On bool }
		if !decode(w, r, &in) {
			return
		}
		attack(w, r, &in.On)
	}))
	// Where each of a site's domains points right now, as public DNS answers.
	mux.HandleFunc("GET /api/sites/{id}/dns/check", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		st, err := s.Sites.Get(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		out := map[string][]string{}
		for _, d := range st.Domains {
			ips := []string{}
			if addrs, err := net.DefaultResolver.LookupIPAddr(ctx, d); err == nil {
				for _, a := range addrs {
					if a.IP.To4() != nil {
						ips = append(ips, a.IP.String())
					}
				}
			}
			out[d] = ips
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /api/sites/{id}/dns", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if _, ok := own(w, r, sess); !ok {
			return
		}
		go s.Sites.SyncDNS(r.PathValue("id"), nil, true)
		w.WriteHeader(http.StatusAccepted)
	}))
	mux.HandleFunc("PUT /api/sites/{id}/dns/proxy", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct {
			Domain string
			On     bool
		}
		if !decode(w, r, &in) {
			return
		}
		if _, ok := own(w, r, sess); !ok {
			return
		}
		st, err := s.Sites.SetProxied(r.Context(), r.PathValue("id"), in.Domain, in.On)
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.proxy", st.ServerID, st.Name+": "+in.Domain)
		writeJSON(w, http.StatusOK, mark(sess, st))
	}))
	mux.HandleFunc("PUT /api/sites/{id}/caddy", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ Config string }
		if !decode(w, r, &in) {
			return
		}
		if !sess.Owner() { // a site's Caddy config can serve any domain or folder of the server
			writeError(w, http.StatusForbidden, "forbidden", "Only the owner can change a site's Caddy config.")
			return
		}
		st, err := s.Sites.SetCaddy(r.PathValue("id"), in.Config, s.actor(r))
		if err != nil {
			failSite(w, err)
			return
		}
		what := "custom Caddy config"
		if in.Config == "" {
			what = "generated Caddy config"
		}
		s.audit(r, "site.caddy", st.ServerID, st.Name+": "+what)
		writeJSON(w, http.StatusOK, mark(sess, st))
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
	mux.HandleFunc("DELETE /api/sites/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if _, ok := own(w, r, sess); !ok {
			return
		}
		st, err := s.Sites.Delete(r.PathValue("id"))
		if err != nil {
			failSite(w, err)
			return
		}
		s.audit(r, "site.delete", st.ServerID, st.Name)
		w.WriteHeader(http.StatusNoContent)
	}))
}
