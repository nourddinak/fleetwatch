package github

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/nacl/box"

	"fleetwatch/internal/server/valid"
)

// What deploys need from a repository, as the app (installation tokens).

var errNoRepo = valid.Error("the GitHub app cannot see this repository; add it under Choose repositories")

func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.ContainsAny(repo, " ?#") || strings.Count(repo, "/") != 1 {
		return "", valid.Error("a repository is owner/name")
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

// Connected reports whether the GitHub app is set up.
func (s *Service) Connected() bool {
	_, _, ok := s.load()
	return ok
}

// RepoToken returns an installation token that can read (and, with the permissions,
// write) one repository.
func (s *Service) RepoToken(ctx context.Context, repo string) (string, error) {
	p, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	st, k, ok := s.load()
	if !ok {
		return "", ErrNotConnected
	}
	jwt, err := appJWT(st.App.ID, k.PEM, s.Now())
	if err != nil {
		return "", err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := s.call(ctx, http.MethodGet, p+"/installation", "Bearer "+jwt, nil, &inst); err != nil {
		return "", errNoRepo
	}
	return s.installationToken(ctx, inst.ID)
}

// repoCall calls the API for one repository with its installation token.
func (s *Service) repoCall(ctx context.Context, method, repo, sub string, body, out any) error {
	p, err := repoPath(repo)
	if err != nil {
		return err
	}
	tok, err := s.RepoToken(ctx, repo)
	if err != nil {
		return err
	}
	err = s.call(ctx, method, p+sub, "token "+tok, body, out)
	if err != nil && strings.Contains(err.Error(), "403") {
		// Tokens keep the permissions they were made with: after permissions are granted
		// on GitHub, a cached one is still refused. Try once more with a fresh one.
		s.mu.Lock()
		clear(s.tokens)
		s.mu.Unlock()
		if tok, err2 := s.RepoToken(ctx, repo); err2 == nil {
			err = s.call(ctx, method, p+sub, "token "+tok, body, out)
		}
	}
	return err
}

// RepoInfo is a repository's default branch.
func (s *Service) RepoInfo(ctx context.Context, repo string) (defaultBranch string, private bool, err error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	}
	err = s.repoCall(ctx, http.MethodGet, repo, "", nil, &r)
	return r.DefaultBranch, r.Private, err
}

// Head is the newest commit of a branch; sha is "" when the branch does not exist.
func (s *Service) Head(ctx context.Context, repo, branch string) (sha, message string, err error) {
	var c struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	err = s.repoCall(ctx, http.MethodGet, repo, "/commits/"+url.PathEscape(branch), nil, &c)
	var v valid.Error
	if errors.As(err, &v) && (strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "422") || strings.Contains(err.Error(), "409")) {
		return "", "", nil // no such branch (yet), or an empty repository
	}
	first, _, _ := strings.Cut(c.Commit.Message, "\n")
	return c.SHA, first, err
}

// List returns the names at the top of a repository (or of a folder) at ref.
func (s *Service) List(ctx context.Context, repo, dir, ref string) ([]string, error) {
	var items []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	sub := "/contents/" + strings.TrimPrefix(dir, "/") + "?ref=" + url.QueryEscape(ref)
	if err := s.repoCall(ctx, http.MethodGet, repo, sub, nil, &items); err != nil {
		return nil, err
	}
	out := []string{}
	for _, i := range items {
		if i.Type == "dir" {
			out = append(out, i.Name+"/")
		} else {
			out = append(out, i.Name)
		}
	}
	return out, nil
}

// File reads one file; ok is false when it does not exist.
func (s *Service) File(ctx context.Context, repo, path, ref string) (content []byte, sha string, ok bool, err error) {
	var f struct {
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	err = s.repoCall(ctx, http.MethodGet, repo, "/contents/"+path+"?ref="+url.QueryEscape(ref), nil, &f)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	return b, f.SHA, true, err
}

// PutFile creates or updates a file with a commit on branch; it does nothing when the
// file already has that content. changed reports whether a commit was made.
func (s *Service) PutFile(ctx context.Context, repo, path, branch string, content []byte, message string) (changed bool, err error) {
	old, sha, exists, err := s.File(ctx, repo, path, branch)
	if err != nil {
		return false, err
	}
	if exists && string(old) == string(content) {
		return false, nil
	}
	body := map[string]any{"message": message, "content": base64.StdEncoding.EncodeToString(content), "branch": branch}
	if exists {
		body["sha"] = sha
	}
	return true, s.repoCall(ctx, http.MethodPut, repo, "/contents/"+path, body, nil)
}

// SetSecret stores a GitHub Actions secret of the repository, encrypted for GitHub
// (sealed box with the repository's public key, as GitHub requires).
func (s *Service) SetSecret(ctx context.Context, repo, name, value string) error {
	var pk struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := s.repoCall(ctx, http.MethodGet, repo, "/actions/secrets/public-key", nil, &pk); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(pk.Key)
	if err != nil || len(raw) != 32 {
		return errors.New("GitHub returned an unusable public key")
	}
	var key [32]byte
	copy(key[:], raw)
	sealed, err := box.SealAnonymous(nil, []byte(value), &key, rand.Reader)
	if err != nil {
		return err
	}
	return s.repoCall(ctx, http.MethodPut, repo, "/actions/secrets/"+url.PathEscape(name),
		map[string]string{"encrypted_value": base64.StdEncoding.EncodeToString(sealed), "key_id": pk.KeyID}, nil)
}

// gone treats "not there" as done, for the Delete calls below.
func gone(err error) error {
	if err != nil && (strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "422")) {
		return nil
	}
	return err
}

