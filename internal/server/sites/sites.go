// Package sites deploys static sites from GitHub repositories onto servers: plain HTML
// as is, anything with a build step through a GitHub Actions workflow that publishes to
// the fleetwatch-build branch. Every push is deployed again by itself.
package sites

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/github"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/valid"
)

const (
	MinAgent  = "v0.9.43"
	envAD     = "site.env"
	envSecret = "FLEETWATCH_ENV"
	keepLog   = 20 // deploys remembered per site
)

var (
	ErrNotFound = errors.New("site not found")
	nameRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	hostRe      = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9-]{2,63}$`)
	pathRe      = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]{0,199}$`)
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Site is one deployed site.
type Site struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"` // /var/www/<name>
	Repo       string   `json:"repo"` // owner/name
	Branch     string   `json:"branch"`
	ServerID   string   `json:"serverId"`
	Plan       Plan     `json:"plan"`
	Domains    []string `json:"domains"` // your own
	Preview    string   `json:"preview"` // <name>.<server-ip>.sslip.io
	AutoDeploy bool     `json:"autoDeploy"`
	EnvKeys    []string `json:"envKeys"`

	Status string `json:"status"` // building | deploying | live | failed | waiting
	AtPath bool   `json:"atPath"` // at <dashboard>/sites/<name>/: served from this machine or relayed from its preview address
	// CaddyCustom, set by the owner, replaces the generated Caddy site on every deploy.
	CaddyCustom string `json:"caddyCustom,omitempty"`
	// CaddyConfig is the Caddy site file the last deploy put in place, and CaddySource
	// where it came from: generated | repository | custom.
	CaddyConfig string `json:"caddyConfig,omitempty"`
	CaddySource string `json:"caddySource,omitempty"`
	// DNS is how each of the site's own domains stands with its owner's DNS accounts.
	DNS map[string]dns.State `json:"dns,omitempty"`
	// DNSManual: the person adds the domains' records themselves; Fleetwatch only shows them.
	DNSManual bool `json:"dnsManual,omitempty"`
	// OwnerKey is who manages the site: "" (the dashboard owner) or a member ID.
	OwnerKey   string `json:"ownerKey,omitempty"`
	Mine       bool   `json:"mine"`   // set per request: the viewer may change it
	Commit     string `json:"commit"` // deployed commit of the branch (for builds: of the source)
	Message    string `json:"message"`
	DeployedAt int64  `json:"deployedAt"`
	Error      string `json:"error,omitempty"`
	BuildURL   string `json:"buildUrl,omitempty"` // the GitHub Actions run
	BuildRunID int64  `json:"buildRunId,omitempty"`
	// Pinned, after a rollback, is the deploy branch's head then: automatic deploys wait
	// for a newer commit instead of undoing the rollback.
	Pinned    string `json:"pinned,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	CreatedBy string `json:"createdBy"`

	tried string // last deploy-branch commit attempted (no automatic retry of a failure)
}

// Deploy is one deploy of a site, for its history.
type Deploy struct {
	TS      int64  `json:"ts"`
	Commit  string `json:"commit"`
	Message string `json:"message"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Log     string `json:"log,omitempty"`
	By      string `json:"by"`
	// Deployed is the exact commit of the deploy branch that went live (for builds, the
	// built commit, not its source): what a rollback deploys again.
	Deployed string `json:"deployed,omitempty"`
}

// GitHub is what deploys use of the GitHub integration (*github.Service).
type GitHub interface {
	Connected() bool
	RepoInfo(ctx context.Context, repo string) (defaultBranch string, private bool, err error)
	RepoToken(ctx context.Context, repo string) (string, error)
	Head(ctx context.Context, repo, branch string) (sha, message string, err error)
	List(ctx context.Context, repo, dir, ref string) ([]string, error)
	File(ctx context.Context, repo, path, ref string) ([]byte, string, bool, error)
	PutFile(ctx context.Context, repo, path, branch string, content []byte, message string) (bool, error)
	SetSecret(ctx context.Context, repo, name, value string) error
	Dispatch(ctx context.Context, repo, workflow, ref string) error
	LastRun(ctx context.Context, repo, workflow string) (*github.Run, error)
	Tarball(repo, sha string) string
	DeleteFile(ctx context.Context, repo, path, branch, message string) error
	DeleteBranch(ctx context.Context, repo, branch string) error
	DeleteSecret(ctx context.Context, repo, name string) error
}

type Service struct {
	db  *sql.DB
	box *secret.Box
	Bus *live.Bus
	Now func() time.Time

	GitHub       GitHub
	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string
	PublicIP     func(serverID string) string
	Notify       func(title, body, severity string)
	// DNS points the sites' domains at their server through their owner's DNS accounts.
	DNS *dns.Service
	// RegistryAuths are the private Docker registry logins, for Docker apps' image pulls.
	RegistryAuths func() []proto.RegistryAuth
	// IsLocal reports whether a server is the dashboard's own machine: its sites are
	// served at <dashboard>/sites/<name>/ instead of a preview address.
	IsLocal func(serverID string) bool

	mu      sync.Mutex
	running map[string]pending // run ID → what it was for
	syncing sync.Mutex
}

type pending struct {
	site, commit, message, by string
}

func New(d *sql.DB, box *secret.Box, bus *live.Bus, gh GitHub) *Service {
	return &Service{db: d, box: box, Bus: bus, Now: time.Now, GitHub: gh, running: map[string]pending{},
		Notify: func(string, string, string) {}, PublicIP: func(string) string { return "" }, IsLocal: func(string) bool { return false }}
}

