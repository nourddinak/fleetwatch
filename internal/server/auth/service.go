// Package auth implements single-admin authentication: argon2id passwords,
// server-side sessions, TOTP 2FA with recovery codes, and login rate limiting.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

const (
	SessionMaxAge   = 7 * 24 * time.Hour // absolute lifetime of a signed-in session
	pendingMaxAge   = 5 * time.Minute    // time allowed between password and TOTP steps
	touchEvery      = time.Minute        // last_seen_at write throttle
	recoveryCodeCnt = 10
	totpAD          = "admin.totp"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidCode        = errors.New("invalid code")
	ErrNoSession          = errors.New("not signed in")
	ErrWrongPassword      = errors.New("current password is incorrect")
	ErrTwoFAEnabled       = errors.New("two-factor authentication is already enabled")
	ErrTwoFADisabled      = errors.New("two-factor authentication is not enabled")
	ErrNoPendingSetup     = errors.New("start 2FA setup first")
)

// LockedError means the client IP is temporarily blocked after repeated failures.
type LockedError struct{ RetryAfter time.Duration }

func (e LockedError) Error() string {
	return fmt.Sprintf("too many failed attempts, try again in %d minutes", int(e.RetryAfter.Minutes())+1)
}

// Security is the "security" settings section.
type Security struct {
	SessionTimeoutMin int  `json:"sessionTimeoutMin"`
	LockoutAfter      int  `json:"lockoutAfter"`
	ReauthForCommands bool `json:"reauthForCommands"`
	Require2FA        bool `json:"require2FA"` // team members must turn on 2FA before anything else
}

func DefaultSecurity() Security { return Security{SessionTimeoutMin: 60, LockoutAfter: 5} }

func (s Security) Validate() error {
	if s.SessionTimeoutMin < 5 || s.SessionTimeoutMin > 7*24*60 {
		return InputError("session timeout must be between 5 minutes and 7 days")
	}
	if s.LockoutAfter < 3 || s.LockoutAfter > 20 {
		return InputError("lockout threshold must be between 3 and 20")
	}
	return nil
}

// InputError is a user-correctable validation error; the API returns it as 400.
type InputError = valid.Error

// Client identifies who is making a request, for rate limiting and audit.
type Client struct{ IP, UserAgent string }

type Session struct {
	ID         string `json:"id"`
	MFAPending bool   `json:"-"`
	CreatedAt  int64  `json:"createdAt"`
	LastSeenAt int64  `json:"lastActive"`
	ExpiresAt  int64  `json:"expiresAt"`
	IP         string `json:"ip"`
	UserAgent  string `json:"userAgent"`
	// Who: the owner (MemberID "") or a team member with a role and, optionally, a
	// limited list of servers (nil = every server).
	MemberID string   `json:"memberId,omitempty"`
	Username string   `json:"username,omitempty"`
	Role     string   `json:"role"`
	Servers  []string `json:"-"`
}

// Owner reports whether the session belongs to the owner (the admin account).
func (s Session) Owner() bool { return s.MemberID == "" }

type Profile struct {
	Role              string   `json:"role"`              // owner | operator | viewer
	Servers           []string `json:"servers,omitempty"` // a member limited to these servers
	Username          string   `json:"username"`
	DisplayName       string   `json:"displayName"`
	Email             string   `json:"email"`
	PasswordChangedAt int64    `json:"passwordChangedAt"`
	TwoFAEnabled      bool     `json:"twofaEnabled"`
	RecoveryCodesLeft int      `json:"recoveryCodesLeft"`
	TwoFARequired     bool     `json:"twofaRequired,omitempty"` // must turn on 2FA before using the dashboard
}

type Service struct {
	db        *sql.DB
	box       *secret.Box
	lim       *limiter
	Now       func() time.Time
	dummyHash string // verified against when the username is wrong, to keep timing equal
	setupMu   sync.Mutex
	setupCode string // one-time first-run code; empty once an admin exists
}

func NewService(d *sql.DB, box *secret.Box) *Service {
	h, err := HashPassword("timing-equalizer")
	if err != nil {
		panic(err)
	}
	return &Service{db: d, box: box, lim: newLimiter(), Now: time.Now, dummyHash: h}
}

