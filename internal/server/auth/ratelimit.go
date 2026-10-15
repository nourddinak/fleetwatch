package auth

import (
	"sync"
	"time"
)

const (
	failWindow   = 15 * time.Minute
	lockDuration = 15 * time.Minute
)

// limiter locks an IP out after too many failed login/2FA attempts.
// shortcut: in-memory, so a restart clears lockouts; fine for one admin, revisit if multi-node.
type limiter struct {
	mu  sync.Mutex
	ips map[string]*attempts
}

type attempts struct {
	fails       int
	first       time.Time
	lockedUntil time.Time
}

func newLimiter() *limiter { return &limiter{ips: map[string]*attempts{}} }

// retryAfter returns how long ip is still locked out (0 = allowed).
func (l *limiter) retryAfter(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.ips[ip]
	if a == nil || !now.Before(a.lockedUntil) {
		return 0
	}
	return a.lockedUntil.Sub(now)
}

// fail records a failure and reports whether ip is now locked.
func (l *limiter) fail(ip string, max int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.ips[ip]
	if a == nil || now.Sub(a.first) > failWindow {
		a = &attempts{first: now}
		l.ips[ip] = a
	}
	a.fails++
	if a.fails >= max {
		a.lockedUntil = now.Add(lockDuration)
		a.fails, a.first = 0, now
		return true
	}
	return false
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	delete(l.ips, ip)
	l.mu.Unlock()
}

// prune drops stale entries so the map cannot grow without bound.
func (l *limiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, a := range l.ips {
		if now.Sub(a.first) > failWindow && !now.Before(a.lockedUntil) {
			delete(l.ips, ip)
		}
	}
}
