// Package api wires the central server's HTTP routes.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/anomaly"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/backups"
	"fleetwatch/internal/server/commands"
	"fleetwatch/internal/server/cron"
	"fleetwatch/internal/server/dbtool"
	"fleetwatch/internal/server/disk"
	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/docker"
	"fleetwatch/internal/server/domains"
	"fleetwatch/internal/server/envfile"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/forecast"
	"fleetwatch/internal/server/geo"
	"fleetwatch/internal/server/github"
	"fleetwatch/internal/server/hostadmin"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/maintenance"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/mobile"
	"fleetwatch/internal/server/notify"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/security"
	"fleetwatch/internal/server/sites"
	"fleetwatch/internal/server/statuspage"
	"fleetwatch/internal/server/tokens"
	"fleetwatch/internal/server/uptime"
	"fleetwatch/internal/server/valid"
	"fleetwatch/internal/server/vulns"
	"fleetwatch/internal/server/whatsapp"
)

type Deps struct {
	UI         fs.FS
	DataDir    string // OPS_DATA_DIR, for the dashboard's own storage use
	Anomaly    *anomaly.Service
	Version    string
	DB         *sql.DB
	Auth       *auth.Service
	Fleet      *fleet.Hub
	Metrics    *metrics.Store
	Bus        *live.Bus
	PublicURL  string // OPS_PUBLIC_URL; empty = derive from each request
	Apps       *apps.Service
	Logs       *logs.Store
	Commands   *commands.Service
	Incidents  *incidents.Store
	Detector   *incidents.Detector
	Notify     *notify.Service
	AppMetrics *metrics.Store
	Release    *release.Checker
	Maint      *maintenance.Service
	Domains    *domains.Checker
	Tokens     *tokens.Service
	Status     *statuspage.Service
	Disk       *disk.Service
	Forecast   *forecast.Service
	Security   *security.Service
	OnCall     *oncall.Service
	Mobile     *mobile.Service
	Uptime     *uptime.Service
	Cron       *cron.Service
	Backups    *backups.Service
	Databases  *dbtool.Service
	Geo        *geo.Service
	Docker     *docker.Service
	Admin      *hostadmin.Service
	Vulns      *vulns.Service
	GitHub     *github.Service
	Sites      *sites.Service
	EnvFiles   *envfile.Service
	DNS        *dns.Service
	WhatsApp   *whatsapp.Service
	AI         *ai.Service
	EnvHistory *envfile.History
	// ServerFileHistory keeps servers' Caddyfile versions.
	ServerFileHistory *envfile.History
}

type server struct{ Deps }

// New returns the root handler: JSON API under /api, the built UI everywhere else.
func New(d Deps) http.Handler {
	s := &server{d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": d.Version})
	})
	s.authRoutes(mux)
	s.memberRoutes(mux)
	s.activityRoutes(mux)
	s.timelineRoutes(mux)
	s.portalRoutes(mux)
	mux.HandleFunc("GET /api/system/usage", s.authed(s.usage))
	// What a server normally does at this hour (learned): the server page shows it.
	mux.HandleFunc("GET /api/servers/{id}/normal", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		out := map[string]anomaly.Range{}
		if s.Anomaly != nil {
			out = s.Anomaly.Normal(r.PathValue("id"))
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /api/system/cleanup", s.authed(s.cleanup))
	if d.Fleet != nil {
		s.serverRoutes(mux)
		s.agentRoutes(mux)
	}
	if d.Apps != nil {
		s.appRoutes(mux)
	}
	if d.Logs != nil {
		s.logRoutes(mux)
	}
	if d.Commands != nil {
		s.commandRoutes(mux)
	}
	if d.Incidents != nil {
		s.incidentRoutes(mux)
	}
	if d.Tokens != nil {
		s.tokenRoutes(mux)
	}
	if d.Domains != nil {
		mux.HandleFunc("GET /api/domains", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			type item struct {
				domains.Domain
				Renew *domains.Renewal `json:"renew,omitempty"` // the last "renew now"
			}
			renews := s.Domains.Renewals()
			out := []item{}
			for _, d := range s.Domains.List() {
				it := item{Domain: d}
				if r, ok := renews[d.Host]; ok {
					it.Renew = &r
				}
				out = append(out, it)
			}
			writeJSON(w, http.StatusOK, out)
		}))
		mux.HandleFunc("POST /api/domains/{host}/renew", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			if _, err := s.Domains.Renew(r.PathValue("host"), s.actor(r)); err != nil {
				fail(w, err)
				return
			}
			s.audit(r, "domain.renew", "", r.PathValue("host"))
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "renewing"})
		}))
		mux.HandleFunc("POST /api/domains/check", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
			s.Domains.CheckSoon() // results arrive over the live socket ("domains")
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "checking"})
		}))
	}
	if d.Status != nil {
		s.statusRoutes(mux)
	}
	if d.Fleet != nil {
		s.hostRoutes(mux)
	}
	if d.Security != nil {
		s.securityRoutes(mux)
	}
	if d.OnCall != nil {
		s.onCallRoutes(mux)
	}
	if d.Mobile != nil {
		s.phoneRoutes(mux)
		if d.Fleet != nil && d.Apps != nil {
			s.mobileMoreRoutes(mux)
		}
	}
	if d.Uptime != nil {
		s.uptimeRoutes(mux)
	}
	if d.Cron != nil {
		s.cronRoutes(mux)
	}
	if d.Backups != nil {
		s.backupRoutes(mux)
	}
	if d.Databases != nil {
		s.databaseRoutes(mux)
	}
	if d.Geo != nil {
		s.geoRoutes(mux)
	}
	if d.Docker != nil {
		s.dockerRoutes(mux)
	}
	if d.Admin != nil {
		s.adminRoutes(mux)
	}
	if d.GitHub != nil {
		s.integrationRoutes(mux)
		s.myGitHubRoutes(mux)
	}
	if d.Sites != nil {
		s.siteRoutes(mux)
	}
	s.appEnvRoutes(mux) // needs EnvFiles only once the password checked out
	if d.DNS != nil {
		s.myDNSRoutes(mux)
	}
	if d.WhatsApp != nil {
		s.whatsAppRoutes(mux)
	}
	if d.AI != nil {
		s.aiRoutes(mux)
	}
	if d.Maint != nil {
		mux.HandleFunc("GET /api/maintenance", s.authed(s.maintenanceList))
		mux.HandleFunc("PUT /api/maintenance", s.authed(s.maintenanceSet))
	}
	if d.Release != nil {
		mux.HandleFunc("GET /api/system/update", s.authed(s.updateStatus))
		mux.HandleFunc("POST /api/system/update/check", s.authed(s.updateCheck))
		mux.HandleFunc("POST /api/system/update/apply", s.authed(s.updateApply))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
	})
	if d.Sites != nil {
		mux.Handle("/sites/", d.Sites.Files()) // sites on this server, at <dashboard>/sites/<name>/
	}
	mux.Handle("/", static(d.UI))
	var h http.Handler = mux
	if d.Status != nil {
		h = s.statusHost(h)
	}
	return securityHeaders(sameOriginWrites(h))
}

