package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/dns"
)

// myDNSRoutes: everyone's own DNS and CDN accounts (Cloudflare, Porkbun, deSEC). The
// keys are checked by listing the account's domains, stored encrypted, never shown again.
func (s *server) myDNSRoutes(mux *http.ServeMux) {
	failDNS := func(w http.ResponseWriter, err error) {
		if errors.Is(err, dns.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "DNS account not found.")
			return
		}
		fail(w, err)
	}
	mux.HandleFunc("GET /api/me/dns", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		out := map[string]any{"accounts": s.DNS.Accounts(linkKey(sess)), "providers": dns.Providers}
		if sess.Owner() {
			out["everyone"] = s.DNS.Accounts("") // the owner's overview of the team
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /api/me/dns", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct {
			Provider, Label string
			Creds           map[string]string
		}
		if !decode(w, r, &in) {
			return
		}
		a, err := s.DNS.Add(r.Context(), linkKey(sess), in.Provider, in.Label, in.Creds)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "dns.connect", "", a.Label)
		writeJSON(w, http.StatusCreated, a)
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
