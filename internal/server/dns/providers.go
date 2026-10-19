// Package dns connects people's Cloudflare accounts (they sign in with Cloudflare): it
// points their sites' domains at the server, clears the cache after a deploy, switches
// the proxy and "Under attack" mode, and mirrors IP blocks.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Zone is a domain an account manages.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Record is one A record.
type Record struct {
	ID      string `json:"id"`
	Name    string `json:"name"` // full name, e.g. shop.example.com
	Content string `json:"content"`
	Proxied bool   `json:"proxied,omitempty"`
}

// Provider is what every DNS account can do.
type Provider interface {
	Zones(ctx context.Context) ([]Zone, error)
	FindA(ctx context.Context, z Zone, name string) (*Record, error)
	CreateA(ctx context.Context, z Zone, r Record) (string, error)
	UpdateA(ctx context.Context, z Zone, r Record) error
	DeleteA(ctx context.Context, z Zone, id string) error
}

// CDN is what Cloudflare adds.
type CDN interface {
	Purge(ctx context.Context, z Zone, urls []string) error
	PurgeHosts(ctx context.Context, z Zone, hosts []string) error // everything cached for those hostnames
	SecurityLevel(ctx context.Context, z Zone) (string, error)
	SetSecurityLevel(ctx context.Context, z Zone, level string) error
	SetProxied(ctx context.Context, z Zone, id string, on bool) error
	BlockRules(ctx context.Context, z Zone) (map[string]string, error) // ip → rule ID, Fleetwatch's own
	Block(ctx context.Context, z Zone, ip string) error
	DeleteRule(ctx context.Context, z Zone, id string) error
}

// Base addresses; tests point them at fakes.
var (
	CloudflareAPI = "https://api.cloudflare.com/client/v4"
	CloudflareWeb = "https://dash.cloudflare.com" // sign-in and tokens
	client        = &http.Client{Timeout: 20 * time.Second}
)

func doJSON(ctx context.Context, method, u string, header http.Header, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cannot reach the DNS provider: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if out != nil && len(raw) > 0 {
		json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

// ---------- Cloudflare ----------

type cloudflare struct{ token string }

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (c cloudflare) call(ctx context.Context, method, path string, body, result any) (cfEnvelope, error) {
	var env cfEnvelope
	_, err := doJSON(ctx, method, CloudflareAPI+path, http.Header{"Authorization": {"Bearer " + c.token}}, body, &env)
	if err != nil {
		return env, err
	}
	if !env.Success {
		msg := "Cloudflare refused the request"
		if len(env.Errors) > 0 {
			msg = "Cloudflare: " + env.Errors[0].Message
		}
		return env, errors.New(msg)
	}
	if result != nil {
		json.Unmarshal(env.Result, result)
	}
	return env, nil
}

func (c cloudflare) Zones(ctx context.Context) ([]Zone, error) {
	out := []Zone{}
	for page := 1; page <= 20; page++ {
		var zs []Zone
		env, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &zs)
		if err != nil {
			return nil, err
		}
		out = append(out, zs...)
		if page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return out, nil
}

func (c cloudflare) FindA(ctx context.Context, z Zone, name string) (*Record, error) {
	var rs []Record
	if _, err := c.call(ctx, http.MethodGet, "/zones/"+z.ID+"/dns_records?type=A&name="+url.QueryEscape(name), nil, &rs); err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}
	return &rs[0], nil
}

func (c cloudflare) CreateA(ctx context.Context, z Zone, r Record) (string, error) {
	var got Record
	_, err := c.call(ctx, http.MethodPost, "/zones/"+z.ID+"/dns_records", map[string]any{"type": "A", "name": r.Name, "content": r.Content, "ttl": 1, "proxied": r.Proxied}, &got)
	return got.ID, err
}

func (c cloudflare) UpdateA(ctx context.Context, z Zone, r Record) error {
	_, err := c.call(ctx, http.MethodPatch, "/zones/"+z.ID+"/dns_records/"+r.ID, map[string]any{"content": r.Content}, nil)
	return err
}

func (c cloudflare) DeleteA(ctx context.Context, z Zone, id string) error {
	_, err := c.call(ctx, http.MethodDelete, "/zones/"+z.ID+"/dns_records/"+id, nil, nil)
	return err
}

func (c cloudflare) Purge(ctx context.Context, z Zone, urls []string) error {
	_, err := c.call(ctx, http.MethodPost, "/zones/"+z.ID+"/purge_cache", map[string]any{"files": urls}, nil)
	return err
}

func (c cloudflare) PurgeHosts(ctx context.Context, z Zone, hosts []string) error {
	_, err := c.call(ctx, http.MethodPost, "/zones/"+z.ID+"/purge_cache", map[string]any{"hosts": hosts}, nil)
	return err
}

func (c cloudflare) SecurityLevel(ctx context.Context, z Zone) (string, error) {
	var s struct {
		Value string `json:"value"`
	}
	_, err := c.call(ctx, http.MethodGet, "/zones/"+z.ID+"/settings/security_level", nil, &s)
	return s.Value, err
}

func (c cloudflare) SetSecurityLevel(ctx context.Context, z Zone, level string) error {
	_, err := c.call(ctx, http.MethodPatch, "/zones/"+z.ID+"/settings/security_level", map[string]string{"value": level}, nil)
	return err
}

func (c cloudflare) SetProxied(ctx context.Context, z Zone, id string, on bool) error {
	_, err := c.call(ctx, http.MethodPatch, "/zones/"+z.ID+"/dns_records/"+id, map[string]bool{"proxied": on}, nil)
	return err
}

// ruleNote marks the access rules Fleetwatch made, so it never touches anyone else's.
const ruleNote = "fleetwatch: blocked by the dashboard"

func (c cloudflare) BlockRules(ctx context.Context, z Zone) (map[string]string, error) {
	out := map[string]string{}
	for page := 1; page <= 50; page++ {
		var rules []struct {
			ID            string `json:"id"`
			Notes         string `json:"notes"`
			Configuration struct {
				Value string `json:"value"`
			} `json:"configuration"`
		}
		env, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/firewall/access_rules/rules?per_page=100&page=%d&notes=%s", z.ID, page, url.QueryEscape(ruleNote)), nil, &rules)
		if err != nil {
			return nil, err
		}
		for _, r := range rules {
			if r.Notes == ruleNote {
				out[r.Configuration.Value] = r.ID
			}
		}
		if page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return out, nil
}

func (c cloudflare) Block(ctx context.Context, z Zone, ip string) error {
	target := "ip"
	if strings.Contains(ip, "/") {
		target = "ip_range"
	}
	_, err := c.call(ctx, http.MethodPost, "/zones/"+z.ID+"/firewall/access_rules/rules",
		map[string]any{"mode": "block", "configuration": map[string]string{"target": target, "value": ip}, "notes": ruleNote}, nil)
	return err
}

func (c cloudflare) DeleteRule(ctx context.Context, z Zone, id string) error {
	_, err := c.call(ctx, http.MethodDelete, "/zones/"+z.ID+"/firewall/access_rules/rules/"+id, nil, nil)
	return err
}
