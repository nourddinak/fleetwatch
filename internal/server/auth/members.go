package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/valid"
)

// Roles. The owner is the admin account; members are operators or viewers.
const (
	RoleOwner    = "owner"
	RoleOperator = "operator" // sees everything allowed, restarts apps, runs commands, handles incidents
	RoleViewer   = "viewer"   // read-only
	RoleClient   = "client"   // a customer: only their portal (Portal), nothing of the dashboard
)

// Portal is what a client sees: their apps, uptime checks and status pages, by ID.
type Portal struct {
	Apps   []string `json:"apps"`
	Checks []string `json:"checks"`
	Pages  []string `json:"pages"`
}

func encodePortal(p Portal) string {
	b, _ := json.Marshal(p)
	return string(b)
}

func decodePortal(s string) Portal {
	p := Portal{}
	json.Unmarshal([]byte(s), &p)
	for _, l := range []*[]string{&p.Apps, &p.Checks, &p.Pages} {
		if *l == nil {
			*l = []string{}
		}
	}
	return p
}

var ErrNoMember = errors.New("team member not found")

// Member is a team member as the owner manages them.
type Member struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	Role        string   `json:"role"`
	Servers     []string `json:"servers"` // nil = every server
	Disabled    bool     `json:"disabled"`
	CreatedAt   int64    `json:"createdAt"`
	LastLoginAt int64    `json:"lastLoginAt"`
	TwoFA       bool     `json:"twofaEnabled"`
	Portal      Portal   `json:"portal"`
}

// MemberInput creates or changes a member; an empty Password keeps the current one.
type MemberInput struct {
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	Role        string   `json:"role"`
	Servers     []string `json:"servers"`
	Disabled    bool     `json:"disabled"`
	Password    string   `json:"password"`
	Portal      Portal   `json:"portal"`
}

