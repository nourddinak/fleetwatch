// Package security collects each server's security findings (from its agent and from
// checks the dashboard makes itself), records SSH logins with where they came from,
// and alerts on new problems, new countries and new SSH keys.
package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/settings"
)

// MinAgent is the first agent version with security checks.
const MinAgent = "v0.9.32"

const (
	scanEvery     = 12 * time.Hour
	expectedKey   = "securityExpectedPorts"
	maxLoginsKept = 90 * 24 * time.Hour
)

var ErrOldAgent = errors.New("this server's agent is too old for security checks; update the agent first")

// ServerReport is one server's findings, newest scan.
type ServerReport struct {
	ServerID string          `json:"serverId"`
	At       int64           `json:"at"` // agent scan time; 0 = never
	Findings []proto.Finding `json:"findings"`
	Scanning bool            `json:"scanning"`
	OldAgent bool            `json:"oldAgent"`
}

// Login is a stored SSH login.
type Login struct {
	ServerID   string `json:"serverId"`
	TS         int64  `json:"ts"`
	User       string `json:"user"`
	IP         string `json:"ip"`
	Method     string `json:"method"`
	Key        string `json:"key"`
	Country    string `json:"country"` // ISO code, "" unknown, "LAN" private network
	NewCountry bool   `json:"newCountry"`
	NewKey     bool   `json:"newKey"`
	// Automated logins: Source "github" (a GitHub Actions runner) or a key the admin
	// named (Label).
	Source    string `json:"source,omitempty"`
	Label     string `json:"label,omitempty"`
	Automated bool   `json:"automated"`
}

// LoginList is a page of logins and how many automated ones were left out.
type LoginList struct {
	Items  []Login `json:"items"`
	Hidden int     `json:"hidden"`
}

type Service struct {
	db           *sql.DB
	Send         func(serverID, typ string, v any) error
	ServerIDs    func() []string
	AgentVersion func(serverID string) string
	Connected    func(serverID string) bool
	Name         func(serverID string) string
	// Ports, Domains and AppPaths come from the apps service.
	Ports    func(serverID string) ([]apps.PortRow, error)
	Domains  func() []apps.DomainRef
	AppPaths func(serverID string) []string
	// Notify tells the admin (all notification channels).
	Notify func(title, body, severity string)
	// Country looks up where an IP address is registered; replaced in tests.
	Country func(ctx context.Context, ip string) string
	// Fetch reads a public URL for the web checks; replaced in tests.
	Fetch func(ctx context.Context, url string) (int, []byte)
	// IsGitHub tells GitHub Actions runner addresses; replaced in tests.
	IsGitHub func(ctx context.Context, ip string) bool
	Now      func() time.Time

	mu       sync.Mutex
	scanning map[string]time.Time
	web      map[string][]proto.Finding // per server, from the dashboard's own web checks
	webAt    time.Time
	ports    map[string]map[string]bool // server → public port findings seen last (WatchPorts)
}

func New(d *sql.DB) *Service {
	s := &Service{db: d, Now: time.Now, scanning: map[string]time.Time{}, web: map[string][]proto.Finding{}, ports: map[string]map[string]bool{}}
	rd := &rdap{client: &http.Client{Timeout: 15 * time.Second}}
	s.Country = rd.country
	gh := &githubRanges{}
	s.IsGitHub = gh.contains
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s.Fetch = func(ctx context.Context, url string) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, nil
		}
		req.Header.Set("User-Agent", "Fleetwatch-SecurityCheck/1")
		res, err := client.Do(req)
		if err != nil {
			return 0, nil
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return res.StatusCode, b
	}
	return s
}

func (s *Service) oldAgent(id string) bool {
	return s.AgentVersion != nil && release.Newer(MinAgent, s.AgentVersion(id))
}

// Scan asks a server's agent for a fresh report and re-runs the web checks.
func (s *Service) Scan(serverID string) error {
	if s.oldAgent(serverID) {
		return ErrOldAgent
	}
	if err := s.Send(serverID, proto.TypeSecurityScan, proto.SecurityScan{AppPaths: s.AppPaths(serverID)}); err != nil {
		return err
	}
	s.mu.Lock()
	s.scanning[serverID] = s.Now()
	s.mu.Unlock()
	go s.webChecks(context.Background())
	return nil
}