// EnsureAdmin creates the admin from env values on first start. It reports whether it did;
// once an admin exists the env values are ignored (use `ops-server reset-admin` instead).
func (s *Service) EnsureAdmin(username, hash string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	username = strings.TrimSpace(username)
	if username == "" || hash == "" {
		return false, nil // the setup wizard creates the admin
	}
	if _, err := VerifyPassword(hash, ""); err != nil {
		return false, fmt.Errorf("ADMIN_PASSWORD_HASH: %w", err)
	}
	_, err := s.db.Exec(`INSERT INTO admin (id, username, display_name, password_hash, password_changed_at) VALUES (1, ?, ?, ?, ?)`,
		username, username, hash, s.Now().UnixMilli())
	return err == nil, err
}

// ResetAdmin sets a new password hash, turns 2FA off and signs out every session (CLI recovery).
func (s *Service) ResetAdmin(hash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE admin SET password_hash = ?, password_changed_at = ?, totp_secret_enc = NULL, totp_pending_enc = NULL, totp_last_step = 0`, hash, s.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no admin exists yet; finish the setup wizard in the browser first")
	}
	for _, q := range []string{`DELETE FROM recovery_codes WHERE member_id = ''`, `DELETE FROM sessions`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) Security() Security {
	sec := DefaultSecurity()
	if err := settings.Load(s.db, "security", &sec); err != nil || sec.Validate() != nil {
		return DefaultSecurity()
	}
	return sec
}

func (s *Service) SaveSecurity(sec Security) error {
	if err := sec.Validate(); err != nil {
		return err
	}
	return settings.Save(s.db, "security", sec)
}

type adminRow struct {
	username, passwordHash string
	totpEnc, pendingEnc    []byte
	lastStep               int64
}

func (s *Service) admin() (adminRow, error) {
	var a adminRow
	err := s.db.QueryRow(`SELECT username, password_hash, totp_secret_enc, totp_pending_enc, totp_last_step FROM admin WHERE id = 1`).
		Scan(&a.username, &a.passwordHash, &a.totpEnc, &a.pendingEnc, &a.lastStep)
	return a, err
}

// guard returns LockedError while c is locked out.
func (s *Service) guard(c Client) error {
	if wait := s.lim.retryAfter(c.IP, s.Now()); wait > 0 {
		return LockedError{wait}
	}
	return nil
}

func (s *Service) failed(c Client, action, detail string) {
	s.lim.fail(c.IP, s.Security().LockoutAfter, s.Now())
	audit.Record(s.db, audit.Event{Action: action, OK: false, IP: c.IP, UserAgent: c.UserAgent, Detail: detail})
}

// Login checks the password. It returns a session token; pending is true when the
// token only allows the 2FA step.
func (s *Service) Login(c Client, username, password string) (token string, pending bool, err error) {
	if err := s.guard(c); err != nil {
		return "", false, err
	}
	a, err := s.admin()
	if err != nil {
		return "", false, err
	}
	userOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(a.username)) == 1
	hash := a.passwordHash
	if !userOK {
		hash = s.dummyHash
	}
	pwOK, err := VerifyPassword(hash, password)
	if err != nil {
		return "", false, err
	}
	if !userOK || !pwOK {
		if memberID, ok := s.memberLogin(username, password); ok {
			if f, err := s.factor(memberID); err == nil && f.totpEnc != nil {
				token, _, err = s.createSession(c, true, memberID)
				return token, true, err
			}
			s.lim.reset(c.IP)
			token, _, err = s.createSession(c, false, memberID)
			if err == nil {
				audit.Record(s.db, audit.Event{Action: "login", OK: true, IP: c.IP, UserAgent: c.UserAgent, Detail: "password (team member " + strings.TrimSpace(username) + ")"})
			}
			return token, false, err
		}
		s.failed(c, "login", "bad credentials")
		return "", false, ErrInvalidCredentials
	}
	if a.totpEnc != nil {
		token, _, err = s.createSession(c, true, "")
		return token, true, err
	}
	s.lim.reset(c.IP)
	token, _, err = s.createSession(c, false, "")
	if err == nil {
		audit.Record(s.db, audit.Event{Action: "login", OK: true, IP: c.IP, UserAgent: c.UserAgent, Detail: "password"})
	}
	return token, false, err
}

// LoginExternal signs in a user another sign-in vouched for (GitHub): member "" is the
// owner. A disabled member is refused, and two-factor still applies.
func (s *Service) LoginExternal(c Client, member, how string) (token string, pending bool, err error) {
	if err := s.guard(c); err != nil {
		return "", false, err
	}
	if member != "" {
		var disabled bool
		if s.db.QueryRow(`SELECT disabled FROM members WHERE id = ?`, member).Scan(&disabled) != nil || disabled {
			s.failed(c, "login", how+": no active member")
			return "", false, ErrInvalidCredentials
		}
		s.db.Exec(`UPDATE members SET last_login_at = ? WHERE id = ?`, s.Now().UnixMilli(), member)
	}
	if f, err := s.factor(member); err == nil && f.totpEnc != nil {
		token, _, err = s.createSession(c, true, member)
		return token, true, err
	}
	s.lim.reset(c.IP)
	token, _, err = s.createSession(c, false, member)
	if err == nil {
		audit.Record(s.db, audit.Event{Action: "login", OK: true, IP: c.IP, UserAgent: c.UserAgent, Detail: how})
	}
	return token, false, err
}

// Verify2FA completes a pending login. The pending session is replaced by a new one.
func (s *Service) Verify2FA(c Client, pendingToken, code string, recovery bool) (string, error) {
	if err := s.guard(c); err != nil {
		return "", err
	}
	sess, err := s.lookup(pendingToken)
	if err != nil || !sess.MFAPending {
		return "", ErrNoSession
	}
	method := "totp"
	if recovery {
		method = "recovery code"
	}
	if !s.checkSecondFactor(sess.MemberID, code, recovery) {
		s.failed(c, "login", "bad "+method)
		return "", ErrInvalidCode
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID); err != nil {
		return "", err
	}
	s.lim.reset(c.IP)
	if sess.MemberID != "" {
		if m, err := s.GetMember(sess.MemberID); err == nil {
			method += " (team member " + m.Username + ")"
		}
	}
	token, _, err := s.createSession(c, false, sess.MemberID)
	if err == nil {
		audit.Record(s.db, audit.Event{Action: "login", OK: true, IP: c.IP, UserAgent: c.UserAgent, Detail: "password + " + method})
	}
	return token, err
}

// checkSecondFactor consumes a recovery code or validates (and burns) a TOTP code of the
// owner (member "") or a team member.
func (s *Service) checkSecondFactor(member, code string, recovery bool) bool {
	if recovery {
		res, err := s.db.Exec(`UPDATE recovery_codes SET used_at = ? WHERE code_hash = ? AND member_id = ? AND used_at IS NULL`, s.Now().UnixMilli(), hashRecovery(code), member)
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n == 1
	}
	f, err := s.factor(member)
	if err != nil || f.totpEnc == nil {
		return false
	}
	sec, err := s.box.Open(f.totpEnc, totpADFor(member))
	if err != nil {
		return false
	}
	step, ok := CheckTOTP(string(sec), code, s.Now(), f.lastStep)
	if !ok {
		return false
	}
	// Conditional update: two concurrent requests with the same code cannot both win.
	var res sql.Result
	if member == "" {
		res, err = s.db.Exec(`UPDATE admin SET totp_last_step = ? WHERE id = 1 AND totp_last_step < ?`, step, step)
	} else {
		res, err = s.db.Exec(`UPDATE members SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, member, step)
	}
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// factorRow is one account's 2FA state: the owner's (admin table) or a member's.
type factorRow struct {
	username            string
	totpEnc, pendingEnc []byte
	lastStep            int64
}

