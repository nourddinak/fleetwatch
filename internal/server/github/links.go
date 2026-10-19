package github

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

// Link is a dashboard user's own GitHub account: who they are there and which of the
// app's installations they can reach (their account and organizations).
type Link struct {
	Login         string  `json:"login"`
	ID            int64   `json:"id,omitempty"` // GitHub's user ID: unlike the login, never reused
	Installations []int64 `json:"installations"`
	LinkedAt      int64   `json:"linkedAt"`
}

// OwnerKey is the link key of the dashboard owner; members use their member ID.
const OwnerKey = "owner"

// CallbackPath is where GitHub sends people back after signing in.
const CallbackPath = "/api/integrations/github/oauth"

// LinkOf returns a user's link.
func (s *Service) LinkOf(key string) (Link, bool) {
	st, _, ok := s.load()
	if !ok {
		return Link{}, false
	}
	l, ok := st.Links[key]
	return l, ok
}

// Links returns every linked user, for the owner's overview.
func (s *Service) Links() map[string]Link {
	st, _, _ := s.load()
	if st.Links == nil {
		return map[string]Link{}
	}
	return st.Links
}

// Unlink forgets a user's GitHub account (the app stays installed on it).
func (s *Service) Unlink(key string) error {
	st, _, ok := s.load()
	if !ok {
		return ErrNotConnected
	}
	delete(st.Links, key)
	return s.save(st, nil)
}

func (s *Service) newState(key string) string {
	b := make([]byte, 16)
	rand.Read(b)
	st := hex.EncodeToString(b)
	s.mu.Lock()
	for k, p := range s.linking {
		if s.Now().After(p.expires) {
			delete(s.linking, k)
		}
	}
	s.linking[st] = pendingState{by: key, expires: s.Now().Add(15 * time.Minute)}
	s.mu.Unlock()
	return st
}

func (s *Service) takeState(st string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.linking[st]
	delete(s.linking, st)
	return p.by, ok && s.Now().Before(p.expires)
}

// appClientID asks GitHub for the app's OAuth client ID (once).
func (s *Service) appClientID(ctx context.Context) (string, error) {
	s.mu.Lock()
	id := s.clientID
	s.mu.Unlock()
	if id != "" {
		return id, nil
	}
	st, k, ok := s.load()
	if !ok {
		return "", ErrNotConnected
	}
	jwt, err := appJWT(st.App.ID, k.PEM, s.Now())
	if err != nil {
		return "", err
	}
	var app struct {
		ClientID string `json:"client_id"`
	}
	if err := s.call(ctx, http.MethodGet, "/app", "Bearer "+jwt, nil, &app); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.clientID = app.ClientID
	s.mu.Unlock()
	return app.ClientID, nil
}

