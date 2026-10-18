// Package github connects the dashboard to GitHub through a GitHub App of its own.
//
// The app is created in one click with GitHub's app manifest flow: the dashboard sends
// the browser to GitHub with a ready-made app description, GitHub creates the app and
// hands back its keys. The owner then installs it on the repositories they choose. The
// dashboard talks to GitHub with one-hour installation tokens signed by the app's key,
// and GitHub sends push events to a webhook checked with the app's secret.
package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/valid"
)

const secretAD = "integration.github"

var ErrNotConnected = valid.Error("GitHub is not connected")

// App is the GitHub App the dashboard created (what may be shown).
type App struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Owner     string `json:"owner"` // account (user or organization) that owns the app
	HTMLURL   string `json:"htmlUrl"`
	CreatedAt int64  `json:"createdAt"`
	CreatedBy string `json:"createdBy"`
}

// Installation is the app installed on one account.
type Installation struct {
	ID            int64    `json:"id"`
	Account       string   `json:"account"`
	AccountType   string   `json:"accountType"`   // User | Organization
	RepoSelection string   `json:"repoSelection"` // all | selected
	Suspended     bool     `json:"suspended"`
	Missing       []string `json:"missing,omitempty"` // permissions deploys need that were not granted
}

// Push is one push GitHub reported.
type Push struct {
	TS      int64  `json:"ts"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Message string `json:"message"`
	Pusher  string `json:"pusher"`
}

// state is what is stored: App and Installations in the clear, keys encrypted.
type state struct {
	App           App            `json:"app"`
	Installations []Installation `json:"installations"`
	Pushes        []Push         `json:"pushes"` // newest first
	// Links: dashboard users (owner or member ID) and their own GitHub account.
	Links map[string]Link `json:"links,omitempty"`
}

type keys struct {
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhookSecret"`
	ClientSecret  string `json:"clientSecret"`
}

type Service struct {
	db  *sql.DB
	box *secret.Box
	Bus *live.Bus
	Now func() time.Time

	API    string // https://api.github.com
	Web    string // https://github.com
	Client *http.Client

	// OnPush is told about every push (a site may need deploying).
	OnPush func(repo, branch, sha string)

	mu       sync.Mutex
	pending  map[string]pendingState // manifest flow state → who started it
	linking  map[string]pendingState // sign-in state → which dashboard user
	tokens   map[int64]token         // installation ID → token
	clientID string                  // the app's OAuth client ID (from GitHub, cached)
}

type pendingState struct {
	by      string
	expires time.Time
}

type token struct {
	value   string
	expires time.Time
}

func New(d *sql.DB, box *secret.Box, bus *live.Bus) *Service {
	return &Service{db: d, box: box, Bus: bus, Now: time.Now, API: "https://api.github.com", Web: "https://github.com",
		Client: &http.Client{Timeout: 30 * time.Second}, pending: map[string]pendingState{}, linking: map[string]pendingState{}, tokens: map[int64]token{}}
}

// ---------- storage ----------

func (s *Service) load() (state, keys, bool) {
	var pub string
	var enc []byte
	var st state
	var k keys
	if s.db.QueryRow(`SELECT public, secret_enc FROM integrations WHERE provider = 'github'`).Scan(&pub, &enc) != nil {
		return st, k, false
	}
	json.Unmarshal([]byte(pub), &st)
	if raw, err := s.box.Open(enc, secretAD); err == nil {
		json.Unmarshal(raw, &k)
	}
	return st, k, st.App.ID != 0
}

func (s *Service) save(st state, k *keys) error {
	if st.Installations == nil {
		st.Installations = []Installation{}
	}
	if st.Pushes == nil {
		st.Pushes = []Push{}
	}
	pub, _ := json.Marshal(st)
	if k == nil {
		_, err := s.db.Exec(`UPDATE integrations SET public = ? WHERE provider = 'github'`, string(pub))
		return err
	}
	raw, _ := json.Marshal(k)
	_, err := s.db.Exec(`INSERT INTO integrations (provider, public, secret_enc, created_at) VALUES ('github', ?, ?, ?)
		ON CONFLICT (provider) DO UPDATE SET public = excluded.public, secret_enc = excluded.secret_enc`,
		string(pub), s.box.Seal(raw, secretAD), s.Now().UnixMilli())
	return err
}

