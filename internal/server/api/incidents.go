package api

import (
	"cmp"
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
	mux.HandleFunc("GET /api/incidents/{id}/comments", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		list, err := s.Incidents.Comments(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("POST /api/incidents/{id}/comments", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Text string }
		if !decode(w, r, &in) {
			return
		}
		c, err := s.Incidents.AddComment(r.PathValue("id"), cmp.Or(s.actor(r), "someone"), in.Text)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	}))
	mux.HandleFunc("GET /api/incidents/{id}/postmortem", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		p, err := s.Incidents.Postmortem(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"postmortem": p, "runbook": incidents.Runbooks(s.DB)[p.Incident.TypeKey]})
	}))
	mux.HandleFunc("POST /api/incidents/{id}/postmortem", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in incidents.Notes
		if !decode(w, r, &in) {
			return
		}
		n, err := s.Incidents.SaveNotes(r.PathValue("id"), in, s.actor(r))
		if err != nil {
			fail(w, err)
			return
		}
		x, _ := s.Incidents.Get(r.PathValue("id"))
		s.audit(r, "postmortem.save", x.ID, x.Title)
		writeJSON(w, http.StatusOK, n)
	}))
	mux.HandleFunc("GET /api/runbooks", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, map[string]any{"runbooks": incidents.Runbooks(s.DB), "types": incidents.Labels})
	}))
	mux.HandleFunc("PUT /api/runbooks/{type}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Text string }
		if !decode(w, r, &in) {
			return
		}
		if err := incidents.SaveRunbook(s.DB, r.PathValue("type"), in.Text); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "runbook.save", r.PathValue("type"), "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	mux.HandleFunc("GET /api/settings/incident-rules", s.authed(s.getIncidentRules))
	mux.HandleFunc("PUT /api/settings/incident-rules", s.authed(s.putIncidentRules))

	mux.HandleFunc("GET /api/notifications", s.authed(s.listNotifications))
	mux.HandleFunc("POST /api/notifications/read", s.authed(s.markNotificationsRead))
	s.myChannelRoutes(mux)
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
	x, err := s.Incidents.Act(r.PathValue("id"), r.PathValue("action"), in.Note, s.actor(r))
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