// ---------- storage ----------

func (s *Service) Get(id string) (Site, error) {
	var raw, tried string
	var st Site
	err := s.db.QueryRow(`SELECT data, tried FROM sites WHERE id = ?`, id).Scan(&raw, &tried)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	if err != nil {
		return st, err
	}
	err = json.Unmarshal([]byte(raw), &st)
	st.tried = tried
	st.Preview = s.preview(st.Name, st.ServerID)
	st.AtPath = s.IsLocal(st.ServerID) || st.Preview != ""
	return st, err
}

func (s *Service) List() []Site {
	out := []Site{}
	rows, err := s.db.Query(`SELECT id FROM sites ORDER BY created_at`)
	if err != nil {
		return out
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if st, err := s.Get(id); err == nil {
			out = append(out, st)
		}
	}
	return out
}

func dockerKey(st Site) string {
	return st.Name + "-" + strings.TrimPrefix(st.ID, "site_")
}

func (s *Service) save(st Site) error {
	if st.Domains == nil {
		st.Domains = []string{}
	}
	if st.EnvKeys == nil {
		st.EnvKeys = []string{}
	}
	raw, _ := json.Marshal(st)
	_, err := s.db.Exec(`INSERT INTO sites (id, server_id, data, tried, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET data = excluded.data, tried = excluded.tried`, st.ID, st.ServerID, string(raw), st.tried, st.CreatedAt)
	if err == nil {
		s.Bus.Publish("site", st)
	}
	return err
}

func (s *Service) History(id string) []Deploy {
	out := []Deploy{}
	rows, err := s.db.Query(`SELECT data FROM site_deploys WHERE site_id = ? ORDER BY id DESC LIMIT ?`, id, keepLog)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var d Deploy
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *Service) remember(id string, d Deploy) {
	raw, _ := json.Marshal(d)
	s.db.Exec(`INSERT INTO site_deploys (site_id, ts, data) VALUES (?, ?, ?)`, id, d.TS, string(raw))
	s.db.Exec(`DELETE FROM site_deploys WHERE site_id = ? AND id NOT IN (SELECT id FROM site_deploys WHERE site_id = ? ORDER BY id DESC LIMIT ?)`, id, id, keepLog)
}

// ---------- creating and changing ----------

// Input creates a site.
type Input struct {
	Repo     string   `json:"repo"`
	ServerID string   `json:"serverId"`
	Branch   string   `json:"branch"` // "" = the default branch
	Name     string   `json:"name"`   // "" = from the repository name
	Plan     *Plan    `json:"plan"`   // nil = detected
	Domains  []string `json:"domains"`
	Env      string   `json:"env"` // .env lines (build sites)
	// DNSManual: no records are made through the person's Cloudflare for its domains.
	DNSManual bool   `json:"dnsManual"`
	Owner     string `json:"-"` // member ID of who creates it ("" = the owner); never from the request
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

func checkPlan(p Plan) error {
	switch p.Kind {
	case "static":
		if p.Dir != "" && (!pathRe.MatchString(p.Dir) || strings.Contains(p.Dir, "..")) {
			return valid.Error("the folder to publish must be a path inside the repository")
		}
	case "build":
		for _, c := range []string{p.Install, p.Build} {
			if c == "" || len(c) > 300 || strings.ContainsAny(c, "\n\r") {
				return valid.Error("install and build commands are one line each")
			}
		}
		if !pathRe.MatchString(p.Output) || strings.Contains(p.Output, "..") {
			return valid.Error("the output folder must be a path inside the repository, like dist")
		}
		if p.Node != "" && p.Node != ".nvmrc" && p.Node != ".node-version" {
			return valid.Error("unknown Node.js version file")
		}
	case "docker":
		if p.Compose != "" && !slices.Contains(composeFiles, p.Compose) {
			return valid.Error("the compose file is compose.yaml, compose.yml, docker-compose.yml or docker-compose.yaml")
		}
		if p.Port < 0 || p.Port > 65535 {
			return valid.Error("the port is 1-65535 (or empty: no web address)")
		}
	default:
		return valid.Error("a site is static, built or a Docker app")
	}
	return nil
}

// DockerAgent is the first agent that pulls Docker images built by GitHub Actions.
const DockerAgent = "v0.9.157"

// hostPort picks the 127.0.0.1 port a Dockerfile app is published on: its own if it has
// one, else the first in 20000-29999 no other Docker app on the server uses.
func (s *Service) hostPort(st Site) int {
	if st.Plan.Kind != "docker" || st.Plan.Compose != "" || st.Plan.Port == 0 {
		return 0
	}
	if st.Plan.HostPort != 0 {
		return st.Plan.HostPort
	}
	used := map[int]bool{}
	for _, o := range s.List() {
		if o.ServerID == st.ServerID && o.ID != st.ID {
			used[o.Plan.HostPort] = true
		}
	}
	for p := 20000; p < 30000; p++ {
		if !used[p] {
			return p
		}
	}
	return 0
}

func checkDomains(list []string) ([]string, error) {
	out := []string{}
	for _, d := range list {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" || slices.Contains(out, d) {
			continue
		}
		if !hostRe.MatchString(d) || len(d) > 253 {
			return nil, valid.Error(fmt.Sprintf("%q is not a domain name", d))
		}
		out = append(out, d)
	}
	if len(out) > 20 {
		return nil, valid.Error("20 domains per site at most")
	}
	return out, nil
}