// SignInURL starts linking a dashboard user's own GitHub account: GitHub asks them to
// sign in to the app, then sends them back to CallbackPath.
func (s *Service) SignInURL(ctx context.Context, publicURL, key string) (string, error) {
	id, err := s.appClientID(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{"client_id": {id}, "redirect_uri": {publicURL + CallbackPath}, "state": {s.newState(key)}}
	return s.Web + "/login/oauth/authorize?" + q.Encode(), nil
}

// InstallURL sends a user to install the app on their own account; GitHub's setup
// redirect brings the state back (AfterInstall), then the sign-in runs again.
func (s *Service) InstallURL(key string) (string, error) {
	st, _, ok := s.load()
	if !ok {
		return "", ErrNotConnected
	}
	return s.Web + "/apps/" + st.App.Slug + "/installations/new?state=" + s.newState("install:"+key), nil
}

// AfterInstall reports whose install a setup redirect belongs to ("" if none).
func (s *Service) AfterInstall(state string) string {
	if state == "" {
		return ""
	}
	key, ok := s.takeState(state)
	if !ok || !strings.HasPrefix(key, "install:") {
		return ""
	}
	return strings.TrimPrefix(key, "install:")
}

// FinishSignIn takes GitHub's one-time code, learns who the user is there and which of
// the app's installations they can reach (GitHub answers that, so nobody can claim an
// installation that is not theirs), and stores the link. The user token is not kept.
func (s *Service) FinishSignIn(ctx context.Context, code, state string) (string, Link, error) {
	key, ok := s.takeState(state)
	if !ok || strings.HasPrefix(key, "install:") || key == loginKey {
		return "", Link{}, valid.Error("this GitHub sign-in was not started here, or took too long; start again")
	}
	if code == "" || len(code) > 200 {
		return "", Link{}, valid.Error("GitHub did not send a code")
	}
	auth, user, err := s.userOf(ctx, code)
	if err != nil {
		return "", Link{}, err
	}
	var inst struct {
		Installations []struct {
			ID int64 `json:"id"`
		} `json:"installations"`
	}
	if err := s.call(ctx, http.MethodGet, "/user/installations?per_page=100", auth, nil, &inst); err != nil {
		return "", Link{}, err
	}
	l := Link{Login: user.Login, ID: user.ID, Installations: []int64{}, LinkedAt: s.Now().UnixMilli()}
	for _, i := range inst.Installations {
		l.Installations = append(l.Installations, i.ID)
	}
	if len(l.Installations) > 0 {
		s.Refresh(ctx) // the dashboard learns about installations made just now
	}
	st, _, _ := s.load()
	if st.Links == nil {
		st.Links = map[string]Link{}
	}
	st.Links[key] = l
	if err := s.save(st, nil); err != nil {
		return "", Link{}, err
	}
	s.publish()
	return key, l, nil
}

// ReposOf is what one dashboard user may deploy: the owner sees every repository the
// app can reach, a member only those of their own linked installations.
func (s *Service) ReposOf(ctx context.Context, key string) ([]Repo, error) {
	all, err := s.Repos(ctx)
	if err != nil || key == OwnerKey {
		return all, err
	}
	l, ok := s.LinkOf(key)
	if !ok {
		return []Repo{}, nil
	}
	out := []Repo{}
	for _, r := range all {
		if slices.Contains(l.Installations, r.Installation) {
			out = append(out, r)
		}
	}
	return out, nil
}

// CanDeploy reports whether a user may deploy from a repository.
func (s *Service) CanDeploy(ctx context.Context, key, repo string) bool {
	repos, err := s.ReposOf(ctx, key)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(repos, func(r Repo) bool { return strings.EqualFold(r.FullName, repo) })
}

type ghUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// userOf trades a sign-in code for the GitHub user behind it (and their token, as an
// Authorization value).
func (s *Service) userOf(ctx context.Context, code string) (string, ghUser, error) {
	_, k, connected := s.load()
	if !connected {
		return "", ghUser{}, ErrNotConnected
	}
	id, err := s.appClientID(ctx)
	if err != nil {
		return "", ghUser{}, err
	}
	body, _ := json.Marshal(map[string]string{"client_id": id, "client_secret": k.ClientSecret, "code": code})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.Web+"/login/oauth/access_token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", ghUser{}, fmt.Errorf("cannot reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	if tok.AccessToken == "" {
		return "", ghUser{}, valid.Error("GitHub refused the sign-in: " + tok.Error)
	}
	auth := "Bearer " + tok.AccessToken
	var user ghUser
	if err := s.call(ctx, http.MethodGet, "/user", auth, nil, &user); err != nil {
		return "", ghUser{}, err
	}
	return auth, user, nil
}

// loginKey marks a sign-in to the dashboard itself, rather than linking an account.
const loginKey = "login:"

// LoginURL starts signing in to the dashboard with GitHub; the state goes back to the
// browser too, so the end of the sign-in can be tied to the browser that started it.
func (s *Service) LoginURL(ctx context.Context, publicURL string) (string, string, error) {
	u, err := s.SignInURL(ctx, publicURL, loginKey)
	if err != nil {
		return "", "", err
	}
	pu, _ := url.Parse(u)
	return u, pu.Query().Get("state"), nil
}

// IsLogin reports whether a sign-in state was started by LoginURL, without using it up.
func (s *Service) IsLogin(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.linking[state].by == loginKey
}

// FinishLogin answers whose dashboard account a GitHub sign-in is: the user who linked
// that GitHub account, matched by its user ID (which a rename does not change).
func (s *Service) FinishLogin(ctx context.Context, code, state string) (key, login string, err error) {
	k, ok := s.takeState(state)
	if !ok || k != loginKey {
		return "", "", valid.Error("this GitHub sign-in was not started here, or took too long; start again")
	}
	if code == "" || len(code) > 200 {
		return "", "", valid.Error("GitHub did not send a code")
	}
	_, user, err := s.userOf(ctx, code)
	if err != nil {
		return "", "", err
	}
	links := s.Links()
	for k, l := range links {
		if l.ID != 0 && l.ID == user.ID {
			return k, user.Login, nil
		}
	}
	for _, l := range links {
		if l.ID == 0 && strings.EqualFold(l.Login, user.Login) {
			return "", "", valid.Error("link your GitHub account again once (Sites → Unlink, then Connect your GitHub); after that you can sign in with it")
		}
	}
	return "", "", valid.Error("no one here has linked the GitHub account " + user.Login + ": sign in with your password, link GitHub on the Sites page, then use GitHub next time")
}
