package api

import (
	"net/http"
	"strings"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
)

// The client portal: what a client member may see of their own things, and nothing
// else (no server names, addresses or other apps). Branded like their first status page.
type portalApp struct {
	Name      string           `json:"name"`
	Status    string           `json:"status"` // online | offline | warning | …
	Health    string           `json:"health"` // passing | failed | "" (no health check)
	Domain    string           `json:"domain,omitempty"`
	Incidents []portalIncident `json:"incidents"`
}

type portalIncident struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Since    int64  `json:"since"`
}

type portalCheck struct {
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	Status    string  `json:"status"`
	Uptime24h float64 `json:"uptime24h"`
	Uptime30d float64 `json:"uptime30d"`
	P95       int64   `json:"p95"`
	Days      any     `json:"days"`
}

type portalPage struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Overall string `json:"overall"`
}

func (s *server) portalRoutes(mux *http.ServeMux) {
	// A client gets their own portal; the owner can look at a client's (?member=).
	mux.HandleFunc("GET /api/portal", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		id := sess.MemberID
		if sess.Owner() {
			id = r.URL.Query().Get("member")
		}
		m, err := s.Auth.GetMember(id)
		if err != nil || m.Role != auth.RoleClient {
			writeError(w, http.StatusNotFound, "not_found", "No portal here.")
			return
		}
		out := map[string]any{"name": m.DisplayName, "apps": []portalApp{}, "checks": []portalCheck{}, "pages": []portalPage{},
			"brand": map[string]string{"title": "Status", "accent": "#16a34a"}}
		if out["name"] == "" {
			out["name"] = m.Username
		}
		apps := []portalApp{}
		for _, id := range m.Portal.Apps {
			v, err := s.Apps.Get(id)
			if err != nil {
				continue
			}
			a := portalApp{Name: v.Name, Status: v.Status, Health: v.HealthStatus, Incidents: []portalIncident{}}
			if v.Domain != nil {
				a.Domain = *v.Domain
			}
			if list, err := s.Incidents.List(incidents.Filter{AppID: id, Status: "open", Limit: 20}); err == nil {
				for _, x := range list {
					a.Incidents = append(a.Incidents, portalIncident{Title: x.Title, Severity: x.Severity, Since: x.FirstAt})
				}
			}
			apps = append(apps, a)
		}
		out["apps"] = apps
		if s.Uptime != nil {
			checks := []portalCheck{}
			for _, id := range m.Portal.Checks {
				c, err := s.Uptime.Get(id)
				if err != nil {
					continue
				}
				v := s.Uptime.View(c)
				checks = append(checks, portalCheck{Name: v.Name, URL: v.URL, Status: v.Status, Uptime24h: v.Uptime24h, Uptime30d: v.Uptime30d, P95: v.P95, Days: v.Days})
			}
			out["checks"] = checks
		}
		if s.Status != nil {
			pages := []portalPage{}
			base := strings.TrimRight(s.publicURL(r), "/")
			for i, id := range m.Portal.Pages {
				c, ok := s.Status.Page(id)
				if !ok || !c.Enabled {
					continue
				}
				url := base + "/status"
				if c.Slug != "" {
					url += "/" + c.Slug
				}
				if c.Domain != "" {
					url = "https://" + c.Domain
				}
				pages = append(pages, portalPage{Title: c.Title, URL: url, Overall: s.Status.Public(c).Overall})
				if i == 0 {
					out["brand"] = map[string]string{"title": c.Title, "logo": c.Logo, "accent": c.Accent}
				}
			}
			out["pages"] = pages
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}))
}