// OnReport stores an agent's findings and tells the admin about new serious ones.
func (s *Service) OnReport(serverID string, data json.RawMessage) {
	var rep proto.SecurityReport
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	s.mu.Lock()
	delete(s.scanning, serverID)
	s.mu.Unlock()
	before := s.findings(serverID)
	raw, _ := json.Marshal(rep.Findings)
	s.db.Exec(`INSERT INTO security_reports (server_id, at, findings) VALUES (?, ?, ?)
		ON CONFLICT (server_id) DO UPDATE SET at = excluded.at, findings = excluded.findings`, serverID, rep.At, string(raw))
	s.alertNew(serverID, before, s.findings(serverID))
}

func (s *Service) agentFindings(serverID string) ([]proto.Finding, int64) {
	var raw string
	var at int64
	if s.db.QueryRow(`SELECT at, findings FROM security_reports WHERE server_id = ?`, serverID).Scan(&at, &raw) != nil {
		return nil, 0
	}
	var fs []proto.Finding
	json.Unmarshal([]byte(raw), &fs)
	return fs, at
}

// expected are ports the admin marked as meant to be public, per server.
func (s *Service) expected() map[string][]int {
	m := map[string][]int{}
	settings.Load(s.db, expectedKey, &m)
	return m
}

// SetExpected marks (or unmarks) a public port as intended.
func (s *Service) SetExpected(serverID string, port int, on bool) error {
	m := s.expected()
	list := slices.DeleteFunc(m[serverID], func(p int) bool { return p == port })
	if on {
		list = append(list, port)
	}
	m[serverID] = list
	return settings.Save(s.db, expectedKey, m)
}

var webServers = map[string]bool{"caddy": true, "nginx": true, "apache2": true, "httpd": true, "traefik": true, "haproxy": true, "envoy": true}

// portFindings flags TCP ports open to the internet that nobody said should be.
func (s *Service) portFindings(serverID string) []proto.Finding {
	rows, err := s.Ports(serverID)
	if err != nil {
		return nil
	}
	ok := s.expected()[serverID]
	seen := map[int]bool{}
	var fs []proto.Finding
	for _, r := range rows {
		public := r.Bind == "0.0.0.0" || r.Bind == "::" || r.Bind == "*" || r.Bind == "[::]"
		if !public && r.Bind != "" {
			if a, err := netip.ParseAddr(strings.Trim(r.Bind, "[]")); err == nil && a.IsGlobalUnicast() && !a.IsPrivate() {
				public = true
			}
		}
		proc := strings.ToLower(r.Process)
		if !public || !strings.EqualFold(r.Proto, "tcp") || seen[r.Port] || slices.Contains(ok, r.Port) ||
			(r.Port == 22 && strings.HasPrefix(proc, "sshd")) || ((r.Port == 80 || r.Port == 443) && webServers[proc]) {
			continue
		}
		seen[r.Port] = true
		who := r.Process
		if r.AppName != "" {
			who = r.AppName + " (" + r.Process + ")"
		}
		fs = append(fs, proto.Finding{ID: fmt.Sprintf("port:%d", r.Port), Severity: "warning",
			Title:  fmt.Sprintf("Port %d (%s) is open to the internet", r.Port, who),
			Detail: "It listens on all addresses, so anyone can reach it directly, around your web server and its HTTPS.",
			Fix: fmt.Sprintf("If it should only be reached through your site: make the program listen on 127.0.0.1 instead of 0.0.0.0, "+
				"or block it with the firewall: sudo ufw deny %d\nIf it is meant to be public, mark it as expected.", r.Port)})
	}
	return fs
}