// Status is what the dashboard shows.
type Status struct {
	Connected     bool           `json:"connected"`
	App           *App           `json:"app,omitempty"`
	Installations []Installation `json:"installations"`
	Pushes        []Push         `json:"pushes"`
	InstallURL    string         `json:"installUrl,omitempty"`
}

func (s *Service) Status() Status {
	st, _, ok := s.load()
	out := Status{Connected: ok, Installations: []Installation{}, Pushes: []Push{}}
	if !ok {
		return out
	}
	out.App, out.InstallURL = &st.App, s.Web+"/apps/"+st.App.Slug+"/installations/new"
	if st.Installations != nil {
		out.Installations = st.Installations
	}
	if st.Pushes != nil {
		out.Pushes = st.Pushes
	}
	return out
}

// ---------- creating the app (manifest flow) ----------

var orgRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// Manifest starts the creation of the app. It returns where the browser must POST the
// manifest (a form field named "manifest") and the manifest itself. org creates the app
// under an organization instead of the signed-in GitHub user.
func (s *Service) Manifest(publicURL, org, by string) (action, manifest string, err error) {
	if _, _, ok := s.load(); ok {
		return "", "", valid.Error("GitHub is already connected; disconnect it first")
	}
	if org != "" && !orgRe.MatchString(org) {
		return "", "", valid.Error("that is not a GitHub organization name")
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Host == "" {
		return "", "", valid.Error("the dashboard's address is unknown; set OPS_PUBLIC_URL")
	}
	b := make([]byte, 16)
	rand.Read(b)
	st := hex.EncodeToString(b)
	s.mu.Lock()
	for k, p := range s.pending {
		if s.Now().After(p.expires) {
			delete(s.pending, k)
		}
	}
	s.pending[st] = pendingState{by: by, expires: s.Now().Add(time.Hour)}
	s.mu.Unlock()

	// App names are unique on all of GitHub: the dashboard's host and a few random letters.
	host := strings.Split(u.Hostname(), ".")[0]
	if net.ParseIP(u.Hostname()) != nil {
		host = "dashboard"
	}
	if len(host) > 18 {
		host = host[:18]
	}
	m := map[string]any{
		"name":            fmt.Sprintf("Fleetwatch %s %s", host, st[:4]),
		"url":             publicURL,
		"redirect_url":    publicURL + "/api/integrations/github/created",
		"setup_url":       publicURL + "/api/integrations/github/installed",
		"setup_on_update": true,
		// Public: team members install it on their own GitHub accounts.
		"public":        true,
		"callback_urls": []string{publicURL + CallbackPath},
		"description":   "Fleetwatch dashboard at " + publicURL,
		// contents/workflows/secrets: deploys add a build workflow and its .env secret;
		// actions: they follow the builds.
		"default_permissions": map[string]string{
			"contents": "write", "metadata": "read", "deployments": "write", "statuses": "write", "pull_requests": "read",
			"workflows": "write", "secrets": "write", "actions": "write",
		},
		"default_events": []string{"push", "pull_request", "workflow_run"},
		// GitHub only reaches a public HTTPS address; elsewhere the webhook stays off.
		"hook_attributes": map[string]any{"url": publicURL + "/api/integrations/github/webhook", "active": u.Scheme == "https" && !local(u.Hostname())},
	}
	raw, _ := json.Marshal(m)
	action = s.Web + "/settings/apps/new?state=" + st
	if org != "" {
		action = s.Web + "/organizations/" + org + "/settings/apps/new?state=" + st
	}
	return action, string(raw), nil
}

func local(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") || host == "::1"
}

// Created finishes the creation: GitHub sent the browser back with a one-time code.
func (s *Service) Created(ctx context.Context, code, st string) (installURL, by string, err error) {
	s.mu.Lock()
	p, ok := s.pending[st]
	delete(s.pending, st)
	s.mu.Unlock()
	if !ok || s.Now().After(p.expires) {
		return "", "", valid.Error("this GitHub connection was not started here, or took longer than an hour; start again")
	}
	if code == "" || len(code) > 200 {
		return "", "", valid.Error("GitHub did not send a code")
	}
	var conv struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		HTMLURL       string `json:"html_url"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := s.call(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &conv); err != nil {
		return "", "", err
	}
	if conv.ID == 0 || conv.PEM == "" {
		return "", "", errors.New("GitHub did not return the app's keys")
	}
	app := App{ID: conv.ID, Slug: conv.Slug, Name: conv.Name, Owner: conv.Owner.Login, HTMLURL: conv.HTMLURL, CreatedAt: s.Now().UnixMilli(), CreatedBy: p.by}
	if err := s.save(state{App: app}, &keys{PEM: conv.PEM, WebhookSecret: conv.WebhookSecret, ClientSecret: conv.ClientSecret}); err != nil {
		return "", "", err
	}
	s.publish()
	return s.Web + "/apps/" + app.Slug + "/installations/new", p.by, nil
}

// Installed records an installation GitHub sent the browser back about, once GitHub
// confirms it belongs to this app (so the unsigned redirect cannot add anything else).
func (s *Service) Installed(ctx context.Context, installationID int64) error {
	if installationID <= 0 {
		return valid.Error("GitHub did not say which installation")
	}
	return s.Refresh(ctx)
}

// Refresh reloads the app's installations from GitHub.
func (s *Service) Refresh(ctx context.Context) error {
	st, k, ok := s.load()
	if !ok {
		return ErrNotConnected
	}
	jwt, err := appJWT(st.App.ID, k.PEM, s.Now())
	if err != nil {
		return err
	}
	var list []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
		RepositorySelection string            `json:"repository_selection"`
		SuspendedAt         *string           `json:"suspended_at"`
		Permissions         map[string]string `json:"permissions"`
	}
	if err := s.call(ctx, http.MethodGet, "/app/installations?per_page=100", "Bearer "+jwt, nil, &list); err != nil {
		return err
	}
	st.Installations = []Installation{}
	for _, i := range list {
		st.Installations = append(st.Installations, Installation{ID: i.ID, Account: i.Account.Login, AccountType: i.Account.Type,
			RepoSelection: i.RepositorySelection, Suspended: i.SuspendedAt != nil, Missing: missing(i.Permissions)})
	}
	if err := s.save(st, nil); err != nil {
		return err
	}
	s.publish()
	return nil
}

// Disconnect uninstalls the app everywhere (best effort) and forgets it. The app itself
// stays on GitHub until deleted there (GitHub has no API for that).
func (s *Service) Disconnect(ctx context.Context) (App, error) {
	st, k, ok := s.load()
	if !ok {
		return App{}, ErrNotConnected
	}
	if jwt, err := appJWT(st.App.ID, k.PEM, s.Now()); err == nil {
		for _, i := range st.Installations {
			s.call(ctx, http.MethodDelete, fmt.Sprintf("/app/installations/%d", i.ID), "Bearer "+jwt, nil, nil)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM integrations WHERE provider = 'github'`); err != nil {
		return App{}, err
	}
	s.mu.Lock()
	s.tokens = map[int64]token{}
	s.mu.Unlock()
	s.publish()
	return st.App, nil
}