func (s *Service) factor(member string) (factorRow, error) {
	var f factorRow
	q, args := `SELECT username, totp_secret_enc, totp_pending_enc, totp_last_step FROM admin WHERE id = 1`, []any{}
	if member != "" {
		q, args = `SELECT username, totp_secret_enc, totp_pending_enc, totp_last_step FROM members WHERE id = ?`, []any{member}
	}
	err := s.db.QueryRow(q, args...).Scan(&f.username, &f.totpEnc, &f.pendingEnc, &f.lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNoSession
	}
	return f, err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// setFactor updates the 2FA columns of one account.
func setFactor(ex execer, member, set string, args ...any) error {
	var err error
	if member == "" {
		_, err = ex.Exec(`UPDATE admin SET `+set+` WHERE id = 1`, args...)
	} else {
		_, err = ex.Exec(`UPDATE members SET `+set+` WHERE id = ?`, append(args, member)...)
	}
	return err
}

// totpADFor binds an encrypted TOTP secret to its account.
func totpADFor(member string) string {
	if member == "" {
		return totpAD
	}
	return "member.totp." + member
}

// HasTwoFA reports whether the owner (member "") or a member has 2FA turned on.
func (s *Service) HasTwoFA(member string) bool {
	f, err := s.factor(member)
	return err == nil && f.totpEnc != nil
}

// confirmAccountPassword re-checks the password of the owner or a member.
func (s *Service) confirmAccountPassword(c Client, member, pw, action string) error {
	if member == "" {
		return s.checkPassword(c, pw, action)
	}
	if err := s.guard(c); err != nil {
		return err
	}
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM members WHERE id = ?`, member).Scan(&hash); err != nil {
		return ErrNoSession
	}
	if ok, _ := VerifyPassword(hash, pw); !ok {
		s.failed(c, action, "wrong password")
		return ErrWrongPassword
	}
	return nil
}

func (s *Service) createSession(c Client, pending bool, memberID string) (string, Session, error) {
	tok := make([]byte, 32)
	idb := make([]byte, 9)
	if _, err := rand.Read(tok); err != nil {
		return "", Session{}, err
	}
	if _, err := rand.Read(idb); err != nil {
		return "", Session{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tok)
	now := s.Now()
	age := SessionMaxAge
	if pending {
		age = pendingMaxAge
	}
	ua := c.UserAgent
	if len(ua) > 300 {
		ua = ua[:300]
	}
	sess := Session{ID: hex.EncodeToString(idb), MFAPending: pending, CreatedAt: now.UnixMilli(), LastSeenAt: now.UnixMilli(), ExpiresAt: now.Add(age).UnixMilli(), IP: c.IP, UserAgent: ua}
	_, err := s.db.Exec(`INSERT INTO sessions (id, token_hash, mfa_pending, created_at, last_seen_at, expires_at, ip, user_agent, member_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, hashToken(token), pending, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt, sess.IP, sess.UserAgent, memberID)
	return token, sess, err
}

// lookup finds a live session (full or pending) by token, deleting it if it expired.
func (s *Service) lookup(token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	var sess Session
	err := s.db.QueryRow(`SELECT id, mfa_pending, created_at, last_seen_at, expires_at, ip, user_agent, member_id FROM sessions WHERE token_hash = ?`, hashToken(token)).
		Scan(&sess.ID, &sess.MFAPending, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt, &sess.IP, &sess.UserAgent, &sess.MemberID)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	now := s.Now().UnixMilli()
	idle := int64(s.Security().SessionTimeoutMin) * 60_000
	if now >= sess.ExpiresAt || (!sess.MFAPending && now-sess.LastSeenAt >= idle) {
		s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID)
		return Session{}, ErrNoSession
	}
	sess.Role = RoleOwner
	if sess.MemberID != "" { // role and servers are read on every request, so changes apply at once
		m, err := s.GetMember(sess.MemberID)
		if err != nil || m.Disabled {
			s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID)
			return Session{}, ErrNoSession
		}
		sess.Role, sess.Servers, sess.Username = m.Role, m.Servers, m.Username
	}
	return sess, nil
}

