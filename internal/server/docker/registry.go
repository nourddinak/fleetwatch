package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Whether a container's image tag has moved on: the registry is asked (anonymously)
// which digest the tag points to now, and that is compared with the digests the image
// was pulled as. Private images answer an error and simply show nothing.

const checkEvery = 6 * time.Hour

type latest struct {
	digest string
	err    string
	at     time.Time
}

var (
	hexID    = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)
	authPart = regexp.MustCompile(`(\w+)="([^"]*)"`)
)

// parseRef splits an image reference into its registry host, repository and tag. A
// reference pinned to a digest, or an image ID, has nothing to check.
func parseRef(ref string) (registry, repo, tag string, ok bool) {
	if ref == "" || strings.Contains(ref, "@") || hexID.MatchString(ref) {
		return "", "", "", false
	}
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	registry, repo = "registry-1.docker.io", name
	if first, rest, found := strings.Cut(name, "/"); found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		registry, repo = first, rest
	}
	if registry == "docker.io" || registry == "index.docker.io" {
		registry = "registry-1.docker.io"
	}
	if registry == "registry-1.docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return registry, repo, tag, repo != "" && tag != ""
}

var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// latestDigest asks the registry which digest ref's tag points to now.
func latestDigest(ctx context.Context, client *http.Client, ref string) (string, error) {
	registry, repo, tag, ok := parseRef(ref)
	if !ok {
		return "", errors.New("nothing to check")
	}
	u := "https://" + registry + "/v2/" + repo + "/manifests/" + url.PathEscape(tag)
	head := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", manifestTypes)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	resp, err := head("")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized { // the usual: an anonymous token first
		token, err := registryToken(ctx, client, resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return "", err
		}
		if resp, err = head(token); err != nil {
			return "", err
		}
		resp.Body.Close()
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry answered %d (a private image?)", resp.StatusCode)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(d, "sha256:") {
		return "", errors.New("the registry did not say which digest the tag is")
	}
	return d, nil
}

// registryToken gets an anonymous pull token from the realm a registry names.
func registryToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", errors.New("the registry wants a password (a private image?)")
	}
	p := map[string]string{}
	for _, m := range authPart.FindAllStringSubmatch(challenge, -1) {
		p[strings.ToLower(m[1])] = m[2]
	}
	realm, err := url.Parse(p["realm"])
	if err != nil || realm.Scheme != "https" {
		return "", errors.New("the registry names no usable sign-in address")
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if p[k] != "" {
			q.Set(k, p[k])
		}
	}
	realm.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	if tok.Token == "" {
		return "", fmt.Errorf("the registry gave no token (%d)", resp.StatusCode)
	}
	return tok.Token, nil
}

// annotate marks each container whose tag has a newer image (lock held). The report
// is copied: one already published may still be being sent.
func (s *Service) annotate(st *State) {
	if st.Report == nil {
		return
	}
	rep := *st.Report
	rep.Containers = slices.Clone(rep.Containers)
	for i, c := range rep.Containers {
		rep.Containers[i].Update = ""
		if l, ok := s.latest[c.Image]; ok && l.digest != "" && len(c.Digests) > 0 {
			rep.Containers[i].Update = "current"
			if !slices.Contains(c.Digests, l.digest) {
				rep.Containers[i].Update = "available"
			}
		}
	}
	st.Report = &rep
}

// CheckUpdates asks the registries about the images the servers run: those not asked
// for checkEvery, or all of them with force. Then every server's list is marked again.
func (s *Service) CheckUpdates(ctx context.Context, force bool) {
	s.mu.Lock()
	var refs []string
	for _, st := range s.state {
		if st.Report == nil {
			continue
		}
		for _, c := range st.Report.Containers {
			l, known := s.latest[c.Image]
			if _, _, _, ok := parseRef(c.Image); ok && len(c.Digests) > 0 && !s.asking[c.Image] && (force || !known || s.Now().Sub(l.at) > checkEvery) && !slices.Contains(refs, c.Image) {
				refs = append(refs, c.Image)
				s.asking[c.Image] = true
			}
		}
	}
	s.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	got := make(chan latest, len(refs))
	slots := make(chan struct{}, 4)
	for _, ref := range refs {
		go func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			d, err := latestDigest(ctx, s.Client, ref)
			l := latest{digest: d, at: s.Now()}
			if err != nil {
				l.err = err.Error()
			}
			s.mu.Lock()
			s.latest[ref] = l
			delete(s.asking, ref)
			s.mu.Unlock()
			got <- l
		}()
	}
	for range refs {
		<-got
	}
	s.mu.Lock()
	var out []func()
	for id, st := range s.state {
		s.annotate(st)
		cp := *st
		out = append(out, func() { s.publish(id, cp) })
	}
	s.mu.Unlock()
	for _, f := range out {
		f()
	}
}
