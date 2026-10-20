// Package hostadmin administers the servers' hosts: it blocks addresses that keep
// guessing SSH passwords (on every server at once), shows and edits each firewall, and
// installs package updates, by hand or in a weekly window.
package hostadmin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// MinAgent is the first agent version that can block, edit the firewall and update packages.
const MinAgent = "v0.9.38"

const maxBlocks = 5000

// BlockSettings is the "blocking" settings section.
type BlockSettings struct {
	Enabled   bool     `json:"enabled"`
	Threshold int      `json:"threshold"` // failed SSH logins from one address…
	WindowMin int      `json:"windowMin"` // …within this many minutes, on all servers together
	BanHours  int      `json:"banHours"`  // how long an automatic block lasts; 0 = until removed
	Allow     []string `json:"allow"`     // never blocked: addresses or CIDR ranges
}

func DefaultBlockSettings() BlockSettings {
	return BlockSettings{Threshold: 10, WindowMin: 10, BanHours: 24, Allow: []string{}}
}

func (b BlockSettings) Validate() error {
	switch {
	case b.Threshold < 3 || b.Threshold > 1000:
		return valid.Error("block after 3-1000 failed logins")
	case b.WindowMin < 1 || b.WindowMin > 1440:
		return valid.Error("the counting window is 1-1440 minutes")
	case b.BanHours < 0 || b.BanHours > 24*365:
		return valid.Error("block for up to a year (0 = until removed)")
	case len(b.Allow) > 200:
		return valid.Error("200 allowed addresses at most")
	}
	for _, a := range b.Allow {
		if _, err := parse(a); err != nil {
			return valid.Error(fmt.Sprintf("%q is not an IP address or range", a))
		}
	}
	return nil
}

// Block is one blocked address.
type Block struct {
	IP        string   `json:"ip"`
	Reason    string   `json:"reason"`
	Source    string   `json:"source"` // auto | manual
	By        string   `json:"by,omitempty"`
	CreatedAt int64    `json:"createdAt"`
	ExpiresAt int64    `json:"expiresAt"` // 0 = until removed
	Servers   []string `json:"servers,omitempty"`
}

type failure struct {
	ts     time.Time
	n      int
	server string
}

type Service struct {
	db  *sql.DB
	Bus *live.Bus
	Now func() time.Time

	Send            func(serverID, typ string, v any) error
	AgentVersion    func(serverID string) string
	BroadcastConfig func()                                 // send every agent its config (with the block list)
	OnBlocks        func()                                 // the block list changed (Cloudflare mirrors it)
	Protected       func() []string                        // never block: servers' own and recently used addresses
	Notify          func(title, body, severity string)     // a notification to the team
	AutoBlocked     func(title, body string, ips []string) // addresses just blocked by themselves; nil = Notify

	mu       sync.Mutex
	fails    map[string][]failure
	status   map[string]proto.BlockStatus
	firewall map[string]*FirewallState
	updates  map[string]*UpdatesState
	waiting  map[string]string // run ID → server ID, for results
}

func New(d *sql.DB, bus *live.Bus) *Service {
	return &Service{db: d, Bus: bus, Now: time.Now, fails: map[string][]failure{}, status: map[string]proto.BlockStatus{},
		firewall: map[string]*FirewallState{}, updates: map[string]*UpdatesState{}, waiting: map[string]string{},
		Protected: func() []string { return nil }, Notify: func(string, string, string) {}, BroadcastConfig: func() {}}
}

func (s *Service) supported(serverID string) error {
	if s.AgentVersion != nil && release.Newer(MinAgent, s.AgentVersion(serverID)) {
		return valid.Error("this server's agent is too old for this; update the agent first")
	}
	return nil
}

func parse(a string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(strings.TrimSpace(a)); err == nil {
		return p.Masked(), nil
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(a))
	if err != nil {
		return netip.Prefix{}, err
	}
	ip = ip.Unmap()
	return netip.PrefixFrom(ip, ip.BitLen()), nil
}

