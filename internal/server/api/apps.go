package api

import (
	"net/http"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/metrics"
)

func (s *server) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/apps", s.authed(s.listApps))
	mux.HandleFunc("POST /api/apps", s.authed(s.createApp))
	mux.HandleFunc("GET /api/apps/{id}", s.authed(s.getApp))
	mux.HandleFunc("PATCH /api/apps/{id}", s.authed(s.patchApp))
	mux.HandleFunc("DELETE /api/apps/{id}", s.authed(s.deleteApp))
	mux.HandleFunc("GET /api/apps/{id}/events", s.authed(s.appEvents))
	mux.HandleFunc("GET /api/apps/{id}/metrics", s.authed(s.appMetrics))
	mux.HandleFunc("GET /api/discoveries", s.authed(s.listDiscoveries))
	mux.HandleFunc("POST /api/discoveries/scan", s.authed(s.scan))
	mux.HandleFunc("POST /api/discoveries/reset-ignored", s.authed(s.resetIgnored))
	mux.HandleFunc("POST /api/discoveries/{id}/add", s.authed(s.addDiscovery))
	mux.HandleFunc("POST /api/discoveries/{id}/ignore", s.authed(s.ignoreDiscovery))
	mux.HandleFunc("POST /api/discoveries/{id}/restore", s.authed(s.restoreDiscovery))
	mux.HandleFunc("GET /api/servers/{id}/ports", s.authed(s.serverPorts))
	mux.HandleFunc("GET /api/settings/discovery", s.authed(s.getDiscoverySettings))
	mux.HandleFunc("PUT /api/settings/discovery", s.authed(s.putDiscoverySettings))
}

func (s *server) listApps(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	list, err := s.Apps.List(r.URL.Query().Get("server"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) createApp(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in apps.Input
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Apps.Create(in, "manual")
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "app.create", v.ID, v.Name)
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) getApp(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	v, err := s.Apps.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) patchApp(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var p apps.Patch
	if !decode(w, r, &p) {
		return
	}
	v, err := s.Apps.Update(r.PathValue("id"), p)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "app.update", v.ID, v.Name)
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteApp(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	a, err := s.Apps.Delete(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "app.delete", a.ID, a.Name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) appEvents(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	ev, err := s.Apps.Events(r.PathValue("id"), 30)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) appMetrics(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	if _, err := s.Apps.Get(id); err != nil {
		fail(w, err)
		return
	}
	rows, err := s.AppMetrics.Rows(id, r.URL.Query().Get("range"), time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics.AppPoints(rows))
}

func (s *server) listDiscoveries(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	state := r.URL.Query().Get("state")
	if state != "ignored" {
		state = "pending"
	}
	list, err := s.Apps.Discoveries(state)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) addDiscovery(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ Name string }
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	v, err := s.Apps.AddDiscovery(r.PathValue("id"), in.Name)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "app.create", v.ID, v.Name+" (from discovery)")
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) ignoreDiscovery(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if err := s.Apps.IgnoreDiscovery(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) restoreDiscovery(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if err := s.Apps.RestoreDiscovery(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) resetIgnored(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	n, err := s.Apps.ResetIgnored()
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "discovery.reset_ignored", "", "")
	writeJSON(w, http.StatusOK, map[string]int64{"restored": n})
}

// scan asks one server's agent (or every connected agent) for a fresh inventory.
func (s *server) scan(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ ServerID string }
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	ids := []string{in.ServerID}
	if in.ServerID == "" {
		recs, err := s.Fleet.Reg.List()
		if err != nil {
			fail(w, err)
			return
		}
		ids = ids[:0]
		for _, rec := range recs {
			ids = append(ids, rec.ID)
		}
	}
	sent := 0
	for _, id := range ids {
		if s.Fleet.Send(id, proto.TypeScan, nil) == nil {
			sent++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": sent})
}

func (s *server) serverPorts(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	id := r.PathValue("id")
	if _, err := s.Fleet.Reg.Get(id); err != nil {
		fail(w, err)
		return
	}
	rows, err := s.Apps.ServerPorts(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *server) getDiscoverySettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, s.Apps.DiscoverySettings())
}

func (s *server) putDiscoverySettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in apps.DiscoverySettings
	if !decode(w, r, &in) {
		return
	}
	if err := s.Apps.SaveDiscoverySettings(in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "settings.discovery", "", "")
	s.Fleet.BroadcastConfig()
	writeJSON(w, http.StatusOK, s.Apps.DiscoverySettings())
}