// ---------- repositories ----------

// Repo is a repository the app can see.
type Repo struct {
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"defaultBranch"`
	HTMLURL       string `json:"htmlUrl"`
	PushedAt      string `json:"pushedAt"`
	Installation  int64  `json:"installationId"`
}

// Repos lists every repository of every installation.
func (s *Service) Repos(ctx context.Context) ([]Repo, error) {
	st, _, ok := s.load()
	if !ok {
		return nil, ErrNotConnected
	}
	out := []Repo{}
	for _, inst := range st.Installations {
		tok, err := s.installationToken(ctx, inst.ID)
		if err != nil {
			return nil, err
		}
		for page := 1; page <= 10; page++ { // shortcut: 1000 repos per installation at most; enough for a fleet dashboard
			var res struct {
				Repositories []struct {
					FullName      string `json:"full_name"`
					Private       bool   `json:"private"`
					DefaultBranch string `json:"default_branch"`
					HTMLURL       string `json:"html_url"`
					PushedAt      string `json:"pushed_at"`
				} `json:"repositories"`
			}
			if err := s.call(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), "token "+tok, nil, &res); err != nil {
				return nil, err
			}
			for _, r := range res.Repositories {
				out = append(out, Repo{FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, HTMLURL: r.HTMLURL, PushedAt: r.PushedAt, Installation: inst.ID})
			}
			if len(res.Repositories) < 100 {
				break
			}
		}
	}
	return out, nil
}

// installationToken returns a token for one installation, made fresh when the cached
// one is about to expire (they last an hour).
func (s *Service) installationToken(ctx context.Context, id int64) (string, error) {
	s.mu.Lock()
	t, ok := s.tokens[id]
	s.mu.Unlock()
	if ok && s.Now().Before(t.expires.Add(-5*time.Minute)) {
		return t.value, nil
	}
	st, k, connected := s.load()
	if !connected {
		return "", ErrNotConnected
	}
	jwt, err := appJWT(st.App.ID, k.PEM, s.Now())
	if err != nil {
		return "", err
	}
	var res struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := s.call(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", id), "Bearer "+jwt, nil, &res); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[id] = token{res.Token, res.ExpiresAt}
	s.mu.Unlock()
	return res.Token, nil
}

// ---------- webhook ----------

// Webhook checks a delivery's signature and handles it. It returns false when the
// signature is wrong (the caller answers 401).
func (s *Service) Webhook(event, signature string, body []byte) bool {
	st, k, ok := s.load()
	if !ok || k.WebhookSecret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(k.WebhookSecret))
	mac.Write(body)
	if !hmac.Equal([]byte(signature), []byte("sha256="+hex.EncodeToString(mac.Sum(nil)))) {
		return false
	}
	switch event {
	case "installation", "installation_repositories":
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.Refresh(ctx)
		}()
	case "push":
		var p struct {
			Ref        string `json:"ref"`
			After      string `json:"after"`
			Deleted    bool   `json:"deleted"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			HeadCommit *struct {
				Message string `json:"message"`
			} `json:"head_commit"`
			Pusher struct {
				Name string `json:"name"`
			} `json:"pusher"`
		}
		if json.Unmarshal(body, &p) != nil || p.Deleted || !strings.HasPrefix(p.Ref, "refs/heads/") {
			return true
		}
		push := Push{TS: s.Now().UnixMilli(), Repo: p.Repository.FullName, Branch: strings.TrimPrefix(p.Ref, "refs/heads/"), Commit: p.After, Pusher: p.Pusher.Name}
		if p.HeadCommit != nil {
			push.Message, _, _ = strings.Cut(p.HeadCommit.Message, "\n")
		}
		st.Pushes = append([]Push{push}, st.Pushes...)
		if len(st.Pushes) > 30 {
			st.Pushes = st.Pushes[:30]
		}
		s.save(st, nil)
		s.Bus.Publish("github.push", push)
		if s.OnPush != nil {
			go s.OnPush(push.Repo, push.Branch, push.Commit)
		}
	case "workflow_run":
		var w struct {
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			WorkflowRun struct {
				HeadBranch string `json:"head_branch"`
				HeadSHA    string `json:"head_sha"`
			} `json:"workflow_run"`
		}
		if json.Unmarshal(body, &w) == nil && s.OnPush != nil {
			go s.OnPush(w.Repository.FullName, w.WorkflowRun.HeadBranch, w.WorkflowRun.HeadSHA) // a build started or ended
		}
	}
	return true
}

func (s *Service) publish() {
	s.Bus.Publish("github", s.Status())
}

// ---------- GitHub API ----------

func (s *Service) call(ctx context.Context, method, path, auth string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.API+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "fleetwatch")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
		return valid.Error(fmt.Sprintf("GitHub answered %d: %s", resp.StatusCode, e.Message))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// appJWT is the app's own short-lived token (RS256, 9 minutes), used to list its
// installations and make installation tokens.
func appJWT(appID int64, pemKey string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return "", errors.New("the GitHub app key is unreadable")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		k8, err8 := x509.ParsePKCS8PrivateKey(block.Bytes)
		var ok bool
		if key, ok = k8.(*rsa.PrivateKey); err8 != nil || !ok {
			return "", errors.New("the GitHub app key is unreadable")
		}
	}
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": fmt.Sprint(appID)})
	signing := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
