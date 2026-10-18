// Package nginx reads which sites Nginx serves from its configuration: each server
// block's names, the folder it serves (root) and the app it forwards to (proxy_pass).
package nginx

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

// Conf is Nginx's main configuration file; tests point it elsewhere.
var Conf = "/etc/nginx/nginx.conf"

const (
	ttl        = time.Minute
	maxNesting = 8
	maxFiles   = 200
)

// Cache keeps the parsed routes for a minute.
type Cache struct {
	mu     sync.Mutex
	at     time.Time
	routes []proto.Route
}

func (c *Cache) Routes() []proto.Route {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || time.Since(c.at) >= ttl {
		c.routes, c.at = Routes(Conf), time.Now()
		if len(c.routes) > 0 && running() {
			for i := range c.routes {
				c.routes[i].Live = true // Nginx is up, so what its configuration says is served
			}
		}
	}
	return c.routes
}

// running asks systemd whether Nginx is active.
var running = func() bool {
	return exec.Command("systemctl", "is-active", "--quiet", "nginx").Run() == nil
}

// Routes parses a configuration file and the files it includes ([] without Nginx).
func Routes(conf string) []proto.Route {
	if _, err := os.Stat(conf); err != nil {
		return []proto.Route{}
	}
	p := &parser{base: filepath.Dir(conf), upstreams: map[string]string{}, seen: map[string]bool{}}
	toks := p.file(conf, 0)
	p.collectUpstreams(toks)
	out := []proto.Route{}
	for _, srv := range blocks(toks, "server") {
		out = append(out, p.serverRoutes(srv)...)
	}
	return out
}

type parser struct {
	base      string            // relative includes resolve from the main file's folder
	upstreams map[string]string // upstream name → its first server (host:port)
	seen      map[string]bool
}

// file returns the tokens of a file with its includes expanded in place.
func (p *parser) file(name string, nesting int) []string {
	if nesting > maxNesting || len(p.seen) >= maxFiles || p.seen[name] {
		return nil
	}
	p.seen[name] = true
	b, err := os.ReadFile(name)
	if err != nil {
		return nil
	}
	var out []string
	toks := tokenize(string(b))
	for i := 0; i < len(toks); i++ {
		if toks[i] == "include" && i+2 < len(toks) && toks[i+2] == ";" {
			pat := toks[i+1]
			if !filepath.IsAbs(pat) {
				pat = filepath.Join(p.base, pat)
			}
			matches, _ := filepath.Glob(pat)
			for _, m := range matches {
				out = append(out, p.file(m, nesting+1)...)
			}
			i += 2
			continue
		}
		out = append(out, toks[i])
	}
	return out
}

// tokenize splits configuration text into words, ";", "{" and "}", without comments.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			flush()
		case c == '"' || c == '\'':
			flush()
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				j = len(s) - i - 1
			}
			out = append(out, s[i+1:i+1+j])
			i += j + 1
		case c == ';' || c == '{' || c == '}':
			flush()
			out = append(out, string(c))
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// blocks returns the bodies of every "<name> ... { body }" block, at any depth.
func blocks(toks []string, name string) [][]string {
	var out [][]string
	for i := 0; i < len(toks); i++ {
		if toks[i] != name {
			continue
		}
		j := i + 1
		for j < len(toks) && toks[j] != "{" && toks[j] != ";" {
			j++
		}
		if j >= len(toks) || toks[j] != "{" {
			continue
		}
		end := closing(toks, j)
		out = append(out, toks[j+1:end])
		i = end
	}
	return out
}

// closing finds the "}" that ends the block opened at open.
func closing(toks []string, open int) int {
	depth := 0
	for k := open; k < len(toks); k++ {
		switch toks[k] {
		case "{":
			depth++
		case "}":
			depth--
			if depth == 0 {
				return k
			}
		}
	}
	return len(toks)
}

// directives returns this block's own directives (not those of nested blocks).
func directives(body []string) [][]string {
	var out [][]string
	var cur []string
	for k := 0; k < len(body); k++ {
		switch body[k] {
		case "{":
			k = closing(body, k)
			cur = nil
		case ";":
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
		default:
			cur = append(cur, body[k])
		}
	}
	return out
}

func (p *parser) collectUpstreams(toks []string) {
	for i := 0; i+2 < len(toks); i++ {
		if toks[i] != "upstream" || toks[i+2] != "{" {
			continue
		}
		body := toks[i+3 : closing(toks, i+2)]
		for _, d := range directives(body) {
			if d[0] == "server" && len(d) > 1 {
				p.upstreams[toks[i+1]] = d[1]
				break
			}
		}
	}
}

// serverRoutes turns one server block into routes, one per name.
func (p *parser) serverRoutes(body []string) []proto.Route {
	var names []string
	root, topUp, anyUp := "", "", ""
	for _, d := range directives(body) {
		switch d[0] {
		case "server_name":
			names = append(names, d[1:]...)
		case "root":
			if len(d) > 1 {
				root = d[1]
			}
		}
	}
	// What "location /" does decides the site; then a folder it serves; then the first
	// location that forwards (a site whose /api/ goes to an app is still a static site).
	for i := 0; i < len(body); i++ {
		if body[i] != "location" {
			continue
		}
		j := i + 1
		for j < len(body) && body[j] != "{" {
			j++
		}
		if j >= len(body) {
			break
		}
		end := closing(body, j)
		top := strings.Join(body[i+1:j], " ") == "/"
		for _, d := range directives(body[j+1 : end]) {
			switch {
			case d[0] == "proxy_pass" && len(d) > 1 && top:
				topUp = d[1]
			case d[0] == "proxy_pass" && len(d) > 1 && anyUp == "":
				anyUp = d[1]
			case d[0] == "root" && len(d) > 1 && top:
				root = d[1]
			}
		}
		i = end
	}
	var out []proto.Route
	for _, n := range names {
		if n == "_" || n == "" || strings.HasPrefix(n, "~") || strings.Contains(n, "*") || strings.Contains(n, "$") {
			continue
		}
		r := proto.Route{Host: strings.TrimSuffix(strings.ToLower(n), "."), Server: "nginx"}
		staticRoot := root != "" && strings.HasPrefix(root, "/") && !strings.Contains(root, "$")
		switch {
		case topUp != "":
			r.Upstream, r.Port = p.target(topUp)
		case staticRoot:
			r.Root = filepath.ToSlash(filepath.Clean(root))
		case anyUp != "":
			r.Upstream, r.Port = p.target(anyUp)
		}
		out = append(out, r)
	}
	return out
}

// target resolves proxy_pass (a URL, or an upstream's name) to an address and port.
func (p *parser) target(pass string) (string, int) {
	host := pass
	if u, err := url.Parse(pass); err == nil && u.Host != "" {
		host = u.Host
	}
	if up, ok := p.upstreams[host]; ok {
		host = up
	}
	port := 0
	if i := strings.LastIndex(host, ":"); i >= 0 {
		port, _ = strconv.Atoi(host[i+1:])
	} else if strings.HasPrefix(pass, "https://") {
		port = 443
	} else if strings.HasPrefix(pass, "http://") {
		port = 80
	}
	return pass, port
}
