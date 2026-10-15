package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/tokens"
)

// tokenDenied are read endpoints an API token may not use: they reveal or manage
// access itself.
var tokenDenied = []string{"/api/sessions", "/api/tokens", "/api/audit", "/api/auth", "/ws"}

// tokenAuthed lets a read-only API token through to GET endpoints.
func (s *server) tokenAuthed(w http.ResponseWriter, r *http.Request, token string, h authedHandler) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusForbidden, "read_only", "API tokens are read-only.")
		return
	}
	for _, p := range tokenDenied {
		if strings.HasPrefix(r.URL.Path, p) {
			writeError(w, http.StatusForbidden, "not_allowed", "API tokens cannot use this endpoint.")
			return
		}
	}
	t, ok := s.Tokens.Verify(token, clientIP(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_token", "Invalid API token.")
		return
	}
	h(w, r, auth.Session{ID: "token:" + t.ID})
}

func (s *server) tokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tokens", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		list, err := s.Tokens.List()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("POST /api/tokens", s.authed(s.createToken))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		t, err := s.Tokens.Revoke(r.PathValue("id"))
		if errors.Is(err, tokens.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Token not found.")
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "token.revoke", t.ID, t.Name)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	mux.HandleFunc("GET /metrics", s.authed(s.prometheus))
}

// createToken needs the admin password, like other actions that grant access.
func (s *server) createToken(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in struct{ Name, Password string }
	if !decode(w, r, &in) {
		return
	}
	if err := s.Auth.ConfirmPassword(client(r), in.Password, "token.create"); err != nil {
		fail(w, err)
		return
	}
	secret, t, err := s.Tokens.Create(in.Name)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "token.create", t.ID, t.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "info": t})
}

// promEscape escapes label values exactly as the exposition format specifies.
var promEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// prom writes the Prometheus text exposition format.
type prom struct {
	b    strings.Builder
	seen map[string]bool
}

func (p *prom) metric(name, typ, help string, value float64, labels ...string) {
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	if !p.seen[name] {
		p.seen[name] = true
		fmt.Fprintf(&p.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	p.b.WriteString(name)
	if len(labels) > 0 {
		p.b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				p.b.WriteByte(',')
			}
			fmt.Fprintf(&p.b, "%s=\"%s\"", labels[i], promEscape.Replace(labels[i+1]))
		}
		p.b.WriteByte('}')
	}
	fmt.Fprintf(&p.b, " %g\n", value)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// prometheus exposes the fleet for Prometheus: scrape /metrics with a bearer token.
func (s *server) prometheus(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var p prom
	p.metric("fleetwatch_info", "gauge", "Fleetwatch version.", 1, "version", s.Version)

	servers, err := s.Fleet.Views()
	if err != nil {
		fail(w, err)
		return
	}
	names := map[string]string{}
	for _, v := range servers {
		names[v.ID] = v.Name
		up := v.Status != "disconnected" && v.Status != "pending"
		l := []string{"server", v.ID, "name", v.Name}
		p.metric("fleetwatch_server_up", "gauge", "1 when the server's agent is connected.", b2f(up), l...)
		if !up {
			continue
		}
		p.metric("fleetwatch_server_cpu_percent", "gauge", "Server CPU use.", v.CPU, l...)
		p.metric("fleetwatch_server_memory_percent", "gauge", "Server memory use.", v.RAM, l...)
		p.metric("fleetwatch_server_disk_percent", "gauge", "Server disk use (fullest watched disk).", v.Disk, l...)
		p.metric("fleetwatch_server_load1", "gauge", "Server 1-minute load average.", v.Load[0], l...)
		p.metric("fleetwatch_server_network_receive_mbps", "gauge", "Server network receive rate.", v.NetRx, l...)
		p.metric("fleetwatch_server_network_transmit_mbps", "gauge", "Server network transmit rate.", v.NetTx, l...)
		p.metric("fleetwatch_server_uptime_seconds", "gauge", "Server uptime.", float64(v.UptimeSec), l...)
	}

	appList, err := s.Apps.List("")
	if err != nil {
		fail(w, err)
		return
	}
	for _, a := range appList {
		l := []string{"app", a.ID, "name", a.Name, "server", names[a.ServerID]}
		p.metric("fleetwatch_app_up", "gauge", "1 when the application is online.", b2f(a.Status == "online"), l...)
		if a.Status == "unknown" {
			continue
		}
		p.metric("fleetwatch_app_cpu_percent", "gauge", "Application CPU use (percent of one core, with children).", a.CPU, l...)
		p.metric("fleetwatch_app_memory_bytes", "gauge", "Application resident memory.", a.RAM*(1<<20), l...)
		p.metric("fleetwatch_app_restarts_total", "counter", "Restarts reported by the process manager.", float64(a.Restarts), l...)
		if a.Health.Enabled && a.HealthStatus != "unknown" {
			p.metric("fleetwatch_app_health_up", "gauge", "1 when the application's health check passes.", b2f(a.HealthStatus == "healthy"), l...)
		}
	}

	open, err := s.Incidents.List(incidents.Filter{Status: "open"})
	if err == nil {
		bySev := map[string]int{"critical": 0, "warning": 0, "info": 0}
		for _, x := range open {
			bySev[x.Severity]++
		}
		sevs := make([]string, 0, len(bySev))
		for k := range bySev {
			sevs = append(sevs, k)
		}
		sort.Strings(sevs)
		for _, k := range sevs {
			p.metric("fleetwatch_incidents_open", "gauge", "Open incidents by severity.", float64(bySev[k]), "severity", k)
		}
	}

	if s.Domains != nil {
		for _, d := range s.Domains.List() {
			if d.Public.NotAfter > 0 {
				p.metric("fleetwatch_certificate_expiry_timestamp_seconds", "gauge", "When the site's certificate expires.", float64(d.Public.NotAfter/1000), "domain", d.Host, "where", "public")
			}
			if d.Origin != nil && d.Origin.NotAfter > 0 {
				p.metric("fleetwatch_certificate_expiry_timestamp_seconds", "gauge", "When the site's certificate expires.", float64(d.Origin.NotAfter/1000), "domain", d.Host, "where", "origin")
			}
			if d.Expires > 0 {
				p.metric("fleetwatch_domain_expiry_timestamp_seconds", "gauge", "When the domain registration expires.", float64(d.Expires/1000), "domain", d.Registered)
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(p.b.String()))
}
