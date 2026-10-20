// Package statuspage builds the public status page: an overall state, notices written
// by the admin, and chosen apps and servers with their current status and daily
// uptime history (derived from recorded incidents). Visitors only ever see the names
// and texts configured here, never internal hostnames, IPs or incident details.
package statuspage

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

const (
	settingsKey = "statusPages"
	legacyKey   = "statusPage" // v0.9.22-23: a single page
	maxPages    = 50
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

// Notice is an announcement ("Scheduled maintenance tonight"), written by the admin or
// created automatically for an outage or a maintenance window.
type Notice struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Status    string         `json:"status"` // impact: info | maintenance | degraded | outage | resolved
	Start     int64          `json:"start"`  // unix ms
	End       int64          `json:"end"`    // unix ms, 0 = until removed or resolved
	CreatedAt int64          `json:"createdAt"`
	Updates   []NoticeUpdate `json:"updates"`         // timeline, oldest first
	Draft     bool           `json:"draft"`           // not public until published
	Auto      string         `json:"auto,omitempty"`  // "app:<id>": opened automatically for that component's outage
	Maint     string         `json:"maint,omitempty"` // "app:<id>" / "server:<id>": opened by the maintenance switch
}

// NoticeUpdate is one step in a notice's timeline.
type NoticeUpdate struct {
	At    int64  `json:"at"`
	Stage string `json:"stage"` // investigating | identified | monitoring | resolved | update
	Body  string `json:"body"`
}

// ReportConfig sends the monthly uptime report by email.
type ReportConfig struct {
	Monthly  bool   `json:"monthly"`
	To       string `json:"to"`       // comma-separated addresses
	LastSent string `json:"lastSent"` // YYYY-MM of the last report sent
}

