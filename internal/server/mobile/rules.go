package mobile

import (
	"fmt"
	"slices"
	"time"

	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// AlertRules is how one person wants dashboard alerts on their phones: only serious
// ones, only some servers, and quiet hours. On-call pages always come through.
type AlertRules struct {
	MinSeverity string   `json:"minSeverity"` // info | warning | critical
	Servers     []string `json:"servers"`     // only these; empty = every server they can see
	QuietFrom   string   `json:"quietFrom"`   // "22:00"; "" = no quiet hours
	QuietTo     string   `json:"quietTo"`     // "07:00"
	TimeZone    string   `json:"timeZone"`
}

func DefaultRules() AlertRules {
	return AlertRules{MinSeverity: "info", Servers: []string{}, TimeZone: "UTC"}
}

var rank = map[string]int{"info": 0, "ok": 0, "warning": 1, "critical": 2}

func minutes(hhmm string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func (r AlertRules) Validate() error {
	if _, ok := rank[r.MinSeverity]; !ok || r.MinSeverity == "ok" {
		return valid.Error("minimum severity is info, warning or critical")
	}
	if (r.QuietFrom == "") != (r.QuietTo == "") {
		return valid.Error("quiet hours need a start and an end")
	}
	if r.QuietFrom != "" {
		_, ok1 := minutes(r.QuietFrom)
		_, ok2 := minutes(r.QuietTo)
		if !ok1 || !ok2 {
			return valid.Error("quiet hours are times like 22:00")
		}
	}
	if _, err := time.LoadLocation(r.TimeZone); err != nil || r.TimeZone == "" {
		return valid.Error("unknown time zone")
	}
	if len(r.Servers) > 500 {
		return valid.Error("too many servers")
	}
	return nil
}

// Quiet reports whether t falls in the quiet hours.
func (r AlertRules) Quiet(t time.Time) bool {
	from, ok1 := minutes(r.QuietFrom)
	to, ok2 := minutes(r.QuietTo)
	if !ok1 || !ok2 || from == to {
		return false
	}
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	l := t.In(loc)
	now := l.Hour()*60 + l.Minute()
	if from < to {
		return now >= from && now < to
	}
	return now >= from || now < to // across midnight
}

// allows decides whether an alert reaches this person's phones now.
func (r AlertRules) allows(m Message, now time.Time) bool {
	if m.Kind != "resolved" && rank[m.Severity] < rank[r.MinSeverity] {
		return false
	}
	if len(r.Servers) > 0 && m.ServerID != "" && !slices.Contains(r.Servers, m.ServerID) {
		return false
	}
	return !r.Quiet(now)
}

func ruleKey(member string) string {
	if member == "" {
		return "owner"
	}
	return member
}

// Rules is one person's alert rules ("" = the owner).
func (s *Service) Rules(member string) AlertRules {
	all := map[string]AlertRules{}
	settings.Load(s.db, "alertRules", &all)
	r, ok := all[ruleKey(member)]
	if !ok || r.Validate() != nil {
		return DefaultRules()
	}
	if r.Servers == nil {
		r.Servers = []string{}
	}
	return r
}

// SaveRules sets one person's alert rules.
func (s *Service) SaveRules(member string, r AlertRules) (AlertRules, error) {
	if r.Servers == nil {
		r.Servers = []string{}
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	all := map[string]AlertRules{}
	settings.Load(s.db, "alertRules", &all)
	all[ruleKey(member)] = r
	return r, settings.Save(s.db, "alertRules", all)
}

// Allows reports whether an alert may reach someone: a server they can see, and their
// own rules (severity, servers, quiet hours). Their other channels follow it too.
func (s *Service) Allows(member string, m Message) bool {
	if m.ServerID == "" && member != "" {
		return false
	}
	servers, ok := s.Scope(member)
	if !ok || (servers != nil && !slices.Contains(servers, m.ServerID)) {
		return false
	}
	return s.Rules(member).allows(m, time.Now())
}