// webChecks fetches /.git/HEAD and /.env from every site: if they answer, anyone can
// download the source history or the secrets.
func (s *Service) webChecks(ctx context.Context) {
	s.mu.Lock()
	if s.Now().Sub(s.webAt) < 5*time.Minute {
		s.mu.Unlock()
		return
	}
	s.webAt = s.Now()
	s.mu.Unlock()
	out := map[string][]proto.Finding{}
	for _, d := range s.Domains() {
		if d.ServerID == "" {
			continue
		}
		base := "https://" + d.Host
		if st, b := s.Fetch(ctx, base+"/.git/HEAD"); st == 200 && strings.HasPrefix(strings.TrimSpace(string(b)), "ref:") {
			out[d.ServerID] = append(out[d.ServerID], proto.Finding{ID: "web_git:" + d.Host, Severity: "critical",
				Title:  base + "/.git/ can be downloaded",
				Detail: "Anyone can fetch the whole source history, often including passwords and keys ever committed.",
				Fix:    "Do not serve the .git folder. In Caddy add to the site:  @hidden path /.git/* /.env*\n  respond @hidden 404\nthen: sudo systemctl reload caddy"})
		}
		if st, b := s.Fetch(ctx, base+"/.env"); st == 200 && looksLikeEnv(b) {
			out[d.ServerID] = append(out[d.ServerID], proto.Finding{ID: "web_env:" + d.Host, Severity: "critical",
				Title:  base + "/.env can be downloaded",
				Detail: "The file with your secrets is public. Treat every key in it as leaked and change them.",
				Fix:    "Block it in Caddy:  @hidden path /.git/* /.env*\n  respond @hidden 404\nthen reload Caddy, and rotate the secrets that were in it."})
		}
	}
	s.mu.Lock()
	before := s.web
	s.web = out
	s.mu.Unlock()
	for _, id := range s.ServerIDs() {
		s.alertNew(id, before[id], out[id])
	}
}

// looksLikeEnv tells a real KEY=value file from an HTML page served for any path.
func looksLikeEnv(b []byte) bool {
	t := strings.TrimSpace(string(b))
	return t != "" && !strings.HasPrefix(t, "<") && strings.Contains(t, "=") && !strings.Contains(strings.ToLower(t[:min(len(t), 200)]), "<html")
}

// findings is everything known about a server right now.
func (s *Service) findings(serverID string) []proto.Finding {
	fs, _ := s.agentFindings(serverID)
	fs = append(fs, s.portFindings(serverID)...)
	s.mu.Lock()
	fs = append(fs, s.web[serverID]...)
	s.mu.Unlock()
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(fs, func(i, j int) bool { return rank[fs[i].Severity] < rank[fs[j].Severity] })
	return fs
}

func (s *Service) alertNew(serverID string, before, after []proto.Finding) {
	if s.Notify == nil {
		return
	}
	known := map[string]bool{}
	for _, f := range before {
		known[f.ID] = true
	}
	for _, f := range after {
		if !known[f.ID] && (f.Severity == "critical" || f.Severity == "warning") {
			s.Notify("Security: "+s.Name(serverID)+": "+f.Title, f.Detail+"\n\nHow to fix:\n"+f.Fix, f.Severity)
		}
	}
}

// Reports returns every server's findings.
func (s *Service) Reports() []ServerReport {
	out := []ServerReport{}
	for _, id := range s.ServerIDs() {
		_, at := s.agentFindings(id)
		s.mu.Lock()
		started, scanning := s.scanning[id]
		if scanning && s.Now().Sub(started) > 10*time.Minute {
			delete(s.scanning, id)
			scanning = false
		}
		s.mu.Unlock()
		out = append(out, ServerReport{ServerID: id, At: at, Findings: s.findings(id), Scanning: scanning, OldAgent: s.oldAgent(id)})
	}
	return out
}

// Run scans every connected server twice a day and does the web checks.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(30 * time.Minute)
	defer tick.Stop()
	ports := time.NewTicker(2 * time.Minute)
	defer ports.Stop()
	first := time.After(90 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ports.C:
			s.WatchPorts()
			continue
		case <-first:
		case <-tick.C:
		}
		for _, id := range s.ServerIDs() {
			if _, at := s.agentFindings(id); s.Connected(id) && !s.oldAgent(id) && s.Now().Sub(time.UnixMilli(at)) > scanEvery {
				s.Scan(id)
			}
		}
		s.markGitHub(ctx)
		s.mu.Lock()
		stale := s.Now().Sub(s.webAt) > scanEvery
		s.mu.Unlock()
		if stale {
			s.webChecks(ctx)
		}
		s.db.Exec(`DELETE FROM ssh_logins WHERE ts < ?`, s.Now().Add(-maxLoginsKept).UnixMilli())
	}
}