// parseEnv checks .env text and returns its keys.
func parseEnv(text string) ([]string, error) {
	var keys []string
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, _, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok || !envKeyRe.MatchString(strings.TrimSpace(k)) {
			return nil, valid.Error(fmt.Sprintf("line %d is not NAME=value", i+1))
		}
		keys = append(keys, strings.TrimSpace(k))
	}
	if len(text) > 48<<10 {
		return nil, valid.Error("the .env is larger than 48 KB")
	}
	return keys, nil
}

// PathAgent is the first agent whose Caddy sites also answer under /sites/<name>/.
const PathAgent = "v0.9.46"

func (s *Service) supported(serverID string) error {
	if s.AgentVersion != nil && release.Newer(MinAgent, s.AgentVersion(serverID)) {
		return valid.Error("this server's agent is too old for sites; update the agent first")
	}
	return nil
}

// Preview is the address a site gets right away: a name under the server's public IPv4
// on sslip.io, a public DNS service that answers with the IP inside the name.
func preview(name, ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return ""
	}
	return name + "." + strings.ReplaceAll(a.String(), ".", "-") + ".sslip.io"
}

// preview is the sslip.io address, only for sites that are not on the dashboard's own
// server (those are at <dashboard>/sites/<name>/).
func (s *Service) preview(name, serverID string) string {
	if s.IsLocal(serverID) {
		return ""
	}
	return preview(name, s.PublicIP(serverID))
}

// Create adds a site and starts its first deploy (or build).
func (s *Service) Create(ctx context.Context, in Input, by string) (Site, error) {
	if err := s.supported(in.ServerID); err != nil {
		return Site{}, err
	}
	def, _, err := s.GitHub.RepoInfo(ctx, in.Repo)
	if err != nil {
		return Site{}, err
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = def
	}
	if branch == github.BuildBranchName || strings.ContainsAny(branch, " \n\"'") || len(branch) > 200 {
		return Site{}, valid.Error("pick the branch your code is on")
	}
	_, repoName, _ := strings.Cut(in.Repo, "/")
	name := slug(in.Name)
	if name == "" {
		name = slug(repoName)
	}
	if !nameRe.MatchString(name) {
		return Site{}, valid.Error("the site name is lowercase letters, digits and dashes")
	}
	for _, o := range s.List() {
		if o.ServerID == in.ServerID && o.Name == name {
			return Site{}, valid.Error(fmt.Sprintf("this server already has a site named %s; choose another name", name))
		}
	}
	plan := Plan{}
	if in.Plan != nil {
		plan = *in.Plan
	} else if plan, err = s.Detect(ctx, in.Repo, branch); err != nil {
		return Site{}, err
	}
	if err := checkPlan(plan); err != nil {
		return Site{}, err
	}
	if plan.Kind == "docker" && s.AgentVersion != nil && release.Newer(DockerAgent, s.AgentVersion(in.ServerID)) {
		return Site{}, valid.Error("this server's agent is too old to run Docker apps; update the agent first")
	}
	if atPath := s.IsLocal(in.ServerID) || s.preview(name, in.ServerID) != ""; atPath && plan.Kind == "build" {
		plan = underPath(plan, "/sites/"+name+"/")
	}
	domains, err := checkDomains(in.Domains)
	if err != nil {
		return Site{}, err
	}
	keys, err := parseEnv(in.Env)
	if err != nil {
		return Site{}, err
	}
	if len(keys) > 0 && plan.Kind == "static" {
		return Site{}, valid.Error("a plain HTML site has no build, so a .env would not be used (and must never be served)")
	}
	b := make([]byte, 6)
	rand.Read(b)
	st := Site{ID: "site_" + hex.EncodeToString(b), Name: name, Repo: in.Repo, Branch: branch, ServerID: in.ServerID, Plan: plan,
		Domains: domains, Preview: s.preview(name, in.ServerID), AutoDeploy: true, EnvKeys: keys,
		Status: "waiting", CreatedAt: s.Now().UnixMilli(), CreatedBy: by, OwnerKey: in.Owner, DNSManual: in.DNSManual}
	st.Plan.HostPort = s.hostPort(st)
	if err := s.save(st); err != nil {
		return Site{}, err
	}
	if in.Env != "" {
		s.db.Exec(`UPDATE sites SET env_enc = ? WHERE id = ?`, s.box.Seal([]byte(in.Env), envAD+st.ID), st.ID)
	}
	if len(domains) > 0 && !st.DNSManual {
		go s.SyncDNS(st.ID, nil, false)
	}
	if plan.Kind == "build" {
		if err := s.setupBuild(ctx, st, in.Env); err != nil {
			st.Status, st.Error = "failed", err.Error()
			s.save(st)
			return st, nil
		}
		st.Status = "building"
		s.save(st)
		go s.SyncSoon()
		return st, nil
	}
	if plan.Kind == "docker" && s.useGHCR(ctx, in.Repo) {
		if err := s.setupDocker(ctx, &st); err != nil {
			st.Status, st.Error = "failed", err.Error()
			s.save(st)
			return st, nil
		}
		st.Status = "building"
		s.save(st)
		go s.SyncSoon()
		return st, nil
	}
	go s.Redeploy(context.Background(), st.ID, by)
	return st, nil
}

// useGHCR reports whether the server can pull GHCR images built by GitHub Actions for repo:
// public repositories publish public GHCR packages, whereas private repositories require a
// ghcr.io login in Integrations → Registries.
func (s *Service) useGHCR(ctx context.Context, repo string) bool {
	if s.RegistryAuths != nil && slices.ContainsFunc(s.RegistryAuths(), func(a proto.RegistryAuth) bool { return a.Host == "ghcr.io" }) {
		return true
	}
	_, private, err := s.GitHub.RepoInfo(ctx, repo)
	return err == nil && !private
}

