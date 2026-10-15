// Package caddy reads reverse-proxy routes (domain → upstream) from a local Caddy,
// via its admin API or, failing that, by parsing the Caddyfile. It never writes.
package caddy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

const ttl = 5 * time.Minute

type Cache struct {
	mu     sync.Mutex
	at     time.Time
	routes []proto.Route
}

func (c *Cache) Routes(ctx context.Context) []proto.Route {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < ttl {
		return c.routes
	}
	c.routes = fromAdminAPI(ctx)
	if c.routes == nil {
		if f, err := os.Open("/etc/caddy/Caddyfile"); err == nil {
			c.routes = ParseCaddyfile(f)
			f.Close()
		}
	}
	if c.routes == nil {
		c.routes = []proto.Route{}
	}
	c.at = time.Now()
	return c.routes
}

func fromAdminAPI(ctx context.Context) []proto.Route {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:2019/config/", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return ParseJSONConfig(raw)
}

type jsonRoute struct {
	Match []struct {
		Host []string `json:"host"`
	} `json:"match"`
	Handle []jsonHandler `json:"handle"`
}

type jsonHandler struct {
	Handler   string      `json:"handler"`
	Routes    []jsonRoute `json:"routes"`
	Upstreams []struct {
		Dial string `json:"dial"`
	} `json:"upstreams"`
}

// ParseJSONConfig extracts routes from Caddy's JSON config (GET /config/).
func ParseJSONConfig(raw []byte) []proto.Route {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []jsonRoute `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	out := []proto.Route{}
	var walk func(routes []jsonRoute, hosts []string)
	walk = func(routes []jsonRoute, hosts []string) {
		for _, r := range routes {
			h := hosts
			for _, m := range r.Match {
				if len(m.Host) > 0 {
					h = m.Host
				}
			}
			for _, hd := range r.Handle {
				switch hd.Handler {
				case "reverse_proxy":
					for _, u := range hd.Upstreams {
						for _, host := range h {
							out = append(out, route(host, u.Dial))
						}
					}
				case "subroute":
					walk(hd.Routes, h)
				}
			}
		}
	}
	for _, s := range cfg.Apps.HTTP.Servers {
		walk(s.Routes, nil)
	}
	return out
}

// ParseCaddyfile handles the common shape: site blocks containing reverse_proxy.
// shortcut: no import/snippet expansion or env placeholders; good enough for suggestions.
func ParseCaddyfile(r io.Reader) []proto.Route {
	out := []proto.Route{}
	var hosts []string
	depth := 0
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		opens, closes := strings.Count(line, "{"), strings.Count(line, "}")
		if depth == 0 && strings.HasSuffix(line, "{") && !strings.HasPrefix(line, "(") && line != "{" {
			hosts = siteHosts(strings.TrimSuffix(line, "{"))
		}
		if depth > 0 {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[0] == "reverse_proxy" {
				for _, f := range fields[1:] {
					if f == "{" || strings.HasPrefix(f, "/") || strings.HasPrefix(f, "@") || f == "*" {
						continue // matchers and block openers
					}
					for _, h := range hosts {
						out = append(out, route(h, f))
					}
				}
			}
		}
		depth += opens - closes
		if depth <= 0 {
			depth = 0
		}
	}
	return out
}

func siteHosts(addr string) []string {
	var hosts []string
	for _, a := range strings.FieldsFunc(addr, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if u, err := url.Parse(a); err == nil && u.Host != "" {
			a = u.Host
		}
		if h, _, err := net.SplitHostPort(a); err == nil {
			a = h
		}
		if a != "" && !strings.HasPrefix(a, ":") {
			hosts = append(hosts, a)
		}
	}
	return hosts
}

func route(host, upstream string) proto.Route {
	up := upstream
	if u, err := url.Parse(upstream); err == nil && u.Host != "" {
		up = u.Host
	}
	port := 0
	if i := strings.LastIndex(up, ":"); i >= 0 {
		port, _ = strconv.Atoi(up[i+1:])
	}
	return proto.Route{Host: host, Upstream: upstream, Port: port}
}
