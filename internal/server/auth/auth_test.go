package auth

import (
	"encoding/base32"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Fatalf("verify correct: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	for _, bad := range []string{"", "plaintext", "$argon2i$v=19$m=1,t=1,p=1$a$b", "$argon2id$v=19$m=99999999,t=1,p=1$YQ$Yg"} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
	if ok, _ := VerifyPassword(h, string(make([]byte, MaxPasswordLen+1))); ok {
		t.Fatal("oversized password accepted")
	}
}

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 appendix B, SHA1 secret "12345678901234567890", T=59 -> 94287082 (8 digits).
	key := []byte("12345678901234567890")
	if got := hotp(key, 59/30, 8); got != "94287082" {
		t.Fatalf("hotp = %s", got)
	}
	secretB32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	now := time.Unix(59, 0)
	step, ok := CheckTOTP(secretB32, "287082", now, 0)
	if !ok || step != 1 {
		t.Fatalf("CheckTOTP = %d %v", step, ok)
	}
	if _, ok := CheckTOTP(secretB32, "287082", now, step); ok {
		t.Fatal("replayed code accepted")
	}
	if _, ok := CheckTOTP(secretB32, "287082", now.Add(5*time.Minute), 0); ok {
		t.Fatal("stale code accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := CheckTOTP(secretB32, bad, now, 0); ok {
			t.Errorf("code %q accepted", bad)
		}
	}
}

func TestSecretBox(t *testing.T) {
	box, _ := secret.New(make([]byte, 32))
	sealed := box.Seal([]byte("s3cret"), "a")
	if p, err := box.Open(sealed, "a"); err != nil || string(p) != "s3cret" {
		t.Fatalf("open: %q %v", p, err)
	}
	if _, err := box.Open(sealed, "b"); err == nil {
		t.Fatal("opened with wrong associated data")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := box.Open(sealed, "a"); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
}

type clock struct{ t time.Time }

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(make([]byte, 32))
	s := NewService(d, box)
	c := &clock{time.Unix(1_700_000_000, 0)}
	s.Now = func() time.Time { return c.t }
	h, _ := HashPassword("admin-password-1")
	if created, err := s.EnsureAdmin("admin", h); !created || err != nil {
		t.Fatalf("EnsureAdmin: %v %v", created, err)
	}
	return s, c
}

var cl = Client{IP: "203.0.113.5", UserAgent: "test"}

func TestEnsureAdminValidates(t *testing.T) {
	s, _ := newService(t)
	if created, err := s.EnsureAdmin("other", "whatever"); created || err != nil {
		t.Fatalf("second EnsureAdmin must be a no-op: %v %v", created, err)
	}
	d, _ := db.Open(filepath.Join(t.TempDir(), "t2.db"))
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	if _, err := NewService(d, box).EnsureAdmin("admin", "not-a-hash"); err == nil {
		t.Fatal("bad hash accepted")
	}
	if created, err := NewService(d, box).EnsureAdmin("", ""); created || err != nil {
		t.Fatalf("missing env must leave the admin to the setup wizard: %v %v", created, err)
	}
}

func TestSetupWizard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	s := NewService(d, box)
	if !s.NeedsSetup() {
		t.Fatal("fresh database must need setup")
	}
	if _, err := s.Setup(cl, "", "admin", "admin-password-1"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("setup without a started code: %v", err)
	}
	code := s.StartSetup()
	if _, err := s.Setup(cl, "AAAA-AAAA-AAAA-AAAA", "admin", "admin-password-1"); !errors.Is(err, ErrBadSetupCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := s.Setup(cl, code, "ad min", "admin-password-1"); err == nil {
		t.Fatal("bad username accepted")
	}
	if _, err := s.Setup(cl, code, "admin", "short"); err == nil {
		t.Fatal("weak password accepted")
	}
	// Codes are accepted without dashes and in lower case.
	tok, err := s.Setup(cl, strings.ToLower(strings.ReplaceAll(code, "-", "")), "admin", "admin-password-1")
	if err != nil || tok == "" {
		t.Fatalf("setup: %v", err)
	}
	if _, err := s.Authenticate(tok); err != nil || s.NeedsSetup() {
		t.Fatalf("not signed in after setup: %v", err)
	}
	if _, err := s.Setup(cl, code, "evil", "evil-password-1"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("second setup: %v", err)
	}
	if _, _, err := s.Login(cl, "admin", "admin-password-1"); err != nil {
		t.Fatalf("login with wizard password: %v", err)
	}
}

