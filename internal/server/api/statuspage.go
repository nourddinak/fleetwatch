package api

import (
	"errors"
	"html"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/statuspage"
)

// pageSummary is a status page in the list, without its logo and contents.
type pageSummary struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Title      string `json:"title"`
	Enabled    bool   `json:"enabled"`
	Domain     string `json:"domain"`
	Components int    `json:"components"`
	Notices    int    `json:"notices"`
	UpdatedAt  int64  `json:"updatedAt"`
}

func (s *server) statusRoutes(mux *http.ServeMux) {
	// Public: no sign-in. Returns only what the admin chose to publish.
	public := func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.Status.Find(requestHost(r), r.PathValue("slug"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "There is no status page here.")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*") // embeddable in the admin's own sites
		writeJSON(w, http.StatusOK, s.Status.Public(c))
	}
	mux.HandleFunc("GET /api/public/status", public)
	mux.HandleFunc("GET /api/public/status/{slug}", public)

	mux.HandleFunc("GET /api/status-pages", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		out := []pageSummary{}
		for _, c := range s.Status.Pages() {
			n := 0
			for _, g := range c.Groups {
				n += len(g.Components)
			}
			out = append(out, pageSummary{c.ID, c.Slug, c.Title, c.Enabled, c.Domain, n, len(c.Notices), c.UpdatedAt})
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("GET /api/status-pages/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		c, ok := s.Status.Page(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "Status page not found.")
			return
		}
		writeJSON(w, http.StatusOK, c)
	}))
	save := func(w http.ResponseWriter, r *http.Request, id string) {
		var c statuspage.Config
		if !decode(w, r, &c) {
			return
		}
		c.ID = id
		saved, err := s.Status.Save(c)
		if errors.Is(err, statuspage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Status page not found.")
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		state := "off"
		if saved.Enabled {
			state = "on"
		}
		s.audit(r, "statuspage.save", saved.ID, saved.Title+" ("+state+")")
		status := http.StatusOK
		if id == "" {
			status = http.StatusCreated
		}
		writeJSON(w, status, saved)
	}
	mux.HandleFunc("POST /api/status-pages", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { save(w, r, "") }))
	mux.HandleFunc("PUT /api/status-pages/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		save(w, r, r.PathValue("id"))
	}))
	mux.HandleFunc("DELETE /api/status-pages/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		id := r.PathValue("id")
		if err := s.Status.Delete(id); errors.Is(err, statuspage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Status page not found.")
			return
		} else if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "statuspage.delete", id, "")
		w.WriteHeader(http.StatusNoContent)
	}))
}

var titleRe = regexp.MustCompile(`<title>[^<]*</title>`)

// statusHTML is index.html marked so the UI renders that public status page.
func (s *server) statusHTML(w http.ResponseWriter, c statuspage.Config, ok bool) {
	b, err := fs.ReadFile(s.UI, "index.html")
	if err != nil || !ok {
		http.NotFound(w, nil)
		return
	}
	// The slug is validated (a-z, 0-9, dashes), so it is safe in an attribute.
	page := strings.Replace(string(b), `<div id="root">`, `<div id="root" data-page="status" data-slug="`+c.Slug+`">`, 1)
	page = titleRe.ReplaceAllLiteralString(page, "<title>"+html.EscapeString(c.Title)+"</title>")
	page = strings.Replace(page, `<link rel="manifest" href="/manifest.json" />`, "", 1) // not the dashboard app
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(page))
}

func requestHost(r *http.Request) string {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// statusHost serves /status and /status/<slug>, and on a page's own domain serves
// only that page: the dashboard and its API are not reachable through that name.
func (s *server) statusHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := requestHost(r)
		_, own := s.Status.ForHost(host)
		p := r.URL.Path
		slug, isStatus := strings.TrimPrefix(p, "/status/"), strings.HasPrefix(p, "/status/")
		switch {
		case p == "/status" || isStatus || (own && p == "/"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !isStatus {
				slug = ""
			}
			c, ok := s.Status.Find(host, strings.TrimSuffix(slug, "/"))
			s.statusHTML(w, c, ok)
		case !own, strings.HasPrefix(p, "/assets/"), p == "/api/public/status", strings.HasPrefix(p, "/api/public/status/"), p == "/api/health",
			p == "/icon-192.png", p == "/apple-touch-icon.png":
			next.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
