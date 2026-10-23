// Package geo blocks or allows whole countries: on the servers' firewalls (nftables,
// through the agents) and at Cloudflare for domains. Country address lists come from
// ipdeny.com, cached for a week.
package geo

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Agent is the first agent version with country rules.
const Agent = "v0.9.158"

var (
	IPv4List = "https://www.ipdeny.com/ipblocks/data/aggregated/%s-aggregated.zone" // tests point them at a fake
	IPv6List = "https://www.ipdeny.com/ipv6/ipaddresses/aggregated/%s-aggregated.zone"
	ccRe     = regexp.MustCompile(`^[A-Z]{2}$`)
	ErrNone  = errors.New("not found")
)

// Rule is one country rule on servers' firewalls.
type Rule struct {
	ID        string   `json:"id"`
	Mode      string   `json:"mode"` // block | allow (only these countries)
	Countries []string `json:"countries"`
	Ports     []int    `json:"ports"`   // none = every port
	Servers   []string `json:"servers"` // none = every server
	CreatedAt int64    `json:"createdAt"`
}

// Zone is a country rule at Cloudflare for one domain.
type Zone struct {
	Account   string   `json:"account"`
	Zone      string   `json:"zone"`
	Mode      string   `json:"mode"`
	Countries []string `json:"countries"`
}

type DNS interface {
	CountryRule(ctx context.Context, owner, accountID, zone, mode string, countries []string) error
}

type Service struct {
	db           *sql.DB
	Dir          string // where country lists are cached
	Client       *http.Client
	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string
	ServerIDs    func() []string
	Safe         func() []string // never blocked: the servers' own addresses, recent sign-ins
	DNS          DNS

	mu     sync.Mutex
	status map[string]proto.GeoStatus
	lists  sync.Mutex
	pushMu sync.Mutex
}

func New(d *sql.DB, dir string) *Service {
	return &Service{db: d, Dir: dir, Client: &http.Client{Timeout: 30 * time.Second}, status: map[string]proto.GeoStatus{},
		ServerIDs: func() []string { return nil }, Safe: func() []string { return nil }}
}

const (
	rulesKey = "geo.rules"
	zonesKey = "geo.zones"
)

func (s *Service) Rules() []Rule {
	list := []Rule{}
	settings.Load(s.db, rulesKey, &list)
	return list
}

func (s *Service) Zones() []Zone {
	list := []Zone{}
	settings.Load(s.db, zonesKey, &list)
	return list
}

func (s *Service) Status() map[string]proto.GeoStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]proto.GeoStatus{}
	for k, v := range s.status {
		out[k] = v
	}
	return out
}

