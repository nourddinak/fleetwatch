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
}

type Profile struct {
	Username          string `json:"username"`
	DisplayName       string `json:"displayName"`
	Email             string `json:"email"`
	PasswordChangedAt int64  `json:"passwordChangedAt"`
	TwoFAEnabled      bool   `json:"twofaEnabled"`
	RecoveryCodesLeft int    `json:"recoveryCodesLeft"`
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
	for _, q := range []string{`DELETE FROM recovery_codes`, `DELETE FROM sessions`} {
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
		s.failed(c, "login", "bad credentials")
		return "", false, ErrInvalidCredentials
	}
	if a.totpEnc != nil {
		token, _, err = s.createSession(c, true)
		return token, true, err
	}
	s.lim.reset(c.IP)
	token, _, err = s.createSession(c, false)
	if err == nil {
		audit.Record(s.db, audit.Event{Action: "login", OK: true, IP: c.IP, UserAgent: c.UserAgent, Detail: "password"})
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
	if !s.checkSecondFactor(code, recovery) {
		s.failed(c, "login", "bad "+method)
		return "", ErrInvalidCode
	}
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID); err != nil {
		return "", err
	}
	s.lim.reset(c.IP)
	token, _, err := s.createSession(c, false)
	if err == nil {
		audit.Record(s.db, audit.Event{Action: "login", OK: true, IP: c.IP, UserAgent: c.UserAgent, Detail: "password + " + method})
	}
	return token, err
}

// checkSecondFactor consumes a recovery code or validates (and burns) a TOTP code.
func (s *Service) checkSecondFactor(code string, recovery bool) bool {
	if recovery {
		res, err := s.db.Exec(`UPDATE recovery_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL`, s.Now().UnixMilli(), hashRecovery(code))
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n == 1
	}
	a, err := s.admin()
	if err != nil || a.totpEnc == nil {
		return false
	}
	sec, err := s.box.Open(a.totpEnc, totpAD)
	if err != nil {
		return false
	}
	step, ok := CheckTOTP(string(sec), code, s.Now(), a.lastStep)
	if !ok {
		return false
	}
	// Conditional update: two concurrent requests with the same code cannot both win.
	res, err := s.db.Exec(`UPDATE admin SET totp_last_step = ? WHERE id = 1 AND totp_last_step < ?`, step, step)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func (s *Service) createSession(c Client, pending bool) (string, Session, error) {
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
	_, err := s.db.Exec(`INSERT INTO sessions (id, token_hash, mfa_pending, created_at, last_seen_at, expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, hashToken(token), pending, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt, sess.IP, sess.UserAgent)
	return token, sess, err
}

// lookup finds a live session (full or pending) by token, deleting it if it expired.
func (s *Service) lookup(token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	var sess Session
	err := s.db.QueryRow(`SELECT id, mfa_pending, created_at, last_seen_at, expires_at, ip, user_agent FROM sessions WHERE token_hash = ?`, hashToken(token)).
		Scan(&sess.ID, &sess.MFAPending, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt, &sess.IP, &sess.UserAgent)
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
	rows, err := s.db.Query(`SELECT id, created_at, last_seen_at, expires_at, ip, user_agent FROM sessions
		WHERE mfa_pending = 0 AND expires_at > ? ORDER BY last_seen_at DESC`, s.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt, &x.IP, &x.UserAgent); err != nil {
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
		(SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NULL) FROM admin WHERE id = 1`).
		Scan(&p.Username, &p.DisplayName, &p.Email, &p.PasswordChangedAt, &totp, &p.RecoveryCodesLeft)
	p.TwoFAEnabled = totp != nil
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

// Begin2FA creates a new pending TOTP secret. The secret is returned only here, once.
func (s *Service) Begin2FA(c Client, password string) (secretB32, uri string, err error) {
	if err := s.checkPassword(c, password, "2fa.setup"); err != nil {
		return "", "", err
	}
	a, err := s.admin()
	if err != nil {
		return "", "", err
	}
	if a.totpEnc != nil {
		return "", "", ErrTwoFAEnabled
	}
	secretB32, err = NewTOTPSecret()
	if err != nil {
		return "", "", err
	}
	if _, err := s.db.Exec(`UPDATE admin SET totp_pending_enc = ? WHERE id = 1`, s.box.Seal([]byte(secretB32), totpAD)); err != nil {
		return "", "", err
	}
	return secretB32, TOTPURI("Fleetwatch", a.username, secretB32), nil
}

// Enable2FA activates the pending secret once the user proves their app produces valid codes.
func (s *Service) Enable2FA(c Client, code string) ([]string, error) {
	a, err := s.admin()
	if err != nil {
		return nil, err
	}
	if a.totpEnc != nil {
		return nil, ErrTwoFAEnabled
	}
	if a.pendingEnc == nil {
		return nil, ErrNoPendingSetup
	}
	sec, err := s.box.Open(a.pendingEnc, totpAD)
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
	if _, err := tx.Exec(`UPDATE admin SET totp_secret_enc = totp_pending_enc, totp_pending_enc = NULL, totp_last_step = ? WHERE id = 1`, step); err != nil {
		return nil, err
	}
	codes, err := replaceRecoveryCodes(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	audit.Record(s.db, audit.Event{Action: "2fa.enable", OK: true, IP: c.IP, UserAgent: c.UserAgent})
	return codes, nil
}

// Disable2FA requires the password plus a TOTP or recovery code.
func (s *Service) Disable2FA(c Client, password, code string, recovery bool) error {
	if err := s.checkPassword(c, password, "2fa.disable"); err != nil {
		return err
	}
	a, err := s.admin()
	if err != nil {
		return err
	}
	if a.totpEnc == nil {
		return ErrTwoFADisabled
	}
	if !s.checkSecondFactor(code, recovery) {
		s.failed(c, "2fa.disable", "bad code")
		return ErrInvalidCode
	}
	if _, err := s.db.Exec(`UPDATE admin SET totp_secret_enc = NULL, totp_pending_enc = NULL, totp_last_step = 0 WHERE id = 1`); err != nil {
		return err
	}
	s.db.Exec(`DELETE FROM recovery_codes`)
	audit.Record(s.db, audit.Event{Action: "2fa.disable", OK: true, IP: c.IP, UserAgent: c.UserAgent})
	return nil
}

// RegenerateRecoveryCodes replaces all recovery codes; old ones stop working.
func (s *Service) RegenerateRecoveryCodes(c Client, password string) ([]string, error) {
	if err := s.checkPassword(c, password, "2fa.recovery_codes"); err != nil {
		return nil, err
	}
	a, err := s.admin()
	if err != nil {
		return nil, err
	}
	if a.totpEnc == nil {
		return nil, ErrTwoFADisabled
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	codes, err := replaceRecoveryCodes(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	audit.Record(s.db, audit.Event{Action: "2fa.recovery_codes", OK: true, IP: c.IP, UserAgent: c.UserAgent})
	return codes, nil
}

func replaceRecoveryCodes(tx *sql.Tx) ([]string, error) {
	if _, err := tx.Exec(`DELETE FROM recovery_codes`); err != nil {
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
		if _, err := tx.Exec(`INSERT INTO recovery_codes (code_hash) VALUES (?)`, hashRecovery(codes[i])); err != nil {
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
