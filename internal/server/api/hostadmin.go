package api

import (
	"net/http"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/hostadmin"
)

// adminRoutes: fleet-wide IP blocking, firewalls, package updates and dependency vulnerabilities.
func (s *server) adminRoutes(mux *http.ServeMux) {
	ids := func() []string {
		recs, _ := s.Fleet.Reg.List()
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.ID)
		}
		return out
	}

	// Blocked addresses.
	mux.HandleFunc("GET /api/blocks", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		on, _ := s.Admin.BlockedIPs()
		writeJSON(w, http.StatusOK, map[string]any{"enabled": on, "blocks": s.Admin.Blocked(), "servers": s.Admin.Status()})
	}))
	mux.HandleFunc("POST /api/blocks", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct {
			IP, Reason string
			Hours      int
		}
		if !decode(w, r, &in) {
			return
		}
		b, err := s.Admin.Block(in.IP, in.Reason, s.actor(r), clientIP(r), in.Hours)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "ip.block", b.IP, b.Reason)
		writeJSON(w, http.StatusCreated, b)
	}))
	mux.HandleFunc("DELETE /api/blocks/{ip...}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Admin.Unblock(r.PathValue("ip")); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "ip.unblock", r.PathValue("ip"), "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/settings/blocking", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, s.Admin.Settings())
	}))
	mux.HandleFunc("PUT /api/settings/blocking", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in hostadmin.BlockSettings
		if !decode(w, r, &in) {
			return
		}
		if err := s.Admin.SaveSettings(in); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "settings.blocking", "", "")
		writeJSON(w, http.StatusOK, s.Admin.Settings())
	}))

	// Firewalls.
	mux.HandleFunc("GET /api/servers/{id}/firewall", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, s.Admin.Firewall(r.PathValue("id")))
	}))
	mux.HandleFunc("POST /api/servers/{id}/firewall/scan", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Admin.ScanFirewall(r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "scanning"})
	}))
	mux.HandleFunc("POST /api/servers/{id}/firewall/change", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in proto.FirewallChange
		if !decode(w, r, &in) {
			return
		}
		if err := s.Admin.ChangeFirewall(r.PathValue("id"), in); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "firewall."+in.Action, r.PathValue("id"), in.Port+" "+in.Proto+" "+in.From+in.Rule)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "changing"})
	}))

	// Package updates.
	mux.HandleFunc("GET /api/updates", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, map[string]any{"servers": s.Admin.Updates(ids()), "auto": s.Admin.AutoUpdates()})
	}))
	mux.HandleFunc("POST /api/servers/{id}/updates/scan", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Refresh bool }
		if !decode(w, r, &in) {
			return
		}
		if err := s.Admin.ScanUpdates(r.PathValue("id"), in.Refresh); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "scanning"})
	}))
	mux.HandleFunc("POST /api/servers/{id}/updates/install", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ SecurityOnly bool }
		if !decode(w, r, &in) {
			return
		}
		if err := s.Admin.InstallUpdates(r.PathValue("id"), in.SecurityOnly, s.actor(r)); err != nil {
			fail(w, err)
			return
		}
		kind := "all"
		if in.SecurityOnly {
			kind = "security"
		}
		s.audit(r, "updates.install", r.PathValue("id"), kind)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "installing"})
	}))
	mux.HandleFunc("PUT /api/settings/auto-updates", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in hostadmin.AutoUpdates
		if !decode(w, r, &in) {
			return
		}
		if err := s.Admin.SaveAutoUpdates(in); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "settings.auto_updates", "", "")
		writeJSON(w, http.StatusOK, s.Admin.AutoUpdates())
	}))

	// Vulnerable dependencies.
	if s.Vulns != nil {
		mux.HandleFunc("GET /api/integrations/vulns", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			writeJSON(w, http.StatusOK, s.Vulns.Summary())
		}))
		mux.HandleFunc("PUT /api/integrations/vulns", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			var in struct{ Enabled bool }
			if !decode(w, r, &in) {
				return
			}
			if err := s.Vulns.SetEnabled(in.Enabled); err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, s.Vulns.Summary())
		}))
		mux.HandleFunc("GET /api/vulns", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			writeJSON(w, http.StatusOK, map[string]any{"apps": s.Vulns.List(), "scanning": s.Vulns.Scanning()})
		}))
		mux.HandleFunc("POST /api/vulns/scan", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			writeJSON(w, http.StatusAccepted, map[string]int{"servers": s.Vulns.ScanAll()})
		}))
	}
}