func clean(countries []string) ([]string, error) {
	out := []string{}
	for _, c := range countries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if !ccRe.MatchString(c) {
			return nil, valid.Error(fmt.Sprintf("%q is not a country code (two letters, like MA)", c))
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 || len(out) > 60 {
		return nil, valid.Error("pick 1 to 60 countries")
	}
	return out, nil
}

// SaveRule adds a rule (its countries' lists are fetched first) and applies it.
func (s *Service) SaveRule(ctx context.Context, r Rule) (Rule, error) {
	var err error
	if r.Mode != "allow" && r.Mode != "block" {
		return Rule{}, valid.Error("block these countries, or allow only them")
	}
	if r.Countries, err = clean(r.Countries); err != nil {
		return Rule{}, err
	}
	if len(r.Ports) > 20 {
		return Rule{}, valid.Error("at most 20 ports per rule")
	}
	for _, p := range r.Ports {
		if p < 1 || p > 65535 {
			return Rule{}, valid.Error("ports are 1-65535")
		}
	}
	if r.Ports == nil {
		r.Ports = []int{}
	}
	if r.Servers == nil {
		r.Servers = []string{}
	}
	for _, c := range r.Countries {
		if _, _, err := s.list(ctx, c); err != nil {
			return Rule{}, err
		}
	}
	b := make([]byte, 6)
	rand.Read(b)
	r.ID, r.CreatedAt = "geo_"+hex.EncodeToString(b), time.Now().UnixMilli()
	list := append(s.Rules(), r)
	if len(list) > 50 {
		return Rule{}, valid.Error("at most 50 country rules")
	}
	if err := settings.Save(s.db, rulesKey, list); err != nil {
		return Rule{}, err
	}
	go s.PushAll()
	return r, nil
}

func (s *Service) DeleteRule(id string) error {
	list := s.Rules()
	n := len(list)
	if list = slices.DeleteFunc(list, func(r Rule) bool { return r.ID == id }); len(list) == n {
		return ErrNone
	}
	if err := settings.Save(s.db, rulesKey, list); err != nil {
		return err
	}
	go s.PushAll()
	return nil
}

// SaveZone sets a domain's country rule at Cloudflare (no countries: removes it).
func (s *Service) SaveZone(ctx context.Context, z Zone) error {
	if s.DNS == nil {
		return valid.Error("Cloudflare is not set up")
	}
	if z.Mode != "allow" && z.Mode != "block" {
		return valid.Error("block these countries, or allow only them")
	}
	if len(z.Countries) > 0 {
		var err error
		if z.Countries, err = clean(z.Countries); err != nil {
			return err
		}
	}
	if err := s.DNS.CountryRule(ctx, "", z.Account, z.Zone, z.Mode, z.Countries); err != nil {
		return err
	}
	list := slices.DeleteFunc(s.Zones(), func(x Zone) bool { return x.Account == z.Account && x.Zone == z.Zone })
	if len(z.Countries) > 0 {
		list = append(list, z)
	}
	return settings.Save(s.db, zonesKey, list)
}

// PushAll sends every server its rules.
func (s *Service) PushAll() {
	for _, id := range s.ServerIDs() {
		s.Push(id)
	}
}

// Push sends one server its rules (an agent that reconnects gets them again: nftables
// rules do not survive a reboot).
func (s *Service) Push(serverID string) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	var rules []Rule
	for _, r := range s.Rules() {
		if len(r.Servers) == 0 || slices.Contains(r.Servers, serverID) {
			rules = append(rules, r)
		}
	}
	old := s.AgentVersion != nil && release.Newer(Agent, s.AgentVersion(serverID))
	if old {
		if len(rules) > 0 {
			s.setStatus(serverID, proto.GeoStatus{At: time.Now().UnixMilli(), Error: "the agent is too old for country rules; update it"})
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := proto.GeoApply{Rules: []proto.GeoRule{}, Safe: s.Safe()}
	for _, r := range rules {
		g := proto.GeoRule{Mode: r.Mode, Ports: r.Ports}
		for _, c := range r.Countries {
			v4, v6, err := s.list(ctx, c)
			if err != nil { // never apply half a rule: an allow rule would shut out too much
				s.setStatus(serverID, proto.GeoStatus{At: time.Now().UnixMilli(), Error: err.Error()})
				return
			}
			g.V4, g.V6 = append(g.V4, v4...), append(g.V6, v6...)
		}
		req.Rules = append(req.Rules, g)
	}
	if s.Send != nil && s.Send(serverID, proto.TypeGeoApply, req) != nil && len(rules) > 0 {
		s.setStatus(serverID, proto.GeoStatus{At: time.Now().UnixMilli(), Error: "the server is not connected; the rules go there when it is"})
	}
}

func (s *Service) setStatus(id string, st proto.GeoStatus) {
	s.mu.Lock()
	s.status[id] = st
	s.mu.Unlock()
}

func (s *Service) OnStatus(serverID string, data json.RawMessage) {
	var st proto.GeoStatus
	if json.Unmarshal(data, &st) == nil {
		s.setStatus(serverID, st)
	}
}

// list is a country's IPv4 and IPv6 ranges: cached for a week, fetched again after, the
// old copy used while ipdeny.com cannot be reached.
func (s *Service) list(ctx context.Context, cc string) (v4, v6 []string, err error) {
	s.lists.Lock()
	defer s.lists.Unlock()
	cc = strings.ToLower(cc)
	dir := filepath.Join(s.Dir, "geo")
	get := func(kind, url string, must bool) ([]string, error) {
		file := filepath.Join(dir, cc+"."+kind)
		if st, err := os.Stat(file); err == nil && time.Since(st.ModTime()) < 7*24*time.Hour {
			return readList(file)
		}
		list, ferr := s.fetch(ctx, fmt.Sprintf(url, cc))
		if ferr == nil {
			os.MkdirAll(dir, 0o700)
			os.WriteFile(file, []byte(strings.Join(list, "\n")), 0o600)
			return list, nil
		}
		if cached, err := readList(file); err == nil {
			return cached, nil
		}
		if !must && errors.Is(ferr, ErrNone) {
			return nil, nil // some countries have no IPv6 list
		}
		if errors.Is(ferr, ErrNone) {
			return nil, valid.Error(fmt.Sprintf("no address list for %s: check the country code", strings.ToUpper(cc)))
		}
		return nil, fmt.Errorf("could not get the address list of %s: %w", strings.ToUpper(cc), ferr)
	}
	if v4, err = get("v4", IPv4List, true); err != nil {
		return nil, nil, err
	}
	v6, err = get("v6", IPv6List, false)
	return v4, v6, err
}

func (s *Service) fetch(ctx context.Context, url string) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNone
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, res.Status)
	}
	list := parse(io.LimitReader(res.Body, 16<<20))
	if len(list) == 0 {
		return nil, ErrNone
	}
	return list, nil
}

func parse(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if p, err := netip.ParsePrefix(strings.TrimSpace(sc.Text())); err == nil {
			out = append(out, p.Masked().String())
		}
	}
	return out
}

func readList(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parse(f), nil
}
