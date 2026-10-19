// Package oncall decides who is woken for an incident: a weekly rotation with
// overrides, and escalation to the next person when nobody acknowledges in time.
package oncall

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // time zones even on servers without the tz database

	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

const (
	settingsKey = "oncall"
	week        = 7 * 24 * time.Hour
	Owner       = "owner" // the owner's person ID
)

// Override puts someone else on call for a while (holiday cover, a swap).
type Override struct {
	Person string `json:"person"`
	From   int64  `json:"from"`
	To     int64  `json:"to"`
}

type Config struct {
	Enabled          bool       `json:"enabled"`
	Rotation         []string   `json:"rotation"`     // person IDs in order; each takes a week
	HandoverDay      int        `json:"handoverDay"`  // 0 = Sunday … 6 = Saturday
	HandoverHour     int        `json:"handoverHour"` // 0-23, in TimeZone
	TimeZone         string     `json:"timeZone"`
	EscalateAfterMin int        `json:"escalateAfterMin"`
	Severity         string     `json:"severity"` // critical | warning (warnings and worse)
	Overrides        []Override `json:"overrides"`
	Start            int64      `json:"start"` // the handover when Rotation[0]'s week began
	// FixedOrder turns weekly shifts off: Rotation[0] is always on call, and the others
	// are paged in that order (overrides still apply).
	FixedOrder bool `json:"fixedOrder"`
	// DaysOff are when someone cannot be on call (holidays): the next person in the
	// rotation takes their place, and they are not paged.
	DaysOff []Override `json:"daysOff"`
	// QuietNights: warnings opened between QuietFrom and QuietTo (hours, TimeZone) wait
	// until QuietTo to page; critical incidents still wake people.
	QuietNights bool `json:"quietNights"`
	QuietFrom   int  `json:"quietFrom"`
	QuietTo     int  `json:"quietTo"`
}

func Defaults() Config {
	return Config{HandoverDay: 1, HandoverHour: 9, TimeZone: "UTC", EscalateAfterMin: 10, Severity: "critical", Rotation: []string{}, Overrides: []Override{}, DaysOff: []Override{}, QuietFrom: 22, QuietTo: 8}
}

// Person is someone who can be on call.
type Person struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Email    string `json:"email"`
	Devices  int    `json:"devices"` // phones paired with the app
}

