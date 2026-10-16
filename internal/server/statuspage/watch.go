package statuspage

import (
	"context"
	"log/slog"
	"time"

	"fleetwatch/internal/proto"
)

const (
	watchEvery  = 30 * time.Second
	confirmTick = 2 // a new state must hold this many checks (about a minute) before it counts
)

type seenState struct {
	state, pending string
	count          int
}

// event is something subscribers hear about.
type event struct {
	kind     string // notice.posted | notice.updated | status.changed | test
	page     Config
	notice   *Notice
	update   *NoticeUpdate
	down, up []string // component names (status.changed)
	only     *Subscriber
}

func (s *Service) send(e event) {
	select {
	case s.queue <- e:
	default:
		slog.Warn("status page: subscriber queue full; dropping", "event", e.kind)
	}
}

// Run watches the pages' components for outages (auto-notices and subscriber
// messages), delivers to subscribers, and sends the monthly reports.
func (s *Service) Run(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-s.queue:
				s.deliver(ctx, e)
			}
		}
	}()
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	s.housekeeping(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.tick()
		case <-hourly.C:
			s.housekeeping(ctx)
		}
	}
}

func (s *Service) stateOf(x Component) string {
	switch x.Kind {
	case "app":
		if s.AppState != nil {
			if st, _, _, ok := s.AppState(x.ID); ok {
				return st
			}
		}
	case "server":
		if s.ServerState != nil {
			if st, _, ok := s.ServerState(x.ID); ok {
				return st
			}
		}
	}
	return "unknown"
}

// tick compares every watched component with its last confirmed state. Only the
// watcher goroutine touches s.seen.
func (s *Service) tick() {
	live := map[string]bool{}
	for _, c := range s.Pages() {
		auto := c.AutoNotices == "draft" || c.AutoNotices == "publish"
		if !c.Enabled || (!auto && s.subscriberCount(c.ID) == 0) {
			continue
		}
		live[c.ID] = true
		seen := s.seen[c.ID]
		if seen == nil {
			seen = map[string]*seenState{}
			s.seen[c.ID] = seen
		}
		var down, up []string
		for _, g := range c.Groups {
			for _, x := range g.Components {
				key, state := x.Kind+":"+x.ID, s.stateOf(x)
				prev := seen[key]
				switch {
				case prev == nil: // first look: nothing to compare with
					seen[key] = &seenState{state: state}
					continue
				case state == prev.state:
					prev.pending, prev.count = "", 0
					continue
				case state != prev.pending:
					prev.pending, prev.count = state, 1
					continue
				}
				if prev.count++; prev.count < confirmTick {
					continue
				}
				old := prev.state
				prev.state, prev.pending, prev.count = state, "", 0
				switch {
				case state == "outage" && old != "outage":
					down = append(down, x.Name)
					s.componentDown(c, key, x.Name)
				case old == "outage" && (state == "operational" || state == "degraded"):
					up = append(up, x.Name)
					s.componentUp(c, key, x.Name)
				}
			}
		}
		// With published auto-notices the notices themselves tell the subscribers.
		if len(down)+len(up) > 0 && c.AutoNotices != "publish" {
			s.send(event{kind: "status.changed", page: c, down: down, up: up})
		}
	}
	for id := range s.seen {
		if !live[id] {
			delete(s.seen, id)
		}
	}
}

// housekeeping sends due monthly reports and forgets unconfirmed sign-ups.
func (s *Service) housekeeping(ctx context.Context) {
	s.db.Exec(`DELETE FROM status_subscribers WHERE confirmed = 0 AND created_at < ?`, s.Now().Add(-7*day).UnixMilli())
	now := s.Now()
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Format("2006-01")
	for _, c := range s.Pages() {
		if !c.Report.Monthly || c.Report.To == "" || c.Report.LastSent == last {
			continue
		}
		if err := s.EmailReport(ctx, c.ID, last, c.Report.To); err != nil {
			slog.Warn("status page: monthly report not sent", "page", c.Title, "err", err)
			continue
		}
		s.mutate(c.ID, func(c *Config) error { c.Report.LastSent = last; return nil })
	}
}

// latency averages the last 24 hours of successful checks into half hours.
func latency(results []proto.HealthResult, now time.Time) []LatencyPoint {
	const slot = 30 * time.Minute
	from := now.Add(-24 * time.Hour).Truncate(slot)
	var sum, n [49]int64
	for _, r := range results {
		if !r.OK || r.TS < from.UnixMilli() || r.TS > now.UnixMilli() {
			continue
		}
		i := (r.TS - from.UnixMilli()) / slot.Milliseconds()
		sum[i] += int64(r.LatencyMs)
		n[i]++
	}
	var out []LatencyPoint
	for i := range n {
		if n[i] > 0 {
			out = append(out, LatencyPoint{T: from.Add(time.Duration(i) * slot).UnixMilli(), Ms: int(sum[i] / n[i])})
		}
	}
	return out
}
