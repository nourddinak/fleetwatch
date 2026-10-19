package dns

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/valid"
)

// Account is one person's DNS account. Owner is the dashboard user it belongs to
// ("owner" or a member ID); the credentials are kept encrypted, never shown again.
type Account struct {
	ID         string   `json:"id"`
	Owner      string   `json:"owner"`
	Provider   string   `json:"provider"`
	Label      string   `json:"label"`
	Zones      []Zone   `json:"zones"`
	SyncBlocks bool     `json:"syncBlocks,omitempty"` // Cloudflare: block the dashboard's blocked IPs there too
	Scopes     []string `json:"scopes,omitempty"`     // what the sign-in granted ("" for older sign-ins)
	CreatedAt  int64    `json:"createdAt"`
	Error      string   `json:"error,omitempty"` // the last problem talking to the provider
}

// State is how one site domain stands with DNS.
type State struct {
	Provider string `json:"provider"`
	Zone     string `json:"zone"`
	Account  string `json:"account"`
	RecordID string `json:"recordId,omitempty"`
	Created  bool   `json:"created,omitempty"` // Fleetwatch made the record, so it may remove it
	Proxied  bool   `json:"proxied,omitempty"`
	Status   string `json:"status"` // pointed | updated | error
	Error    string `json:"error,omitempty"`
	Previous string `json:"previous,omitempty"` // what the record pointed to before
}

type Service struct {
	db  *sql.DB
	box *secret.Box
	Bus *live.Bus
	Now func() time.Time

	mu      sync.Mutex // sign-ins under way, token refreshes
	syncing sync.Mutex
	pending map[string]signIn
}

func New(d *sql.DB, box *secret.Box, bus *live.Bus) *Service {
	return &Service{db: d, box: box, Bus: bus, Now: time.Now, pending: map[string]signIn{}}
}

const secretAD = "dns.account:"

func (s *Service) save(a Account) {
	data, _ := json.Marshal(a)
	s.db.Exec(`UPDATE dns_accounts SET data = ? WHERE id = ?`, string(data), a.ID)
}

func (s *Service) publish(owner string) {
	if s.Bus != nil {
		s.Bus.Publish("dns", map[string]string{"owner": owner})
	}
}

// Accounts lists one person's accounts ("" = everyone's, for the owner's overview).
func (s *Service) Accounts(owner string) []Account {
	q, args := `SELECT data FROM dns_accounts ORDER BY created_at`, []any{}
	if owner != "" {
		q, args = `SELECT data FROM dns_accounts WHERE owner = ? ORDER BY created_at`, []any{owner}
	}
	rows, err := s.db.Query(q, args...)
	out := []Account{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var a Account
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &a) == nil {
			if a.Zones == nil {
				a.Zones = []Zone{}
			}
			out = append(out, a)
		}
	}
	return out
}

// ErrNotFound: no such account (for this person).
var ErrNotFound = errors.New("dns account not found")

func (s *Service) account(owner, id string) (Account, Provider, error) {
	var raw string
	var enc []byte
	if err := s.db.QueryRow(`SELECT data, secret_enc FROM dns_accounts WHERE id = ? AND (owner = ? OR ? = '')`, id, owner, owner).Scan(&raw, &enc); err != nil {
		return Account{}, nil, ErrNotFound
	}
	var a Account
	json.Unmarshal([]byte(raw), &a)
	tok, err := s.token(context.Background(), &a, enc)
	if err != nil {
		return a, nil, err
	}
	return a, cloudflare{token: tok}, nil
}

