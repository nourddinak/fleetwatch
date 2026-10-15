package domains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultBootstrap is IANA's list of RDAP servers per top-level domain.
const DefaultBootstrap = "https://data.iana.org/rdap/dns.json"

var ErrNoRDAP = errors.New("this domain's registry offers no RDAP lookup")

// RDAP looks up domain registrations directly at each registry.
type RDAP struct {
	Bootstrap string
	client    *http.Client

	mu      sync.Mutex
	servers map[string]string // tld → base URL
	at      time.Time
}

func NewRDAP() *RDAP {
	return &RDAP{Bootstrap: DefaultBootstrap, client: &http.Client{Timeout: 20 * time.Second}}
}

// Registration is what a registry says about a domain.
type Registration struct {
	Expires   int64 // unix ms
	Registrar string
}

func (r *RDAP) get(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	res, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s", req.URL.Host)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return errors.New("the registry does not know this domain")
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("registry answered HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(v)
}

func (r *RDAP) base(ctx context.Context, tld string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.servers == nil || time.Since(r.at) > 7*24*time.Hour {
		var boot struct {
			Services [][][]string `json:"services"`
		}
		if err := r.get(ctx, r.Bootstrap, &boot); err != nil {
			return "", fmt.Errorf("registry directory (IANA): %w", err)
		}
		servers := map[string]string{}
		for _, svc := range boot.Services {
			if len(svc) == 2 && len(svc[1]) > 0 {
				for _, t := range svc[0] {
					servers[strings.ToLower(t)] = svc[1][0]
				}
			}
		}
		r.servers, r.at = servers, time.Now()
	}
	b, ok := r.servers[tld]
	if !ok {
		return "", ErrNoRDAP
	}
	if !strings.HasSuffix(b, "/") {
		b += "/"
	}
	return b, nil
}

// Lookup returns the registration of a registrable domain such as example.com.
func (r *RDAP) Lookup(ctx context.Context, domain string) (Registration, error) {
	tld := domain[strings.LastIndex(domain, ".")+1:]
	base, err := r.base(ctx, tld)
	if err != nil {
		return Registration{}, err
	}
	var doc struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
		Entities []struct {
			Roles []string `json:"roles"`
			Vcard []any    `json:"vcardArray"`
		} `json:"entities"`
	}
	if err := r.get(ctx, base+"domain/"+domain, &doc); err != nil {
		return Registration{}, err
	}
	var reg Registration
	for _, e := range doc.Events {
		if e.Action == "expiration" {
			if t, err := time.Parse(time.RFC3339, e.Date); err == nil {
				reg.Expires = t.UnixMilli()
			}
		}
	}
	for _, e := range doc.Entities {
		for _, role := range e.Roles {
			if role == "registrar" {
				reg.Registrar = vcardName(e.Vcard)
			}
		}
	}
	if reg.Expires == 0 {
		return reg, errors.New("the registry does not publish an expiry date")
	}
	return reg, nil
}

// vcardName reads "fn" from a jCard: ["vcard", [["fn", {}, "text", "Name"], …]].
func vcardName(v []any) string {
	if len(v) < 2 {
		return ""
	}
	props, _ := v[1].([]any)
	for _, p := range props {
		if f, ok := p.([]any); ok && len(f) >= 4 && f[0] == "fn" {
			if s, ok := f[3].(string); ok {
				return s
			}
		}
	}
	return ""
}