// Config is everything the admin can customize.
type Config struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"` // address /status/<slug>; "" = the main page at /status
	Enabled     bool     `json:"enabled"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Logo        string   `json:"logo"`   // https:// link, or an uploaded image as a data: URL (png, jpeg, webp, svg)
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
	Notices     []Notice `json:"notices"` // changed through the notice calls, not by saving the page
	UpdatedAt   int64    `json:"updatedAt"`

	ShowResponseTime bool         `json:"showResponseTime"` // 24-hour response time of apps with a health check
	Subscribe        bool         `json:"subscribe"`        // visitors may subscribe by email
	SubscribeHooks   bool         `json:"subscribeHooks"`   // ... and by webhook
	AutoNotices      string       `json:"autoNotices"`      // off | draft | publish: a notice when a component goes down
	SkipMaintNotices bool         `json:"skipMaintNotices"` // no notice when the maintenance switch is turned on
	Access           string       `json:"access"`           // public | password | team (signed-in dashboard users)
	PasswordHash     string       `json:"passwordHash,omitempty"`
	Password         string       `json:"password,omitempty"` // write-only: sets a new password
	HasPassword      bool         `json:"hasPassword"`        // read-only
	CustomCSS        string       `json:"customCss"`
	HeaderHTML       string       `json:"headerHtml"`
	Report           ReportConfig `json:"report"`
	Sender           string       `json:"sender"` // its emails' sender (Integrations → Email); "" = the default
}

// Redacted is the page as the editor gets it: without the password hash.
func (c Config) Redacted() Config {
	c.HasPassword, c.PasswordHash, c.Password = c.PasswordHash != "", "", ""
	return c
}

func Defaults() Config {
	return Config{Title: "Service status", Accent: "#16a34a", Theme: "auto", PoweredBy: true, HistoryDays: 90, ShowUptime: true,
		Groups: []Group{}, Notices: []Notice{}, AutoNotices: "off", Access: "public"}
}

var (
	accentRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	slugRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	hostRe   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	logoRe   = regexp.MustCompile(`^data:image/(png|jpeg|webp|svg\+xml);base64,([A-Za-z0-9+/=]+)$`)
	notices  = map[string]bool{"info": true, "maintenance": true, "degraded": true, "outage": true, "resolved": true}
	stages   = map[string]bool{"investigating": true, "identified": true, "monitoring": true, "resolved": true, "update": true}
)

// validateNotice normalizes one notice.
func validateNotice(n *Notice) error {
	n.Title = strings.TrimSpace(n.Title)
	if n.Title == "" || len(n.Title) > 120 || len(n.Body) > 2000 || !notices[n.Status] || n.Start <= 0 || (n.End != 0 && n.End < n.Start) {
		return valid.Error("each notice needs a title (up to 120 characters), a status and a valid time range")
	}
	if len(n.Updates) > 50 {
		return valid.Error("a notice can have at most 50 updates")
	}
	for _, u := range n.Updates {
		if !stages[u.Stage] || len(u.Body) > 2000 || u.At <= 0 {
			return valid.Error("each update needs a stage and at most 2000 characters")
		}
	}
	if n.Updates == nil {
		n.Updates = []NoticeUpdate{}
	}
	return nil
}

func text(name, v string, max int) error {
	if len(v) > max {
		return valid.Error(fmt.Sprintf("%s must be at most %d characters", name, max))
	}
	return nil
}

// Validate normalizes c and rejects anything unsafe to publish.
func (c *Config) Validate() error {
	c.Title, c.Domain = strings.TrimSpace(c.Title), strings.ToLower(strings.TrimSpace(c.Domain))
	c.Slug = strings.ToLower(strings.TrimSpace(c.Slug))
	// A pasted address works too: https://status.example.com/ is status.example.com.
	c.Domain = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(c.Domain, "https://"), "http://"), "/")
	if c.Slug != "" && !slugRe.MatchString(c.Slug) {
		return valid.Error("page address may use a-z, 0-9 and dashes (up to 40)")
	}
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
	if strings.HasPrefix(c.Logo, "https://") { // a link: visitors' browsers load it from there
		if u, err := url.Parse(c.Logo); err != nil || u.Host == "" || len(c.Logo) > 1000 || strings.ContainsAny(c.Logo, " \"'<>") {
			return valid.Error("logo link must be a valid https:// address")
		}
	} else if c.Logo != "" {
		m := logoRe.FindStringSubmatch(c.Logo)
		if m == nil {
			return valid.Error("logo must be an https:// link or a PNG, JPEG, WebP or SVG image")
		}
		if base64.StdEncoding.DecodedLen(len(m[2])) > maxLogo {
			return valid.Error("logo must be at most 256 KB")
		}
	}
	if c.Domain != "" && !hostRe.MatchString(c.Domain) {
		return valid.Error("the domain may only use letters, numbers, dashes and dots, like status.example.com (no underscores or spaces)")
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
		if err := validateNotice(n); err != nil {
			return err
		}
		if n.ID == "" {
			n.ID = fmt.Sprintf("ntc_%d_%d", n.CreatedAt, i)
		}
	}
	switch c.AutoNotices {
	case "":
		c.AutoNotices = "off"
	case "off", "draft", "publish":
	default:
		return valid.Error("automatic notices must be off, draft or publish")
	}
	switch c.Access {
	case "":
		c.Access = "public"
	case "public", "password", "team":
	default:
		return valid.Error("access must be public, password or team")
	}
	if c.Password != "" && (len(c.Password) < 6 || len(c.Password) > 128) {
		return valid.Error("the page password must be 6 to 128 characters")
	}
	for _, e := range []error{text("custom CSS", c.CustomCSS, 20000), text("header HTML", c.HeaderHTML, 10000)} {
		if e != nil {
			return e
		}
	}
	if c.Report.To = strings.TrimSpace(c.Report.To); c.Report.To != "" {
		if _, err := parseAddresses(c.Report.To); err != nil {
			return err
		}
	}
	if c.Report.Monthly && c.Report.To == "" {
		return valid.Error("enter who gets the monthly report")
	}
	if c.Groups == nil {
		c.Groups = []Group{}
	}
	if c.Notices == nil {
		c.Notices = []Notice{}
	}
	return nil
}

// ErrNotFound means there is no status page with that ID.
var ErrNotFound = errors.New("status page not found")

// Service keeps the status pages (in memory, saved in settings) and builds their
// public views.
type Service struct {
	db        *sql.DB
	Incidents *incidents.Store
	// AppState and ServerState return a component's current state (operational,
	// degraded, outage, maintenance, unknown), its server, and when it was created.
	AppState    func(id string) (state, serverID string, created int64, ok bool)
	ServerState func(id string) (state string, created int64, ok bool)
	Now         func() time.Time
	// DataDir and HelperPath enable the automatic Caddy setup for page domains (see
	// caddy.go); empty DataDir turns it off.
	DataDir, HelperPath string
	// Latency returns an app's recent health check results, newest first.
	Latency func(appID string) []proto.HealthResult
	// Mail sends subscriber emails and reports; OnDraft tells the admin a draft
	// notice is waiting (dashboard notification channels).
	Mail    *Mailer
	OnDraft func(page Config, n Notice)

	mu    sync.Mutex
	pages []Config
	cache map[string]cached
	key   []byte // signs the cookies of unlocked password pages
	seen  map[string]map[string]*seenState
	queue chan event
}

type cached struct {
	p  *Public
	at time.Time
}

func New(d *sql.DB, inc *incidents.Store) *Service {
	s := &Service{db: d, Incidents: inc, Now: time.Now, cache: map[string]cached{}, seen: map[string]map[string]*seenState{}, queue: make(chan event, 256)}
	s.key = loadKey(d)
	var pages *[]Config
	settings.Load(d, settingsKey, &pages)
	if pages == nil {
		pages = &[]Config{}
		var old *Config
		settings.Load(d, legacyKey, &old)
		if old != nil { // the single page of earlier versions becomes the main page
			c := Defaults()
			settings.Load(d, legacyKey, &c)
			c.ID, c.Slug = "sp_main", ""
			*pages = append(*pages, c)
			settings.Save(d, settingsKey, *pages)
		}
	}
	s.pages = *pages
	for i := range s.pages { // pages saved by older versions lack the newer settings
		c := &s.pages[i]
		if c.Access == "" {
			c.Access = "public"
		}
		if c.AutoNotices == "" {
			c.AutoNotices = "off"
		}
		for j := range c.Notices {
			if c.Notices[j].Updates == nil {
				c.Notices[j].Updates = []NoticeUpdate{}
			}
		}
	}
	return s
}

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "sp_" + hex.EncodeToString(b)
}

// Pages returns every page, in creation order.
func (s *Service) Pages() []Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Config(nil), s.pages...)
}

func (s *Service) Page(id string) (Config, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.pages {
		if c.ID == id {
			return c, true
		}
	}
	return Config{}, false
}

// Save creates (empty ID) or replaces a page. Addresses and domains must be unique.
func (s *Service) Save(c Config) (Config, error) {
	if err := c.Validate(); err != nil {
		return c, err
	}
	if c.Sender != "" && s.Mail != nil && !slices.ContainsFunc(s.Mail.Senders(), func(x Sender) bool { return x.ID == c.Sender }) {
		return c, valid.Error("that email sender is gone: pick another in Subscribers")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at := -1
	for i, x := range s.pages {
		switch {
		case x.ID == c.ID:
			at = i
		case x.Slug == c.Slug && c.Slug == "":
			return c, valid.Error(fmt.Sprintf("%q is already the main page (at /status); give this one its own address", x.Title))
		case x.Slug == c.Slug:
			return c, valid.Error(fmt.Sprintf("the address /status/%s is used by %q", c.Slug, x.Title))
		case c.Domain != "" && x.Domain == c.Domain:
			return c, valid.Error(fmt.Sprintf("%s is already the domain of %q", c.Domain, x.Title))
		}
	}
	if c.ID == "" {
		if len(s.pages) >= maxPages {
			return c, valid.Error(fmt.Sprintf("at most %d status pages", maxPages))
		}
		c.ID = newID()
	} else if at < 0 {
		return c, ErrNotFound
	}
	if at >= 0 {
		old := s.pages[at]
		c.Notices, c.Report.LastSent = old.Notices, old.Report.LastSent // notices change through their own calls
		c.PasswordHash = old.PasswordHash
	} else {
		c.PasswordHash = ""
	}
	if c.Password != "" {
		h, err := auth.HashPassword(c.Password)
		if err != nil {
			return c, err
		}
		c.PasswordHash = h
	}
	c.Password, c.HasPassword = "", false
	if c.Access == "password" && c.PasswordHash == "" {
		return c, valid.Error("set a password for this page")
	}
	c.UpdatedAt = s.Now().UnixMilli()
	before := s.domains()
	next := append([]Config(nil), s.pages...)
	if at < 0 {
		next = append(next, c)
	} else {
		next[at] = c
	}
	if err := settings.Save(s.db, settingsKey, next); err != nil {
		return c, err
	}
	s.pages = next
	delete(s.cache, c.ID) // show changes right away
	if !slices.Equal(before, s.domains()) {
		go s.SyncWebServer()
	}
	return c, nil
}

func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]Config, 0, len(s.pages))
	for _, c := range s.pages {
		if c.ID != id {
			next = append(next, c)
		}
	}
	if len(next) == len(s.pages) {
		return ErrNotFound
	}
	before := s.domains()
	if err := settings.Save(s.db, settingsKey, next); err != nil {
		return err
	}
	s.pages = next
	delete(s.cache, id)
	if !slices.Equal(before, s.domains()) {
		go s.SyncWebServer()
	}
	return nil
}

// ForHost is the page that owns host as its own domain.
func (s *Service) ForHost(host string) (Config, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.pages {
		if host != "" && c.Domain == host {
			return c, true
		}
	}
	return Config{}, false
}

// Find is the enabled page a visitor asked for: the one owning host if any, else the
// one at /status/<slug> ("" = the main page).
func (s *Service) Find(host, slug string) (Config, bool) {
	c, ok := s.ForHost(host)
	if ok {
		if slug != "" && slug != c.Slug {
			return Config{}, false // a page's own domain shows only that page
		}
	} else {
		s.mu.Lock()
		for _, x := range s.pages {
			if x.Slug == slug {
				c, ok = x, true
				break
			}
		}
		s.mu.Unlock()
	}
	return c, ok && c.Enabled
}

// ---------- public view ----------

type Day struct {
	Date        string `json:"date"`   // YYYY-MM-DD (UTC)
	Status      string `json:"status"` // operational | degraded | outage | nodata
	DownMinutes int    `json:"downMinutes,omitempty"`
}

type PublicComponent struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Status      string         `json:"status"`
	Uptime      *float64       `json:"uptime,omitempty"` // percent over the history window
	Days        []Day          `json:"days,omitempty"`
	Latency     []LatencyPoint `json:"latency,omitempty"` // last 24 hours
}

type PublicGroup struct {
	Name       string            `json:"name"`
	Components []PublicComponent `json:"components"`
}

type PublicNotice struct {
	Title   string         `json:"title"`
	Body    string         `json:"body"`
	Status  string         `json:"status"`
	Start   int64          `json:"start"`
	End     int64          `json:"end,omitempty"`
	Updates []NoticeUpdate `json:"updates"` // newest first
}

// LatencyPoint is the average response time of one half hour.
type LatencyPoint struct {
	T  int64 `json:"t"`
	Ms int   `json:"ms"`
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
	Subscribe   []string       `json:"subscribe"` // ways to subscribe: email, webhook
	CustomCSS   string         `json:"customCss,omitempty"`
	HeaderHTML  string         `json:"headerHtml,omitempty"`
}

// Public returns a page's public data, rebuilt at most every 30 seconds.
func (s *Service) Public(c Config) *Public {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x, ok := s.cache[c.ID]; ok && s.Now().Sub(x.at) < cacheFor {
		return x.p
	}
	p := s.build(c)
	s.cache[c.ID] = cached{p, s.Now()}
	return p
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
		Groups: []PublicGroup{}, UpdatedAt: now.UnixMilli(), Subscribe: []string{}, CustomCSS: c.CustomCSS, HeaderHTML: c.HeaderHTML}
	if c.Subscribe && s.Mail != nil && s.Mail.ReadyFor(c.Sender) {
		p.Subscribe = append(p.Subscribe, "email")
	}
	if c.SubscribeHooks {
		p.Subscribe = append(p.Subscribe, "webhook")
	}
	ms := now.UnixMilli()
	noticeDown, noticeSlow, noticeMaint := false, false, false
	for _, n := range c.Notices {
		if n.Draft {
			continue
		}
		active := n.Start <= ms && (n.End == 0 || n.End > ms) && n.Status != "resolved"
		recent := (n.Status == "resolved" || (n.End != 0 && n.End <= ms)) && ms-max(n.End, n.CreatedAt) < day.Milliseconds()
		upcoming := n.Status == "maintenance" && n.Start > ms && n.Start-ms < 7*day.Milliseconds()
		if !active && !recent && !upcoming {
			continue
		}
		pn := PublicNotice{Title: n.Title, Body: n.Body, Status: n.Status, Start: n.Start, End: n.End, Updates: []NoticeUpdate{}}
		for i := len(n.Updates) - 1; i >= 0; i-- {
			pn.Updates = append(pn.Updates, n.Updates[i])
		}
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
	pc := PublicComponent{Name: x.Name, Description: x.Description, Status: s.stateOf(x)}
	if pc.Status == "unknown" {
		return pc // deleted since it was added to the page
	}
	if c.ShowResponseTime && x.Kind == "app" && s.Latency != nil {
		pc.Latency = latency(s.Latency(x.ID), now)
	}
	if c.ShowUptime {
		related, created := s.related(x)
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
