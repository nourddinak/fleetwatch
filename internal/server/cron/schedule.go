// Package cron watches scheduled jobs: the crontab lines and systemd timers agents
// find on servers, and heartbeat links that jobs call themselves.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a five-field crontab schedule: the minutes, hours, days, months and
// weekdays it runs on.
type Schedule struct {
	min, hour, dom, month, dow [64]bool
	domAny, dowAny             bool
}

var shortcuts = map[string]string{"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *"}

var names = map[string]string{"jan": "1", "feb": "2", "mar": "3", "apr": "4", "may": "5", "jun": "6", "jul": "7", "aug": "8",
	"sep": "9", "oct": "10", "nov": "11", "dec": "12", "sun": "0", "mon": "1", "tue": "2", "wed": "3", "thu": "4", "fri": "5", "sat": "6"}

// Parse reads "*/5 * * * *", "0 3 * * mon-fri", "@daily" and the like.
func Parse(expr string) (Schedule, error) {
	if s, ok := shortcuts[strings.ToLower(strings.TrimSpace(expr))]; ok {
		expr = s
	}
	f := strings.Fields(strings.ToLower(expr))
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("not a five-field schedule: %q", expr)
	}
	var s Schedule
	for i, spec := range []struct {
		set      *[64]bool
		lo, hi   int
		wildcard *bool
	}{{&s.min, 0, 59, nil}, {&s.hour, 0, 23, nil}, {&s.dom, 1, 31, &s.domAny}, {&s.month, 1, 12, nil}, {&s.dow, 0, 7, &s.dowAny}} {
		if spec.wildcard != nil {
			*spec.wildcard = f[i] == "*" || f[i] == "?"
		}
		if err := field(f[i], spec.lo, spec.hi, spec.set); err != nil {
			return Schedule{}, err
		}
	}
	s.dow[0] = s.dow[0] || s.dow[7] // 7 is Sunday too
	return s, nil
}

func field(v string, lo, hi int, set *[64]bool) error {
	for _, part := range strings.Split(v, ",") {
		rng, step := part, 1
		if r, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return fmt.Errorf("bad step %q", part)
			}
			rng, step = r, n
		}
		from, to := lo, hi
		if rng != "*" && rng != "?" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if from, err = num(a); err != nil {
				return err
			}
			to = from
			if isRange {
				if to, err = num(b); err != nil {
					return err
				}
			} else if step > 1 {
				to = hi // "5/15": from 5 on
			}
		}
		if from < lo || to > hi || from > to {
			return fmt.Errorf("%q is out of range", part)
		}
		for i := from; i <= to; i += step {
			set[i] = true
		}
	}
	return nil
}

func num(v string) (int, error) {
	if n, ok := names[v]; ok {
		v = n
	}
	return strconv.Atoi(v)
}

func (s Schedule) matches(t time.Time) bool {
	if !s.min[t.Minute()] || !s.hour[t.Hour()] || !s.month[int(t.Month())] {
		return false
	}
	dom, dow := s.dom[t.Day()], s.dow[int(t.Weekday())]
	switch {
	case s.domAny && s.dowAny:
		return true
	case s.domAny:
		return dow
	case s.dowAny:
		return dom
	}
	return dom || dow // both restricted: either one (as cron does)
}

// Prev is the last time at or before t the schedule fires; false if none in 400 days.
func (s Schedule) Prev(t time.Time) (time.Time, bool) {
	t = t.Truncate(time.Minute)
	for i := 0; i < 400*24*60; i++ {
		if s.matches(t) {
			return t, true
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}, false
}

// Next is the first time after t the schedule fires.
func (s Schedule) Next(t time.Time) (time.Time, bool) {
	t = t.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 400*24*60; i++ {
		if s.matches(t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}
