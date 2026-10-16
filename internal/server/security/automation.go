package security

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Automated logins (CI deploys) are told apart from people: GitHub Actions runners by
// the addresses GitHub publishes, anything else by a name the admin gives its SSH key.
// They never raise login alerts and are hidden from the list unless asked for.

const keyLabelsKey = "sshKeyLabels"

// GitHubMeta lists GitHub's address ranges; "actions" are the hosted runners.
var GitHubMeta = "https://api.github.com/meta"

type githubRanges struct {
	mu     sync.Mutex
	list   []netip.Prefix
	at     time.Time
	failAt time.Time
}

// contains reports whether ip belongs to a GitHub Actions runner, refreshing the
// published list daily.
func (g *githubRanges) contains(ctx context.Context, ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Since(g.at) > 24*time.Hour && time.Since(g.failAt) > 10*time.Minute {
		if list, err := fetchActions(ctx); err == nil {
			g.list, g.at = list, time.Now()
		} else {
			g.failAt = time.Now()
		}
	}
	a = a.Unmap()
	for _, p := range g.list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func fetchActions(ctx context.Context) ([]netip.Prefix, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, GitHubMeta, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Fleetwatch")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var meta struct {
		Actions []string `json:"actions"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&meta); err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, c := range meta.Actions {
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// KeyLabels are the names given to SSH keys, by fingerprint ("ED25519 SHA256:…").
func (s *Service) KeyLabels() map[string]string {
	m := map[string]string{}
	settings.Load(s.db, keyLabelsKey, &m)
	return m
}

// SetKeyLabel names a key (an empty label removes the name). Named keys count as
// automated: their logins raise no alerts.
func (s *Service) SetKeyLabel(key, label string) error {
	key, label = strings.TrimSpace(key), strings.TrimSpace(label)
	if key == "" || len(key) > 200 || len(label) > 60 {
		return valid.Error("a key and a name of up to 60 characters")
	}
	m := s.KeyLabels()
	if label == "" {
		delete(m, key)
	} else {
		m[key] = label
	}
	if err := settings.Save(s.db, keyLabelsKey, m); err != nil {
		return err
	}
	if label != "" { // its earlier logins were not news either
		s.db.Exec(`UPDATE ssh_logins SET new_country = 0, new_key = 0 WHERE key_fp = ?`, key)
	}
	return nil
}

// markGitHub tags stored logins from GitHub Actions runners (those that arrived
// before this version, or before the list could be read).
func (s *Service) markGitHub(ctx context.Context) {
	rows, err := s.db.Query(`SELECT DISTINCT ip FROM ssh_logins WHERE source = ''`)
	if err != nil {
		return
	}
	var ips []string
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			ips = append(ips, ip)
		}
	}
	rows.Close()
	for _, ip := range ips {
		if s.IsGitHub(ctx, ip) {
			s.db.Exec(`UPDATE ssh_logins SET source = 'github', new_country = 0, new_key = 0 WHERE ip = ? AND source = ''`, ip)
		}
	}
}