// ErrPermissions: the GitHub app was created without the permissions sites need (apps
// made before sites existed only read).
var ErrPermissions = valid.Error("the GitHub app is missing permissions sites need (Contents, Workflows, Secrets and Actions: read and write): grant them (Integrations → GitHub shows how), then press Retry")

func permissionError(err error) error {
	if err != nil && (strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "not accessible by integration")) {
		return valid.Error(string(ErrPermissions) + " (GitHub said: " + err.Error() + ")")
	}
	return err
}

// setupBuild adds (or updates) the workflow in the repository and the .env secret.
func (s *Service) setupBuild(ctx context.Context, st Site, env string) error {
	if err := s.setupBuildOnce(ctx, st, env); err != nil {
		return permissionError(err)
	}
	return nil
}

func (s *Service) setupBuildOnce(ctx context.Context, st Site, env string) error {
	if env != "" {
		if err := s.GitHub.SetSecret(ctx, st.Repo, envSecret, env); err != nil {
			return fmt.Errorf("could not store the .env on GitHub: %w", err)
		}
	}
	changed, err := s.GitHub.PutFile(ctx, st.Repo, ".github/workflows/"+workflowFile, st.Branch, []byte(workflow(st.Plan, st.Branch)), "Build the site with Fleetwatch")
	if err != nil {
		return fmt.Errorf("could not add the build workflow to the repository: %w", err)
	}
	if !changed { // the commit adding it starts a build by itself; otherwise ask for one
		if err := s.GitHub.Dispatch(ctx, st.Repo, workflowFile, st.Branch); err != nil {
			if run, e := s.GitHub.LastRun(ctx, st.Repo, workflowFile); e == nil && run != nil && run.Status != "completed" {
				return nil // a build is already running: that one goes live
			}
			return err
		}
	}
	return nil
}

// setupDocker adds the repository workflow that builds and pushes commit-tagged images.
func (s *Service) setupDocker(ctx context.Context, st *Site) error {
	key := dockerKey(*st)
	file := dockerWorkflowFile(key)
	run, err := s.GitHub.LastRun(ctx, st.Repo, file)
	if err != nil {
		return permissionError(fmt.Errorf("could not check the Docker image workflow: %w", err))
	}
	changed, err := s.GitHub.PutFile(ctx, st.Repo, ".github/workflows/"+file, st.Branch,
		[]byte(dockerWorkflow(st.Plan, st.Branch, key)), "Build Docker images with Fleetwatch")
	if err != nil {
		return permissionError(fmt.Errorf("could not add the Docker image workflow to the repository: %w", err))
	}
	if !changed && run != nil && run.Status != "completed" {
		st.BuildURL = run.HTMLURL
		st.Status, st.Error = "building", ""
		return s.save(*st)
	}
	if run != nil {
		st.BuildRunID = run.ID
	} else {
		st.BuildRunID = 0
	}
	st.Status, st.Error = "building", ""
	if err := s.save(*st); err != nil {
		return err
	}
	if !changed { // the commit adding it starts a build by itself; otherwise ask for one
		if err := s.GitHub.Dispatch(ctx, st.Repo, file, st.Branch); err != nil {
			if r, e := s.GitHub.LastRun(ctx, st.Repo, file); e == nil && r != nil && r.Status != "completed" {
				return nil
			}
			return permissionError(err)
		}
	}
	return nil
}

// Change edits a site's addresses, plan or automatic deploys.
type Change struct {
	Domains    []string `json:"domains"`
	AutoDeploy bool     `json:"autoDeploy"`
	Plan       *Plan    `json:"plan"`
	DNSManual  *bool    `json:"dnsManual"` // nil = unchanged
}

func (s *Service) Update(ctx context.Context, id string, c Change, by string) (Site, error) {
	st, err := s.Get(id)
	if err != nil {
		return st, err
	}
	domains, err := checkDomains(c.Domains)
	if err != nil {
		return st, err
	}
	rebuild := false
	if c.Plan != nil && c.Plan.Kind == "docker" { // Fleetwatch picks the host port, not the request
		next := st
		next.Plan = *c.Plan
		next.Plan.HostPort = st.Plan.HostPort
		p := *c.Plan
		p.HostPort = s.hostPort(next)
		c.Plan = &p
	}
	if c.Plan != nil && *c.Plan != st.Plan {
		if err := checkPlan(*c.Plan); err != nil {
			return st, err
		}
		if c.Plan.Kind != st.Plan.Kind {
			return st, valid.Error("a site cannot change between plain HTML and built: add a new site")
		}
		st.Plan, rebuild = *c.Plan, c.Plan.Kind == "build" || c.Plan.Kind == "docker"
	}
	redeploy := !slices.Equal(domains, st.Domains) || (c.Plan != nil && !rebuild)
	old, sync := st.Domains, !slices.Equal(domains, st.Domains)
	if c.DNSManual != nil && *c.DNSManual != st.DNSManual {
		st.DNSManual = *c.DNSManual
		sync = sync || !st.DNSManual // back to automatic: point what is missing
	}
	if sync {
		defer func() { go s.SyncDNS(st.ID, old, false) }()
	}
	st.Domains, st.AutoDeploy = domains, c.AutoDeploy
	if err := s.save(st); err != nil {
		return st, err
	}
	switch {
	case rebuild && st.Plan.Kind == "docker" && !s.useGHCR(ctx, st.Repo):
		go s.Redeploy(context.Background(), st.ID, by)
	case rebuild:
		var err error
		if st.Plan.Kind == "docker" {
			err = s.setupDocker(ctx, &st)
		} else {
			err = s.setupBuild(ctx, st, "")
		}
		if err != nil {
			return st, err
		}
		st.Status = "building"
		s.save(st)
	case redeploy && st.Commit != "":
		go s.deployCurrent(st.ID, by)
	}
	return st, nil
}