func encodeServers(list []string) string {
	if list == nil {
		return "*"
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func decodeServers(s string) []string {
	if s == "*" {
		return nil
	}
	out := []string{}
	json.Unmarshal([]byte(s), &out)
	return out
}

func (in *MemberInput) validate(creating bool) error {
	in.Username, in.DisplayName, in.Email = strings.TrimSpace(in.Username), strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.Email)
	if creating && !usernameRe.MatchString(in.Username) {
		return valid.Error("username may use letters, numbers, dots, dashes and underscores (up to 64)")
	}
	if in.Role != RoleOperator && in.Role != RoleViewer && in.Role != RoleClient {
		return valid.Error("role must be operator, viewer or client")
	}
	if in.Role == RoleClient {
		in.Servers = []string{} // no server of the dashboard: only the portal
		n := len(in.Portal.Apps) + len(in.Portal.Checks) + len(in.Portal.Pages)
		if n == 0 {
			return valid.Error("pick what the client sees: apps, uptime checks or status pages")
		}
		if n > 300 {
			return valid.Error("too many things for one client")
		}
	} else {
		in.Portal = Portal{}
	}
	if len(in.DisplayName) > 80 || len(in.Email) > 254 {
		return valid.Error("name or email is too long")
	}
	if len(in.Servers) > 500 {
		return valid.Error("too many servers")
	}
	if creating || in.Password != "" {
		if err := ValidateNewPassword(in.Password); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ListMembers() ([]Member, error) {
	rows, err := s.db.Query(`SELECT id, username, display_name, email, role, servers, disabled, created_at, last_login_at, totp_secret_enc IS NOT NULL, portal FROM members ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var servers, portal string
		if err := rows.Scan(&m.ID, &m.Username, &m.DisplayName, &m.Email, &m.Role, &servers, &m.Disabled, &m.CreatedAt, &m.LastLoginAt, &m.TwoFA, &portal); err != nil {
			return nil, err
		}
		m.Servers, m.Portal = decodeServers(servers), decodePortal(portal)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) GetMember(id string) (Member, error) {
	var m Member
	var servers, portal string
	err := s.db.QueryRow(`SELECT id, username, display_name, email, role, servers, disabled, created_at, last_login_at, totp_secret_enc IS NOT NULL, portal FROM members WHERE id = ?`, id).
		Scan(&m.ID, &m.Username, &m.DisplayName, &m.Email, &m.Role, &servers, &m.Disabled, &m.CreatedAt, &m.LastLoginAt, &m.TwoFA, &portal)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNoMember
	}
	m.Servers, m.Portal = decodeServers(servers), decodePortal(portal)
	return m, err
}

func (s *Service) CreateMember(c Client, in MemberInput) (Member, error) {
	if err := in.validate(true); err != nil {
		return Member{}, err
	}
	if a, err := s.admin(); err == nil && strings.EqualFold(a.username, in.Username) {
		return Member{}, valid.Error("that username is taken")
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return Member{}, err
	}
	b := make([]byte, 8)
	rand.Read(b)
	id := "mem_" + hex.EncodeToString(b)
	_, err = s.db.Exec(`INSERT INTO members (id, username, display_name, email, password_hash, role, servers, disabled, created_at, portal) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.Username, in.DisplayName, in.Email, hash, in.Role, encodeServers(in.Servers), in.Disabled, s.Now().UnixMilli(), encodePortal(in.Portal))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return Member{}, valid.Error("that username is taken")
	}
	if err != nil {
		return Member{}, err
	}
	audit.Record(s.db, audit.Event{Action: "member.create", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: in.Username, Detail: in.Role})
	return s.GetMember(id)
}

// UpdateMember changes role, servers, details or password. Signing someone out is
// part of it: a disabled member or a new password ends their sessions.
func (s *Service) UpdateMember(c Client, id string, in MemberInput) (Member, error) {
	old, err := s.GetMember(id)
	if err != nil {
		return old, err
	}
	in.Username = old.Username
	if err := in.validate(false); err != nil {
		return old, err
	}
	if _, err := s.db.Exec(`UPDATE members SET display_name = ?, email = ?, role = ?, servers = ?, disabled = ?, portal = ? WHERE id = ?`,
		in.DisplayName, in.Email, in.Role, encodeServers(in.Servers), in.Disabled, encodePortal(in.Portal), id); err != nil {
		return old, err
	}
	if in.Password != "" {
		hash, err := HashPassword(in.Password)
		if err != nil {
			return old, err
		}
		s.db.Exec(`UPDATE members SET password_hash = ? WHERE id = ?`, hash, id)
	}
	if in.Disabled || in.Password != "" {
		s.db.Exec(`DELETE FROM sessions WHERE member_id = ?`, id)
	}
	audit.Record(s.db, audit.Event{Action: "member.update", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: old.Username, Detail: in.Role})
	return s.GetMember(id)
}

func (s *Service) DeleteMember(c Client, id string) error {
	m, err := s.GetMember(id)
	if err != nil {
		return err
	}
	s.db.Exec(`DELETE FROM sessions WHERE member_id = ?`, id)
	s.db.Exec(`DELETE FROM recovery_codes WHERE member_id = ?`, id)
	if _, err := s.db.Exec(`DELETE FROM members WHERE id = ?`, id); err != nil {
		return err
	}
	audit.Record(s.db, audit.Event{Action: "member.delete", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: m.Username})
	return nil
}

// memberLogin checks a member's password (constant work when the name is unknown).
func (s *Service) memberLogin(username, password string) (string, bool) {
	var id, hash string
	var disabled bool
	err := s.db.QueryRow(`SELECT id, password_hash, disabled FROM members WHERE username = ?`, strings.TrimSpace(username)).Scan(&id, &hash, &disabled)
	if err != nil {
		VerifyPassword(s.dummyHash, password)
		return "", false
	}
	ok, _ := VerifyPassword(hash, password)
	if !ok || disabled {
		return "", false
	}
	s.db.Exec(`UPDATE members SET last_login_at = ? WHERE id = ?`, s.Now().UnixMilli(), id)
	return id, true
}

// ChangeMemberPassword is a member changing their own password.
func (s *Service) ChangeMemberPassword(c Client, memberID, current, next, keepSessionID string) error {
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM members WHERE id = ?`, memberID).Scan(&hash); err != nil {
		return ErrNoSession
	}
	if ok, _ := VerifyPassword(hash, current); !ok {
		s.failed(c, "password.change", "wrong current password")
		return ErrWrongPassword
	}
	if err := ValidateNewPassword(next); err != nil {
		return err
	}
	h, err := HashPassword(next)
	if err != nil {
		return err
	}
	s.db.Exec(`UPDATE members SET password_hash = ? WHERE id = ?`, h, memberID)
	s.db.Exec(`DELETE FROM sessions WHERE member_id = ? AND id <> ?`, memberID, keepSessionID)
	audit.Record(s.db, audit.Event{Action: "password.change", OK: true, IP: c.IP, UserAgent: c.UserAgent, Target: memberID})
	return nil
}

// MemberProfile is what /api/auth/me shows a member.
func (s *Service) MemberProfile(id string) (Profile, error) {
	m, err := s.GetMember(id)
	if err != nil {
		return Profile{}, err
	}
	p := Profile{Username: m.Username, DisplayName: m.DisplayName, Email: m.Email, Role: m.Role, Servers: m.Servers, TwoFAEnabled: m.TwoFA}
	s.db.QueryRow(`SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NULL AND member_id = ?`, id).Scan(&p.RecoveryCodesLeft)
	return p, nil
}

// UpdateMemberProfile lets a member change their own name and email.
func (s *Service) UpdateMemberProfile(id, displayName, email string) error {
	displayName, email = strings.TrimSpace(displayName), strings.TrimSpace(email)
	if len(displayName) > 80 || len(email) > 254 {
		return valid.Error("name or email is too long")
	}
	_, err := s.db.Exec(`UPDATE members SET display_name = ?, email = ? WHERE id = ?`, displayName, email, id)
	return err
}