func (s *Service) Settings() BlockSettings {
	b := DefaultBlockSettings()
	if settings.Load(s.db, "blocking", &b) != nil || b.Validate() != nil {
		return DefaultBlockSettings()
	}
	return b
}

func (s *Service) SaveSettings(b BlockSettings) error {
	if b.Allow == nil {
		b.Allow = []string{}
	}
	if err := b.Validate(); err != nil {
		return err
	}
	if err := settings.Save(s.db, "blocking", b); err != nil {
		return err
	}
	s.BroadcastConfig()
	return nil
}

// protectedReason says why an address must never be blocked ("" = it may be).
func (s *Service) protectedReason(p netip.Prefix, extra []string) string {
	a := p.Addr()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() || p.Bits() < 8 {
		return "it is a local or private address (or too large a range)"
	}
	for _, list := range [][]string{s.Settings().Allow, s.Protected(), extra} {
		for _, x := range list {
			q, err := parse(x)
			if err == nil && (q.Overlaps(p)) {
				return x + " is allowed: it is one of your servers, someone signed in from it, or it is on the allow list"
			}
		}
	}
	return ""
}

// Blocked lists the blocks still in force.
func (s *Service) Blocked() []Block {
	rows, err := s.db.Query(`SELECT data FROM ip_blocks WHERE expires_at = 0 OR expires_at > ? ORDER BY created_at DESC`, s.Now().UnixMilli())
	out := []Block{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var b Block
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &b) == nil {
			out = append(out, b)
		}
	}
	return out
}

// BlockedIPs are what agents enforce; blocking off = nothing.
func (s *Service) BlockedIPs() (bool, []string) {
	if !s.Settings().Enabled {
		return false, []string{}
	}
	list := []string{}
	for _, b := range s.Blocked() {
		list = append(list, b.IP)
	}
	return true, list
}

// Block adds a block by hand. requester is the address of whoever asks: never theirs.
func (s *Service) Block(ip, reason, by, requester string, hours int) (Block, error) {
	p, err := parse(ip)
	if err != nil {
		return Block{}, valid.Error("enter an IP address or a range like 203.0.113.0/24")
	}
	if why := s.protectedReason(p, []string{requester}); why != "" {
		return Block{}, valid.Error("cannot block " + p.String() + ": " + why)
	}
	if hours < 0 || hours > 24*365 {
		return Block{}, valid.Error("block for up to a year (0 = until removed)")
	}
	b := Block{IP: p.String(), Reason: strings.TrimSpace(reason), Source: "manual", By: by, CreatedAt: s.Now().UnixMilli()}
	if b.Reason == "" {
		b.Reason = "Blocked by hand"
	}
	if hours > 0 {
		b.ExpiresAt = s.Now().Add(time.Duration(hours) * time.Hour).UnixMilli()
	}
	if err := s.save(b); err != nil {
		return Block{}, err
	}
	return b, nil
}

