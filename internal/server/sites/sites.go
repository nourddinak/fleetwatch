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

	Status     string `json:"status"` // building | deploying | live | failed | waiting
	Commit     string `json:"commit"` // deployed commit of the branch (for builds: of the source)
	Message    string `json:"message"`
	DeployedAt int64  `json:"deployedAt"`
	Error      string `json:"error,omitempty"`
	BuildURL   string `json:"buildUrl,omitempty"` // the GitHub Actions run
	CreatedAt  int64  `json:"createdAt"`
	CreatedBy  string `json:"createdBy"`

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

	mu      sync.Mutex
	running map[string]pending // run ID → what it was for
	syncing sync.Mutex
}

type pending struct {
	site, commit, message, by string
}

func New(d *sql.DB, box *secret.Box, bus *live.Bus, gh GitHub) *Service {
	return &Service{db: d, box: box, Bus: bus, Now: time.Now, GitHub: gh, running: map[string]pending{},
		Notify: func(string, string, string) {}, PublicIP: func(string) string { return "" }}
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
	default:
		return valid.Error("a site is static or built")
	}
	return nil
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
	domains, err := checkDomains(in.Domains)
	if err != nil {
		return Site{}, err
	}
	keys, err := parseEnv(in.Env)
	if err != nil {
		return Site{}, err
	}
	if len(keys) > 0 && plan.Kind != "build" {
		return Site{}, valid.Error("a plain HTML site has no build, so a .env would not be used (and must never be served)")
	}
	b := make([]byte, 6)
	rand.Read(b)
	st := Site{ID: "site_" + hex.EncodeToString(b), Name: name, Repo: in.Repo, Branch: branch, ServerID: in.ServerID, Plan: plan,
		Domains: domains, Preview: preview(name, s.PublicIP(in.ServerID)), AutoDeploy: true, EnvKeys: keys,
		Status: "waiting", CreatedAt: s.Now().UnixMilli(), CreatedBy: by}
	if err := s.save(st); err != nil {
		return Site{}, err
	}
	if in.Env != "" {
		s.db.Exec(`UPDATE sites SET env_enc = ? WHERE id = ?`, s.box.Seal([]byte(in.Env), envAD+st.ID), st.ID)
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
	go s.Redeploy(context.Background(), st.ID, by)
	return st, nil
}

// setupBuild adds (or updates) the workflow in the repository and the .env secret.
func (s *Service) setupBuild(ctx context.Context, st Site, env string) error {
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
		return s.GitHub.Dispatch(ctx, st.Repo, workflowFile, st.Branch)
	}
	return nil
}

// Change edits a site's addresses, plan or automatic deploys.
type Change struct {
	Domains    []string `json:"domains"`
	AutoDeploy bool     `json:"autoDeploy"`
	Plan       *Plan    `json:"plan"`
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
	if c.Plan != nil && *c.Plan != st.Plan {
		if err := checkPlan(*c.Plan); err != nil {
			return st, err
		}
		if c.Plan.Kind != st.Plan.Kind {
			return st, valid.Error("a site cannot change between plain HTML and built: add a new site")
		}
		st.Plan, rebuild = *c.Plan, c.Plan.Kind == "build"
	}
	redeploy := !slices.Equal(domains, st.Domains) || (c.Plan != nil && !rebuild)
	st.Domains, st.AutoDeploy = domains, c.AutoDeploy
	if err := s.save(st); err != nil {
		return st, err
	}
	switch {
	case rebuild:
		if err := s.setupBuild(ctx, st, ""); err != nil {
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
	if st.Plan.Kind != "build" {
		return st, valid.Error("a plain HTML site has no build, so a .env would not be used")
	}
	keys, err := parseEnv(env)
	if err != nil {
		return st, err
	}
	if err := s.GitHub.SetSecret(ctx, st.Repo, envSecret, env); err != nil {
		return st, fmt.Errorf("could not store the .env on GitHub: %w", err)
	}
	s.db.Exec(`UPDATE sites SET env_enc = ? WHERE id = ?`, s.box.Seal([]byte(env), envAD+st.ID), st.ID)
	st.EnvKeys = keys
	if err := s.GitHub.Dispatch(ctx, st.Repo, workflowFile, st.Branch); err == nil {
		st.Status = "building"
	}
	return st, s.save(st)
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
		s.Send(st.ServerID, proto.TypeSiteRemove, proto.SiteRemove{RunID: "rm-" + st.ID, Name: st.Name})
	}
	if _, err := s.db.Exec(`DELETE FROM sites WHERE id = ?`, id); err != nil {
		return st, err
	}
	s.Bus.Publish("site.removed", map[string]string{"id": id, "serverId": st.ServerID})
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
	if st.Plan.Kind == "build" {
		if err := s.GitHub.Dispatch(ctx, st.Repo, workflowFile, st.Branch); err != nil {
			return err
		}
		st.Status, st.Error = "building", ""
		return s.save(st)
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
	domains := slices.Clone(st.Domains)
	if st.Preview = preview(st.Name, s.PublicIP(st.ServerID)); st.Preview != "" {
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
	if err := s.Send(st.ServerID, proto.TypeSiteDeploy, proto.SiteDeploy{RunID: run, Name: st.Name, Archive: s.GitHub.Tarball(st.Repo, sha), Token: tok,
		Dir: dir, Domains: domains, SPA: st.Plan.SPA}); err != nil {
		s.mu.Lock()
		delete(s.running, run)
		s.mu.Unlock()
		return err
	}
	st.Status, st.Error, st.tried = "deploying", "", sha
	return s.save(st)
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
	s.remember(st.ID, Deploy{TS: s.Now().UnixMilli(), Commit: commit, Message: msg, OK: res.OK, Error: res.Error, Log: res.Log, By: p.by})
	if res.OK {
		st.Status, st.Commit, st.Message, st.DeployedAt, st.Error = "live", commit, msg, s.Now().UnixMilli(), ""
	} else {
		st.Status, st.Error = "failed", res.Error
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
		if !st.AutoDeploy || st.Status == "deploying" {
			continue
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
		if err != nil || sha == "" || sha == st.tried {
			continue
		}
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
