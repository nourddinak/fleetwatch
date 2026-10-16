// Package domains watches the certificates and registrations of the sites the
// dashboard knows about, and opens incidents before they expire.
//
// Three things are checked for each domain:
//   - the public certificate, as visitors get it (behind a CDN: the CDN's);
//   - the origin certificate the server's own web server presents (reported by its
//     agent), which a CDN hides but which breaks the site when it expires;
//   - the registration of the registered domain (example.co.uk for www.example.co.uk),
//     through RDAP, the registries' official successor to WHOIS.
package domains

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/publicsuffix"

	"fleetwatch/internal/agent/caddy"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
)

const (
	checkEvery = 6 * time.Hour
	rdapEvery  = 24 * time.Hour
	day        = 24 * time.Hour
	critical   = 3 // days left at which a warning becomes critical
)

// Cert is one certificate check.
type Cert struct {
	NotAfter  int64  `json:"notAfter,omitempty"` // unix ms
	NotBefore int64  `json:"notBefore,omitempty"`
	Issuer    string `json:"issuer,omitempty"`
	Error     string `json:"error,omitempty"`
	Problem   bool   `json:"problem,omitempty"`   // the certificate itself is wrong (not just unreachable)
	AutoRenew bool   `json:"autoRenew,omitempty"` // short-lived and renewed automatically: expiry soon is normal
}

// shortLived marks certificates that a local CA renews on its own, many times before
// they could be noticed expiring: Caddy's internal CA issues ~12-hour certificates,
// step-ca ~24-hour ones. Expiring "today" is their normal state.
func shortLived(c Cert) bool {
	if strings.Contains(c.Issuer, "Caddy Local Authority") {
		return true
	}
	return c.NotBefore > 0 && c.NotAfter > 0 && c.NotAfter-c.NotBefore <= (10*day).Milliseconds()
}

// Domain is the latest check of one site domain.
type Domain struct {
	Host        string `json:"host"`
	ServerID    string `json:"serverId,omitempty"`
	AppID       string `json:"appId,omitempty"`
	Public      Cert   `json:"public"`
	Origin      *Cert  `json:"origin,omitempty"`
	Registered  string `json:"registered,omitempty"` // registrable domain
	Expires     int64  `json:"expires,omitempty"`    // registration expiry, unix ms
	Registrar   string `json:"registrar,omitempty"`
	DomainError string `json:"domainError,omitempty"`
	CheckedAt   int64  `json:"checkedAt"`
	RDAPAt      int64  `json:"rdapCheckedAt,omitempty"`
}

type Checker struct {
	db        *sql.DB
	Apps      *apps.Service
	Incidents *incidents.Store
	Rules     func() incidents.Rules
	Bus       *live.Bus
	PublicURL string // the dashboard's own domain is checked too
	Now       func() time.Time
	RDAP      *RDAP

	// Source lists the domains to check (default: Apps.Domains).
	Source func() []apps.DomainRef

	// tests: where to connect for a host, and which roots to trust
	addrFor func(host string) string
	roots   *x509.CertPool

	running atomic.Bool
	mu      sync.Mutex // one check at a time
}

func New(d *sql.DB, a *apps.Service, inc *incidents.Store, rules func() incidents.Rules, bus *live.Bus) *Checker {
	c := &Checker{db: d, Apps: a, Incidents: inc, Rules: rules, Bus: bus, Now: time.Now, RDAP: NewRDAP()}
	if a != nil {
		c.Source = a.Domains
	}
	return c
}

// Run checks shortly after start and then every few hours.
func (c *Checker) Run(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Check(ctx)
			t.Reset(checkEvery)
		}
	}
}

// CheckSoon starts a check in the background unless one is running.
func (c *Checker) CheckSoon() {
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.running.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		c.Check(ctx)
	}()
}

// targets are the domains to check right now.
func (c *Checker) targets() []apps.DomainRef {
	var refs []apps.DomainRef
	seen := map[string]bool{}
	for _, r := range c.Source() {
		if caddy.PublicName(r.Host) && !seen[r.Host] {
			seen[r.Host] = true
			refs = append(refs, r)
		}
	}
	if u, err := url.Parse(c.PublicURL); err == nil && u.Hostname() != "" {
		h := strings.ToLower(u.Hostname())
		if caddy.PublicName(h) && !seen[h] {
			refs = append(refs, apps.DomainRef{Host: h})
		}
	}
	return refs
}