// Authenticate returns the session for token, including pending (2FA-step) sessions;
// callers decide whether a pending session is enough. It also refreshes the idle timer.
func (s *Service) Authenticate(token string) (Session, error) {
	sess, err := s.lookup(token)
	if err != nil {
		return sess, err
	}
	now := s.Now().UnixMilli()
	if !sess.MFAPending && now-sess.LastSeenAt >= touchEvery.Milliseconds() {
		s.db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now, sess.ID)
		sess.LastSeenAt = now
	}
	return sess, nil
}

func (s *Service) Logout(c Client, token string) {
	s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	audit.Record(s.db, audit.Event{Action: "logout", OK: true, IP: c.IP, UserAgent: c.UserAgent})
}

func (s *Service) ListSessions() ([]Session, error) {
	rows, err := s.db.Query(`SELECT s.id, s.created_at, s.last_seen_at, s.expires_at, s.ip, s.user_agent, s.member_id, COALESCE(m.username, '')
		FROM sessions s LEFT JOIN members m ON m.id = s.member_id
		WHERE s.mfa_pending = 0 AND s.expires_at > ? ORDER BY s.last_seen_at DESC`, s.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt, &x.IP, &x.UserAgent, &x.MemberID, &x.Username); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Service) RevokeSession(c Client, id string) error {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSession
	}
	audit.Record(s.db, audit.Event{Action: "session.revoke", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: id})
	return nil
}

