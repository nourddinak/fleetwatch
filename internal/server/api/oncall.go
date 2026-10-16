package api

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/oncall"
)

type onCallView struct {
	Config   oncall.Config   `json:"config"`
	People   []oncall.Person `json:"people"`
	Now      string          `json:"now"` // person on call right now
	Schedule []oncall.Shift  `json:"schedule"`
}

// onCallRoutes: the schedule (owner only, under /api/oncall) and the acknowledge
// link in pages, which works without signing in.
func (s *server) onCallRoutes(mux *http.ServeMux) {
	view := func() onCallView {
		c := s.OnCall.Config()
		return onCallView{Config: c, People: s.OnCall.People(), Now: c.OnCall(time.Now()), Schedule: c.Schedule(time.Now(), 6)}
	}
	mux.HandleFunc("GET /api/oncall", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, view())
	}))
	mux.HandleFunc("PUT /api/oncall", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in oncall.Config
		if !decode(w, r, &in) {
			return
		}
		if _, err := s.OnCall.Save(in); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "oncall.save", "", fmt.Sprintf("enabled=%v, %d in rotation", in.Enabled, len(in.Rotation)))
		writeJSON(w, http.StatusOK, view())
	}))
	// The handoff between shifts and the on-call report: for the whole team.
	mux.HandleFunc("GET /api/handoff", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		h := s.OnCall.Handoff()
		names := map[string]string{}
		for _, p := range s.OnCall.People() {
			names[p.ID] = p.Name
		}
		writeJSON(w, http.StatusOK, map[string]any{"handoff": h, "names": names})
	}))
	mux.HandleFunc("PUT /api/handoff", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Note string }
		if !decode(w, r, &in) {
			return
		}
		n, err := s.OnCall.SaveNote(in.Note, s.actor(r))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, n)
	}))
	mux.HandleFunc("GET /api/reports/oncall", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		people, pages := s.OnCall.Report(days)
		writeJSON(w, http.StatusOK, map[string]any{"people": people, "pages": pages})
	}))
	// GET shows a button (mail scanners open links); POST acknowledges.
	mux.HandleFunc("GET /api/public/ack", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		action := "/api/public/ack?i=" + html.EscapeString(q.Get("i")) + "&p=" + html.EscapeString(q.Get("p")) + "&s=" + html.EscapeString(q.Get("s"))
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Acknowledge</title>%s`+
			`<main><h1>Acknowledge this incident?</h1><p>Paging stops and the incident shows that you are on it.</p>`+
			`<form method="post" action="%s"><button>I'm on it</button></form></main>`, smallCSS, action)
	})
	mux.HandleFunc("POST /api/public/ack", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		x, who, err := s.OnCall.Ack(q.Get("i"), q.Get("p"), q.Get("s"))
		if err != nil {
			smallPage(w, http.StatusBadRequest, "Link not valid", "This acknowledge link is not valid. Open the dashboard to acknowledge.", "")
			return
		}
		msg := "Thanks " + who + ": paging stopped for “" + x.Title + "”."
		if x.Status == "resolved" {
			msg = "“" + x.Title + "” is already resolved."
		}
		smallPage(w, http.StatusOK, "Acknowledged", msg, s.publicURL(r)+"/#/incidents")
	})
}