// SetEnv replaces the .env used by the build and starts a new build.
func (s *Service) SetEnv(ctx context.Context, id, env string) (Site, error) {
	st, err := s.Get(id)
	if err != nil {
		return st, err
	}
	if st.Plan.Kind == "static" {
		return st, valid.Error("a plain HTML site has no build, so a .env would not be used")
	}
	keys, err := parseEnv(env)
	if err != nil {
		return st, err
	}
	if st.Plan.Kind == "docker" { // kept here and written next to the app on the server
		s.db.Exec(`UPDATE sites SET env_enc = ? WHERE id = ?`, s.box.Seal([]byte(env), envAD+st.ID), st.ID)
		st.EnvKeys = keys
		if err := s.save(st); err != nil {
			return st, err
		}
		if st.Commit != "" {
			go s.deployCurrent(st.ID, "")
		}
		return st, nil
	}
	if err := s.GitHub.SetSecret(ctx, st.Repo, envSecret, env); err != nil {
		return st, permissionError(fmt.Errorf("could not store the .env on GitHub: %w", err))
	}
	s.db.Exec(`UPDATE sites SET env_enc = ? WHERE id = ?`, s.box.Seal([]byte(env), envAD+st.ID), st.ID)
	st.EnvKeys = keys
	if err := s.GitHub.Dispatch(ctx, st.Repo, workflowFile, st.Branch); err == nil {
		st.Status = "building"
	}
	return st, s.save(st)
}

// exampleFiles are where projects keep a template of their .env, in order.
var exampleFiles = []string{".env.example", ".env.sample", ".env.template", ".env.dist", ".env.defaults"}

// EnvExample returns the repository's .env template (name and text), if it has one.
func (s *Service) EnvExample(ctx context.Context, repo, branch string) (name, text string) {
	for _, f := range exampleFiles {
		if b, _, ok, err := s.GitHub.File(ctx, repo, f, branch); err == nil && ok && len(b) < 48<<10 {
			return f, string(b)
		}
	}
	return "", ""
}

// Env returns the stored .env (owner only).
func (s *Service) Env(id string) (string, error) {
	var enc []byte
	if err := s.db.QueryRow(`SELECT env_enc FROM sites WHERE id = ?`, id).Scan(&enc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if enc == nil {
		return "", nil
	}
	raw, err := s.box.Open(enc, envAD+id)
	return string(raw), err
}

// Delete takes the site off its server and forgets it. The workflow and build branch
// stay in the repository (another server may use them).
func (s *Service) Delete(id string) (Site, error) {
	st, err := s.Get(id)
	if err != nil {
		return st, err
	}
	if s.Send != nil {
		remove := proto.SiteRemove{RunID: "rm-" + st.ID, Name: st.Name}
		if st.Plan.Kind == "docker" {
			remove.ImageBase = dockerImageBase(st.Repo, dockerKey(st))
		}
		s.Send(st.ServerID, proto.TypeSiteRemove, remove)
	}
	if _, err := s.db.Exec(`DELETE FROM sites WHERE id = ?`, id); err != nil {
		return st, err
	}
	s.Bus.Publish("site.removed", map[string]string{"id": id, "serverId": st.ServerID})
	if s.DNS != nil && len(st.DNS) > 0 { // the records Fleetwatch made for it go too
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.DNS.Forget(ctx, DNSOwner(st), st.DNS)
		}()
	}
	// What Fleetwatch added to the repository for building goes too, unless another
	// site still builds from it.
	if st.Plan.Kind == "build" && !slices.ContainsFunc(s.List(), func(o Site) bool { return o.Repo == st.Repo && o.Plan.Kind == "build" }) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := errors.Join(
			s.GitHub.DeleteFile(ctx, st.Repo, ".github/workflows/"+workflowFile, st.Branch, "Remove the Fleetwatch build"),
			s.GitHub.DeleteBranch(ctx, st.Repo, github.BuildBranchName),
			s.GitHub.DeleteSecret(ctx, st.Repo, envSecret),
		); err != nil {
			slog.Warn("site removal on GitHub", "site", st.Name, "err", err)
		}
	}
	if st.Plan.Kind == "docker" && !slices.ContainsFunc(s.List(), func(o Site) bool { return o.Repo == st.Repo && o.Plan.Kind == "docker" }) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.GitHub.DeleteFile(ctx, st.Repo, ".github/workflows/"+dockerWorkflowFile(dockerKey(st)), st.Branch, "Remove the Fleetwatch Docker build"); err != nil {
			slog.Warn("Docker workflow removal", "site", st.Name, "err", err)
		}
	}
	return st, nil
}

// ---------- deploying ----------

func (s *Service) deployBranch(st Site) string {
	if st.Plan.Kind == "build" {
		return github.BuildBranchName
	}
	return st.Branch
}

