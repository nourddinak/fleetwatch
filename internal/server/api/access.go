package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"

	"fleetwatch/internal/server/auth"
)

// Team members are operators or viewers, optionally limited to some servers. The owner
// can do everything; everything below only narrows what members can do and see.

// ownerOnly paths are off limits to members, reading included: secrets, settings,
// the team itself, status pages and platform updates.
var ownerOnly = []string{"/api/tokens", "/api/sessions", "/api/audit", "/api/members", "/api/settings", "/api/status-pages",
	"/api/notifications/channels", "/api/integrations", "/api/system/update/apply", "/api/system/update/check", "/api/oncall"}

// Writes any signed-in member may make (their own account, sign-out).
var memberWrites = regexp.MustCompile(`^(POST /api/auth/logout|POST /api/account/password|PUT /api/account|POST /api/notifications/read|` +
	`POST /api/2fa/(setup|enable|disable|recovery-codes)|PUT /api/account/alert-rules|PUT /api/handoff|` +
	`POST /api/push/(subscribe|unsubscribe|test)|DELETE /api/push/devices/[^/]+|` +
	`POST /api/phones/pairing|(PATCH|DELETE) /api/phones/[^/]+|POST /api/phones/[^/]+/test|POST /api/servers/[^/]+/(docker/refresh|docker/updates|firewall/scan))$`)

// Writes operators may also make: operate apps and handle incidents, not configure.
var operatorWrites = regexp.MustCompile(`^(POST /api/apps/[^/]+/commands/[^/]+/run|POST /api/apps/[^/]+/(health/check|storage/scan)|` +
	`POST /api/incidents/[^/]+/[^/]+|PUT /api/maintenance|POST /api/servers/[^/]+/(disk/scan|disk/clean|security/scan)|` +
	`POST /api/(domains/check|discoveries/scan)|POST /api/uptime/[^/]+/check|POST /api/servers/[^/]+/(docker/action|updates/scan)|POST /api/vulns/scan|POST /api/sites/[^/]+/redeploy|` +
	`POST /api/sites(/detect)?|PUT /api/sites/[^/]+(/env)?|DELETE /api/sites/[^/]+|POST /api/me/github/(connect|install)|DELETE /api/me/github|` +
	`POST /api/me/dns/(connect|[^/]+/refresh)|DELETE /api/me/dns/[^/]+|PUT /api/me/dns/[^/]+/(sync-blocks|zones/[^/]+/attack)|` +
	`POST /api/sites/[^/]+/dns|PUT /api/sites/[^/]+/dns/proxy)$`)

var (
	serverPath   = regexp.MustCompile(`^/api/servers/([^/]+)`)
	appPath      = regexp.MustCompile(`^/api/apps/([^/]+)`)
	incidentPath = regexp.MustCompile(`^/api/incidents/([^/]+)`)
	commandPath  = regexp.MustCompile(`^/api/commands/([^/]+)`)
	runPath      = regexp.MustCompile(`^/api/runs/([^/]+)`)
	sitePath     = regexp.MustCompile(`^/api/sites/([^/]+)`)
	appEnvPath   = regexp.MustCompile(`^/api/(apps/[^/]+/env|servers/[^/]+/web)(/history)?$`)
)

// scope answers "may this session see that server or app?" with a small cache of
// app → server lookups.
type scope struct {
	s       *server
	servers []string // nil = all
	mu      sync.Mutex
	appSrv  map[string]string
}

func (s *server) scopeOf(sess auth.Session) *scope {
	if sess.Owner() || sess.Servers == nil {
		return nil
	}
	return &scope{s: s, servers: sess.Servers, appSrv: map[string]string{}}
}

func (sc *scope) server(id string) bool { return sc == nil || slices.Contains(sc.servers, id) }

func (sc *scope) app(id string) bool {
	if sc == nil {
		return true
	}
	sc.mu.Lock()
	srv, ok := sc.appSrv[id]
	sc.mu.Unlock()
	if !ok && sc.s.Apps != nil {
		if v, err := sc.s.Apps.Get(id); err == nil {
			srv = v.ServerID
		}
		sc.mu.Lock()
		sc.appSrv[id] = srv
		sc.mu.Unlock()
	}
	return sc.server(srv)
}

// keep decides about one JSON object; idIsServer means its "id" is a server ID.
func (sc *scope) keep(o map[string]any, idIsServer bool) bool {
	if v, ok := o["serverId"].(string); ok && v != "" {
		return sc.server(v)
	}
	if v, ok := o["appId"].(string); ok && v != "" {
		return sc.app(v)
	}
	if v, ok := o["id"].(string); ok && idIsServer {
		return sc.server(v)
	}
	return true
}