func (s *Service) save(b Block) error {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM ip_blocks`).Scan(&n)
	if n >= maxBlocks {
		return valid.Error("too many blocked addresses; remove some first")
	}
	raw, _ := json.Marshal(b)
	if _, err := s.db.Exec(`INSERT INTO ip_blocks (ip, data, created_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (ip) DO UPDATE SET data = excluded.data, created_at = excluded.created_at, expires_at = excluded.expires_at`,
		b.IP, string(raw), b.CreatedAt, b.ExpiresAt); err != nil {
		return err
	}
	s.Bus.Publish("blocks", s.Blocked())
	s.BroadcastConfig()
	if s.OnBlocks != nil {
		go s.OnBlocks()
	}
	return nil
}

// Unblock removes a block.
func (s *Service) Unblock(ip string) error {
	if _, err := s.db.Exec(`DELETE FROM ip_blocks WHERE ip = ?`, ip); err != nil {
		return err
	}
	s.Bus.Publish("blocks", s.Blocked())
	s.BroadcastConfig()
	if s.OnBlocks != nil {
		go s.OnBlocks()
	}
	return nil
}

// Sweep drops expired blocks (agents then stop blocking them).
func (s *Service) Sweep() {
	res, err := s.db.Exec(`DELETE FROM ip_blocks WHERE expires_at > 0 AND expires_at <= ?`, s.Now().UnixMilli())
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.Bus.Publish("blocks", s.Blocked())
		s.BroadcastConfig()
		if s.OnBlocks != nil {
			go s.OnBlocks()
		}
	}
	s.mu.Lock()
	cut := s.Now().Add(-24 * time.Hour)
	for ip, list := range s.fails {
		if len(list) == 0 || list[len(list)-1].ts.Before(cut) {
			delete(s.fails, ip)
		}
	}
	s.mu.Unlock()
}

// OnFailures counts failed SSH logins reported by an agent and blocks an address once it
// failed often enough, on all servers together.
func (s *Service) OnFailures(serverID string, data json.RawMessage) {
	var list []proto.SSHFailure
	if json.Unmarshal(data, &list) != nil {
		return
	}
	cfg := s.Settings()
	now := s.Now()
	var toBlock []Block
	s.mu.Lock()
	for _, f := range list {
		p, err := parse(f.IP)
		if err != nil || f.Count <= 0 {
			continue
		}
		ip := p.String()
		ev := append(s.fails[ip], failure{now, f.Count, serverID})
		cut := now.Add(-time.Duration(cfg.WindowMin) * time.Minute)
		total, servers := 0, []string{}
		kept := ev[:0]
		for _, e := range ev {
			if e.ts.After(cut) {
				kept = append(kept, e)
				total += e.n
				if !slices.Contains(servers, e.server) {
					servers = append(servers, e.server)
				}
			}
		}
		s.fails[ip] = kept
		if cfg.Enabled && total >= cfg.Threshold {
			b := Block{IP: ip, Source: "auto", CreatedAt: now.UnixMilli(), Servers: servers,
				Reason: fmt.Sprintf("%d failed SSH logins in %d min on %d server(s)", total, cfg.WindowMin, len(servers))}
			if cfg.BanHours > 0 {
				b.ExpiresAt = now.Add(time.Duration(cfg.BanHours) * time.Hour).UnixMilli()
			}
			toBlock = append(toBlock, b)
			delete(s.fails, ip)
		}
	}
	s.mu.Unlock()
	blocked := map[string]bool{}
	for _, b := range s.Blocked() {
		blocked[b.IP] = true
	}
	var done, ips []string
	for _, b := range toBlock {
		p, _ := parse(b.IP)
		if blocked[b.IP] || s.protectedReason(p, nil) != "" {
			continue
		}
		if s.save(b) == nil {
			done = append(done, b.IP+" ("+b.Reason+")")
			ips = append(ips, strings.TrimSuffix(strings.TrimSuffix(b.IP, "/32"), "/128"))
		}
	}
	if len(done) > 0 {
		sort.Strings(done)
		title, body := fmt.Sprintf("Blocked %d address(es) guessing SSH logins", len(done)), strings.Join(done, "\n")+"\nThey are blocked on every server."
		if s.AutoBlocked != nil {
			s.AutoBlocked(title, body, ips)
		} else {
			s.Notify(title, body, "info")
		}
	}
}

// OnBlockStatus records how an agent applied the list.
func (s *Service) OnBlockStatus(serverID string, data json.RawMessage) {
	var st proto.BlockStatus
	if json.Unmarshal(data, &st) != nil {
		return
	}
	s.mu.Lock()
	s.status[serverID] = st
	s.mu.Unlock()
	s.Bus.Publish("blocks.status", map[string]any{"serverId": serverID, "status": st})
}

// Status is how each server applied the block list.
func (s *Service) Status() map[string]proto.BlockStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]proto.BlockStatus{}
	for k, v := range s.status {
		out[k] = v
	}
	return out
}