// Redeploy deploys the newest commit now (for a built site: starts a new build).
func (s *Service) Redeploy(ctx context.Context, id, by string) error {
	st, err := s.Get(id)
	if err != nil {
		return err
	}
	st.Pinned = "" // the latest again
	if st.Plan.Kind == "build" {
		env, _ := s.Env(st.ID)
		if err := s.setupBuild(ctx, st, env); err != nil { // adds the workflow if it is missing, else starts a build
			st.Status, st.Error = "failed", err.Error()
			s.save(st)
			return err
		}
		st.Status, st.Error = "building", ""
		return s.save(st)
	}
	if st.Plan.Kind == "docker" && s.useGHCR(ctx, st.Repo) {
		key := dockerKey(st)
		file := dockerWorkflowFile(key)
		if content, _, exists, err := s.GitHub.File(ctx, st.Repo, ".github/workflows/"+file, st.Branch); err == nil &&
			exists && string(content) == dockerWorkflow(st.Plan, st.Branch, key) {
			if sha, msg, err := s.GitHub.Head(ctx, st.Repo, st.Branch); err == nil && sha != "" {
				if run, err := s.GitHub.LastRun(ctx, st.Repo, file); err == nil && run != nil &&
					run.HeadSHA == sha && run.Status == "completed" && run.Conclusion == "success" {
					st.BuildRunID = 0
					st.BuildURL = run.HTMLURL
					return s.deploy(ctx, st, sha, msg, by)
				}
			}
		}
		if err := s.setupDocker(ctx, &st); err != nil {
			st.Status, st.Error = "failed", err.Error()
			s.save(st)
			return err
		}
		st.tried = ""
		st.Status, st.Error = "building", ""
		if err := s.save(st); err != nil {
			return err
		}
		go s.SyncSoon()
		return nil
	}
	sha, msg, err := s.GitHub.Head(ctx, st.Repo, st.Branch)
	if err != nil {
		return err
	}
	if sha == "" {
		return valid.Error("the branch " + st.Branch + " has no commits")
	}
	return s.deploy(ctx, st, sha, msg, by)
}

// CustomAgent is the first agent that takes a Caddy site from the dashboard.
const CustomAgent = "v0.9.72"

// SetCaddy replaces the site's generated Caddy config with the owner's ("" goes back to
// the generated one) and deploys the live commit again, so Caddy checks and loads it.
// Rollback deploys an earlier successful deploy again: the one at ts, or (ts 0) the last
// one before what is live. Automatic deploys then wait for a newer commit.
func (s *Service) Rollback(ctx context.Context, id string, ts int64, by string) (Deploy, error) {
	st, err := s.Get(id)
	if err != nil {
		return Deploy{}, err
	}
	if st.Status == "deploying" {
		return Deploy{}, valid.Error("a deploy is running: wait for it to finish")
	}
	live := ""
	var target *Deploy
	for _, d := range s.History(id) { // newest first
		sha := d.Deployed
		if sha == "" && st.Plan.Kind != "build" {
			sha = d.Commit // older entries: for a plain site, the same commit
		}
		if !d.OK || sha == "" {
			continue
		}
		if live == "" && ts == 0 {
			live = sha // what is live now
			continue
		}
		if (ts == 0 && sha != live) || (ts != 0 && d.TS == ts) {
			d.Deployed = sha
			target = &d
			break
		}
	}
	if target == nil {
		return Deploy{}, valid.Error("there is no earlier successful deploy to go back to")
	}
	if head, _, err := s.GitHub.Head(ctx, st.Repo, s.deployBranch(st)); err == nil {
		st.Pinned = head
	}
	msg := target.Message
	if st.Plan.Kind == "build" {
		msg = "Build of " + target.Commit // so the history shows its source commit again
	}
	return *target, s.deploy(ctx, st, target.Deployed, msg, by+" (rollback)")
}

func (s *Service) SetCaddy(id, config, by string) (Site, error) {
	st, err := s.Get(id)
	if err != nil {
		return st, err
	}
	config = strings.TrimSpace(strings.ReplaceAll(config, "\r\n", "\n"))
	if len(config) > 64<<10 {
		return st, valid.Error("the Caddy config is larger than 64 KB")
	}
	if config != "" && s.AgentVersion != nil && release.Newer(CustomAgent, s.AgentVersion(st.ServerID)) {
		return st, valid.Error("this server's agent is too old for a custom Caddy config; update the agent first")
	}
	st.CaddyCustom = config
	if err := s.save(st); err != nil {
		return st, err
	}
	go s.deployCurrent(st.ID, by)
	return st, nil
}

// deployCurrent deploys the commit that is live again (after an address change).
func (s *Service) deployCurrent(id, by string) {
	st, err := s.Get(id)
	if err != nil || st.tried == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	msg := st.Message
	if sha, m, err := s.GitHub.Head(ctx, st.Repo, s.deployBranch(st)); err == nil && sha == st.tried {
		msg = m // the commit's own message (for builds, "Build of <source commit>")
	}
	if err := s.deploy(ctx, st, st.tried, msg, by); err != nil {
		slog.Warn("site redeploy", "site", st.Name, "err", err)
	}
}

