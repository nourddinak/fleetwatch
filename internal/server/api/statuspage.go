package api

import (
	"html"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/statuspage"
)

func (s *server) statusRoutes(mux *http.ServeMux) {
	// Public: no sign-in. Returns only what the admin chose to publish.
	mux.HandleFunc("GET /api/public/status", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.Status.Public()
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "There is no status page.")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*") // embeddable in the admin's own sites
		writeJSON(w, http.StatusOK, p)
	})
	mux.HandleFunc("GET /api/status-page", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, s.Status.Config())
	}))
	mux.HandleFunc("PUT /api/status-page", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var c statuspage.Config
		if !decode(w, r, &c) {
			return
		}
		saved, err := s.Status.Save(c)
		if err != nil {
			fail(w, err)
			return
		}
		state := "off"
		if saved.Enabled {
			state = "on"
		}
		s.audit(r, "statuspage.update", "", state)
		writeJSON(w, http.StatusOK, saved)
	}))
}

var titleRe = regexp.MustCompile(`<title>[^<]*</title>`)

// statusHTML is index.html marked so the UI renders the public status page.
func (s *server) statusHTML(w http.ResponseWriter) {
	b, err := fs.ReadFile(s.UI, "index.html")
	c := s.Status.Config()
	if err != nil || !c.Enabled {
		http.NotFound(w, nil)
		return
	}
	page := strings.Replace(string(b), `<div id="root">`, `<div id="root" data-page="status">`, 1)
	page = titleRe.ReplaceAllLiteralString(page, "<title>"+html.EscapeString(c.Title)+"</title>")
	page = strings.Replace(page, `<link rel="manifest" href="/manifest.json" />`, "", 1) // not the dashboard app
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(page))
}

// statusHost serves /status, and on the status page's own domain serves only the
// status page: the dashboard and its API are not reachable through that name.
func (s *server) statusHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		own := host != "" && host == s.Status.Domain()
		switch {
		case r.URL.Path == "/status" || (own && r.URL.Path == "/"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.statusHTML(w)
		case !own, strings.HasPrefix(r.URL.Path, "/assets/"), r.URL.Path == "/api/public/status", r.URL.Path == "/api/health",
			r.URL.Path == "/icon-192.png", r.URL.Path == "/apple-touch-icon.png":
			next.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