// Remove forgets an account and signs it out of Cloudflare (the records it made stay).
func (s *Service) Remove(owner, id string) error {
	var enc []byte
	if s.db.QueryRow(`SELECT secret_enc FROM dns_accounts WHERE id = ? AND (owner = ? OR ? = '')`, id, owner, owner).Scan(&enc) == nil {
		s.revoke(id, enc)
	}
	res, err := s.db.Exec(`DELETE FROM dns_accounts WHERE id = ? AND (owner = ? OR ? = '')`, id, owner, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.publish(owner)
	return nil
}

// Refresh reloads an account's domains.
func (s *Service) Refresh(ctx context.Context, owner, id string) (Account, error) {
	a, p, err := s.account(owner, id)
	if err != nil {
		return a, err
	}
	zones, err := p.Zones(ctx)
	if err != nil {
		a.Error = err.Error()
	} else {
		a.Zones, a.Error = zones, ""
	}
	s.save(a)
	return a, err
}

// zoneFor finds the person's account and zone that hold host (the longest match).
func (s *Service) zoneFor(owner, host string) (Account, Zone, bool) {
	var best Account
	var zone Zone
	for _, a := range s.Accounts(owner) {
		for _, z := range a.Zones {
			if (host == z.Name || strings.HasSuffix(host, "."+z.Name)) && len(z.Name) > len(zone.Name) {
				best, zone = a, z
			}
		}
	}
	return best, zone, zone.Name != ""
}

// Point makes each domain's A record point at ip, in whichever of the person's
// accounts holds it. Domains none of them hold are left out of the answer.
func (s *Service) Point(ctx context.Context, owner, ip string, domains []string) map[string]State {
	out := map[string]State{}
	for _, d := range domains {
		a, z, ok := s.zoneFor(owner, d)
		if !ok {
			continue
		}
		st := State{Provider: a.Provider, Zone: z.Name, Account: a.ID}
		if ip == "" {
			st.Status, st.Error = "error", "the server has no public IPv4 address to point the domain at"
			out[d] = st
			continue
		}
		_, p, err := s.account(owner, a.ID)
		if err == nil {
			var rec *Record
			if rec, err = p.FindA(ctx, z, d); err == nil {
				switch {
				case rec == nil:
					st.RecordID, err = p.CreateA(ctx, z, Record{Name: d, Content: ip})
					st.Created, st.Status = true, "pointed"
				case rec.Content == ip:
					st.RecordID, st.Proxied, st.Status = rec.ID, rec.Proxied, "pointed"
				default:
					st.Previous = rec.Content
					rec.Content = ip
					err = p.UpdateA(ctx, z, *rec)
					st.RecordID, st.Proxied, st.Status = rec.ID, rec.Proxied, "updated"
				}
			}
		}
		if err != nil {
			st.Status, st.Error = "error", err.Error()
		}
		out[d] = st
	}
	return out
}

// Forget removes the records Fleetwatch itself made for domains a site no longer has.
func (s *Service) Forget(ctx context.Context, owner string, states map[string]State) {
	for d, st := range states {
		if !st.Created || st.RecordID == "" {
			continue
		}
		a, p, err := s.account(owner, st.Account)
		if err != nil {
			continue
		}
		for _, z := range a.Zones {
			if z.Name == st.Zone {
				if err := p.DeleteA(ctx, z, st.RecordID); err != nil {
					slog.Warn("dns: remove record", "domain", d, "err", err)
				}
			}
		}
	}
}

// Purge clears Cloudflare's cache of a site's pages after a deploy.
func (s *Service) Purge(ctx context.Context, owner string, domains []string) {
	byZone := map[string][]string{}
	zones := map[string]Zone{}
	accounts := map[string]string{}
	for _, d := range domains {
		a, z, ok := s.zoneFor(owner, d)
		if !ok || a.Provider != "cloudflare" {
			continue
		}
		byZone[z.ID] = append(byZone[z.ID], "https://"+d+"/", "https://"+d+"/index.html", "http://"+d+"/")
		zones[z.ID], accounts[z.ID] = z, a.ID
	}
	for id, urls := range byZone {
		if _, p, err := s.account(owner, accounts[id]); err == nil {
			if cdn, ok := p.(CDN); ok {
				if err := cdn.Purge(ctx, zones[id], urls[:min(len(urls), 30)]); err != nil {
					slog.Warn("dns: purge", "zone", zones[id].Name, "err", err)
				}
			}
		}
	}
}

// PurgeSite empties Cloudflare's cache for a site's domains: all they cached. It
// answers the domains done (an error when none could be).
func (s *Service) PurgeSite(ctx context.Context, owner string, domains []string) ([]string, error) {
	byZone := map[string][]string{}
	zones, accounts := map[string]Zone{}, map[string]string{}
	for _, d := range domains {
		if a, z, ok := s.zoneFor(owner, d); ok && a.Provider == "cloudflare" {
			byZone[z.ID] = append(byZone[z.ID], d)
			zones[z.ID], accounts[z.ID] = z, a.ID
		}
	}
	if len(byZone) == 0 {
		return nil, ErrNoZone
	}
	var done []string
	var last error
	for id, hosts := range byZone {
		_, p, err := s.account(owner, accounts[id])
		if err == nil {
			if cdn, ok := p.(CDN); ok {
				err = cdn.PurgeHosts(ctx, zones[id], hosts)
			}
		}
		if err != nil {
			last = err
			continue
		}
		done = append(done, hosts...)
	}
	if len(done) == 0 {
		return nil, last
	}
	return done, nil
}

// SiteUnderAttack reads (on=nil) or switches "Under attack" for the zones of a site's domains.
func (s *Service) SiteUnderAttack(ctx context.Context, owner string, domains []string, on *bool) (bool, error) {
	seen := map[string]bool{}
	any, found := false, false
	for _, d := range domains {
		a, z, ok := s.zoneFor(owner, d)
		if !ok || a.Provider != "cloudflare" || seen[z.ID] {
			continue
		}
		seen[z.ID], found = true, true
		v, err := s.UnderAttack(ctx, owner, a.ID, z.Name, on)
		if err != nil {
			return false, err
		}
		any = any || v
	}
	if !found {
		return false, ErrNoZone
	}
	return any, nil
}

func (s *Service) cdn(owner, accountID, zoneName string) (Account, CDN, Zone, error) {
	a, p, err := s.account(owner, accountID)
	if err != nil {
		return a, nil, Zone{}, err
	}
	cdn, ok := p.(CDN)
	if !ok {
		return a, nil, Zone{}, valid.Error("only Cloudflare can do this")
	}
	i := slices.IndexFunc(a.Zones, func(z Zone) bool { return z.Name == zoneName })
	if i < 0 {
		return a, nil, Zone{}, valid.Error("this account does not hold " + zoneName)
	}
	return a, cdn, a.Zones[i], nil
}

// SetProxied switches Cloudflare's proxy for one record.
func (s *Service) SetProxied(ctx context.Context, owner string, st State, on bool) (State, error) {
	_, cdn, z, err := s.cdn(owner, st.Account, st.Zone)
	if err != nil {
		return st, err
	}
	if st.RecordID == "" {
		return st, valid.Error("the domain has no record yet")
	}
	if err := cdn.SetProxied(ctx, z, st.RecordID, on); err != nil {
		return st, err
	}
	st.Proxied = on
	return st, nil
}

// UnderAttack reads or switches Cloudflare's "Under attack" mode of a zone (on=nil: read).
func (s *Service) UnderAttack(ctx context.Context, owner, accountID, zone string, on *bool) (bool, error) {
	_, cdn, z, err := s.cdn(owner, accountID, zone)
	if err != nil {
		return false, err
	}
	if on != nil {
		level := "medium"
		if *on {
			level = "under_attack"
		}
		if err := cdn.SetSecurityLevel(ctx, z, level); err != nil {
			return false, err
		}
		return *on, nil
	}
	level, err := cdn.SecurityLevel(ctx, z)
	return level == "under_attack", err
}

// SetSyncBlocks turns mirroring the dashboard's blocked IPs on or off for an account.
func (s *Service) SetSyncBlocks(owner, id string, on bool) (Account, error) {
	a, p, err := s.account(owner, id)
	if err != nil {
		return a, err
	}
	if _, ok := p.(CDN); !ok {
		return a, valid.Error("only Cloudflare can block addresses")
	}
	a.SyncBlocks = on
	s.save(a)
	return a, nil
}

// SyncBlocks makes Cloudflare block exactly the dashboard's blocked addresses, in every
// zone of the accounts that asked for it; it only touches rules it made itself. When
// mirroring is turned off, its rules go away on the next run. Zones are done a few at a
// time; what does not fit in ctx is picked up by the next run.
func (s *Service) SyncBlocks(ctx context.Context, ips []string) {
	if !s.syncing.TryLock() {
		return
	}
	defer s.syncing.Unlock()
	for _, a := range s.Accounts("") {
		_, p, err := s.account("", a.ID)
		if err != nil {
			continue
		}
		cdn := p.(CDN)
		want := ips
		if !a.SyncBlocks {
			want = nil
		}
		var (
			mu         sync.Mutex
			wg         sync.WaitGroup
			problem    error
			todo, done int
		)
		fail := func(err error) {
			mu.Lock()
			if problem == nil {
				problem = err
			}
			mu.Unlock()
		}
		slots := make(chan struct{}, 4)
		for _, z := range a.Zones {
			wg.Add(1)
			go func(z Zone) {
				defer wg.Done()
				slots <- struct{}{}
				defer func() { <-slots }()
				have, err := cdn.BlockRules(ctx, z)
				if err != nil {
					fail(err)
					return
				}
				var jobs []func() error
				for _, ip := range want {
					if _, ok := have[ip]; !ok {
						jobs = append(jobs, func() error { return cdn.Block(ctx, z, ip) })
					}
				}
				for ip, id := range have {
					if !slices.Contains(want, ip) {
						jobs = append(jobs, func() error { return cdn.DeleteRule(ctx, z, id) })
					}
				}
				mu.Lock()
				todo += len(jobs)
				mu.Unlock()
				for _, job := range jobs {
					if ctx.Err() != nil {
						fail(ctx.Err())
						return
					}
					if err := job(); err != nil {
						fail(err)
						continue
					}
					mu.Lock()
					done++
					mu.Unlock()
				}
			}(z)
		}
		wg.Wait()
		msg := ""
		switch {
		case problem == nil:
		case ctx.Err() != nil:
			msg = fmt.Sprintf("Cloudflare: %d of %d blocked-address changes made so far; the rest follow by themselves within 15 minutes", done, todo)
		default:
			msg = "blocking on Cloudflare: " + problem.Error()
		}
		if !a.SyncBlocks && problem != nil {
			msg = a.Error // turned off: removing its rules is best effort
		}
		if msg != a.Error {
			a.Error = msg
			s.save(a)
			s.publish(a.Owner)
		}
	}
}