func static(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vite puts content-hashed files in assets/, so they never change.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https://github.com")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// sameOriginWrites is the CSRF defence (with SameSite=Strict cookies): every
// state-changing /api request must carry an Origin header matching the Host.
func sameOriginWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			// Unsubscribe links carry their own secret and are posted by mail apps.
			// The phone app uses its own bearer token, never cookies; GitHub signs its webhooks;
			// heartbeat links are called by scripts, with their secret in the path.
			if strings.HasPrefix(r.URL.Path, "/api/") && !sameOrigin(r) && r.URL.Path != "/api/public/subscribe/remove" && r.URL.Path != "/api/public/ack" && !strings.HasPrefix(r.URL.Path, "/api/mobile/") && !strings.HasPrefix(r.URL.Path, "/api/public/ping/") &&
				r.URL.Path != "/api/integrations/github/webhook" {
				writeError(w, http.StatusForbidden, "forbidden_origin", "cross-origin request refused")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
}

// clientIP is the peer address, or the proxy-reported one when the peer is a
// loopback reverse proxy (Caddy on the same host appends the real client last).
// When that client is a Cloudflare edge (the domain is proxied by Cloudflare), the
// real address is Cloudflare's CF-Connecting-IP. The header is trusted only from
// Cloudflare's own networks, so a direct request to the server cannot fake it.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				host = last
			}
		}
	}
	if isCloudflare(host) {
		if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); net.ParseIP(cf) != nil {
			return cf
		}
	}
	return host
}

func client(r *http.Request) auth.Client {
	return auth.Client{IP: clientIP(r), UserAgent: r.UserAgent()}
}

// decode reads a JSON body (max 1 MiB) into v, rejecting unknown fields.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// fail maps service errors to HTTP responses; unknown errors are logged and hidden.
func fail(w http.ResponseWriter, err error) {
	var locked auth.LockedError
	var input valid.Error
	switch {
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", strconv.Itoa(int(locked.RetryAfter.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "locked", capitalize(locked.Error())+".")
	case errors.As(err, &input):
		writeError(w, http.StatusBadRequest, "invalid_input", capitalize(input.Error())+".")
	case errors.Is(err, fleet.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Server not found.")
	case errors.Is(err, apps.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Application not found.")
	case errors.Is(err, apps.ErrDiscoveryNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Discovery not found.")
	case errors.Is(err, incidents.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Incident not found.")
	case errors.Is(err, commands.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Command not found.")
	case errors.Is(err, fleet.ErrOffline), errors.Is(err, commands.ErrOffline):
		writeError(w, http.StatusConflict, "offline", "The agent is not connected.")
	case errors.Is(err, metrics.ErrBadRange):
		writeError(w, http.StatusBadRequest, "invalid_input", "Unknown time range.")
	case errors.Is(err, auth.ErrNoSession):
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Not signed in.")
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.")
	case errors.Is(err, auth.ErrInvalidCode):
		writeError(w, http.StatusBadRequest, "invalid_code", "Invalid code.")
	case errors.Is(err, auth.ErrWrongPassword):
		writeError(w, http.StatusBadRequest, "wrong_password", "Password is incorrect.")
	case errors.Is(err, auth.ErrTwoFAEnabled), errors.Is(err, auth.ErrTwoFADisabled), errors.Is(err, auth.ErrNoPendingSetup):
		writeError(w, http.StatusConflict, "conflict", capitalize(err.Error())+".")
	default:
		slog.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Internal server error.")
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
