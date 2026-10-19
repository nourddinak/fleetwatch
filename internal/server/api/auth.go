package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
)

const sessionCookie = "fw_session"

func (s *server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", s.login)
	if s.GitHub != nil {
		s.githubLoginRoutes(mux)
	}
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/auth/2fa", s.verify2FA)
	mux.HandleFunc("GET /api/auth/me", s.me)
	mux.HandleFunc("POST /api/auth/logout", s.logout)

	mux.HandleFunc("PUT /api/account", s.authed(s.updateAccount))
	mux.HandleFunc("POST /api/account/password", s.authed(s.changePassword))
	mux.HandleFunc("GET /api/sessions", s.authed(s.listSessions))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.authed(s.revokeSession))
	mux.HandleFunc("POST /api/sessions/revoke-others", s.authed(s.revokeOthers))
	mux.HandleFunc("GET /api/settings/security", s.authed(s.getSecurity))
	mux.HandleFunc("PUT /api/settings/security", s.authed(s.putSecurity))
	mux.HandleFunc("GET /api/audit/logins", s.authed(s.loginHistory))
	mux.HandleFunc("GET /api/audit", s.authed(s.auditLog))
	mux.HandleFunc("POST /api/2fa/setup", s.authed(s.setup2FA))
	mux.HandleFunc("POST /api/2fa/enable", s.authed(s.enable2FA))
	mux.HandleFunc("POST /api/2fa/disable", s.authed(s.disable2FA))
	mux.HandleFunc("POST /api/2fa/recovery-codes", s.authed(s.regenRecovery))
}

type authedHandler func(w http.ResponseWriter, r *http.Request, sess auth.Session)

// authed only lets fully signed-in sessions (password + 2FA if enabled) through.
func (s *server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.Tokens != nil {
			s.tokenAuthed(w, r, strings.TrimSpace(bearer), h)
			return
		}
		sess, err := s.Auth.Authenticate(cookieToken(r))
		if err != nil || sess.MFAPending {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Not signed in.")
			return
		}
		if !s.allowed(w, r, sess) {
			return
		}
		// "Require 2FA for everyone": a member without it can only set it up (or sign out).
		if !sess.Owner() && !strings.HasPrefix(r.URL.Path, "/api/2fa/") && !strings.HasPrefix(r.URL.Path, "/api/account") &&
			s.Auth.Security().Require2FA && !s.Auth.HasTwoFA(sess.MemberID) {
			writeError(w, http.StatusForbidden, "2fa_required", "The owner requires two-factor authentication: turn it on to continue.")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess))
		// A member limited to some servers only gets their part of every list.
		if sc := s.scopeOf(sess); sc != nil && r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/ws") {
			sw := &scopedWriter{ResponseWriter: w}
			h(sw, r, sess)
			sw.flush(sc, r.URL.Path == "/api/servers")
			return
		}
		h(w, r, sess)
	}
}

func cookieToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func setSessionCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if !decode(w, r, &in) {
		return
	}
	// A fresh login replaces whatever session this browser had (prevents fixation).
	if old := cookieToken(r); old != "" {
		s.Auth.Logout(client(r), old)
	}
	token, pending, err := s.Auth.Login(client(r), in.Username, in.Password)
	if err != nil {
		clearSessionCookie(w)
		fail(w, err)
		return
	}
	if pending {
		setSessionCookie(w, token, 5*time.Minute)
		writeJSON(w, http.StatusOK, map[string]string{"status": "2fa"})
		return
	}
	setSessionCookie(w, token, auth.SessionMaxAge)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) verify2FA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string
		Recovery bool
	}
	if !decode(w, r, &in) {
		return
	}
	token, err := s.Auth.Verify2FA(client(r), cookieToken(r), in.Code, in.Recovery)
	if err != nil {
		fail(w, err)
		return
	}
	setSessionCookie(w, token, auth.SessionMaxAge)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) setup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code, Username, Password string }
	if !decode(w, r, &in) {
		return
	}
	token, err := s.Auth.Setup(client(r), in.Code, in.Username, in.Password)
	switch {
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, "setup_done", "Setup is already complete. Sign in instead.")
	case errors.Is(err, auth.ErrBadSetupCode):
		writeError(w, http.StatusForbidden, "bad_setup_code", "Wrong setup code. Find it in the server log: journalctl -u ops-server | grep 'setup code'")
	case err != nil:
		fail(w, err)
	default:
		setSessionCookie(w, token, auth.SessionMaxAge)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	if s.Auth.NeedsSetup() {
		writeError(w, http.StatusUnauthorized, "setup_required", "Create the admin account to finish setup.")
		return
	}
	sess, err := s.Auth.Authenticate(cookieToken(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Not signed in.")
		return
	}
	if sess.MFAPending {
		writeError(w, http.StatusUnauthorized, "mfa_required", "Two-factor code required.")
		return
	}
	if !sess.Owner() {
		p, err := s.Auth.MemberProfile(sess.MemberID)
		if err != nil {
			fail(w, err)
			return
		}
		p.TwoFARequired = s.Auth.Security().Require2FA && !p.TwoFAEnabled
		writeJSON(w, http.StatusOK, p)
		return
	}
	p, err := s.Auth.Profile()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if tok := cookieToken(r); tok != "" {
		s.Auth.Logout(client(r), tok)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) updateAccount(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct{ DisplayName, Email string }
	if !decode(w, r, &in) {
		return
	}
	update := func() error { return s.Auth.UpdateProfile(client(r), in.DisplayName, in.Email) }
	if !sess.Owner() {
		update = func() error { return s.Auth.UpdateMemberProfile(sess.MemberID, in.DisplayName, in.Email) }
	}
	if err := update(); err != nil {
		fail(w, err)
		return
	}
	s.me(w, r)
}

func (s *server) changePassword(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct{ Current, Next string }
	if !decode(w, r, &in) {
		return
	}
	change := s.Auth.ChangePassword
	if !sess.Owner() {
		change = func(c auth.Client, cur, next, keep string) error {
			return s.Auth.ChangeMemberPassword(c, sess.MemberID, cur, next, keep)
		}
	}
	if err := change(client(r), in.Current, in.Next, sess.ID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type sessionView struct {
	auth.Session
	Current bool `json:"current"`
}

func (s *server) listSessions(w http.ResponseWriter, r *http.Request, cur auth.Session) {
	list, err := s.Auth.ListSessions()
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]sessionView, len(list))
	for i, x := range list {
		out[i] = sessionView{x, x.ID == cur.ID}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) revokeSession(w http.ResponseWriter, r *http.Request, cur auth.Session) {
	id := r.PathValue("id")
	if err := s.Auth.RevokeSession(client(r), id); err != nil {
		if err == auth.ErrNoSession {
			writeError(w, http.StatusNotFound, "not_found", "Session not found.")
			return
		}
		fail(w, err)
		return
	}
	if id == cur.ID {
		clearSessionCookie(w)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) revokeOthers(w http.ResponseWriter, r *http.Request, cur auth.Session) {
	n, err := s.Auth.RevokeOtherSessions(client(r), cur.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

func (s *server) getSecurity(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	writeJSON(w, http.StatusOK, s.Auth.Security())
}

func (s *server) putSecurity(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	var in auth.Security
	if !decode(w, r, &in) {
		return
	}
	if in.Require2FA && !s.Auth.HasTwoFA("") {
		writeError(w, http.StatusBadRequest, "invalid_input", "Turn on your own two-factor authentication first.")
		return
	}
	if err := s.Auth.SaveSecurity(in); err != nil {
		fail(w, err)
		return
	}
	audit.Record(s.DB, audit.Event{Action: "settings.security", OK: true, IP: clientIP(r), UserAgent: r.UserAgent()})
	writeJSON(w, http.StatusOK, in)
}

func (s *server) loginHistory(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	rows, err := audit.List(s.DB, "login", 25)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *server) auditLog(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	rows, err := audit.List(s.DB, "", 200)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *server) setup2FA(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct{ Password string }
	if !decode(w, r, &in) {
		return
	}
	secretB32, uri, err := s.Auth.Begin2FAFor(client(r), sess.MemberID, in.Password)
	if err != nil {
		fail(w, err)
		return
	}
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secretB32,
		"qr":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

func (s *server) enable2FA(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct{ Code string }
	if !decode(w, r, &in) {
		return
	}
	codes, err := s.Auth.Enable2FAFor(client(r), sess.MemberID, in.Code)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"recoveryCodes": codes})
}

func (s *server) disable2FA(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct {
		Password, Code string
		Recovery       bool
	}
	if !decode(w, r, &in) {
		return
	}
	if s.Auth.Security().Require2FA && !sess.Owner() {
		writeError(w, http.StatusConflict, "conflict", "Two-factor authentication is required on this dashboard; it cannot be turned off.")
		return
	}
	if s.Auth.Security().Require2FA && sess.Owner() {
		writeError(w, http.StatusConflict, "conflict", "Turn off “Require 2FA for everyone” first.")
		return
	}
	if err := s.Auth.Disable2FAFor(client(r), sess.MemberID, in.Password, in.Code, in.Recovery); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) regenRecovery(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct{ Password string }
	if !decode(w, r, &in) {
		return
	}
	codes, err := s.Auth.RegenerateRecoveryCodesFor(client(r), sess.MemberID, in.Password)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"recoveryCodes": codes})
}
