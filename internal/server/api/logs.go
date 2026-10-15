package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/logs"
)

func (s *server) logRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/logs", s.authed(s.searchLogs))
	mux.HandleFunc("GET /api/logs/stats", s.authed(s.logStats))
	mux.HandleFunc("GET /api/settings/logs", s.authed(s.getLogSettings))
	mux.HandleFunc("PUT /api/settings/logs", s.authed(s.putLogSettings))
	mux.HandleFunc("GET /ws/logs", s.authed(s.logsWS))
}

func csv(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func int64Param(r *http.Request, k string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(k), 10, 64)
	return n
}

// searchLogs pages through history newest-first. Pass the smallest id of a page as
// `before` to get the next (older) page.
func (s *server) searchLogs(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	q := r.URL.Query()
	if len(q.Get("q")) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_input", "Search text is too long.")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	query := logs.Query{
		AppID: q.Get("app"), ServerID: q.Get("server"), Text: q.Get("q"),
		Severities: csv(q.Get("severity")), Sources: csv(q.Get("source")), Streams: csv(q.Get("stream")),
		From: int64Param(r, "from"), To: int64Param(r, "to"), Before: int64Param(r, "before"), Limit: limit,
	}
	lines, err := s.Logs.Search(query)
	if err != nil {
		fail(w, err)
		return
	}
	var next int64
	if len(lines) > 0 && len(lines) >= max(1, min(limit, 500)) {
		next = lines[len(lines)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "nextBefore": next})
}

func (s *server) logStats(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	st, err := s.Logs.Stats()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) getLogSettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, s.Logs.Settings())
}

func (s *server) putLogSettings(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in logs.Settings
	if !decode(w, r, &in) {
		return
	}
	if err := s.Logs.SaveSettings(in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "settings.logs", "", "")
	s.Fleet.BroadcastConfig() // max line length is enforced on agents too
	writeJSON(w, http.StatusOK, in)
}

// logsWS streams newly stored lines for one app (?app=) or server (?server=), or
// all lines. It sends {"type":"lines","data":[...]} and {"type":"dropped","data":n}.
func (s *server) logsWS(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	token := cookieToken(r)
	c, err := websocket.Accept(w, r, nil) // same-origin only
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	sub := s.Logs.Live.Subscribe(r.URL.Query().Get("app"), r.URL.Query().Get("server"))
	defer s.Logs.Live.Unsubscribe(sub)
	check := time.NewTicker(time.Minute)
	defer check.Stop()
	gap := time.NewTicker(2 * time.Second)
	defer gap.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			if sess, err := s.Auth.Authenticate(token); err != nil || sess.MFAPending {
				c.Close(websocket.StatusPolicyViolation, "session ended")
				return
			}
		case <-gap.C:
			if n := sub.TakeDropped(); n > 0 {
				if wsWriteJSON(ctx, c, map[string]any{"type": "dropped", "data": n}) != nil {
					return
				}
			}
		case lines := <-sub.C:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := wsWriteJSON(wctx, c, map[string]any{"type": "lines", "data": lines})
			cancel()
			if err != nil {
				return
			}
		}
	}
}
