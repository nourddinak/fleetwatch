package caddy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

const certEvery = 6 * time.Hour

// CertCache checks the certificate the local web server presents for each site, at
// most every few hours. Behind a CDN this origin certificate is invisible from outside,
// yet it breaks the site when it expires (e.g. Cloudflare "Full (strict)").
type CertCache struct {
	mu   sync.Mutex
	at   time.Time
	last []proto.CertInfo
	// Addr is where the local web server listens for TLS (tests).
	Addr string
}

// Certs returns the cached results, refreshing them when stale.
func (c *CertCache) Certs(ctx context.Context, routes []proto.Route) []proto.CertInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < certEvery {
		return c.last
	}
	addr := c.Addr
	if addr == "" {
		addr = "127.0.0.1:443"
	}
	seen := map[string]bool{}
	var out []proto.CertInfo
	for _, r := range routes {
		h := strings.ToLower(r.Host)
		if seen[h] || !PublicName(h) {
			continue
		}
		seen[h] = true
		out = append(out, checkLocal(ctx, addr, h))
		if len(out) >= 100 {
			break
		}
	}
	c.at, c.last = time.Now(), out
	return out
}

func checkLocal(ctx context.Context, addr, host string) proto.CertInfo {
	info := proto.CertInfo{Host: host}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Origin certificates are often not publicly trusted (e.g. Cloudflare Origin CA),
	// so the chain is not verified here; dates and the name are what matter.
	d := tls.Dialer{Config: &tls.Config{ServerName: host, InsecureSkipVerify: true}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		info.Error = "no TLS answer on " + addr + " for this name"
		return info
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		info.Error = "no certificate presented"
		return info
	}
	leaf := certs[0]
	info.NotAfter = leaf.NotAfter.UnixMilli()
	info.Issuer = IssuerName(leaf)
	if err := leaf.VerifyHostname(host); err != nil {
		info.Error = "certificate does not cover " + host
	}
	return info
}

// IssuerName is a short, human name for a certificate's issuer.
func IssuerName(c *x509.Certificate) string {
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0]
	}
	return c.Issuer.CommonName
}

// PublicName reports whether host is a real public domain name worth checking (not an
// IP, wildcard, localhost or a private-use suffix).
func PublicName(host string) bool {
	if host == "" || strings.ContainsAny(host, "*:/ ") || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".test", ".example", ".invalid", ".lan", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}
