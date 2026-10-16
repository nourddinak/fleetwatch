package api

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"fleetwatch/internal/server/auth"
)

// statusSetup tells the admin exactly which DNS record a page's own domain needs,
// whether it already points here, and whether Caddy was set up for it.
type statusSetup struct {
	Domain           string   `json:"domain"`
	Zone             string   `json:"zone"`             // the registered domain, e.g. example.com
	Name             string   `json:"name"`             // record name inside the zone ("@" = the zone itself)
	DashboardHost    string   `json:"dashboardHost"`    // where this dashboard is reached
	DashboardProxied bool     `json:"dashboardProxied"` // its name answers with Cloudflare addresses
	ServerIP         string   `json:"serverIp"`         // this dashboard's server, when an agent runs on it
	RecordType       string   `json:"recordType"`       // CNAME | A
	RecordValue      string   `json:"recordValue"`      // "" = unknown IP
	Resolves         []string `json:"resolves"`         // what the domain answers now
	DNS              string   `json:"dns"`              // ok | cloudflare | elsewhere | missing
	WebServer        any      `json:"webServer"`
}

func publicIP(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback()
}

func lookup(ctx context.Context, host string) []string {
	addrs, _ := net.DefaultResolver.LookupHost(ctx, host)
	slices.Sort(addrs)
	return addrs
}

func (s *server) statusSetupInfo(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	out := statusSetup{Domain: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain"))), DashboardHost: requestHost(r), Resolves: []string{},
		WebServer: s.Status.WebServerState()}
	dash := lookup(ctx, out.DashboardHost)
	out.DashboardProxied = len(dash) > 0 && !slices.ContainsFunc(dash, func(ip string) bool { return !isCloudflare(ip) })

	// This dashboard's own server: the one whose web server serves the dashboard's name.
	if s.Apps != nil && s.Fleet != nil {
		for _, d := range s.Apps.Domains() {
			if strings.EqualFold(d.Host, out.DashboardHost) && d.ServerID != "" {
				if rec, err := s.Fleet.Reg.Get(d.ServerID); err == nil && publicIP(rec.PublicIP) {
					out.ServerIP = rec.PublicIP
				}
				break
			}
		}
	}
	if out.ServerIP == "" && !out.DashboardProxied {
		if i := slices.IndexFunc(dash, publicIP); i >= 0 {
			out.ServerIP = dash[i] // the dashboard's name points straight at its server
		}
	}

	// Behind Cloudflare's proxy (or reached by IP) a CNAME to the dashboard would not
	// reach the server directly, so then an A record with the server's IP is the way.
	if net.ParseIP(out.DashboardHost) != nil || out.DashboardHost == "localhost" || out.DashboardProxied {
		out.RecordType, out.RecordValue = "A", out.ServerIP
	} else {
		out.RecordType, out.RecordValue = "CNAME", out.DashboardHost
	}
	if out.Domain == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Zone, out.Name = out.Domain, "@"
	if zone, err := publicsuffix.EffectiveTLDPlusOne(out.Domain); err == nil {
		out.Zone = zone
		if zone != out.Domain {
			out.Name = strings.TrimSuffix(out.Domain, "."+zone)
		}
	}
	out.Resolves = lookup(ctx, out.Domain)
	if out.Resolves == nil {
		out.Resolves = []string{}
	}
	switch {
	case len(out.Resolves) == 0:
		out.DNS = "missing"
	case out.ServerIP != "" && slices.Contains(out.Resolves, out.ServerIP), !out.DashboardProxied && slices.Equal(out.Resolves, dash):
		out.DNS = "ok"
	case !slices.ContainsFunc(out.Resolves, func(ip string) bool { return !isCloudflare(ip) }):
		out.DNS = "cloudflare" // proxied: the real target is hidden
	default:
		out.DNS = "elsewhere"
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) statusSetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status-pages/setup", s.authed(s.statusSetupInfo))
	mux.HandleFunc("POST /api/status-pages/setup/apply", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Status.SyncWebServer(); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "statuspage.webserver", "", "requested")
		writeJSON(w, http.StatusAccepted, s.Status.WebServerState())
	}))
}