func TestLoginLockoutAndIdleTimeout(t *testing.T) {
	s, c := newService(t)
	for i := 0; i < 4; i++ {
		if _, _, err := s.Login(cl, "admin", "nope"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, _, err := s.Login(cl, "nobody", "admin-password-1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong username: %v", err)
	}
	// 5 failures (default threshold) -> locked even with the right password.
	var locked LockedError
	if _, _, err := s.Login(cl, "admin", "admin-password-1"); !errors.As(err, &locked) {
		t.Fatalf("expected lockout, got %v", err)
	}
	// Other IPs are unaffected.
	other := Client{IP: "198.51.100.1"}
	tok, pending, err := s.Login(other, "admin", "admin-password-1")
	if err != nil || pending || tok == "" {
		t.Fatalf("login from other IP: %v", err)
	}
	c.t = c.t.Add(16 * time.Minute)
	if _, _, err := s.Login(cl, "admin", "admin-password-1"); err != nil {
		t.Fatalf("lock did not expire: %v", err)
	}

	// 16 minutes idle is within the default 60-minute timeout.
	if _, err := s.Authenticate(tok); err != nil {
		t.Fatalf("session expired too early: %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, c := newService(t)
	tok, _, err := s.Login(cl, "admin", "admin-password-1")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Authenticate(tok)
	if err != nil || sess.MFAPending {
		t.Fatalf("authenticate: %+v %v", sess, err)
	}
	c.t = c.t.Add(59 * time.Minute)
	if _, err := s.Authenticate(tok); err != nil {
		t.Fatalf("session expired before idle timeout: %v", err)
	}
	c.t = c.t.Add(61 * time.Minute)
	if _, err := s.Authenticate(tok); !errors.Is(err, ErrNoSession) {
		t.Fatalf("idle session still valid: %v", err)
	}
	if _, err := s.Authenticate("garbage"); !errors.Is(err, ErrNoSession) {
		t.Fatal("garbage token accepted")
	}

	t1, _, _ := s.Login(cl, "admin", "admin-password-1")
	t2, _, _ := s.Login(cl, "admin", "admin-password-1")
	s1, _ := s.Authenticate(t1)
	if n, _ := s.RevokeOtherSessions(cl, s1.ID); n != 1 {
		t.Fatalf("revoked %d", n)
	}
	if _, err := s.Authenticate(t2); err == nil {
		t.Fatal("revoked session still valid")
	}
	if err := s.ChangePassword(cl, "wrong", "a-new-password-2", s1.ID); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("change with wrong current: %v", err)
	}
	if err := s.ChangePassword(cl, "admin-password-1", "short", s1.ID); err == nil {
		t.Fatal("short password accepted")
	}
	if err := s.ChangePassword(cl, "admin-password-1", "a-new-password-2", s1.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(cl, "admin", "a-new-password-2"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

func currentCode(t *testing.T, secretB32 string, now time.Time) string {
	key, err := b32.DecodeString(secretB32)
	if err != nil {
		t.Fatal(err)
	}
	return hotp(key, uint64(now.Unix()/totpPeriod), 6)
}

func TestTwoFactorFlow(t *testing.T) {
	s, c := newService(t)
	if _, _, err := s.Begin2FA(cl, "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("setup without password: %v", err)
	}
	sec, uri, err := s.Begin2FA(cl, "admin-password-1")
	if err != nil || uri == "" {
		t.Fatal(err)
	}
	if _, err := s.Enable2FA(cl, "000000"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("enable with bad code: %v", err)
	}
	codes, err := s.Enable2FA(cl, currentCode(t, sec, c.t))
	if err != nil || len(codes) != recoveryCodeCnt {
		t.Fatalf("enable: %v %d", err, len(codes))
	}
	p, _ := s.Profile()
	if !p.TwoFAEnabled || p.RecoveryCodesLeft != recoveryCodeCnt {
		t.Fatalf("profile: %+v", p)
	}

	// Password alone now yields only a pending session.
	pend, pending, err := s.Login(cl, "admin", "admin-password-1")
	if err != nil || !pending {
		t.Fatalf("login: pending=%v err=%v", pending, err)
	}
	// The code used to enable is burned; wait a step for a fresh one.
	c.t = c.t.Add(30 * time.Second)
	full, err := s.Verify2FA(cl, pend, currentCode(t, sec, c.t), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(pend); err == nil {
		t.Fatal("pending token still valid after upgrade")
	}
	if sess, err := s.Authenticate(full); err != nil || sess.MFAPending {
		t.Fatalf("full session: %v", err)
	}

	// Recovery code works exactly once, with any formatting.
	pend2, _, _ := s.Login(cl, "admin", "admin-password-1")
	if _, err := s.Verify2FA(cl, pend2, " "+codes[0]+" ", true); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	pend3, _, _ := s.Login(cl, "admin", "admin-password-1")
	if _, err := s.Verify2FA(cl, pend3, codes[0], true); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reused recovery code: %v", err)
	}
	// Pending token expires after 5 minutes.
	c.t = c.t.Add(6 * time.Minute)
	if _, err := s.Verify2FA(cl, pend3, codes[1], true); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired pending token: %v", err)
	}
	// A full session token cannot be used for the 2FA step.
	if _, err := s.Verify2FA(cl, full, codes[1], true); !errors.Is(err, ErrNoSession) {
		t.Fatalf("full token used as pending: %v", err)
	}

	if err := s.Disable2FA(cl, "admin-password-1", "000000", false); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("disable with bad code: %v", err)
	}
	if err := s.Disable2FA(cl, "admin-password-1", codes[2], true); err != nil {
		t.Fatal(err)
	}
	if _, pending, _ := s.Login(cl, "admin", "admin-password-1"); pending {
		t.Fatal("2FA still required after disable")
	}
}

func TestMemberTwoFactor(t *testing.T) {
	s, c := newService(t)
	m, err := s.CreateMember(cl, MemberInput{Username: "sam", Role: RoleOperator, Password: "member-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if s.HasTwoFA(m.ID) {
		t.Fatal("2FA on before setup")
	}
	if _, _, err := s.Begin2FAFor(cl, m.ID, "admin-password-1"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("member setup with the owner's password: %v", err)
	}
	sec, _, err := s.Begin2FAFor(cl, m.ID, "member-password-1")
	if err != nil {
		t.Fatal(err)
	}
	codes, err := s.Enable2FAFor(cl, m.ID, currentCode(t, sec, c.t))
	if err != nil || !s.HasTwoFA(m.ID) || s.HasTwoFA("") {
		t.Fatal(err)
	}
	// Login now stops at the second step, and only the member's own codes work.
	tok, pending, err := s.Login(cl, "sam", "member-password-1")
	if err != nil || !pending {
		t.Fatal("member login not pending", err)
	}
	c.t = c.t.Add(time.Minute)
	full, err := s.Verify2FA(cl, tok, currentCode(t, sec, c.t), false)
	if err != nil {
		t.Fatal(err)
	}
	if sess, err := s.Authenticate(full); err != nil || sess.MemberID != m.ID || sess.MFAPending {
		t.Fatalf("%+v %v", sess, err)
	}
	tok2, _, _ := s.Login(cl, "sam", "member-password-1")
	if _, err := s.Verify2FA(cl, tok2, codes[0], true); err != nil {
		t.Fatal("member recovery code:", err)
	}
	if p, _ := s.MemberProfile(m.ID); !p.TwoFAEnabled || p.RecoveryCodesLeft != recoveryCodeCnt-1 {
		t.Fatalf("%+v", p)
	}
	if p, _ := s.Profile(); p.TwoFAEnabled || p.RecoveryCodesLeft != 0 {
		t.Fatalf("owner sees the member's codes: %+v", p)
	}
	if err := s.ResetMember2FA(cl, m.ID); err != nil || s.HasTwoFA(m.ID) {
		t.Fatal("reset", err)
	}
	if _, err := s.Authenticate(full); err == nil {
		t.Fatal("sessions kept after a 2FA reset")
	}
}
