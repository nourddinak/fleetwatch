package statuspage

// Status pages with their own domain need a site in Caddy. The dashboard runs as an
// unprivileged user and cannot edit /etc/caddy, so it reuses the root helper that
// already installs one-click updates (ops-server-update.service):
//  1. The dashboard writes every page domain to DATA_DIR/status-domains and "caddy"
//     to DATA_DIR/update-request.
//  2. The helper (`ops-server self-update`, as root) sees "caddy" and runs CaddySync:
//     it writes /etc/caddy/fleetwatch-status.caddy (one site for all those domains,
//     proxied to this dashboard), adds its import line to the Caddyfile if missing,
//     checks the result with `caddy validate`, and reloads Caddy. Caddy then gets the
//     certificates by itself.
//
// A compromised dashboard can therefore only make Caddy serve valid host names with
// this dashboard as their only backend.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	domainsFile    = "status-domains"
	requestFile    = "update-request" // shared with release: the helper's trigger
	caddyRequest   = "caddy"
	caddyResult    = "caddy-status.json"
	maxCaddyDomain = 100
)

// Paths the root helper touches; tests point them elsewhere.
var (
	Caddyfile     = "/etc/caddy/Caddyfile"
	caddySite     = "/etc/caddy/fleetwatch-status.caddy"
	caddyValidate = func(caddyfile string) ([]byte, error) {
		return exec.Command("caddy", "validate", "--config", caddyfile, "--adapter", "caddyfile").CombinedOutput()
	}
	caddyReload = func() ([]byte, error) { return exec.Command("systemctl", "reload", "caddy").CombinedOutput() }
)

// CaddyResult is the outcome of the helper's last run, written by root.
type CaddyResult struct {
	TS      int64    `json:"ts"`
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Domains []string `json:"domains"`
}

// WebServer is what the dashboard shows about the automatic Caddy setup.
type WebServer struct {
	Auto    bool         `json:"auto"`  // the root helper is installed
	State   string       `json:"state"` // pending | ok | error | outdated | none
	Last    *CaddyResult `json:"last,omitempty"`
	Domains []string     `json:"domains"` // what the pages want
}

func (s *Service) domains() []string {
	var out []string
	for _, c := range s.pages {
		if c.Domain != "" && !slices.Contains(out, c.Domain) {
			out = append(out, c.Domain)
		}
	}
	slices.Sort(out)
	return out
}

// WebServerState reports whether Caddy serves every page domain yet.
func (s *Service) WebServerState() WebServer {
	s.mu.Lock()
	want := s.domains()
	s.mu.Unlock()
	w := WebServer{Domains: want, State: "none"}
	if want == nil {
		w.Domains = []string{}
	}
	if s.DataDir == "" {
		return w
	}
	_, err := os.Stat(s.HelperPath)
	w.Auto = err == nil
	if raw, err := os.ReadFile(filepath.Join(s.DataDir, caddyResult)); err == nil {
		var r CaddyResult
		if json.Unmarshal(raw, &r) == nil {
			w.Last = &r
		}
	}
	raw, _ := os.ReadFile(filepath.Join(s.DataDir, requestFile))
	switch {
	case strings.TrimSpace(string(raw)) == caddyRequest:
		w.State = "pending"
	case w.Last == nil:
		if len(want) > 0 {
			w.State = "outdated"
		}
	case !w.Last.OK:
		w.State = "error"
	case !slices.Equal(w.Last.Domains, want) && !(len(want) == 0 && len(w.Last.Domains) == 0):
		w.State = "outdated"
	default:
		w.State = "ok"
	}
	return w
}

// SyncWebServer asks the root helper to bring Caddy in line with the page domains.
// It waits (in the background) while a platform update holds the helper.
func (s *Service) SyncWebServer() error {
	if s.DataDir == "" {
		return nil
	}
	if _, err := os.Stat(s.HelperPath); err != nil {
		return nil // no helper: the page shows the commands to run by hand
	}
	s.mu.Lock()
	list := strings.Join(s.domains(), "\n") + "\n"
	s.mu.Unlock()
	if err := writeAtomic(filepath.Join(s.DataDir, domainsFile), list); err != nil {
		return err
	}
	req := filepath.Join(s.DataDir, requestFile)
	try := func() bool {
		raw, err := os.ReadFile(req)
		if err == nil && strings.TrimSpace(string(raw)) != caddyRequest {
			return false // an update is being installed; ask again after it
		}
		return writeAtomic(req, caddyRequest+"\n") == nil
	}
	if !try() {
		go func() {
			for range 60 {
				time.Sleep(5 * time.Second)
				if try() {
					return
				}
			}
		}()
	}
	return nil
}

