// Package statuspage builds the public status page: an overall state, notices written
// by the admin, and chosen apps and servers with their current status and daily
// uptime history (derived from recorded incidents). Visitors only ever see the names
// and texts configured here, never internal hostnames, IPs or incident details.
package statuspage

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

const (
	settingsKey = "statusPage"
	maxLogo     = 256 << 10 // bytes of image data
	cacheFor    = 30 * time.Second
	day         = 24 * time.Hour
)

// Component is one row on the page: an app or a server under a public name.
type Component struct {
	Kind        string `json:"kind"` // app | server
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Group struct {
	Name       string      `json:"name"`
	Components []Component `json:"components"`
}

// Notice is an announcement written by the admin ("Scheduled maintenance tonight").
type Notice struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"` // info | maintenance | degraded | outage | resolved
	Start     int64  `json:"start"`  // unix ms
	End       int64  `json:"end"`    // unix ms, 0 = until removed or resolved
	CreatedAt int64  `json:"createdAt"`
}

// Config is everything the admin can customize.
type Config struct {
	Enabled     bool     `json:"enabled"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Logo        string   `json:"logo"`   // data: URL (png, jpeg, webp, svg)
	Accent      string   `json:"accent"` // #rrggbb
	Theme       string   `json:"theme"`  // auto | light | dark
	HomeURL     string   `json:"homeUrl"`
	HomeLabel   string   `json:"homeLabel"`
	Footer      string   `json:"footer"`
	PoweredBy   bool     `json:"poweredBy"`
	Domain      string   `json:"domain"` // custom host that serves only the status page
	HistoryDays int      `json:"historyDays"`
	ShowUptime  bool     `json:"showUptime"`
	Groups      []Group  `json:"groups"`
	Notices     []Notice `json:"notices"`
	UpdatedAt   int64    `json:"updatedAt"`
}

func Defaults() Config {
	return Config{Title: "Service status", Accent: "#16a34a", Theme: "auto", PoweredBy: true, HistoryDays: 90, ShowUptime: true,
		Groups: []Group{}, Notices: []Notice{}}
}

var (
	accentRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	hostRe   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	logoRe   = regexp.MustCompile(`^data:image/(png|jpeg|webp|svg\+xml);base64,([A-Za-z0-9+/=]+)$`)
	notices  = map[string]bool{"info": true, "maintenance": true, "degraded": true, "outage": true, "resolved": true}
)

func text(name, v string, max int) error {
	if len(v) > max {
		return valid.Error(fmt.Sprintf("%s must be at most %d characters", name, max))
	}
	return nil
}

// Validate normalizes c and rejects anything unsafe to publish.
func (c *Config) Validate() error {
	c.Title, c.Domain = strings.TrimSpace(c.Title), strings.ToLower(strings.TrimSpace(c.Domain))
	for _, e := range []error{text("title", c.Title, 80), text("description", c.Description, 1000), text("footer", c.Footer, 500),
		text("link label", c.HomeLabel, 40)} {
		if e != nil {
			return e
		}
	}
	if c.Title == "" {
		c.Title = "Service status"
	}
	if !accentRe.MatchString(c.Accent) {
		return valid.Error("accent color must look like #16a34a")
	}
	if c.Theme != "auto" && c.Theme != "light" && c.Theme != "dark" {
		return valid.Error("theme must be auto, light or dark")
	}
	if c.HomeURL != "" {
		u, err := url.Parse(c.HomeURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(c.HomeURL) > 300 {
			return valid.Error("website link must be an http(s) URL")
		}
	}
	if c.Logo != "" {
		m := logoRe.FindStringSubmatch(c.Logo)
		if m == nil {
			return valid.Error("logo must be a PNG, JPEG, WebP or SVG image")
		}
		if base64.StdEncoding.DecodedLen(len(m[2])) > maxLogo {
			return valid.Error("logo must be at most 256 KB")
		}
	}
	if c.Domain != "" && !hostRe.MatchString(c.Domain) {
		return valid.Error("status page domain must look like status.example.com")
	}
	switch c.HistoryDays {
	case 30, 60, 90:
	default:
		return valid.Error("history must be 30, 60 or 90 days")
	}
	if len(c.Groups) > 20 {
		return valid.Error("at most 20 groups")
	}
	total := 0
	for gi := range c.Groups {
		g := &c.Groups[gi]
		g.Name = strings.TrimSpace(g.Name)
		if err := text("group name", g.Name, 60); err != nil {
			return err
		}
		if g.Components == nil {
			g.Components = []Component{}
		}
		for ci := range g.Components {
			x := &g.Components[ci]
			x.Name = strings.TrimSpace(x.Name)
			if x.Kind != "app" && x.Kind != "server" || x.ID == "" || len(x.ID) > 64 {
				return valid.Error("each component must be an app or a server")
			}
			if x.Name == "" || len(x.Name) > 60 {
				return valid.Error("each component needs a public name (1-60 characters)")
			}
			if err := text("component description", x.Description, 200); err != nil {
				return err
			}
			total++
		}
	}
	if total > 100 {
		return valid.Error("at most 100 components")
	}
	if len(c.Notices) > 50 {
		return valid.Error("at most 50 notices; delete old ones")
	}
	for i := range c.Notices {
		n := &c.Notices[i]
		n.Title = strings.TrimSpace(n.Title)
		if n.Title == "" || len(n.Title) > 120 || len(n.Body) > 2000 || !notices[n.Status] || n.Start <= 0 || (n.End != 0 && n.End < n.Start) {
			return valid.Error("each notice needs a title (up to 120 characters), a status and a valid time range")
		}
		if n.ID == "" {
			n.ID = fmt.Sprintf("ntc_%d_%d", n.CreatedAt, i)
		}
	}
	if c.Groups == nil {
		c.Groups = []Group{}
	}
	if c.Notices == nil {
		c.Notices = []Notice{}
	}
	return nil
}

// Service serves the configuration and the public view.
type Service struct {
	db        *sql.DB
	Incidents *incidents.Store
	// AppState and ServerState return a component's current state (operational,
	// degraded, outage, maintenance, unknown), its server, and when it was created.
	AppState    func(id string) (state, serverID string, created int64, ok bool)
	ServerState func(id string) (state string, created int64, ok bool)
	Now         func() time.Time

	mu     sync.Mutex
	cached *Public
	at     time.Time
	host   atomic.Value // custom domain, read on every request
}

func New(d *sql.DB, inc *incidents.Store) *Service {
	s := &Service{db: d, Incidents: inc, Now: time.Now}
	s.host.Store(s.Config().Domain)
	return s
}

// Domain is the custom host that serves only the status page ("" = none).
func (s *Service) Domain() string { return s.host.Load().(string) }

func (s *Service) Config() Config {
	c := Defaults()
	settings.Load(s.db, settingsKey, &c)
	return c
}

func (s *Service) Save(c Config) (Config, error) {
	if err := c.Validate(); err != nil {
		return c, err
	}
	c.UpdatedAt = s.Now().UnixMilli()
	if err := settings.Save(s.db, settingsKey, c); err != nil {
		return c, err
	}
	s.host.Store(c.Domain)
	s.mu.Lock()
	s.cached = nil // show changes right away
	s.mu.Unlock()
	return c, nil
}

// ---------- public view ----------

type Day struct {
	Date        string `json:"date"`   // YYYY-MM-DD (UTC)
	Status      string `json:"status"` // operational | degraded | outage | nodata
	DownMinutes int    `json:"downMinutes,omitempty"`
}

type PublicComponent struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
	Uptime      *float64 `json:"uptime,omitempty"` // percent over the history window
	Days        []Day    `json:"days,omitempty"`
}

type PublicGroup struct {
	Name       string            `json:"name"`
	Components []PublicComponent `json:"components"`
}

type PublicNotice struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Status string `json:"status"`
	Start  int64  `json:"start"`
	End    int64  `json:"end,omitempty"`
}

// Public is exactly what anonymous visitors receive.
type Public struct {
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Logo        string         `json:"logo,omitempty"`
	Accent      string         `json:"accent"`
	Theme       string         `json:"theme"`
	HomeURL     string         `json:"homeUrl,omitempty"`
	HomeLabel   string         `json:"homeLabel,omitempty"`
	Footer      string         `json:"footer,omitempty"`
	PoweredBy   bool           `json:"poweredBy"`
	HistoryDays int            `json:"historyDays"`
	Overall     string         `json:"overall"` // operational | degraded | partial | major | maintenance
	Notices     []PublicNotice `json:"notices"`
	Groups      []PublicGroup  `json:"groups"`
	UpdatedAt   int64          `json:"updatedAt"`
}

// Public returns the page data, rebuilt at most every 30 seconds. ok is false when
// the status page is turned off.
func (s *Service) Public() (*Public, bool) {
	c := s.Config()
	if !c.Enabled {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.Now().Sub(s.at) < cacheFor {
		return s.cached, true
	}
	p := s.build(c)
	s.cached, s.at = p, s.Now()
	return p, true
}

// downTypes take a component down; slowTypes only degrade it.
var (
	downTypes = map[string]bool{"app_offline": true, "crash_loop": true, "pm2_errored": true, "systemd_failed": true, "health_failed": true, "server_offline": true}
	slowTypes = map[string]bool{"repeated_errors": true, "repeated_restarts": true, "port_unavailable": true, "high_cpu": true, "high_ram": true, "disk_full": true}
)

func (s *Service) build(c Config) *Public {
	now := s.Now()
	p := &Public{Title: c.Title, Description: c.Description, Logo: c.Logo, Accent: c.Accent, Theme: c.Theme, HomeURL: c.HomeURL,
		HomeLabel: c.HomeLabel, Footer: c.Footer, PoweredBy: c.PoweredBy, HistoryDays: c.HistoryDays, Notices: []PublicNotice{},
		Groups: []PublicGroup{}, UpdatedAt: now.UnixMilli()}
	ms := now.UnixMilli()
	noticeDown, noticeSlow, noticeMaint := false, false, false
	for _, n := range c.Notices {
		active := n.Start <= ms && (n.End == 0 || n.End > ms) && n.Status != "resolved"
		recent := (n.Status == "resolved" || (n.End != 0 && n.End <= ms)) && ms-max(n.End, n.CreatedAt) < day.Milliseconds()
		upcoming := n.Status == "maintenance" && n.Start > ms && n.Start-ms < 7*day.Milliseconds()
		if !active && !recent && !upcoming {
			continue
		}
		pn := PublicNotice{Title: n.Title, Body: n.Body, Status: n.Status, Start: n.Start, End: n.End}
		if !active && !upcoming {
			pn.Status = "resolved"
		}
		p.Notices = append(p.Notices, pn)
		if active {
			noticeDown = noticeDown || n.Status == "outage"
			noticeSlow = noticeSlow || n.Status == "degraded"
			noticeMaint = noticeMaint || n.Status == "maintenance"
		}
	}
	sort.SliceStable(p.Notices, func(i, j int) bool { return p.Notices[i].Start > p.Notices[j].Start })

	total, down, slow, maint := 0, 0, 0, 0
	for _, g := range c.Groups {
		pg := PublicGroup{Name: g.Name, Components: []PublicComponent{}}
		for _, x := range g.Components {
			pc := s.component(x, c, now)
			pg.Components = append(pg.Components, pc)
			total++
			switch pc.Status {
			case "outage":
				down++
			case "degraded":
				slow++
			case "maintenance":
				maint++
			}
		}
		p.Groups = append(p.Groups, pg)
	}
	switch {
	case total > 0 && down == total:
		p.Overall = "major"
	case down > 0 || noticeDown:
		p.Overall = "partial"
	case slow > 0 || noticeSlow:
		p.Overall = "degraded"
	case maint > 0 || noticeMaint:
		p.Overall = "maintenance"
	default:
		p.Overall = "operational"
	}
	return p
}

func (s *Service) component(x Component, c Config, now time.Time) PublicComponent {
	pc := PublicComponent{Name: x.Name, Description: x.Description, Status: "unknown"}
	var created int64
	var related []incidents.Incident
	switch x.Kind {
	case "app":
		state, serverID, cr, ok := s.AppState(x.ID)
		if !ok {
			return pc
		}
		pc.Status, created = state, cr
		related = s.incidents(incidents.Filter{AppID: x.ID})
		for _, i := range s.incidents(incidents.Filter{ServerID: serverID, Type: "server_offline"}) {
			related = append(related, i) // the whole server being down takes its apps down
		}
	case "server":
		state, cr, ok := s.ServerState(x.ID)
		if !ok {
			return pc
		}
		pc.Status, created = state, cr
		for _, i := range s.incidents(incidents.Filter{ServerID: x.ID}) {
			if i.AppID == nil {
				related = append(related, i)
			}
		}
	}
	if c.ShowUptime {
		pc.Days, pc.Uptime = History(related, created, now, c.HistoryDays)
	}
	return pc
}

func (s *Service) incidents(f incidents.Filter) []incidents.Incident {
	f.Status, f.Limit = "all", 1000
	list, _ := s.Incidents.List(f)
	return list
}

type span struct{ from, to int64 }

// merge joins overlapping intervals so simultaneous incidents count once.
func merge(in []span) []span {
	sort.Slice(in, func(i, j int) bool { return in[i].from < in[j].from })
	var out []span
	for _, x := range in {
		if n := len(out); n > 0 && x.from <= out[n-1].to {
			out[n-1].to = max(out[n-1].to, x.to)
			continue
		}
		out = append(out, x)
	}
	return out
}

func overlap(list []span, from, to int64) int64 {
	var total int64
	for _, x := range list {
		if a, b := max(x.from, from), min(x.to, to); b > a {
			total += b - a
		}
	}
	return total
}

// History turns incidents into one entry per UTC day (oldest first) and the uptime
// percentage since the component existed within the window.
func History(list []incidents.Incident, created int64, now time.Time, days int) ([]Day, *float64) {
	var down, slow []span
	for _, i := range list {
		end := i.ResolvedAt
		if end == 0 {
			end = now.UnixMilli()
		}
		switch {
		case downTypes[i.TypeKey]:
			down = append(down, span{i.FirstAt, end})
		case slowTypes[i.TypeKey]:
			slow = append(slow, span{i.FirstAt, end})
		}
	}
	down, slow = merge(down), merge(slow)
	today := now.UTC().Truncate(day)
	out := make([]Day, 0, days)
	var observed, lost int64
	for d := days - 1; d >= 0; d-- {
		from := today.Add(-time.Duration(d) * day)
		to := from.Add(day)
		f, t := from.UnixMilli(), min(to.UnixMilli(), now.UnixMilli())
		entry := Day{Date: from.Format("2006-01-02"), Status: "operational"}
		if created > 0 && t <= created {
			entry.Status = "nodata"
			out = append(out, entry)
			continue
		}
		f = max(f, created)
		observed += t - f
		if o := overlap(down, f, t); o > 0 {
			lost += o
			entry.Status, entry.DownMinutes = "outage", int((o+59_999)/60_000)
		} else if overlap(slow, f, t) > 0 {
			entry.Status = "degraded"
		}
		out = append(out, entry)
	}
	if observed <= 0 {
		return out, nil
	}
	pct := 100 * float64(observed-lost) / float64(observed)
	pct = float64(int(pct*1000)) / 1000 // three decimals, rounded down: never overstate
	return out, &pct
}