func (s *Service) deploy(ctx context.Context, st Site, sha, message, by string) error {
	if err := s.supported(st.ServerID); err != nil {
		return err
	}
	tok, err := s.GitHub.RepoToken(ctx, st.Repo)
	if err != nil {
		return err
	}
	// The agent's Caddy strips /sites/<name> since 0.9.46, which own domains of a site
	// built for that path need.
	if len(st.Domains) > 0 && st.Plan.BasePath != "" && s.AgentVersion != nil && release.Newer(PathAgent, s.AgentVersion(st.ServerID)) {
		return valid.Error("this server's agent is too old to serve this site on its own domain; update the agent first")
	}
	domains := slices.Clone(st.Domains)
	if st.Preview = s.preview(st.Name, st.ServerID); st.Preview != "" {
		domains = append([]string{st.Preview}, domains...)
	}
	b := make([]byte, 8)
	rand.Read(b)
	run := hex.EncodeToString(b)
	dir := ""
	if st.Plan.Kind == "static" {
		dir = st.Plan.Dir
	}
	s.mu.Lock()
	s.running[run] = pending{st.ID, sha, message, by}
	s.mu.Unlock()
	req := proto.SiteDeploy{RunID: run, Name: st.Name, Archive: s.GitHub.Tarball(st.Repo, sha), Token: tok,
		Dir: dir, Domains: domains, SPA: st.Plan.SPA, CaddyConfig: st.CaddyCustom}
	if st.Plan.Kind == "docker" {
		if s.AgentVersion != nil && release.Newer(DockerAgent, s.AgentVersion(st.ServerID)) {
			s.mu.Lock()
			delete(s.running, run)
			s.mu.Unlock()
			return valid.Error("this server's agent is too old to run Docker apps; update the agent first")
		}
		env, _ := s.Env(st.ID)
		imageBase := ""
		if s.useGHCR(ctx, st.Repo) {
			imageBase = dockerImageBase(st.Repo, dockerKey(st))
		}
		req.Docker = &proto.SiteDocker{Compose: st.Plan.Compose, ImageBase: imageBase,
			ImageTag: sha, Port: st.Plan.Port, HostPort: st.Plan.HostPort, Env: env}
		if s.RegistryAuths != nil {
			req.Docker.Auths = s.RegistryAuths()
		}
		if st.Plan.Compose != "" {
			req.Docker.HostPort = st.Plan.Port // the host port its compose file publishes
		}
	}
	prevStatus, prevErr, prevTried := st.Status, st.Error, st.tried
	st.Status, st.Error, st.tried = "deploying", "", sha
	if err := s.save(st); err != nil {
		s.mu.Lock()
		delete(s.running, run)
		s.mu.Unlock()
		return err
	}
	if err := s.Send(st.ServerID, proto.TypeSiteDeploy, req); err != nil {
		s.mu.Lock()
		delete(s.running, run)
		s.mu.Unlock()
		st.Status, st.Error, st.tried = prevStatus, prevErr, prevTried
		s.save(st)
		return err
	}
	return nil
}

// OnResult takes the agent's answer to a deploy.
func (s *Service) OnResult(serverID string, data json.RawMessage) {
	var res proto.SiteResult
	if json.Unmarshal(data, &res) != nil {
		return
	}
	s.mu.Lock()
	p, ok := s.running[res.RunID]
	delete(s.running, res.RunID)
	s.mu.Unlock()
	if !ok {
		return // a removal, or a deploy from before a restart
	}
	st, err := s.Get(p.site)
	if err != nil || st.ServerID != serverID {
		return
	}
	commit, msg := p.commit, p.message
	if src, ok := strings.CutPrefix(msg, "Build of "); ok && st.Plan.Kind == "build" {
		commit, msg = src, "Build of "+shortSHA(src) // the source commit, as people know it
	}
	s.remember(st.ID, Deploy{TS: s.Now().UnixMilli(), Commit: commit, Message: msg, OK: res.OK, Error: res.Error, Log: res.Log, By: p.by, Deployed: p.commit})
	if res.OK {
		if s.DNS != nil && len(st.Domains) > 0 {
			go func(owner string, domains []string) { // visitors get the new version, not Cloudflare's copy
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				s.DNS.Purge(ctx, owner, domains)
			}(DNSOwner(st), slices.Clone(st.Domains))
		}
		st.Status, st.Commit, st.Message, st.DeployedAt, st.Error = "live", commit, msg, s.Now().UnixMilli(), ""
		st.BuildRunID = 0
		st.CaddyConfig, st.CaddySource = res.CaddyConfig, res.Caddy
	} else {
		st.Status, st.Error = "failed", res.Error
		st.BuildRunID = 0
		s.Notify("Site "+st.Name+" failed to deploy", res.Error, "warning")
	}
	s.save(st)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// SyncSoon checks every site in the background (a push arrived).
func (s *Service) SyncSoon() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.Sync(ctx)
	}()
}

