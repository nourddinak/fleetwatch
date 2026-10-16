package api

import (
	"net/http"
	"strconv"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/notify"
)

func (s *server) incidentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/incidents", s.authed(s.listIncidents))
	mux.HandleFunc("GET /api/incidents/{id}", s.authed(s.getIncident))
	mux.HandleFunc("POST /api/incidents/{id}/{action}", s.authed(s.actIncident))
	mux.HandleFunc("GET /api/settings/incident-rules", s.authed(s.getIncidentRules))
	mux.HandleFunc("PUT /api/settings/incident-rules", s.authed(s.putIncidentRules))

	mux.HandleFunc("GET /api/notifications", s.authed(s.listNotifications))
	mux.HandleFunc("POST /api/notifications/read", s.authed(s.markNotificationsRead))
	mux.HandleFunc("GET /api/notifications/channels", s.authed(s.listChannels))
	mux.HandleFunc("PUT /api/notifications/channels/{provider}", s.authed(s.saveChannel))
	mux.HandleFunc("POST /api/notifications/channels/{provider}/test", s.authed(s.testChannel))
	mux.HandleFunc("GET /api/settings/notify-rules", s.authed(s.getNotifyRules))
	mux.HandleFunc("PUT /api/settings/notify-rules", s.authed(s.putNotifyRules))

	mux.HandleFunc("GET /api/search/commands", s.authed(s.searchCommands))
}

func (s *server) listIncidents(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.Incidents.List(incidents.Filter{Status: q.Get("status"), ServerID: q.Get("server"), AppID: q.Get("app"), Type: q.Get("type"), Severity: q.Get("severity"), Limit: limit})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) getIncident(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	x, err := s.Incidents.Get(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, x)
}

func (s *server) actIncident(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct{ Note string }
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	x, err := s.Incidents.Act(r.PathValue("id"), r.PathValue("action"), in.Note, sess.Username)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "incident."+r.PathValue("action"), x.ID, x.Title)
	writeJSON(w, http.StatusOK, x)
}

func (s *server) getIncidentRules(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, s.Detector.Rules())
}

func (s *server) putIncidentRules(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in incidents.Rules
	if !decode(w, r, &in) {
		return
	}
	if err := s.Detector.SaveRules(in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "settings.incident_rules", "", "")
	writeJSON(w, http.StatusOK, in)
}

func (s *server) listNotifications(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.Notify.History(limit, int64Param(r, "before"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) markNotificationsRead(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ ID int64 } // 0 = all
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	if err := s.Notify.MarkRead(in.ID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) listChannels(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	list, err := s.Notify.Channels()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) saveChannel(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct {
		Enabled bool
		Fields  map[string]string
	}
	if !decode(w, r, &in) {
		return
	}
	p := r.PathValue("provider")
	if err := s.Notify.SaveChannel(p, in.Enabled, in.Fields); err != nil {
		if err == notify.ErrUnknownProvider {
			writeError(w, http.StatusNotFound, "not_found", "Unknown provider.")
			return
		}
		fail(w, err)
		return
	}
	s.audit(r, "notify.channel", p, "")
	s.listChannels(w, r, auth.Session{})
}

func (s *server) testChannel(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	h, err := s.Notify.Test(r.Context(), r.PathValue("provider"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *server) getNotifyRules(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, s.Notify.Rules())
}

func (s *server) putNotifyRules(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in notify.Rules
	if !decode(w, r, &in) {
		return
	}
	if err := s.Notify.SaveRules(in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "settings.notify_rules", "", "")
	writeJSON(w, http.StatusOK, in)
}

func (s *server) searchCommands(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	q := r.URL.Query().Get("q")
	if len(q) < 2 || len(q) > 100 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	hits, err := s.Commands.Search(q, 8)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hits)
}