func (s *Service) RevokeOtherSessions(c Client, keepID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id <> ?`, keepID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	audit.Record(s.db, audit.Event{Action: "session.revoke_others", OK: true, IP: c.IP, UserAgent: c.UserAgent, Detail: fmt.Sprintf("%d sessions", n)})
	return n, nil
}

// PurgeExpired deletes expired sessions; run periodically.
func (s *Service) PurgeExpired() {
	s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, s.Now().UnixMilli())
	s.lim.prune(s.Now())
}

func (s *Service) Profile() (Profile, error) {
	var p Profile
	var totp []byte
	err := s.db.QueryRow(`SELECT username, display_name, email, password_changed_at, totp_secret_enc,
		(SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NULL AND member_id = '') FROM admin WHERE id = 1`).
		Scan(&p.Username, &p.DisplayName, &p.Email, &p.PasswordChangedAt, &totp, &p.RecoveryCodesLeft)
	p.TwoFAEnabled, p.Role = totp != nil, RoleOwner
	return p, err
}

func (s *Service) UpdateProfile(c Client, displayName, email string) error {
	displayName, email = strings.TrimSpace(displayName), strings.TrimSpace(email)
	if len(displayName) > 100 {
		return InputError("display name is too long")
	}
	if len(email) > 254 || (email != "" && !strings.Contains(email, "@")) {
		return InputError("email address is invalid")
	}
	if _, err := s.db.Exec(`UPDATE admin SET display_name = ?, email = ? WHERE id = 1`, displayName, email); err != nil {
		return err
	}
	audit.Record(s.db, audit.Event{Action: "account.update", OK: true, IP: c.IP, UserAgent: c.UserAgent})
	return nil
}

// checkPassword re-confirms the admin password for sensitive actions, rate limited like login.
func (s *Service) checkPassword(c Client, pw, action string) error {
	if err := s.guard(c); err != nil {
		return err
	}
	a, err := s.admin()
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(a.passwordHash, pw)
	if err != nil {
		return err
	}
	if !ok {
		s.failed(c, action, "wrong password")
		return ErrWrongPassword
	}
	return nil
}

// ChangePassword sets a new password and signs out every other session.
func (s *Service) ChangePassword(c Client, current, next, keepSessionID string) error {
	if err := s.checkPassword(c, current, "account.password"); err != nil {
		return err
	}
	if err := ValidateNewPassword(next); err != nil {
		return err
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE admin SET password_hash = ?, password_changed_at = ? WHERE id = 1`, hash, s.Now().UnixMilli()); err != nil {
		return err
	}
	s.db.Exec(`DELETE FROM sessions WHERE id <> ?`, keepSessionID)
	audit.Record(s.db, audit.Event{Action: "account.password", OK: true, IP: c.IP, UserAgent: c.UserAgent})
	return nil
}

// Begin2FA creates a new pending TOTP secret for the owner. The secret is returned only here, once.
func (s *Service) Begin2FA(c Client, password string) (secretB32, uri string, err error) {
	return s.Begin2FAFor(c, "", password)
}

// Begin2FAFor starts 2FA setup for the owner (member "") or a member.
func (s *Service) Begin2FAFor(c Client, member, password string) (secretB32, uri string, err error) {
	if err := s.confirmAccountPassword(c, member, password, "2fa.setup"); err != nil {
		return "", "", err
	}
	f, err := s.factor(member)
	if err != nil {
		return "", "", err
	}
	if f.totpEnc != nil {
		return "", "", ErrTwoFAEnabled
	}
	if secretB32, err = NewTOTPSecret(); err != nil {
		return "", "", err
	}
	if err := setFactor(s.db, member, `totp_pending_enc = ?`, s.box.Seal([]byte(secretB32), totpADFor(member))); err != nil {
		return "", "", err
	}
	return secretB32, TOTPURI("Fleetwatch", f.username, secretB32), nil
}

// Enable2FA activates the owner's pending secret once their app produces valid codes.
func (s *Service) Enable2FA(c Client, code string) ([]string, error) {
	return s.Enable2FAFor(c, "", code)
}

