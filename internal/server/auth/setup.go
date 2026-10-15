package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"

	"fleetwatch/internal/server/audit"
)

// First-run setup: with no admin in the database and none configured through the
// environment, the server prints a one-time code and the web UI creates the admin.
// The code stops a stranger who reaches a fresh install first from claiming it.

var (
	ErrSetupDone    = errors.New("setup is already complete")
	ErrBadSetupCode = errors.New("wrong setup code")
	usernameRe      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// NeedsSetup reports whether no admin exists yet.
func (s *Service) NeedsSetup() bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM admin`).Scan(&n) == nil && n == 0
}

// StartSetup generates the one-time setup code, formatted XXXX-XXXX-XXXX-XXXX.
func (s *Service) StartSetup() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	raw := base32.StdEncoding.EncodeToString(b) // 16 chars, no padding for 10 bytes
	code := raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16]
	s.setupMu.Lock()
	s.setupCode = code
	s.setupMu.Unlock()
	return code
}

// Setup creates the admin and returns a signed-in session token.
func (s *Service) Setup(c Client, code, username, password string) (string, error) {
	if err := s.guard(c); err != nil {
		return "", err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.setupCode == "" || !s.NeedsSetup() {
		return "", ErrSetupDone
	}
	norm := func(v string) string { return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(v), "-", "")) }
	if subtle.ConstantTimeCompare([]byte(norm(code)), []byte(norm(s.setupCode))) != 1 {
		s.failed(c, "setup", "wrong setup code")
		return "", ErrBadSetupCode
	}
	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return "", InputError("username must be 1-64 letters, digits, dots, dashes or underscores")
	}
	if err := ValidateNewPassword(password); err != nil {
		return "", err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`INSERT INTO admin (id, username, display_name, password_hash, password_changed_at) VALUES (1, ?, ?, ?, ?)`,
		username, username, hash, s.Now().UnixMilli()); err != nil {
		return "", err
	}
	s.setupCode = ""
	s.lim.reset(c.IP)
	audit.Record(s.db, audit.Event{Action: "setup", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: username, Detail: "admin created in the setup wizard"})
	token, _, err := s.createSession(c, false)
	return token, err
}