// ---------- SSH logins ----------

// OnLogins stores new logins, finds where they came from, and alerts on a country
// this server never saw a login from, or a key this user never used. The first batch
// from a server is the baseline and raises nothing.
func (s *Service) OnLogins(serverID string, data json.RawMessage) {
	var list []proto.SSHLogin
	if json.Unmarshal(data, &list) != nil || len(list) == 0 {
		return
	}
	go s.storeLogins(serverID, list)
}

func (s *Service) storeLogins(serverID string, list []proto.SSHLogin) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM ssh_logins WHERE server_id = ?`, serverID).Scan(&n)
	baseline := n == 0
	labels := s.KeyLabels()
	for _, l := range list {
		var exists int
		if s.db.QueryRow(`SELECT 1 FROM ssh_logins WHERE server_id = ? AND ts = ? AND user = ? AND ip = ?`, serverID, l.TS, l.User, l.IP).Scan(&exists) == nil {
			continue
		}
		country := s.countryOf(ctx, l.IP)
		source := ""
		if s.IsGitHub != nil && s.IsGitHub(ctx, l.IP) {
			source = "github"
		}
		automated := source != "" || (l.Key != "" && labels[l.Key] != "")
		// Countries count people only: CI runners move around the world.
		var seenCountry, seenKey int
		s.db.QueryRow(`SELECT COUNT(*) FROM ssh_logins WHERE server_id = ? AND country = ? AND source = ''`, serverID, country).Scan(&seenCountry)
		s.db.QueryRow(`SELECT COUNT(*) FROM ssh_logins WHERE server_id = ? AND user = ? AND key_fp = ?`, serverID, l.User, l.Key).Scan(&seenKey)
		newCountry := !baseline && !automated && seenCountry == 0 && country != "" && country != "LAN"
		newKey := !baseline && !automated && seenKey == 0 && l.Key != ""
		s.db.Exec(`INSERT OR IGNORE INTO ssh_logins (server_id, ts, user, ip, method, key_fp, country, new_country, new_key, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			serverID, l.TS, l.User, l.IP, l.Method, l.Key, country, newCountry, newKey, source)
		if !automated && (newCountry || newKey || l.Method == "password" && !baseline) && s.Notify != nil {
			var why []string
			if newCountry {
				why = append(why, "first login from "+country)
			}
			if newKey {
				why = append(why, "a key never used by "+l.User+" before")
			}
			if l.Method == "password" && !newCountry && !newKey {
				why = append(why, "with a password")
			}
			when := time.UnixMilli(l.TS).UTC().Format("2 Jan 15:04 UTC")
			s.Notify(fmt.Sprintf("SSH login on %s as %s: %s", s.Name(serverID), l.User, strings.Join(why, ", ")),
				fmt.Sprintf("From %s (%s) at %s, %s.\n\nIf this was not you or your team, change the server's keys and passwords now.", l.IP, orUnknown(country), when, l.Method), "warning")
		}
	}
}

func orUnknown(c string) string {
	if c == "" {
		return "country unknown"
	}
	return c
}

// countryOf caches lookups for 30 days.
// CountryOf is the country (ISO code) of an address, "LAN" for a private one, "" unknown.
func (s *Service) CountryOf(ctx context.Context, ip string) string { return s.countryOf(ctx, ip) }

func (s *Service) countryOf(ctx context.Context, ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || (a.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(a)) {
		return "LAN"
	}
	var c string
	var at int64
	if s.db.QueryRow(`SELECT country, looked_at FROM ip_countries WHERE ip = ?`, ip).Scan(&c, &at) == nil && s.Now().Sub(time.UnixMilli(at)) < 30*24*time.Hour {
		return c
	}
	c = s.Country(ctx, ip)
	s.db.Exec(`INSERT INTO ip_countries (ip, country, looked_at) VALUES (?, ?, ?) ON CONFLICT (ip) DO UPDATE SET country = excluded.country, looked_at = excluded.looked_at`,
		ip, c, s.Now().UnixMilli())
	return c
}