func (s *Service) Enable2FAFor(c Client, member, code string) ([]string, error) {
	f, err := s.factor(member)
	if err != nil {
		return nil, err
	}
	if f.totpEnc != nil {
		return nil, ErrTwoFAEnabled
	}
	if f.pendingEnc == nil {
		return nil, ErrNoPendingSetup
	}
	sec, err := s.box.Open(f.pendingEnc, totpADFor(member))
	if err != nil {
		return nil, err
	}
	step, ok := CheckTOTP(string(sec), code, s.Now(), 0)
	if !ok {
		s.failed(c, "2fa.enable", "bad code")
		return nil, ErrInvalidCode
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := setFactor(tx, member, `totp_secret_enc = totp_pending_enc, totp_pending_enc = NULL, totp_last_step = ?`, step); err != nil {
		return nil, err
	}
	codes, err := replaceRecoveryCodes(tx, member)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	audit.Record(s.db, audit.Event{Action: "2fa.enable", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: f.username})
	return codes, nil
}

// Disable2FA requires the password plus a TOTP or recovery code.
func (s *Service) Disable2FA(c Client, password, code string, recovery bool) error {
	return s.Disable2FAFor(c, "", password, code, recovery)
}

func (s *Service) Disable2FAFor(c Client, member, password, code string, recovery bool) error {
	if err := s.confirmAccountPassword(c, member, password, "2fa.disable"); err != nil {
		return err
	}
	f, err := s.factor(member)
	if err != nil {
		return err
	}
	if f.totpEnc == nil {
		return ErrTwoFADisabled
	}
	if !s.checkSecondFactor(member, code, recovery) {
		s.failed(c, "2fa.disable", "bad code")
		return ErrInvalidCode
	}
	if err := setFactor(s.db, member, `totp_secret_enc = NULL, totp_pending_enc = NULL, totp_last_step = 0`); err != nil {
		return err
	}
	s.db.Exec(`DELETE FROM recovery_codes WHERE member_id = ?`, member)
	audit.Record(s.db, audit.Event{Action: "2fa.disable", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: f.username})
	return nil
}

// ResetMember2FA turns a member's 2FA off (they lost their phone); the owner does this.
func (s *Service) ResetMember2FA(c Client, member string) error {
	m, err := s.GetMember(member)
	if err != nil {
		return err
	}
	if err := setFactor(s.db, member, `totp_secret_enc = NULL, totp_pending_enc = NULL, totp_last_step = 0`); err != nil {
		return err
	}
	s.db.Exec(`DELETE FROM recovery_codes WHERE member_id = ?`, member)
	s.db.Exec(`DELETE FROM sessions WHERE member_id = ?`, member)
	audit.Record(s.db, audit.Event{Action: "member.2fa_reset", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: m.Username})
	return nil
}

// RegenerateRecoveryCodes replaces all recovery codes; old ones stop working.
func (s *Service) RegenerateRecoveryCodes(c Client, password string) ([]string, error) {
	return s.RegenerateRecoveryCodesFor(c, "", password)
}

func (s *Service) RegenerateRecoveryCodesFor(c Client, member, password string) ([]string, error) {
	if err := s.confirmAccountPassword(c, member, password, "2fa.recovery_codes"); err != nil {
		return nil, err
	}
	f, err := s.factor(member)
	if err != nil {
		return nil, err
	}
	if f.totpEnc == nil {
		return nil, ErrTwoFADisabled
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	codes, err := replaceRecoveryCodes(tx, member)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	audit.Record(s.db, audit.Event{Action: "2fa.recovery_codes", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: f.username})
	return codes, nil
}

func replaceRecoveryCodes(tx *sql.Tx, member string) ([]string, error) {
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE member_id = ?`, member); err != nil {
		return nil, err
	}
	enc := base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding) // no l/o/0/1
	codes := make([]string, recoveryCodeCnt)
	for i := range codes {
		b := make([]byte, 7) // 56 bits -> 12 chars, we keep 10 (50 bits)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		s := enc.EncodeToString(b)[:10]
		codes[i] = s[:5] + "-" + s[5:]
		if _, err := tx.Exec(`INSERT INTO recovery_codes (code_hash, member_id) VALUES (?, ?)`, hashRecovery(codes[i]), member); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

func hashRecovery(code string) string {
	norm := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte("recovery:" + norm))
	return hex.EncodeToString(sum[:])
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConfirmPassword re-checks the admin password for a sensitive action (rate limited
// like login; failures are audited under action).
func (s *Service) ConfirmPassword(c Client, password, action string) error {
	return s.checkPassword(c, password, action)
}
