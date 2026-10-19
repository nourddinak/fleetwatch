package sites

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/github"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
)

// fakeGH is a repository host in memory.
type fakeGH struct {
	mu          sync.Mutex
	files       map[string]map[string]string // repo → path → content
	heads       map[string]string            // repo@branch → sha
	puts        []string
	secrets     map[string]string
	dispatch    int
	dispatchErr error
	run         *github.Run
	syncs       int // calls to Connected, which only Sync makes
}

func (f *fakeGH) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs++
	return true
}
func (f *fakeGH) RepoInfo(_ context.Context, repo string) (string, bool, error) {
	return "main", false, nil
}
func (f *fakeGH) RepoToken(context.Context, string) (string, error) { return "ghs_x", nil }
func (f *fakeGH) Head(_ context.Context, repo, branch string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := f.heads[repo+"@"+branch]
	if branch == github.BuildBranchName && sha != "" {
		return sha, "Build of src" + sha, nil
	}
	return sha, "commit " + sha, nil
}
func (f *fakeGH) List(_ context.Context, repo, dir, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for p := range f.files[repo] {
		if dir != "" {
			if !strings.HasPrefix(p, dir+"/") {
				continue
			}
			p = strings.TrimPrefix(p, dir+"/")
		}
		first, rest, isDir := strings.Cut(p, "/")
		if isDir && rest != "" {
			first += "/"
		}
		if !seen[first] {
			seen[first] = true
			out = append(out, first)
		}
	}
	return out, nil
}
func (f *fakeGH) File(_ context.Context, repo, path, _ string) ([]byte, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.files[repo][path]
	return []byte(c), "sha", ok, nil
}
func (f *fakeGH) PutFile(_ context.Context, repo, path, _ string, content []byte, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := f.files[repo][path] != string(content)
	f.files[repo][path] = string(content)
	f.puts = append(f.puts, path)
	return changed, nil
}
func (f *fakeGH) SetSecret(_ context.Context, repo, name, value string) error {
	f.secrets[repo+"/"+name] = value
	return nil
}
func (f *fakeGH) Dispatch(context.Context, string, string, string) error {
	f.dispatch++
	return f.dispatchErr
}
func (f *fakeGH) LastRun(context.Context, string, string) (*github.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run, nil
}
func (f *fakeGH) DeleteFile(_ context.Context, repo, path, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files[repo], path)
	return nil
}
func (f *fakeGH) DeleteBranch(_ context.Context, repo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.heads, repo+"@"+branch)
	return nil
}
func (f *fakeGH) DeleteSecret(_ context.Context, repo, name string) error {
	delete(f.secrets, repo+"/"+name)
	return nil
}
func (f *fakeGH) Tarball(repo, sha string) string {
	return "https://api.github.test/repos/" + repo + "/tarball/" + sha
}

func setup(t *testing.T) (*Service, *fakeGH, *[]proto.SiteDeploy) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'web-1', 0)`)
	box, _ := secret.New(make([]byte, 32))
	gh := &fakeGH{files: map[string]map[string]string{
		"me/blog": {"index.html": "<h1>hi</h1>", "style.css": "", "Caddyfile": ""},
		"me/shop": {"package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"5"}}`, "pnpm-lock.yaml": "", ".nvmrc": "20", "src/main.js": ""},
		"me/docs": {"README.md": "", "docs/index.html": ""},
		"me/none": {"README.md": ""},
	}, heads: map[string]string{"me/blog@main": "b1"}, secrets: map[string]string{}}
	s := New(d, box, live.NewBus(), gh)
	var sent []proto.SiteDeploy
	var mu sync.Mutex
	s.Send = func(_, typ string, v any) error {
		if dep, ok := v.(proto.SiteDeploy); ok {
			mu.Lock()
			sent = append(sent, dep)
			mu.Unlock()
		}
		return nil
	}
	s.PublicIP = func(string) string { return "203.0.113.5" }
	return s, gh, &sent
}

func answer(s *Service, d proto.SiteDeploy, ok bool) {
	raw, _ := json.Marshal(proto.SiteResult{RunID: d.RunID, Name: d.Name, OK: ok, Error: map[bool]string{false: "boom"}[ok]})
	s.OnResult("s1", raw)
}

func TestDetect(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	p, err := s.Detect(ctx, "me/shop", "main")
	if err != nil || p.Kind != "build" || p.Framework != "Vite" || p.Output != "dist" || !strings.HasPrefix(p.Install, "pnpm") || p.Node != ".nvmrc" || !p.SPA {
		t.Fatalf("%+v %v", p, err)
	}
	if p, _ := s.Detect(ctx, "me/blog", "main"); p.Kind != "static" || p.Dir != "" {
		t.Fatalf("%+v", p)
	}
	if p, _ := s.Detect(ctx, "me/docs", "main"); p.Kind != "static" || p.Dir != "docs" {
		t.Fatalf("%+v", p)
	}
	if _, err := s.Detect(ctx, "me/none", "main"); err == nil {
		t.Fatal("a repository without a site was accepted")
	}
}

