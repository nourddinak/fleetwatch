package api

import (
	"net/http"
	"slices"
	"time"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/mobile"
	"fleetwatch/internal/server/oncall"
)

// mobileMoreRoutes: what the phone app shows besides alerts: the fleet at a glance (also
// for its home screen widget), live charts, and quick actions on apps.
func (s *server) mobileMoreRoutes(mux *http.ServeMux) {
	visible := func(d mobile.Device, serverID string) bool {
		servers, ok := s.Mobile.Scope(d.MemberID)
		return ok && (servers == nil || slices.Contains(servers, serverID))
	}
	type serverItem struct {
		ID     string  `json:"id"`
		Name   string  `json:"name"`
		Status string  `json:"status"` // online | warning | critical | disconnected | pending
		CPU    float64 `json:"cpu"`
		RAM    float64 `json:"ram"`
		Disk   float64 `json:"disk"`
		GPU    float64 `json:"gpu,omitempty"`
		Apps   int     `json:"apps"`
		Down   int     `json:"down"` // apps offline, stopped or crashing
	}
	fleetOf := func(d mobile.Device) []serverItem {
		out := []serverItem{}
		views, err := s.Fleet.Views()
		if err != nil {
			return out
		}
		for _, v := range views {
			if !visible(d, v.ID) || !v.Enrolled {
				continue
			}
			it := serverItem{ID: v.ID, Name: v.Name, Status: v.Status, CPU: v.CPU, RAM: v.RAM, Disk: v.Disk}
			for _, g := range v.GPUs {
				it.GPU = max(it.GPU, g.Util)
			}
			if apps, err := s.Apps.List(v.ID); err == nil {
				for _, a := range apps {
					it.Apps++
					if a.Status == "offline" || a.Status == "stopped" || a.Status == "crash-loop" || a.HealthStatus == "failed" {
						it.Down++
					}
				}
			}
			out = append(out, it)
		}
		return out
	}
	// status is the fleet in one look, for the widget.
	mux.HandleFunc("GET /api/mobile/status", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		list := fleetOf(d)
		open, critical := 0, 0
		type incident struct {
			Title    string `json:"title"`
			Severity string `json:"severity"`
			Status   string `json:"status"`
		}
		latest := []incident{} // the newest few, for the on-call widget
		if s.Incidents != nil {
			servers, _ := s.Mobile.Scope(d.MemberID)
			if xs, err := s.Incidents.List(incidentsOpen()); err == nil {
				for _, x := range xs {
					if servers == nil || slices.Contains(servers, x.ServerID) {
						open++
						if x.Severity == "critical" && x.Status == "active" {
							critical++
						}
						if len(latest) < 4 {
							latest = append(latest, incident{x.Title, x.Severity, x.Status})
						}
					}
				}
			}
		}
		out := map[string]any{"at": time.Now().UnixMilli(), "servers": list, "openIncidents": open, "critical": critical, "incidents": latest}
		if s.OnCall != nil { // who is on call now, and until when (0: no end)
			if c := s.OnCall.Config(); c.Enabled {
				if sched := c.Schedule(time.Now(), 2); len(sched) > 0 {
					now, until := sched[0].Person, sched[0].To
					if c.FixedOrder && len(sched) == 1 {
						until = 0
					}
					name := now
					for _, p := range s.OnCall.People() {
						if p.ID == now {
							name = p.Name
						}
					}
					me := d.MemberID
					if me == "" {
						me = oncall.Owner
					}
					out["onCall"] = map[string]any{"name": name, "until": until, "you": now == me}
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("GET /api/mobile/servers/{id}/metrics", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		if !visible(d, r.PathValue("id")) {
			writeError(w, http.StatusNotFound, "not_found", "Server not found.")
			return
		}
		rows, err := s.Metrics.Rows(r.PathValue("id"), r.URL.Query().Get("range"), time.Now())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, metrics.ServerPoints(rows))
	}))
	type appItem struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Status   string `json:"status"`
		Health   string `json:"health"`
		Commands []any  `json:"commands"`
	}
	mux.HandleFunc("GET /api/mobile/servers/{id}/apps", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		if !visible(d, r.PathValue("id")) {
			writeError(w, http.StatusNotFound, "not_found", "Server not found.")
			return
		}
		out := []appItem{}
		apps, err := s.Apps.List(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		canRun := s.mobileMe(d).Role != auth.RoleViewer
		for _, a := range apps {
			it := appItem{ID: a.ID, Name: a.Name, Status: a.Status, Health: a.HealthStatus, Commands: []any{}}
			if canRun && s.Commands != nil {
				if cmds, err := s.Commands.List(a.ID); err == nil {
					for _, c := range cmds {
						it.Commands = append(it.Commands, map[string]any{"id": c.ID, "name": c.Name, "kind": c.Kind, "danger": c.Danger, "preview": c.Preview})
					}
				}
			}
			out = append(out, it)
		}
		writeJSON(w, http.StatusOK, out)
	}))
	// run: a quick action from the phone (the app asks for the fingerprint first).
	mux.HandleFunc("POST /api/mobile/apps/{id}/commands/{cmd}/run", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		me := s.mobileMe(d)
		v, err := s.Apps.Get(r.PathValue("id"))
		if err != nil || !visible(d, v.ServerID) {
			writeError(w, http.StatusNotFound, "not_found", "Application not found.")
			return
		}
		if me.Role == auth.RoleViewer {
			writeError(w, http.StatusForbidden, "forbidden", "Your account can only look, not change things.")
			return
		}
		if s.Auth.Security().ReauthForCommands {
			writeError(w, http.StatusForbidden, "password_required", "This dashboard asks for your password before commands: run them from the dashboard.")
			return
		}
		run, err := s.Commands.Start(v.ID, r.PathValue("cmd"), me.Name+" (phone)")
		if err != nil {
			fail(w, err)
			return
		}
		if s.Detector != nil {
			s.Detector.Intended(run.AppID, time.Now())
		}
		audit.Record(s.DB, audit.Event{Action: "command.run", OK: true, IP: clientIP(r), UserAgent: "phone: " + d.Name, Target: run.AppID, Detail: run.Name + ": " + run.Preview, Actor: me.Name})
		s.Bus.Publish("activity", s.activityOf(audit.Row{TS: time.Now().UnixMilli(), Action: "command.run", OK: true, Target: run.AppID, Detail: run.Name + ": " + run.Preview, Actor: me.Name}))
		writeJSON(w, http.StatusAccepted, run)
	}))
	mux.HandleFunc("GET /api/mobile/runs/{run}", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		run, err := s.Commands.GetRun(r.PathValue("run"))
		if err != nil {
			fail(w, err)
			return
		}
		if v, err := s.Apps.Get(run.AppID); err != nil || !visible(d, v.ServerID) {
			writeError(w, http.StatusNotFound, "not_found", "Run not found.")
			return
		}
		writeJSON(w, http.StatusOK, run)
	}))
	// mute: "not now" from the notification. One hour without alerts for this incident.
	mux.HandleFunc("POST /api/mobile/incidents/{id}/mute", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		x, err := s.Incidents.Get(r.PathValue("id"))
		if err != nil || !visible(d, x.ServerID) {
			writeError(w, http.StatusNotFound, "not_found", "Incident not found.")
			return
		}
		if x.Status != "resolved" {
			if x, err = s.Incidents.Act(x.ID, "mute1h", "", s.mobileMe(d).Name+" (phone)"); err != nil {
				fail(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": x.Status})
	}))
}