// Shift is one stretch of on-call duty.
type Shift struct {
	Person   string `json:"person"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Override bool   `json:"override"`
}

type step struct {
	incident string
	chain    []string
	next     int       // index in chain to page next
	at       time.Time // when to page next
}

type Service struct {
	db *sql.DB
	// People are everyone who can be on call (the owner and active members).
	People func() []Person
	// Page reaches one person (push to their phones and email); ackURL acknowledges.
	Page      func(ctx context.Context, p Person, x incidents.Incident, ackURL string, level int) error
	Incidents *incidents.Store
	// Muted is true while maintenance pauses alerts for that server or app.
	Muted     func(serverID, appID string) bool
	PublicURL func() string
	Now       func() time.Time
	// OnHandover tells the person coming on call (with the note the last one left).
	OnHandover func(from, to Person, note *Note, openIncidents int)
	// OnCoverAsked tells the team about a cover request; OnCoverTaken, who took it.
	OnCoverAsked, OnCoverTaken func(CoverRequest)

	key       []byte
	mu        sync.Mutex
	queue     map[string]*step
	lastShift int64 // start of the shift seen last, to notice handovers
}

func New(d *sql.DB, key []byte) *Service {
	if key == nil {
		key = loadKey(d)
	}
	return &Service{db: d, key: key, Now: time.Now, queue: map[string]*step{}}
}

// loadKey is the secret that signs acknowledge links, made once.
func loadKey(d *sql.DB) []byte {
	var h string
	settings.Load(d, "oncallKey", &h)
	if k, err := hex.DecodeString(h); err == nil && len(k) == 32 {
		return k
	}
	k := make([]byte, 32)
	rand.Read(k)
	settings.Save(d, "oncallKey", hex.EncodeToString(k))
	return k
}

func (s *Service) Config() Config {
	c := Defaults()
	settings.Load(s.db, settingsKey, &c)
	return c
}

func (c Config) loc() *time.Location {
	if l, err := time.LoadLocation(c.TimeZone); err == nil {
		return l
	}
	return time.UTC
}

// lastHandover is the most recent handover at or before t.
func (c Config) lastHandover(t time.Time) time.Time {
	l := t.In(c.loc())
	h := time.Date(l.Year(), l.Month(), l.Day(), c.HandoverHour, 0, 0, 0, l.Location())
	h = h.AddDate(0, 0, -((int(h.Weekday()) - c.HandoverDay + 7) % 7))
	if h.After(l) {
		h = h.AddDate(0, 0, -7)
	}
	return h
}

func (s *Service) Save(c Config) (Config, error) {
	if _, err := time.LoadLocation(c.TimeZone); err != nil || c.TimeZone == "" {
		return c, valid.Error("unknown time zone (use a name like Europe/Paris or UTC)")
	}
	if c.HandoverDay < 0 || c.HandoverDay > 6 || c.HandoverHour < 0 || c.HandoverHour > 23 {
		return c, valid.Error("handover must be a weekday and an hour")
	}
	if c.EscalateAfterMin < 1 || c.EscalateAfterMin > 240 {
		return c, valid.Error("escalate after 1 to 240 minutes")
	}
	if c.Severity != "critical" && c.Severity != "warning" {
		return c, valid.Error("page for critical incidents, or for warnings too")
	}
	known := map[string]bool{}
	for _, p := range s.People() {
		known[p.ID] = true
	}
	if len(c.Rotation) > 50 || len(c.Overrides) > 100 {
		return c, valid.Error("too many people or overrides")
	}
	for i, id := range c.Rotation {
		if !known[id] || slices.Index(c.Rotation, id) != i {
			return c, valid.Error("the rotation has an unknown or repeated person")
		}
	}
	if c.Enabled && len(c.Rotation) == 0 {
		return c, valid.Error("add at least one person to the rotation")
	}
	now := s.Now().UnixMilli()
	c.Overrides = slices.DeleteFunc(c.Overrides, func(o Override) bool { return o.To < now-week.Milliseconds() }) // forget old ones
	for _, o := range c.Overrides {
		if !known[o.Person] || o.From <= 0 || o.To <= o.From {
			return c, valid.Error("each override needs a person and a time range")
		}
	}
	if c.DaysOff == nil {
		c.DaysOff = []Override{}
	}
	c.DaysOff = slices.DeleteFunc(c.DaysOff, func(o Override) bool { return o.To < now-week.Milliseconds() })
	if len(c.DaysOff) > 100 {
		return c, valid.Error("too many days off")
	}
	for _, o := range c.DaysOff {
		if !known[o.Person] || o.From <= 0 || o.To <= o.From {
			return c, valid.Error("each day off needs a person and a time range")
		}
	}
	if c.QuietFrom < 0 || c.QuietFrom > 23 || c.QuietTo < 0 || c.QuietTo > 23 || (c.QuietNights && c.QuietFrom == c.QuietTo) {
		return c, valid.Error("quiet nights need a start and an end hour")
	}
	old := s.Config()
	// A new rotation starts with its first person in the current week.
	if !slices.Equal(old.Rotation, c.Rotation) || old.HandoverDay != c.HandoverDay || old.HandoverHour != c.HandoverHour || old.TimeZone != c.TimeZone || old.Start == 0 {
		c.Start = c.lastHandover(s.Now()).UnixMilli()
	} else {
		c.Start = old.Start
	}
	return c, settings.Save(s.db, settingsKey, c)
}

// rotationAt is who the rotation (without overrides) puts on call at t.
func (c Config) rotationAt(t time.Time) (string, time.Time, time.Time) {
	if len(c.Rotation) == 0 {
		return "", time.Time{}, time.Time{}
	}
	from := c.lastHandover(t)
	if c.FixedOrder {
		return c.Rotation[0], from, from.AddDate(0, 0, 7)
	}
	weeks := int(from.Sub(time.UnixMilli(c.Start).In(c.loc())).Round(time.Hour) / week)
	i := ((weeks % len(c.Rotation)) + len(c.Rotation)) % len(c.Rotation)
	return c.Rotation[i], from, from.AddDate(0, 0, 7)
}

// OnCall is who is on call at t ("" = nobody).
func (c Config) OnCall(t time.Time) string {
	ms := t.UnixMilli()
	for i := len(c.Overrides) - 1; i >= 0; i-- { // the newest override wins
		if o := c.Overrides[i]; o.From <= ms && ms < o.To {
			return o.Person
		}
	}
	p, _, _ := c.rotationAt(t)
	if !c.off(p, ms) {
		return p
	}
	i := slices.Index(c.Rotation, p)
	for k := 1; k < len(c.Rotation); k++ {
		if q := c.Rotation[(i+k)%len(c.Rotation)]; !c.off(q, ms) {
			return q
		}
	}
	return p // everyone is off: the rotation's person still
}

// off reports whether someone has a day off at ms.
func (c Config) off(person string, ms int64) bool {
	for _, o := range c.DaysOff {
		if o.Person == person && o.From <= ms && ms < o.To {
			return true
		}
	}
	return false
}

// QuietUntil is when a quiet night running at t ends (false when none is).
func (c Config) QuietUntil(t time.Time) (time.Time, bool) {
	if !c.QuietNights || c.QuietFrom == c.QuietTo {
		return time.Time{}, false
	}
	l := t.In(c.loc())
	h := l.Hour()
	in := (c.QuietFrom < c.QuietTo && h >= c.QuietFrom && h < c.QuietTo) || (c.QuietFrom > c.QuietTo && (h >= c.QuietFrom || h < c.QuietTo))
	if !in {
		return time.Time{}, false
	}
	end := time.Date(l.Year(), l.Month(), l.Day(), c.QuietTo, 0, 0, 0, l.Location())
	if !end.After(l) {
		end = end.AddDate(0, 0, 1)
	}
	return end, true
}

// Schedule lists the shifts of the coming weeks, overrides and days off cut in.
func (c Config) Schedule(from time.Time, weeks int) []Shift {
	out := []Shift{}
	t := from
	end := from.AddDate(0, 0, 7*weeks)
	for t.Before(end) && len(out) < 200 {
		ms := t.UnixMilli()
		p := c.OnCall(t)
		rot, _, to := c.rotationAt(t)
		if p == "" {
			break
		}
		next := to.UnixMilli()
		for _, list := range [][]Override{c.Overrides, c.DaysOff} { // the next change cuts this shift
			for _, o := range list {
				for _, edge := range []int64{o.From, o.To} {
					if edge > ms && edge < next {
						next = edge
					}
				}
			}
		}
		shift := Shift{Person: p, From: ms, To: next, Override: p != rot}
		if n := len(out); n > 0 && out[n-1].Person == shift.Person && out[n-1].Override == shift.Override && out[n-1].To == shift.From {
			out[n-1].To = shift.To
		} else {
			out = append(out, shift)
		}
		t = time.UnixMilli(next)
	}
	return out
}

// chain is who gets paged, in order: whoever is on call, then the rest of the
// rotation, then the owner.
func (c Config) chain(t time.Time) []string {
	first := c.OnCall(t)
	var out []string
	if first != "" {
		out = append(out, first)
	}
	if i := slices.Index(c.Rotation, first); i >= 0 {
		for k := 1; k < len(c.Rotation); k++ {
			out = append(out, c.Rotation[(i+k)%len(c.Rotation)])
		}
	} else {
		out = append(out, c.Rotation...)
	}
	ms := t.UnixMilli()
	out = slices.DeleteFunc(out, func(id string) bool { return id != first && c.off(id, ms) })
	out = append(out, Owner)
	return slices.Compact(dedupe(out))
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range list {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// OnEvent starts paging for a new incident that is serious enough.
func (s *Service) OnEvent(ev incidents.Event) {
	c := s.Config()
	x := ev.Incident
	if !c.Enabled || ev.Kind != "opened" || (c.Severity == "critical" && x.Severity != "critical") || x.Severity == "info" {
		return
	}
	app := ""
	if x.AppID != nil {
		app = *x.AppID
	}
	if s.Muted != nil && s.Muted(x.ServerID, app) {
		return
	}
	at, chain := s.Now(), c.chain(s.Now())
	if until, quiet := c.QuietUntil(at); quiet && x.Severity != "critical" {
		at, chain = until, nil // whoever is on call in the morning
	}
	s.mu.Lock()
	if _, ok := s.queue[x.ID]; !ok {
		s.queue[x.ID] = &step{incident: x.ID, chain: chain, at: at}
	}
	s.mu.Unlock()
	go s.tick(context.Background())
}

// Run pages the next person in line when an incident stays unacknowledged.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
			s.watchHandover()
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	c := s.Config()
	now := s.Now()
	type job struct {
		p     Person
		x     incidents.Incident
		level int
	}
	var jobs []job
	people := map[string]Person{}
	for _, p := range s.People() {
		people[p.ID] = p
	}
	s.mu.Lock()
	for id, st := range s.queue {
		x, err := s.Incidents.Get(id)
		if err != nil || x.Status != "active" { // acknowledged, muted or resolved: stop
			delete(s.queue, id)
			continue
		}
		if now.Before(st.at) {
			continue
		}
		if st.chain == nil {
			st.chain = c.chain(now)
		}
		if st.next >= len(st.chain) {
			delete(s.queue, id) // everyone was paged
			continue
		}
		if p, ok := people[st.chain[st.next]]; ok {
			jobs = append(jobs, job{p, x, st.next})
		}
		st.next++
		st.at = now.Add(time.Duration(c.EscalateAfterMin) * time.Minute)
	}
	s.mu.Unlock()
	for _, j := range jobs {
		err := s.Page(ctx, j.p, j.x, s.AckURL(j.x.ID, j.p.ID), j.level)
		s.recordPage(j.x.ID, j.p.ID, j.level, err == nil)
		if err != nil {
			slog.Warn("on-call page failed", "person", j.p.Name, "err", err)
		}
	}
}

// AckURL is the link in a page that acknowledges without signing in.
func (s *Service) AckURL(incidentID, person string) string {
	base := ""
	if s.PublicURL != nil {
		base = strings.TrimRight(s.PublicURL(), "/")
	}
	return fmt.Sprintf("%s/api/public/ack?i=%s&p=%s&s=%s", base, incidentID, person, s.sign(incidentID, person))
}

func (s *Service) sign(incidentID, person string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("ack\x00" + incidentID + "\x00" + person))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// Ack acknowledges an incident from a page's link; it returns who acknowledged.
func (s *Service) Ack(incidentID, person, sig string) (incidents.Incident, string, error) {
	if !hmac.Equal([]byte(sig), []byte(s.sign(incidentID, person))) {
		return incidents.Incident{}, "", valid.Error("this link is not valid")
	}
	name := person
	for _, p := range s.People() {
		if p.ID == person {
			name = p.Name
		}
	}
	x, err := s.Incidents.Get(incidentID)
	if err != nil {
		return x, name, err
	}
	if x.Status != "active" {
		return x, name, nil // someone was quicker; nothing to do
	}
	x, err = s.Incidents.Act(incidentID, "acknowledge", "", name+" (on call)")
	return x, name, err
}