func TestStaticSiteAndPushes(t *testing.T) {
	s, gh, sent := setup(t)
	ctx := context.Background()
	st, err := s.Create(ctx, Input{Repo: "me/blog", ServerID: "s1", Domains: []string{"Blog.Example.com."}}, "Nour")
	if err != nil || st.Name != "blog" || st.Preview != "blog.203-0-113-5.sslip.io" || st.Domains[0] != "blog.example.com" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := s.Create(ctx, Input{Repo: "me/blog", ServerID: "s1"}, "Nour"); err == nil {
		t.Fatal("two sites with one name on a server")
	}
	if _, err := s.Create(ctx, Input{Repo: "me/blog", ServerID: "s1", Name: "blog2", Env: "A=1"}, "Nour"); err == nil {
		t.Fatal(".env accepted for a plain site")
	}
	waitSent(t, sent, 1)
	d := (*sent)[0]
	if d.Name != "blog" || d.Dir != "" || d.Token != "ghs_x" || d.Domains[0] != "blog.203-0-113-5.sslip.io" || d.Domains[1] != "blog.example.com" || !strings.HasSuffix(d.Archive, "/tarball/b1") {
		t.Fatalf("%+v", d)
	}
	answer(s, d, true)
	if st, _ := s.Get(st.ID); st.Status != "live" || st.Commit != "b1" {
		t.Fatalf("%+v", st)
	}
	// Nothing new: nothing deployed.
	s.Sync(ctx)
	if len(*sent) != 1 {
		t.Fatal("deployed without a change")
	}
	// A push: deployed by itself; a failed deploy is not retried for the same commit.
	gh.heads["me/blog@main"] = "b2"
	s.Sync(ctx)
	if len(*sent) != 2 || !strings.HasSuffix((*sent)[1].Archive, "/b2") {
		t.Fatalf("%+v", *sent)
	}
	answer(s, (*sent)[1], false)
	s.Sync(ctx)
	if st, _ := s.Get(st.ID); st.Status != "failed" || st.Commit != "b1" || len(*sent) != 2 {
		t.Fatalf("%+v %d", st, len(*sent))
	}
	if h := s.History(st.ID); len(h) != 2 || h[0].OK || !h[1].OK {
		t.Fatalf("%+v", h)
	}
}

func TestBuiltSite(t *testing.T) {
	s, gh, sent := setup(t)
	ctx := context.Background()
	st, err := s.Create(ctx, Input{Repo: "me/shop", ServerID: "s1", Env: "VITE_API=https://api.example.com\n# comment\n"}, "Nour")
	if err != nil || st.Status != "building" || st.EnvKeys[0] != "VITE_API" {
		t.Fatalf("%+v %v", st, err)
	}
	wf := gh.files["me/shop"][".github/workflows/fleetwatch.yml"]
	for _, want := range []string{"pnpm install --frozen-lockfile", "pnpm run build", "node-version-file: .nvmrc", "corepack enable", "out='dist'", "fleetwatch-build", "secrets.FLEETWATCH_ENV"} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q", want)
		}
	}
	if gh.secrets["me/shop/FLEETWATCH_ENV"] == "" {
		t.Fatal(".env not stored on GitHub")
	}
	if env, _ := s.Env(st.ID); !strings.Contains(env, "VITE_API") {
		t.Fatal(".env not kept")
	}
	// Create syncs in the background, and a Sync while that one runs is skipped: wait for it.
	for i := 0; i < 200; i++ {
		gh.mu.Lock()
		n := gh.syncs
		gh.mu.Unlock()
		if n > 0 {
			break
		}
		sleep()
	}
	s.syncing.Lock()
	s.syncing.Unlock()
	// The build finished: the build branch has a commit; it is deployed from its root.
	gh.heads["me/shop@"+github.BuildBranchName] = "x1"
	gh.run = &github.Run{Status: "completed", Conclusion: "success"}
	s.Sync(ctx)
	waitSent(t, sent, 1)
	if (*sent)[0].Dir != "" || !(*sent)[0].SPA {
		t.Fatalf("%+v", (*sent)[0])
	}
	answer(s, (*sent)[0], true)
	if st, _ := s.Get(st.ID); st.Status != "live" || st.Commit != "srcx1" {
		t.Fatalf("source commit not shown: %+v", st)
	}
	// A failed build is reported.
	gh.run = &github.Run{Status: "in_progress"}
	s.Sync(ctx)
	gh.run = &github.Run{Status: "completed", Conclusion: "failure", HTMLURL: "https://github.com/me/shop/actions/runs/1"}
	s.Sync(ctx)
	if st, _ := s.Get(st.ID); st.Status != "failed" || st.BuildURL == "" {
		t.Fatalf("%+v", st)
	}
	// Bad commands cannot reach the workflow file.
	bad := st.Plan
	bad.Build = "npm run build\n  evil: true"
	if _, err := s.Update(ctx, st.ID, Change{Plan: &bad, AutoDeploy: true}, "Nour"); err == nil {
		t.Fatal("multi-line command accepted")
	}
}

