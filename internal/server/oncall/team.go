package oncall

import (
	"slices"
	"sort"
	"strings"
	"time"

	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/valid"
)

// recordPage remembers who was paged for what, for the on-call report.
func (s *Service) recordPage(incidentID, person string, level int, ok bool) {
	s.db.Exec(`INSERT INTO oncall_pages (incident_id, person, level, ts, ok) VALUES (?, ?, ?, ?, ?)`, incidentID, person, level, s.Now().UnixMilli(), ok)
}

// shifts finds the shift running at t and the one before it.
func (c Config) shifts(t time.Time) (prev, cur *Shift) {
	if !c.Enabled || len(c.Rotation) == 0 {
		return nil, nil
	}
	ms := t.UnixMilli()
	list := c.Schedule(c.lastHandover(t).AddDate(0, 0, -14), 4)
	for i := range list {
		if list[i].From <= ms && ms < list[i].To {
			cur = &list[i]
			if i > 0 {
				prev = &list[i-1]
			}
			return prev, cur
		}
	}
	return nil, nil
}

// ShiftSummary is what happened during a shift.
type ShiftSummary struct {
	Opened    int                  `json:"opened"`
	Resolved  int                  `json:"resolved"`
	StillOpen int                  `json:"stillOpen"`
	Pages     int                  `json:"pages"`
	Incidents []incidents.Incident `json:"incidents"` // opened during the shift, newest first (10 at most)
}

// Handoff is what the person coming on call needs: the shift before theirs, and the
// note its person left. Draft is the note being written for the next shift.
type Handoff struct {
	Enabled  bool          `json:"enabled"`
	Current  *Shift        `json:"current"`
	Previous *Shift        `json:"previous"`
	Note     *Note         `json:"note"`  // left for the current shift
	Draft    *Note         `json:"draft"` // being written for the next one
	Summary  *ShiftSummary `json:"summary"`
	OpenNow  int           `json:"openNow"`
}

type Note struct {
	Text      string `json:"text"`
	By        string `json:"by"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (s *Service) note(shiftFrom int64) *Note {
	var n Note
	if s.db.QueryRow(`SELECT note, written_by, updated_at FROM oncall_handoffs WHERE shift_from = ?`, shiftFrom).Scan(&n.Text, &n.By, &n.UpdatedAt) != nil {
		return nil
	}
	return &n
}

// Handoff describes the current handover.
func (s *Service) Handoff() Handoff {
	c := s.Config()
	now := s.Now()
	h := Handoff{Enabled: c.Enabled}
	h.Previous, h.Current = c.shifts(now)
	if open, err := s.Incidents.List(incidents.Filter{Status: "open"}); err == nil {
		h.OpenNow = len(open)
	}
	if h.Current == nil {
		return h
	}
	h.Draft = s.note(h.Current.From)
	if h.Previous == nil {
		return h
	}
	h.Note = s.note(h.Previous.From)
	sum := &ShiftSummary{Incidents: []incidents.Incident{}}
	if list, err := s.Incidents.List(incidents.Filter{Status: "all", Limit: 1000}); err == nil {
		for _, x := range list {
			if x.FirstAt >= h.Previous.From && x.FirstAt < h.Previous.To {
				sum.Opened++
				if x.Status == "resolved" {
					sum.Resolved++
				} else {
					sum.StillOpen++
				}
				if len(sum.Incidents) < 10 {
					sum.Incidents = append(sum.Incidents, x)
				}
			}
		}
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM oncall_pages WHERE ts >= ? AND ts < ?`, h.Previous.From, h.Previous.To).Scan(&sum.Pages)
	h.Summary = sum
	return h
}

// SaveNote stores the note for the next shift, written during the current one.
func (s *Service) SaveNote(text, by string) (Note, error) {
	text = strings.TrimSpace(text)
	if len(text) > 4000 {
		return Note{}, valid.Error("a handoff note is 4000 characters at most")
	}
	_, cur := s.Config().shifts(s.Now())
	if cur == nil {
		return Note{}, valid.Error("there is no on-call shift right now")
	}
	n := Note{Text: text, By: by, UpdatedAt: s.Now().UnixMilli()}
	_, err := s.db.Exec(`INSERT INTO oncall_handoffs (shift_from, person, note, written_by, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (shift_from) DO UPDATE SET note = excluded.note, written_by = excluded.written_by, updated_at = excluded.updated_at`,
		cur.From, cur.Person, n.Text, n.By, n.UpdatedAt)
	return n, err
}

// watchHandover tells the person coming on call (once per handover).
func (s *Service) watchHandover() {
	c := s.Config()
	prev, cur := c.shifts(s.Now())
	s.mu.Lock()
	last := s.lastShift
	if cur != nil {
		s.lastShift = cur.From
	}
	s.mu.Unlock()
	if cur == nil || last == 0 || last == cur.From || s.OnHandover == nil {
		return
	}
	people := map[string]Person{}
	for _, p := range s.People() {
		people[p.ID] = p
	}
	var from Person
	var note *Note
	if prev != nil {
		from, note = people[prev.Person], s.note(prev.From)
	}
	if to, ok := people[cur.Person]; ok && (prev == nil || prev.Person != cur.Person) {
		s.OnHandover(from, to, note, s.Handoff().OpenNow)
	}
}

