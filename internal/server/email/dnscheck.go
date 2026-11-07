package email

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Resolver answers the DNS lookups of a domain check.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

type netResolver struct{}

func (netResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, name)
}
func (netResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

// DKIMCheck reports the sending provider's signature for a domain (set from the
// Integrations → Email sender: Resend verifies it). nil = unknown.
type DKIMCheck func(ctx context.Context, domain, senderID string) Check

func notFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// CheckDomain looks at a domain's records as the world sees them, and its Cloudflare
// routing, and saves the result.
func (s *Service) CheckDomain(ctx context.Context, id string) (Domain, error) {
	d, err := s.Domain(id)
	if err != nil {
		return d, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var c Checks

	mx, err := s.Resolver.LookupMX(ctx, d.Name)
	switch {
	case notFound(err) || err == nil && len(mx) == 0:
		c.MX = Check{Status: "missing", Detail: "no MX records: mail to this domain goes nowhere"}
	case err != nil:
		c.MX = Check{Status: "error", Detail: err.Error()}
	default:
		var hosts []string
		cf := 0
		for _, m := range mx {
			h := strings.TrimSuffix(strings.ToLower(m.Host), ".")
			hosts = append(hosts, h)
			if strings.HasSuffix(h, "mx.cloudflare.net") {
				cf++
			}
		}
		if cf == len(mx) {
			c.MX = Check{Status: "ok", Detail: strings.Join(hosts, ", ")}
		} else {
			c.MX = Check{Status: "wrong", Detail: "MX points to " + strings.Join(hosts, ", ") + " — all must be Cloudflare's (route*.mx.cloudflare.net)"}
		}
	}

	txt, err := s.Resolver.LookupTXT(ctx, d.Name)
	var spf []string
	for _, t := range txt {
		if strings.HasPrefix(strings.ToLower(t), "v=spf1") {
			spf = append(spf, t)
		}
	}
	switch {
	case err != nil && !notFound(err):
		c.SPF = Check{Status: "error", Detail: err.Error()}
	case len(spf) == 0:
		c.SPF = Check{Status: "missing", Detail: "no SPF record"}
	case len(spf) > 1:
		c.SPF = Check{Status: "wrong", Detail: fmt.Sprintf("%d SPF records: there must be exactly one", len(spf))}
	case !strings.Contains(strings.ToLower(spf[0]), "include:_spf.mx.cloudflare.net"):
		c.SPF = Check{Status: "wrong", Detail: "the SPF record does not include Cloudflare: " + spf[0]}
	default:
		c.SPF = Check{Status: "ok", Detail: spf[0]}
	}

	dm, err := s.Resolver.LookupTXT(ctx, "_dmarc."+d.Name)
	c.DMARC = Check{Status: "missing", Detail: "no DMARC record: Gmail and others trust your mail less"}
	if err != nil && !notFound(err) {
		c.DMARC = Check{Status: "error", Detail: err.Error()}
	}
	for _, t := range dm {
		if strings.HasPrefix(strings.ToUpper(t), "V=DMARC1") {
			c.DMARC = Check{Status: "ok", Detail: t}
		}
	}

	c.DKIM = Check{Status: "unknown", Detail: "set up by your sending provider: check it there"}
	if s.DKIM != nil {
		c.DKIM = s.DKIM(ctx, d.Name, d.SenderID)
	}
	c.Routing = s.routingCheck(ctx, d)
	s.saveChecks(id, c)
	return s.Domain(id)
}

// CheckAll re-checks every domain (daily).
func (s *Service) CheckAll(ctx context.Context) {
	ds, _ := s.Domains()
	for _, d := range ds {
		if s.Now().Unix()-d.CheckedAt > 20*3600 {
			s.CheckDomain(ctx, d.ID)
		}
	}
}
