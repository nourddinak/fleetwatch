package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/cron"
)

func (s *server) cronRoutes(mux *http.ServeMux) {
	failCron := func(w http.ResponseWriter, err error) {
		if errors.Is(err, cron.ErrNoPing) {
			writeError(w, http.StatusNotFound, "not_found", "Heartbeat not found.")
			return
		}
		fail(w, err)
	}
	// The cron jobs and timers found on the servers this session may see, and the heartbeats.
	mux.HandleFunc("GET /api/cron", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		sc := s.scopeOf(sess)
		out := map[string]any{"servers": s.Cron.List(sc.server), "pings": []cron.Ping{}}
		if sess.Owner() {
			out["pings"] = s.Cron.Pings()
		}
		writeJSON(w, http.StatusOK, out)
	}))
	// Owner only (not in the members' write lists): adds and changes run code on the server.
	mux.HandleFunc("POST /api/servers/{id}/cron", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in proto.CronChange
		if !decode(w, r, &in) {
			return
		}
		run, err := s.Cron.Change(r.PathValue("id"), in)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "cron."+in.Op, r.PathValue("id"), strings.TrimSpace(in.Schedule+" "+in.Command+" "+in.JobID))
		writeJSON(w, http.StatusAccepted, map[string]string{"runId": run})
	}))
	mux.HandleFunc("POST /api/cron/pings", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in cron.Ping
		if !decode(w, r, &in) {
			return
		}
		in.ID = ""
		p, err := s.Cron.SavePing(in)
		if err != nil {
			failCron(w, err)
			return
		}
		s.audit(r, "cron.ping_create", p.ID, p.Name)
		writeJSON(w, http.StatusOK, p)
	}))
	mux.HandleFunc("PUT /api/cron/pings/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in cron.Ping
		if !decode(w, r, &in) {
			return
		}
		in.ID = r.PathValue("id")
		p, err := s.Cron.SavePing(in)
		if err != nil {
			failCron(w, err)
			return
		}
		s.audit(r, "cron.ping_update", p.ID, p.Name)
		writeJSON(w, http.StatusOK, p)
	}))
	mux.HandleFunc("DELETE /api/cron/pings/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Cron.DeletePing(r.PathValue("id")); err != nil {
			failCron(w, err)
			return
		}
		s.audit(r, "cron.ping_delete", r.PathValue("id"), "")
		w.WriteHeader(http.StatusNoContent)
	}))
	// Called by the jobs themselves (curl), with the link's secret token: GET or POST,
	// /fail with an optional message (?m= or the request body) when the job failed.
	ping := func(failed bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			msg := r.URL.Query().Get("m")
			if failed && msg == "" && r.Body != nil {
				b, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
				msg = string(b)
			}
			if len(msg) > 1024 {
				msg = msg[:1024]
			}
			if err := s.Cron.Ping(r.PathValue("token"), failed, strings.TrimSpace(msg)); err != nil {
				http.Error(w, "unknown heartbeat link\n", http.StatusNotFound)
				return
			}
			w.Write([]byte("OK\n"))
		}
	}
	mux.HandleFunc("/api/public/ping/{token}", ping(false))
	mux.HandleFunc("/api/public/ping/{token}/fail", ping(true))
}