// DeleteFile removes a file with a commit on branch (nothing when it is not there).
func (s *Service) DeleteFile(ctx context.Context, repo, path, branch, message string) error {
	_, sha, exists, err := s.File(ctx, repo, path, branch)
	if err != nil || !exists {
		return err
	}
	return gone(s.repoCall(ctx, http.MethodDelete, repo, "/contents/"+path, map[string]string{"message": message, "sha": sha, "branch": branch}, nil))
}

// DeleteBranch removes a branch.
func (s *Service) DeleteBranch(ctx context.Context, repo, branch string) error {
	return gone(s.repoCall(ctx, http.MethodDelete, repo, "/git/refs/heads/"+branch, nil, nil))
}

// DeleteSecret removes an Actions secret.
func (s *Service) DeleteSecret(ctx context.Context, repo, name string) error {
	return gone(s.repoCall(ctx, http.MethodDelete, repo, "/actions/secrets/"+url.PathEscape(name), nil, nil))
}

// Dispatch starts a workflow (it must allow workflow_dispatch).
func (s *Service) Dispatch(ctx context.Context, repo, workflow, ref string) error {
	return s.repoCall(ctx, http.MethodPost, repo, "/actions/workflows/"+url.PathEscape(workflow)+"/dispatches", map[string]string{"ref": ref}, nil)
}

// Run is a workflow run.
type Run struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | cancelled …
	HeadSHA    string `json:"head_sha"`
	HTMLURL    string `json:"html_url"`
}

// LastRun is the newest run of a workflow (nil when it never ran).
// When duplicate triggers (e.g. push + workflow_dispatch) create multiple runs for the
// same commit and concurrency cancels one of them, prefer an active or successful run
// for that commit over a cancelled duplicate.
func (s *Service) LastRun(ctx context.Context, repo, workflow string) (*Run, error) {
	var res struct {
		Runs []Run `json:"workflow_runs"`
	}
	err := s.repoCall(ctx, http.MethodGet, repo, "/actions/workflows/"+url.PathEscape(workflow)+"/runs?per_page=10", nil, &res)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, nil
		}
		return nil, err
	}
	if len(res.Runs) == 0 {
		return nil, nil
	}
	head := res.Runs[0].HeadSHA
	for i := range res.Runs {
		if res.Runs[i].HeadSHA == head && res.Runs[i].Status != "completed" {
			return &res.Runs[i], nil
		}
	}
	for i := range res.Runs {
		if res.Runs[i].HeadSHA == head && res.Runs[i].Conclusion == "success" {
			return &res.Runs[i], nil
		}
	}
	for i := range res.Runs {
		if res.Runs[i].HeadSHA == head && res.Runs[i].Conclusion != "cancelled" && res.Runs[i].Conclusion != "skipped" {
			return &res.Runs[i], nil
		}
	}
	return &res.Runs[0], nil
}

// Tarball is the download address of a snapshot (agents fetch it with a repo token).
func (s *Service) Tarball(repo, sha string) string {
	p, _ := repoPath(repo)
	return s.API + p + "/tarball/" + url.PathEscape(sha)
}

// missing names the app permissions deploys need that an installation lacks.
func missing(perms map[string]string) []string {
	var out []string
	for _, p := range []struct{ name, level string }{{"contents", "write"}, {"workflows", "write"}, {"secrets", "write"}, {"actions", "write"}} {
		have := perms[p.name]
		if have != p.level && !(p.level == "read" && have == "write") {
			out = append(out, fmt.Sprintf("%s: %s", p.name, p.level))
		}
	}
	return out
}

// BuildBranchName is the branch build workflows publish to (see internal/server/sites).
const BuildBranchName = "fleetwatch-build"