// filter drops what the session may not see from a decoded JSON value: list items,
// lists and objects nested at any depth, and maps keyed by server or app ID.
func (sc *scope) filter(v any, idIsServer bool) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			if o, ok := e.(map[string]any); ok && !sc.keep(o, idIsServer) {
				continue
			}
			out = append(out, sc.filter(e, false))
		}
		return out
	case map[string]any:
		for k, e := range x {
			switch {
			case k == "servers" || k == "apps":
				if m, ok := e.(map[string]any); ok { // keyed by ID (forecasts, maintenance)
					for id := range m {
						if (k == "servers" && !sc.server(id)) || (k == "apps" && !sc.app(id)) {
							delete(m, id)
						}
					}
					continue
				}
				x[k] = sc.filter(e, k == "servers")
			default:
				x[k] = sc.filter(e, false)
			}
		}
		return x
	}
	return v
}

// allowed checks role and server limits before a handler runs. It answers 403 itself.
func (s *server) allowed(w http.ResponseWriter, r *http.Request, sess auth.Session) bool {
	if sess.Owner() {
		return true
	}
	deny := func(msg string) bool {
		writeError(w, http.StatusForbidden, "forbidden", msg)
		return false
	}
	p := r.URL.Path
	for _, prefix := range ownerOnly {
		if strings.HasPrefix(p, prefix) {
			return deny("Only the owner can do this.")
		}
	}
	if appEnvPath.MatchString(p) { // an app's secrets
		return deny("Only the owner can do this.")
	}
	key := r.Method + " " + p
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case read, memberWrites.MatchString(key):
	case sess.Role == auth.RoleOperator && operatorWrites.MatchString(key):
	default:
		if sess.Role == auth.RoleViewer {
			return deny("Your account can only look, not change things.")
		}
		return deny("Only the owner can do this.")
	}
	sc := s.scopeOf(sess)
	if sc == nil {
		return true
	}
	hidden := func() bool { return deny("You do not have access to this server.") }
	if m := serverPath.FindStringSubmatch(p); m != nil && !sc.server(m[1]) {
		return hidden()
	}
	if m := appPath.FindStringSubmatch(p); m != nil && !sc.app(m[1]) {
		return hidden()
	}
	if m := incidentPath.FindStringSubmatch(p); m != nil && s.Incidents != nil {
		if x, err := s.Incidents.Get(m[1]); err == nil && !sc.server(x.ServerID) {
			return hidden()
		}
	}
	if m := commandPath.FindStringSubmatch(p); m != nil && s.Commands != nil {
		if c, err := s.Commands.Get(m[1]); err == nil && !sc.app(c.AppID) {
			return hidden()
		}
	}
	if m := sitePath.FindStringSubmatch(p); m != nil && s.Sites != nil {
		if st, err := s.Sites.Get(m[1]); err == nil && !sc.server(st.ServerID) {
			return hidden()
		}
	}
	if m := runPath.FindStringSubmatch(p); m != nil && s.Commands != nil {
		if run, err := s.Commands.GetRun(m[1]); err == nil && !sc.app(run.AppID) {
			return hidden()
		}
	}
	q := r.URL.Query()
	if (q.Get("server") != "" && !sc.server(q.Get("server"))) || (q.Get("app") != "" && !sc.app(q.Get("app"))) {
		return hidden()
	}
	if p == "/api/metrics" && q.Get("server") == "" {
		return deny("Pick a server: fleet-wide charts include servers you cannot see.")
	}
	if p == "/api/maintenance" && r.Method == http.MethodPut { // the body names the server or app
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body = io.NopCloser(bytes.NewReader(body))
		var in struct{ Kind, ID string }
		json.Unmarshal(body, &in)
		if (in.Kind == "server" && !sc.server(in.ID)) || (in.Kind == "app" && !sc.app(in.ID)) {
			return hidden()
		}
	}
	return true
}

// scopedWriter buffers a JSON answer so the parts a member may not see can be removed.
type scopedWriter struct {
	http.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (w *scopedWriter) WriteHeader(code int)        { w.status = code }
func (w *scopedWriter) Write(b []byte) (int, error) { return w.buf.Write(b) }

func (w *scopedWriter) flush(sc *scope, idIsServer bool) {
	out := w.buf.Bytes()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		var v any
		if json.Unmarshal(out, &v) == nil {
			if b, err := json.Marshal(sc.filter(v, idIsServer)); err == nil {
				out = append(b, '\n')
			}
		}
	}
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	w.ResponseWriter.Write(out)
}

// liveFilter rewrites (or drops, returning nil) a live event for a limited member.
func (sc *scope) liveFilter(raw []byte) []byte {
	if sc == nil {
		return raw
	}
	var m struct {
		Type string `json:"type"`
		Data any    `json:"data"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	byID := m.Type == "server" || m.Type == "server.removed" || m.Type == "servers"
	if o, ok := m.Data.(map[string]any); ok && !sc.keep(o, byID) {
		return nil
	}
	m.Data = sc.filter(m.Data, byID)
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