func writeAtomic(path, data string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CaddyRequested reports whether the helper was started for a Caddy sync.
func CaddyRequested(dataDir string) bool {
	raw, err := os.ReadFile(filepath.Join(dataDir, requestFile))
	return err == nil && strings.TrimSpace(string(raw)) == caddyRequest
}

// CaddySync runs as root in the helper. listen is the dashboard's OPS_LISTEN address.
func CaddySync(dataDir, listen string) error {
	defer os.Remove(filepath.Join(dataDir, requestFile))
	res := CaddyResult{TS: time.Now().UnixMilli(), Domains: []string{}}
	err := caddySync(dataDir, listen, &res)
	res.OK = err == nil
	if err != nil {
		res.Error = err.Error()
	}
	out, _ := json.Marshal(res)
	// 0644 in the ops-owned data dir: the dashboard reads it, only root can change it.
	if werr := os.WriteFile(filepath.Join(dataDir, caddyResult), out, 0o644); werr != nil && err == nil {
		err = werr
	}
	return err
}

func caddySync(dataDir, listen string, res *CaddyResult) error {
	raw, err := os.ReadFile(filepath.Join(dataDir, domainsFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The list comes from the unprivileged dashboard: every name is checked again here.
	for _, d := range strings.Fields(string(raw)) {
		d = strings.ToLower(d)
		if len(d) > 253 || !hostRe.MatchString(d) {
			return fmt.Errorf("refused invalid domain %q", d)
		}
		if !slices.Contains(res.Domains, d) {
			res.Domains = append(res.Domains, d)
		}
	}
	if len(res.Domains) > maxCaddyDomain {
		return fmt.Errorf("too many domains (%d)", len(res.Domains))
	}
	slices.Sort(res.Domains)
	if h, p, err := net.SplitHostPort(listen); err != nil || p == "" || strings.ContainsAny(h+p, " {}\n") {
		listen = "127.0.0.1:8090"
	} else if h == "" || h == "0.0.0.0" || h == "::" {
		listen = net.JoinHostPort("127.0.0.1", p)
	}

	main, err := os.ReadFile(Caddyfile)
	if err != nil {
		return fmt.Errorf("Caddy is not set up on this server (%s is missing); point the domain at your own web server instead", Caddyfile)
	}
	// An error already in the existing configuration (often a site file imported from
	// elsewhere) would make any reload fail: say so instead of blaming the new site.
	if out, err := caddyValidate(Caddyfile); err != nil {
		return fmt.Errorf("your current Caddy configuration already has an error, so nothing was changed. Fix it, then press Try again. Caddy says: %s", lastLine(out, err))
	}
	oldSite, siteErr := os.ReadFile(caddySite)

	site := "# Written by Fleetwatch for status pages with their own domain. Changes here are overwritten.\n"
	if len(res.Domains) > 0 {
		site += strings.Join(res.Domains, ", ") + " {\n\tencode zstd gzip\n\treverse_proxy " + listen + "\n}\n"
	}
	if err := os.WriteFile(caddySite, []byte(site), 0o644); err != nil {
		return err
	}
	line := "import " + caddySite
	added := false
	if !slices.ContainsFunc(strings.Split(string(main), "\n"), func(l string) bool { return strings.TrimSpace(l) == line }) {
		text := string(main)
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if err := os.WriteFile(Caddyfile, []byte(text+line+"\n"), 0o644); err != nil {
			return err
		}
		added = true
	}
	undo := func() {
		if siteErr == nil {
			os.WriteFile(caddySite, oldSite, 0o644)
		} else {
			os.Remove(caddySite)
		}
		if added {
			os.WriteFile(Caddyfile, main, 0o644)
		}
	}
	if out, err := caddyValidate(Caddyfile); err != nil {
		undo()
		return fmt.Errorf("Caddy rejected the new site, nothing was changed: %s", lastLine(out, err))
	}
	if out, err := caddyReload(); err != nil {
		return fmt.Errorf("could not reload Caddy: %s", lastLine(out, err))
	}
	return nil
}

func lastLine(out []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}