func waitSent(t *testing.T, sent *[]proto.SiteDeploy, n int) {
	t.Helper()
	for i := 0; i < 200 && len(*sent) < n; i++ {
		sleep()
	}
	if len(*sent) < n {
		t.Fatalf("%d deploys sent, want %d", len(*sent), n)
	}
}

func TestHelpers(t *testing.T) {
	if slug("My Cool_Site!!") != "my-cool-site" || preview("x", "10.0.0.1") != "" || preview("x", "1.2.3.4") != "x.1-2-3-4.sslip.io" {
		t.Fatal(slug("My Cool_Site!!"), preview("x", "10.0.0.1"))
	}
	if _, err := parseEnv("A=1\nnot a line"); err == nil {
		t.Fatal("bad .env accepted")
	}
	if _, err := checkDomains([]string{"exa mple.com"}); err == nil {
		t.Fatal("bad domain accepted")
	}
}

func sleep() { time.Sleep(10 * time.Millisecond) }

// Retry while GitHub is already building: the refused dispatch is not a failure.
func TestRedeployWhileBuilding(t *testing.T) {
	s, gh, _ := setup(t)
	st, err := s.Create(context.Background(), Input{Repo: "me/shop", ServerID: "s1"}, "Nour")
	if err != nil {
		t.Fatal(err)
	}
	gh.dispatchErr = errors.New("GitHub answered 403: Resource not accessible by integration")
	gh.run = &github.Run{ID: 1, Status: "in_progress"}
	if err := s.Redeploy(context.Background(), st.ID, "Nour"); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.Get(st.ID); st.Status != "building" || st.Error != "" {
		t.Fatalf("%s %q", st.Status, st.Error)
	}
	gh.run = &github.Run{ID: 1, Status: "completed", Conclusion: "success"}
	if err := s.Redeploy(context.Background(), st.ID, "Nour"); err == nil || !strings.Contains(err.Error(), "GitHub said") {
		t.Fatalf("a refused build with nothing running: %v", err)
	}
}

// Deleting a built site takes its workflow, build branch and .env secret off GitHub.
func TestDeleteCleansGitHub(t *testing.T) {
	s, gh, _ := setup(t)
	st, err := s.Create(context.Background(), Input{Repo: "me/shop", ServerID: "s1", Env: "VITE_API=https://api.example.com"}, "Nour")
	if err != nil {
		t.Fatal(err)
	}
	gh.heads["me/shop@"+github.BuildBranchName] = "b9"
	if _, ok := gh.files["me/shop"][".github/workflows/"+workflowFile]; !ok || gh.secrets["me/shop/"+envSecret] == "" {
		t.Fatal("setup did not add the workflow and secret")
	}
	if _, err := s.Delete(st.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := gh.files["me/shop"][".github/workflows/"+workflowFile]; ok || gh.secrets["me/shop/"+envSecret] != "" || gh.heads["me/shop@"+github.BuildBranchName] != "" {
		t.Fatalf("left on GitHub: %v %v %v", gh.files["me/shop"], gh.secrets, gh.heads)
	}
}

// The owner's Caddy config goes out with the next deploy; the live one comes back.
func TestCustomCaddy(t *testing.T) {
	s, _, sent := setup(t)
	ctx := context.Background()
	st, err := s.Create(ctx, Input{Repo: "me/blog", ServerID: "s1"}, "Nour")
	if err != nil {
		t.Fatal(err)
	}
	waitSent(t, sent, 1)
	answer(s, (*sent)[0], true)
	if _, err := s.SetCaddy(st.ID, "blog.example.com {\r\n\tfile_server\r\n}\r\n", "Nour"); err != nil {
		t.Fatal(err)
	}
	waitSent(t, sent, 2)
	d := (*sent)[1]
	if d.CaddyConfig != "blog.example.com {\n\tfile_server\n}" {
		t.Fatalf("%q", d.CaddyConfig)
	}
	raw, _ := json.Marshal(proto.SiteResult{RunID: d.RunID, Name: d.Name, OK: true, Caddy: "custom", CaddyConfig: "# Site blog\n" + d.CaddyConfig})
	s.OnResult("s1", raw)
	if got, _ := s.Get(st.ID); got.CaddySource != "custom" || !strings.Contains(got.CaddyConfig, "file_server") || got.CaddyCustom == "" {
		t.Fatalf("%+v", got)
	}
	if _, err := s.SetCaddy(st.ID, strings.Repeat("x", 70<<10), "Nour"); err == nil {
		t.Fatal("huge config accepted")
	}
}