// Sync deploys what changed: a new commit on a plain site's branch, or a new build on
// the build branch. For built sites it also follows the build on GitHub Actions.
func (s *Service) Sync(ctx context.Context) {
	if !s.syncing.TryLock() {
		return
	}
	defer s.syncing.Unlock()
	if !s.GitHub.Connected() {
		return
	}
	for _, st := range s.List() {
		// A built site at <dashboard>/sites/<name>/ must be built for that path (sites made
		// before path addresses existed are moved over once).
		if base := "/sites/" + st.Name + "/"; st.Plan.Kind == "build" && st.AtPath && st.Plan.BasePath != base {
			st.Plan = underPath(st.Plan, base)
			if err := s.setupBuild(ctx, st, ""); err == nil {
				st.Status = "building"
			}
			s.save(st)
			continue
		}
		if (!st.AutoDeploy && st.Status != "building") || st.Status == "deploying" {
			continue
		}
		useGHCR := st.Plan.Kind == "docker" && s.useGHCR(ctx, st.Repo)
		if st.Plan.Kind == "docker" && !useGHCR && st.Status == "failed" && strings.Contains(strings.ToLower(st.Error), "unauthorized") {
			st.tried = "" // retry on the server when a private GHCR pull previously failed without a registry token
		}
		if useGHCR {
			key := dockerKey(st)
			file := dockerWorkflowFile(key)
			content, _, exists, err := s.GitHub.File(ctx, st.Repo, ".github/workflows/"+file, st.Branch)
			if err != nil {
				slog.Warn("check Docker workflow", "site", st.Name, "err", err)
				continue
			}
			if !exists || string(content) != dockerWorkflow(st.Plan, st.Branch, key) {
				if err := s.setupDocker(ctx, &st); err != nil {
					st.Status, st.Error = "failed", err.Error()
					s.save(st)
					slog.Warn("setup Docker workflow", "site", st.Name, "err", err)
				}
				continue
			}
		}
		if st.Plan.Kind == "build" {
			if run, err := s.GitHub.LastRun(ctx, st.Repo, workflowFile); err == nil && run != nil {
				st.BuildURL = run.HTMLURL
				switch {
				case run.Status != "completed" && st.Status != "building":
					st.Status, st.Error = "building", ""
					s.save(st)
				case run.Status == "completed" && run.Conclusion != "success" && run.Conclusion != "cancelled" && st.Status == "building":
					st.Status, st.Error = "failed", "The build failed on GitHub Actions: open the run for its log"
					s.save(st)
					s.Notify("Site "+st.Name+" did not build", "GitHub Actions: "+run.HTMLURL, "warning")
				}
			}
		}
		sha, msg, err := s.GitHub.Head(ctx, st.Repo, s.deployBranch(st))
		if err != nil || sha == "" || sha == st.Pinned {
			continue
		}
		if useGHCR {
			run, err := s.GitHub.LastRun(ctx, st.Repo, dockerWorkflowFile(dockerKey(st)))
			switch {
			case err != nil || run == nil:
				st.Status, st.Error = "building", ""
				s.save(st)
				continue
			case run.HeadSHA != sha || run.Status != "completed" || (run.ID <= st.BuildRunID && run.Conclusion != "success") || run.Conclusion == "cancelled":
				st.BuildURL = run.HTMLURL
				st.Status, st.Error = "building", ""
				s.save(st)
				continue
			case run.Conclusion != "success":
				st.BuildURL = run.HTMLURL
				if st.Status != "failed" || st.Error == "" {
					st.Status, st.Error = "failed", "The Docker image build failed on GitHub Actions: open the run for its log"
					s.save(st)
					s.Notify("Site "+st.Name+" Docker build failed", "GitHub Actions: "+run.HTMLURL, "warning")
				}
				continue
			default:
				st.BuildURL = run.HTMLURL
			}
		}
		if sha == st.tried {
			continue
		}
		st.Pinned = "" // a newer commit than the rolled-back one: deploys resume
		if err := s.deploy(ctx, st, sha, msg, "push"); err != nil {
			slog.Warn("site deploy", "site", st.Name, "err", err)
		}
	}
}

// Run checks for changes every minute (pushes trigger a check at once when GitHub can
// reach the dashboard).
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c, cancel := context.WithTimeout(ctx, 2*time.Minute)
			s.Sync(c)
			cancel()
		}
	}
}

// OnPush reacts to a push GitHub reported for a repository with sites.
func (s *Service) OnPush(repo, branch, sha string) {
	for _, st := range s.List() {
		if strings.EqualFold(st.Repo, repo) {
			s.SyncSoon()
			return
		}
	}
}

// DNSOwner is whose DNS accounts a site uses: the person who added it.
func DNSOwner(st Site) string {
	if st.OwnerKey == "" {
		return "owner"
	}
	return st.OwnerKey
}

// SyncDNS brings a site's DNS records in line with its domains: records made for
// domains it no longer has are removed, new domains (or all, with again) are pointed at
// the server, unless the site's DNS is manual. old is the domain list before a change.
func (s *Service) SyncDNS(id string, old []string, again bool) {
	if s.DNS == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	st, err := s.Get(id)
	if err != nil {
		return
	}
	removed := map[string]dns.State{}
	for _, d := range old {
		if v, ok := st.DNS[d]; ok && !slices.Contains(st.Domains, d) {
			removed[d] = v
		}
	}
	s.DNS.Forget(ctx, DNSOwner(st), removed)
	var todo []string
	for _, d := range st.Domains {
		if v, ok := st.DNS[d]; !st.DNSManual && (again || !ok || v.Status == "error") {
			todo = append(todo, d)
		}
	}
	got := s.DNS.Point(ctx, DNSOwner(st), s.PublicIP(st.ServerID), todo)
	s.mu.Lock() // the site may have changed meanwhile: merge into the latest
	defer s.mu.Unlock()
	if st, err = s.Get(id); err != nil {
		return
	}
	if st.DNS == nil {
		st.DNS = map[string]dns.State{}
	}
	for d := range st.DNS {
		if !slices.Contains(st.Domains, d) {
			delete(st.DNS, d)
		}
	}
	for d, v := range got {
		st.DNS[d] = v
	}
	s.save(st)
}

// SetProxied switches Cloudflare's proxy for one of a site's domains.
func (s *Service) SetProxied(ctx context.Context, id, domain string, on bool) (Site, error) {
	st, err := s.Get(id)
	if err != nil {
		return st, err
	}
	v, ok := st.DNS[domain]
	if !ok || s.DNS == nil {
		return st, valid.Error("no DNS account manages " + domain)
	}
	if v, err = s.DNS.SetProxied(ctx, DNSOwner(st), v, on); err != nil {
		return st, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, err = s.Get(id); err == nil && st.DNS != nil {
		st.DNS[domain] = v
		err = s.save(st)
	}
	return st, err
}