// PersonStats is one person's share of on-call work over a period.
type PersonStats struct {
	Person      string  `json:"person"`
	Name        string  `json:"name"`
	HoursOnCall float64 `json:"hoursOnCall"`
	Pages       int     `json:"pages"`
	Night       int     `json:"night"`     // pages between 22:00 and 07:00 (on-call time zone)
	Escalated   int     `json:"escalated"` // pages that came because someone before did not answer
	Acked       int     `json:"acked"`     // incidents this person acknowledged after being paged
	MedianAckMs int64   `json:"medianAckMs"`
	Missed      int     `json:"missed"` // paged but someone else (or nobody) acknowledged
}

// PageRow is one page sent.
type PageRow struct {
	TS       int64  `json:"ts"`
	Incident string `json:"incidentId"`
	ServerID string `json:"serverId,omitempty"` // for members limited to some servers
	Title    string `json:"title"`
	Person   string `json:"person"`
	Level    int    `json:"level"`
	OK       bool   `json:"ok"`
	AckMs    int64  `json:"ackMs,omitempty"` // this person's time to acknowledge
}

// Report sums up on-call work over the last days: who was on call, paged, how fast they answered.
func (s *Service) Report(days int) ([]PersonStats, []PageRow) {
	if days < 1 || days > 365 {
		days = 30
	}
	c := s.Config()
	now := s.Now()
	from := now.AddDate(0, 0, -days)
	people := map[string]Person{}
	stats := map[string]*PersonStats{}
	get := func(id string) *PersonStats {
		if stats[id] == nil {
			stats[id] = &PersonStats{Person: id, Name: people[id].Name}
			if stats[id].Name == "" {
				stats[id].Name = id
			}
		}
		return stats[id]
	}
	for _, p := range s.People() {
		people[p.ID] = p
	}
	for _, id := range c.Rotation {
		get(id)
	}
	if c.Enabled && len(c.Rotation) > 0 {
		for _, sh := range c.Schedule(c.lastHandover(from), days/7+2) {
			a, b := max(sh.From, from.UnixMilli()), min(sh.To, now.UnixMilli())
			if b > a {
				get(sh.Person).HoursOnCall += float64(b-a) / 3_600_000
			}
		}
	}
	rows, err := s.db.Query(`SELECT incident_id, person, level, ts, ok FROM oncall_pages WHERE ts >= ? ORDER BY ts DESC LIMIT 2000`, from.UnixMilli())
	var pages []PageRow
	if err == nil {
		for rows.Next() {
			var p PageRow
			if rows.Scan(&p.Incident, &p.Person, &p.Level, &p.TS, &p.OK) == nil {
				pages = append(pages, p)
			}
		}
		rows.Close()
	}
	acks := map[string][]int64{}
	firstPage := map[string]int64{} // incident|person → first page
	for _, p := range pages {
		k := p.Incident + "|" + p.Person
		if t, ok := firstPage[k]; !ok || p.TS < t {
			firstPage[k] = p.TS
		}
	}
	counted := map[string]bool{}
	for i := range pages {
		p := &pages[i]
		st := get(p.Person)
		st.Pages++
		if h := time.UnixMilli(p.TS).In(c.loc()).Hour(); h >= 22 || h < 7 {
			st.Night++
		}
		if p.Level > 0 {
			st.Escalated++
		}
		x, err := s.Incidents.Get(p.Incident)
		if err != nil {
			continue
		}
		p.Title, p.ServerID = x.Title, x.ServerID
		k := p.Incident + "|" + p.Person
		if counted[k] {
			continue
		}
		counted[k] = true
		at, by := ackOf(x)
		pp := people[p.Person]
		if at > 0 && by != "" && (strings.EqualFold(by, pp.Name) || strings.EqualFold(by, pp.Username)) {
			st.Acked++
			p.AckMs = at - firstPage[k]
			acks[p.Person] = append(acks[p.Person], p.AckMs)
		} else {
			st.Missed++
		}
	}
	out := []PersonStats{}
	for id, st := range stats {
		if l := acks[id]; len(l) > 0 {
			slices.Sort(l)
			st.MedianAckMs = l[len(l)/2]
		}
		st.HoursOnCall = float64(int(st.HoursOnCall*10)) / 10
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Pages > out[j].Pages || out[i].Pages == out[j].Pages && out[i].Name < out[j].Name
	})
	if pages == nil {
		pages = []PageRow{}
	}
	if len(pages) > 100 {
		pages = pages[:100]
	}
	return out, pages
}

// ackOf finds when an incident was first acknowledged, and by whom.
func ackOf(x incidents.Incident) (int64, string) {
	for _, e := range x.Timeline {
		if who, ok := strings.CutPrefix(e.Text, "Acknowledged by "); ok {
			who = strings.TrimSuffix(strings.TrimSuffix(who, " (phone)"), " (on call)")
			return e.TS, who
		}
	}
	return 0, ""
}