// Check refreshes every domain, stores the results and updates incidents.
func (c *Checker) Check(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := map[string]Domain{}
	for _, d := range c.List() {
		prev[d.Host] = d
	}
	refs := c.targets()
	results := make([]Domain, len(refs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, r := range refs {
		wg.Add(1)
		go func(i int, r apps.DomainRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d := Domain{Host: r.Host, ServerID: r.ServerID, AppID: r.AppID, CheckedAt: c.Now().UnixMilli()}
			d.Public = c.publicCert(ctx, r.Host)
			d.Public.AutoRenew = shortLived(d.Public)
			if r.Origin != nil {
				d.Origin = &Cert{NotAfter: r.Origin.NotAfter, NotBefore: r.Origin.NotBefore, Issuer: r.Origin.Issuer, Error: r.Origin.Error}
				d.Origin.AutoRenew = shortLived(*d.Origin)
			}
			results[i] = d
		}(i, r)
	}
	wg.Wait()

	// Registrations: once a day per registrable domain.
	reg := map[string]Domain{} // registrable domain → registration facts
	for i := range results {
		d := &results[i]
		rd, err := publicsuffix.EffectiveTLDPlusOne(d.Host)
		if err != nil {
			continue
		}
		d.Registered = rd
		// Under a shared domain from a free DNS provider (a "private" public suffix such
		// as dpdns.org), there is no registration of your own to look up.
		if suffix, icann := publicsuffix.PublicSuffix(d.Host); !icann {
			d.DomainError = "a name under " + suffix + " (a shared domain): no registration of your own to check"
			continue
		}
		if known, ok := reg[rd]; ok {
			d.Expires, d.Registrar, d.DomainError, d.RDAPAt = known.Expires, known.Registrar, known.DomainError, known.RDAPAt
			continue
		}
		if p, ok := prev[d.Host]; ok && p.Registered == rd && c.Now().Sub(time.UnixMilli(p.RDAPAt)) < rdapEvery {
			d.Expires, d.Registrar, d.DomainError, d.RDAPAt = p.Expires, p.Registrar, p.DomainError, p.RDAPAt
		} else {
			info, err := c.RDAP.Lookup(ctx, rd)
			d.RDAPAt = c.Now().UnixMilli()
			if err != nil {
				d.DomainError = err.Error()
			} else {
				d.Expires, d.Registrar = info.Expires, info.Registrar
			}
		}
		reg[rd] = *d
	}

	current := map[string]bool{}
	for _, d := range results {
		current[d.Host] = true
		raw, _ := json.Marshal(d)
		c.db.Exec(`INSERT INTO domain_checks (host, data, checked_at, rdap_checked_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (host) DO UPDATE SET data = excluded.data, checked_at = excluded.checked_at, rdap_checked_at = excluded.rdap_checked_at`,
			d.Host, string(raw), d.CheckedAt, d.RDAPAt)
		c.evaluateCert(d)
	}
	for host := range prev {
		if !current[host] {
			c.db.Exec(`DELETE FROM domain_checks WHERE host = ?`, host)
			c.Incidents.Clear("ssl_expiring|"+host, "Domain is no longer served")
		}
	}
	c.evaluateRegistrations(results, prev)
	if c.Bus != nil {
		c.Bus.Publish("domains", map[string]int{"count": len(results)})
	}
	slog.Info("domain check finished", "domains", len(results))
}

// List returns the stored results.
func (c *Checker) List() []Domain {
	rows, err := c.db.Query(`SELECT data FROM domain_checks ORDER BY host`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var raw string
		var d Domain
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

// publicCert connects like a visitor would and reads the certificate. A certificate
// that fails verification is still read, to report its dates.
func (c *Checker) publicCert(ctx context.Context, host string) Cert {
	addr := net.JoinHostPort(host, "443")
	if c.addrFor != nil {
		addr = c.addrFor(host)
	}
	dial := func(insecure bool) (*tls.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		d := tls.Dialer{Config: &tls.Config{ServerName: host, RootCAs: c.roots, InsecureSkipVerify: insecure}}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return conn.(*tls.Conn), nil
	}
	conn, err := dial(false)
	var verr *tls.CertificateVerificationError
	switch {
	case err == nil:
		defer conn.Close()
		leaf := conn.ConnectionState().PeerCertificates[0]
		return Cert{NotAfter: leaf.NotAfter.UnixMilli(), NotBefore: leaf.NotBefore.UnixMilli(), Issuer: caddy.IssuerName(leaf)}
	case errors.As(err, &verr):
		out := Cert{Problem: true, Error: certProblem(verr.Err)}
		if len(verr.UnverifiedCertificates) > 0 {
			leaf := verr.UnverifiedCertificates[0]
			out.NotAfter, out.NotBefore, out.Issuer = leaf.NotAfter.UnixMilli(), leaf.NotBefore.UnixMilli(), caddy.IssuerName(leaf)
		}
		return out
	default:
		// Not a certificate problem: DNS, firewall, nothing on 443. Reported, no incident.
		return Cert{Error: unreachable(err)}
	}
}

// unreachable turns a connection error into a short reason.
func unreachable(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return "the name does not resolve (DNS)"
	case errors.As(err, &dns):
		return "DNS lookup failed"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &op) && op.Timeout():
		return "could not connect: timed out"
	case errors.As(err, &op) && op.Op == "dial":
		return "could not connect: nothing answers on port 443"
	}
	return "could not connect"
}

func certProblem(err error) string {
	var exp x509.CertificateInvalidError
	var host x509.HostnameError
	var ua x509.UnknownAuthorityError
	switch {
	case errors.As(err, &exp) && exp.Reason == x509.Expired:
		return "the certificate has expired"
	case errors.As(err, &host):
		return "the certificate is for another name (" + strings.Join(host.Certificate.DNSNames, ", ") + ")"
	case errors.As(err, &ua):
		return "the certificate is not signed by a trusted authority"
	}
	return err.Error()
}

// daysLeft rounds down; negative means expired.
func (c *Checker) daysLeft(t int64) int {
	left := time.UnixMilli(t).Sub(c.Now())
	if left < 0 {
		return -1
	}
	return int(left / day)
}

func (c *Checker) evaluateCert(d Domain) {
	key := "ssl_expiring|" + d.Host
	rules := c.Rules()
	sig := incidents.Signal{Type: "ssl_expiring", Key: key, ServerID: d.ServerID, AppID: d.AppID}
	if d.Public.Problem {
		sig.Severity, sig.Title, sig.Sample = "critical", "Certificate problem on "+d.Host, d.Public.Error
		c.Incidents.Ensure(sig)
		return
	}
	// The soonest of the public and the origin certificate decides. Auto-renewed
	// short-lived certificates only count once they have actually expired.
	now := c.Now().UnixMilli()
	counts := func(x Cert) bool { return x.NotAfter > 0 && (!x.AutoRenew || x.NotAfter < now) }
	which, until, issuer := "", int64(0), ""
	if counts(d.Public) {
		which, until, issuer = "public", d.Public.NotAfter, d.Public.Issuer
	}
	if o := d.Origin; o != nil && counts(*o) && (until == 0 || o.NotAfter < until) {
		which, until, issuer = "origin (on the server)", o.NotAfter, o.Issuer
	}
	if until == 0 {
		if d.Public.NotAfter > 0 || (d.Origin != nil && d.Origin.NotAfter > 0) {
			c.Incidents.Clear(key, "Certificate is renewed automatically")
		}
		return // otherwise nothing readable; keep any open incident until we know more
	}
	left := c.daysLeft(until)
	if left > rules.SSLWarnDays {
		c.Incidents.Clear(key, "Certificate renewed")
		return
	}
	sig.Severity = "warning"
	if left <= critical {
		sig.Severity = "critical"
	}
	sig.Title = fmt.Sprintf("SSL certificate for %s %s", d.Host, inDays(left))
	sig.Sample = fmt.Sprintf("%s certificate from %s, valid until %s", which, issuer, time.UnixMilli(until).UTC().Format("2006-01-02 15:04 UTC"))
	c.Incidents.Ensure(sig)
}

func (c *Checker) evaluateRegistrations(results []Domain, prev map[string]Domain) {
	rules := c.Rules()
	done := map[string]bool{}
	for _, d := range results {
		if d.Registered == "" || done[d.Registered] {
			continue
		}
		done[d.Registered] = true
		key := "domain_expiring|" + d.Registered
		if d.Expires == 0 {
			continue // registry unknown or unreachable: no verdict
		}
		left := c.daysLeft(d.Expires)
		if left > rules.DomainWarnDays {
			c.Incidents.Clear(key, "Domain renewed")
			continue
		}
		sig := incidents.Signal{Type: "domain_expiring", Key: key, ServerID: d.ServerID, Severity: "warning",
			Title:  fmt.Sprintf("Domain %s %s", d.Registered, inDays(left)),
			Sample: fmt.Sprintf("registered until %s with %s", time.UnixMilli(d.Expires).UTC().Format("2006-01-02"), orUnknown(d.Registrar))}
		if left <= critical {
			sig.Severity = "critical"
		}
		c.Incidents.Ensure(sig)
	}
	// Registrable domains no longer used by any site.
	for _, p := range prev {
		if p.Registered != "" && !done[p.Registered] {
			c.Incidents.Clear("domain_expiring|"+p.Registered, "Domain is no longer used")
		}
	}
}

func inDays(left int) string {
	switch {
	case left < 0:
		return "has expired"
	case left == 0:
		return "expires today"
	case left == 1:
		return "expires tomorrow"
	}
	return fmt.Sprintf("expires in %d days", left)
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown registrar"
	}
	return s
}
