package statuspage

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// ErrNoNotice means the page has no notice with that ID.
var ErrNoNotice = errors.New("notice not found")

const maxNotices = 50

// mutate changes one page under the lock and saves all pages.
func (s *Service) mutate(id string, f func(c *Config) error) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.pages, func(c Config) bool { return c.ID == id })
	if i < 0 {
		return Config{}, ErrNotFound
	}
	c := s.pages[i]
	c.Notices = slices.Clone(c.Notices)
	if err := f(&c); err != nil {
		return c, err
	}
	next := slices.Clone(s.pages)
	next[i] = c
	if err := settings.Save(s.db, settingsKey, next); err != nil {
		return c, err
	}
	s.pages = next
	delete(s.cache, id)
	return c, nil
}

// AddNotice posts a notice; subscribers hear about it unless it is a draft.
func (s *Service) AddNotice(pageID string, n Notice, notify bool) (Notice, error) {
	now := s.Now().UnixMilli()
	n.ID, n.CreatedAt = "ntc_"+newID()[3:], now
	if n.Start == 0 {
		n.Start = now
	}
	if strings.TrimSpace(n.Body) != "" && len(n.Updates) == 0 {
		// The first text opens the timeline.
		stage := "update"
		if n.Status == "outage" || n.Status == "degraded" {
			stage = "investigating"
		}
		n.Updates = []NoticeUpdate{{At: now, Stage: stage, Body: n.Body}}
		n.Body = ""
	}
	if err := validateNotice(&n); err != nil {
		return n, err
	}
	c, err := s.mutate(pageID, func(c *Config) error {
		if len(c.Notices) >= maxNotices {
			return valid.Error(fmt.Sprintf("at most %d notices; delete old ones", maxNotices))
		}
		c.Notices = append([]Notice{n}, c.Notices...)
		return nil
	})
	if err == nil && notify && !n.Draft {
		s.send(event{page: c, notice: &n, kind: "notice.posted"})
	}
	return n, err
}

// EditNotice changes a notice's title, impact, times or draft state. Publishing a
// draft tells the subscribers.
func (s *Service) EditNotice(pageID string, n Notice, notify bool) (Notice, error) {
	var out Notice
	published := false
	c, err := s.mutate(pageID, func(c *Config) error {
		i := slices.IndexFunc(c.Notices, func(x Notice) bool { return x.ID == n.ID })
		if i < 0 {
			return ErrNoNotice
		}
		old := c.Notices[i]
		n.CreatedAt, n.Updates, n.Auto, n.Maint = old.CreatedAt, old.Updates, old.Auto, old.Maint
		if err := validateNotice(&n); err != nil {
			return err
		}
		published = old.Draft && !n.Draft
		c.Notices[i], out = n, n
		return nil
	})
	if err == nil && notify && published {
		s.send(event{page: c, notice: &out, kind: "notice.posted"})
	}
	return out, err
}

// PostUpdate adds a step to a notice's timeline; "resolved" also closes it.
func (s *Service) PostUpdate(pageID, noticeID string, u NoticeUpdate, notify bool) (Notice, error) {
	u.At = s.Now().UnixMilli()
	u.Body = strings.TrimSpace(u.Body)
	if !stages[u.Stage] || u.Body == "" || len(u.Body) > 2000 {
		return Notice{}, valid.Error("an update needs a stage and a text of up to 2000 characters")
	}
	var out Notice
	c, err := s.mutate(pageID, func(c *Config) error {
		i := slices.IndexFunc(c.Notices, func(x Notice) bool { return x.ID == noticeID })
		if i < 0 {
			return ErrNoNotice
		}
		n := c.Notices[i]
		if len(n.Updates) >= 50 {
			return valid.Error("a notice can have at most 50 updates")
		}
		n.Updates = append(slices.Clone(n.Updates), u)
		if u.Stage == "resolved" {
			n.Status = "resolved"
			if n.End == 0 || n.End > u.At {
				n.End = max(u.At, n.Start)
			}
		}
		c.Notices[i], out = n, n
		return nil
	})
	if err == nil && notify && !out.Draft {
		s.send(event{page: c, notice: &out, update: &u, kind: "notice.updated"})
	}
	return out, err
}

func (s *Service) DeleteNotice(pageID, noticeID string) error {
	_, err := s.mutate(pageID, func(c *Config) error {
		i := slices.IndexFunc(c.Notices, func(x Notice) bool { return x.ID == noticeID })
		if i < 0 {
			return ErrNoNotice
		}
		c.Notices = slices.Delete(c.Notices, i, i+1)
		return nil
	})
	return err
}

func openNotice(c Config, match func(Notice) bool) (Notice, bool) {
	for _, n := range c.Notices {
		if n.Status != "resolved" && match(n) {
			return n, true
		}
	}
	return Notice{}, false
}

// componentDown opens the automatic notice for a component that went down.
func (s *Service) componentDown(c Config, key, name string) {
	if c.AutoNotices != "draft" && c.AutoNotices != "publish" {
		return
	}
	if _, ok := openNotice(c, func(n Notice) bool { return n.Auto == key }); ok {
		return
	}
	n, err := s.AddNotice(c.ID, Notice{Title: name + " is down", Status: "outage", Auto: key, Draft: c.AutoNotices == "draft",
		Updates: []NoticeUpdate{{At: s.Now().UnixMilli(), Stage: "investigating", Body: "We are aware that " + name + " is not available and are looking into it."}}}, true)
	if err == nil && n.Draft && s.OnDraft != nil {
		s.OnDraft(c, n)
	}
}

// componentUp closes it again. A draft nobody published is simply removed.
func (s *Service) componentUp(c Config, key, name string) {
	n, ok := openNotice(c, func(n Notice) bool { return n.Auto == key })
	if !ok {
		return
	}
	if n.Draft {
		s.DeleteNotice(c.ID, n.ID)
		return
	}
	s.PostUpdate(c.ID, n.ID, NoticeUpdate{Stage: "resolved", Body: name + " is working normally again."}, true)
}

// MaintenanceChanged posts (until > 0) or closes (until == 0) a maintenance notice on
// every page that shows the app or server, or an app on that server. serverOf tells
// an app's server.
func (s *Service) MaintenanceChanged(kind, id string, until int64, serverOf func(appID string) string) {
	key := kind + ":" + id
	now := s.Now().UnixMilli()
	starting, end := until > now, until
	if until > now+8*day.Milliseconds() {
		end = 0 // "until turned off"
	}
	for _, c := range s.Pages() {
		if c.SkipMaintNotices {
			continue
		}
		var names []string
		for _, g := range c.Groups {
			for _, x := range g.Components {
				if (x.Kind == kind && x.ID == id) || (kind == "server" && x.Kind == "app" && serverOf != nil && serverOf(x.ID) == id) {
					names = append(names, x.Name)
				}
			}
		}
		open, isOpen := openNotice(c, func(n Notice) bool { return n.Maint == key })
		switch {
		case starting && isOpen: // extended or shortened
			open.End = end
			s.EditNotice(c.ID, open, false)
		case starting && len(names) > 0:
			s.AddNotice(c.ID, Notice{Title: "Maintenance: " + strings.Join(names, ", "), Status: "maintenance", End: end, Maint: key,
				Updates: []NoticeUpdate{{At: now, Stage: "update", Body: "Planned maintenance is in progress. " + strings.Join(names, ", ") + " may be unavailable for a while."}}}, true)
		case !starting && isOpen:
			s.PostUpdate(c.ID, open.ID, NoticeUpdate{Stage: "resolved", Body: "Maintenance is complete."}, true)
		}
	}
}
