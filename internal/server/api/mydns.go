package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/github"
)

// myDNSRoutes: everyone's own Cloudflare accounts. The owner sets up the OAuth client once
// (Integrations); each person then signs in with Cloudflare, and the tokens are
// stored encrypted.
func (s *server) myDNSRoutes(mux *http.ServeMux) {
	failDNS := func(w http.ResponseWriter, err error) {
		if errors.Is(err, dns.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "DNS account not found.")
			return
		}
		fail(w, err)
	}
	mux.HandleFunc("GET /api/me/dns", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		out := map[string]any{"accounts": s.DNS.Accounts(linkKey(sess)), "ready": s.DNS.AppClientID() != ""}
		if sess.Owner() {
			out["everyone"] = s.DNS.Accounts("") // the owner's overview of the team
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /api/me/dns/connect", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		u, err := s.DNS.SignInURL(s.publicURL(r), linkKey(sess))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": u})
	}))
	// Cloudflare sends the browser back here after the sign-in; the one-time state proves
	// who started it (the cookie may not come along on a redirect from cloudflare.com).
	mux.HandleFunc("GET "+dns.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		q := r.URL.Query()
		a, err := s.DNS.FinishSignIn(ctx, q.Get("code"), q.Get("state"))
		if err == nil && q.Get("error") != "" { // refused on Cloudflare's side: nothing was granted
			err = errors.New("Cloudflare: " + q.Get("error_description"))
		}
		page := "/#/settings/account?"
		if a.Owner == github.OwnerKey || a.Owner == "" {
			page = "/#/integrations?tab=cloudflare&"
		}
		if err != nil {
			http.Redirect(w, r, page+"cloudflare=error&message="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		audit.Record(s.DB, audit.Event{Action: "dns.connect", OK: true, IP: clientIP(r), UserAgent: r.UserAgent(), Actor: a.Owner, Detail: a.Label})
		http.Redirect(w, r, page+"cloudflare=connected", http.StatusSeeOther)
	})
	// The owner's OAuth client, made once in their Cloudflare account.
	mux.HandleFunc("PUT /api/integrations/cloudflare", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ ClientID, ClientSecret string }
		if !decode(w, r, &in) {
			return
		}
		if err := s.DNS.SetApp(in.ClientID, in.ClientSecret); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "integration.cloudflare.connect", "", "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("DELETE /api/integrations/cloudflare", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.DNS.ClearApp(); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "integration.cloudflare.disconnect", "", "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("DELETE /api/me/dns/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if err := s.DNS.Remove(linkKey(sess), r.PathValue("id")); err != nil {
			failDNS(w, err)
			return
		}
		s.audit(r, "dns.disconnect", "", r.PathValue("id"))
		go s.syncBlocks() // its Cloudflare rules go away
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/me/dns/{id}/refresh", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		a, err := s.DNS.Refresh(r.Context(), linkKey(sess), r.PathValue("id"))
		if err != nil {
			failDNS(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	}))
	mux.HandleFunc("PUT /api/me/dns/{id}/sync-blocks", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ On bool }
		if !decode(w, r, &in) {
			return
		}
		a, err := s.DNS.SetSyncBlocks(linkKey(sess), r.PathValue("id"), in.On)
		if err != nil {
			failDNS(w, err)
			return
		}
		go s.syncBlocks()
		writeJSON(w, http.StatusOK, a)
	}))
	mux.HandleFunc("GET /api/me/dns/{id}/zones/{zone}/attack", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		on, err := s.DNS.UnderAttack(r.Context(), linkKey(sess), r.PathValue("id"), r.PathValue("zone"), nil)
		if err != nil {
			failDNS(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"on": on})
	}))
	mux.HandleFunc("PUT /api/me/dns/{id}/zones/{zone}/attack", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ On bool }
		if !decode(w, r, &in) {
			return
		}
		on, err := s.DNS.UnderAttack(r.Context(), linkKey(sess), r.PathValue("id"), r.PathValue("zone"), &in.On)
		if err != nil {
			failDNS(w, err)
			return
		}
		what := "off"
		if on {
			what = "on"
		}
		s.audit(r, "dns.under_attack", "", r.PathValue("zone")+": "+what)
		writeJSON(w, http.StatusOK, map[string]bool{"on": on})
	}))
}

// syncBlocks mirrors the dashboard's blocked addresses to the Cloudflare accounts that ask for it.
func (s *server) syncBlocks() {
	if s.DNS == nil || s.Admin == nil {
		return
	}
	on, ips := s.Admin.BlockedIPs()
	if !on {
		ips = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.DNS.SyncBlocks(ctx, ips)
}