// Logins lists stored logins, newest first. With people set, automated logins
// (GitHub Actions, named keys) are left out and only counted.
func (s *Service) Logins(serverID string, onlyNew, people bool, limit int) (LoginList, error) {
	labels := s.KeyLabels()
	out := LoginList{Items: []Login{}}
	q := `SELECT server_id, ts, user, ip, method, key_fp, country, new_country, new_key, source FROM ssh_logins WHERE (? = '' OR server_id = ?)`
	if onlyNew {
		q += ` AND (new_country = 1 OR new_key = 1)`
	}
	rows, err := s.db.Query(q+` ORDER BY ts DESC LIMIT ?`, serverID, serverID, limit*20)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var l Login
		if err := rows.Scan(&l.ServerID, &l.TS, &l.User, &l.IP, &l.Method, &l.Key, &l.Country, &l.NewCountry, &l.NewKey, &l.Source); err != nil {
			return out, err
		}
		l.Label = labels[l.Key]
		l.Automated = l.Source != "" || l.Label != ""
		if people && l.Automated {
			out.Hidden++
			continue
		}
		if len(out.Items) < limit {
			out.Items = append(out.Items, l)
		}
	}
	return out, rows.Err()
}

// ---------- where an IP address is registered (RDAP at the regional registries) ----------

type rdap struct {
	client *http.Client
	mu     sync.Mutex
	v4, v6 []service
	at     time.Time
}

type service struct {
	prefix netip.Prefix
	base   string
}

func (r *rdap) load(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.v4 != nil && time.Since(r.at) < 7*24*time.Hour {
		return nil
	}
	read := func(url string) ([]service, error) {
		var boot struct {
			Services [][][]string `json:"services"`
		}
		if err := r.get(ctx, url, &boot); err != nil {
			return nil, err
		}
		var out []service
		for _, svc := range boot.Services {
			if len(svc) != 2 || len(svc[1]) == 0 {
				continue
			}
			base := svc[1][0]
			for _, u := range svc[1] { // prefer https
				if strings.HasPrefix(u, "https://") {
					base = u
				}
			}
			for _, pfx := range svc[0] {
				if p, err := netip.ParsePrefix(pfx); err == nil {
					out = append(out, service{p, strings.TrimRight(base, "/") + "/"})
				}
			}
		}
		return out, nil
	}
	v4, err := read("https://data.iana.org/rdap/ipv4.json")
	if err != nil {
		return err
	}
	v6, err := read("https://data.iana.org/rdap/ipv6.json")
	if err != nil {
		return err
	}
	r.v4, r.v6, r.at = v4, v6, time.Now()
	return nil
}

func (r *rdap) get(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	res, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(v)
}

func (r *rdap) country(ctx context.Context, ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || r.load(ctx) != nil {
		return ""
	}
	r.mu.Lock()
	list := r.v4
	if a.Is6() && !a.Is4In6() {
		list = r.v6
	}
	base := ""
	best := -1
	for _, svc := range list {
		if svc.prefix.Contains(a.Unmap()) && svc.prefix.Bits() > best {
			base, best = svc.base, svc.prefix.Bits()
		}
	}
	r.mu.Unlock()
	if base == "" {
		return ""
	}
	var info struct {
		Country string `json:"country"`
	}
	if err := r.get(ctx, base+"ip/"+net.ParseIP(ip).String(), &info); err != nil {
		slog.Debug("rdap ip lookup failed", "ip", ip, "err", err)
		return ""
	}
	return strings.ToUpper(info.Country)
}

// WatchPorts tells about ports newly open to the internet, without waiting for the
// next scan: from what the agents report every minute. The first look at a server
// only remembers what is there.
func (s *Service) WatchPorts() {
	for _, id := range s.ServerIDs() {
		if s.Connected != nil && !s.Connected(id) {
			continue
		}
		now := map[string]bool{}
		fs := s.portFindings(id)
		for _, f := range fs {
			now[f.ID] = true
		}
		s.mu.Lock()
		before, seen := s.ports[id]
		s.ports[id] = now
		s.mu.Unlock()
		if !seen || s.Notify == nil {
			continue
		}
		for _, f := range fs {
			if !before[f.ID] {
				s.Notify("Security: "+s.Name(id)+": "+f.Title, f.Detail+"\n\nHow to fix:\n"+f.Fix, f.Severity)
			}
		}
	}
}
