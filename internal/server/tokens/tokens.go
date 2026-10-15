// Package tokens manages read-only API tokens for scripts and Prometheus. A token is
// shown once at creation; only its SHA-256 is stored.
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/valid"
)

// Prefix marks Fleetwatch tokens, so they are easy to spot in configs and leaks.
const Prefix = "fwt_"

var ErrNotFound = errors.New("token not found")

type Token struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	LastUsedIP string `json:"lastUsedIp"`
}

type Service struct {
	db  *sql.DB
	Now func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time // last-used write throttle per token
}

func New(d *sql.DB) *Service {
	return &Service{db: d, Now: time.Now, touched: map[string]time.Time{}}
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Create returns the new token (shown once) and its record.
func (s *Service) Create(name string) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 {
		return "", Token{}, valid.Error("token name must be 1-60 characters")
	}
	secret := Prefix + random(32)
	t := Token{ID: "tok_" + random(6), Name: name, Prefix: secret[:len(Prefix)+6], CreatedAt: s.Now().UnixMilli()}
	_, err := s.db.Exec(`INSERT INTO api_tokens (id, name, token_hash, prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		t.ID, t.Name, hash(secret), t.Prefix, t.CreatedAt)
	return secret, t, err
}

func (s *Service) List() ([]Token, error) {
	rows, err := s.db.Query(`SELECT id, name, prefix, created_at, last_used_at, last_used_ip FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.LastUsedIP); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) Revoke(id string) (Token, error) {
	var t Token
	err := s.db.QueryRow(`SELECT id, name, prefix FROM api_tokens WHERE id = ?`, id).Scan(&t.ID, &t.Name, &t.Prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	_, err = s.db.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	return t, err
}

// Verify checks a presented token and records its use (at most once a minute).
func (s *Service) Verify(token, ip string) (Token, bool) {
	if !strings.HasPrefix(token, Prefix) || len(token) > 200 {
		return Token{}, false
	}
	var t Token
	err := s.db.QueryRow(`SELECT id, name, prefix, created_at FROM api_tokens WHERE token_hash = ?`, hash(token)).Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt)
	if err != nil {
		return Token{}, false
	}
	now := s.Now()
	s.mu.Lock()
	stale := now.Sub(s.touched[t.ID]) > time.Minute
	if stale {
		s.touched[t.ID] = now
	}
	s.mu.Unlock()
	if stale {
		s.db.Exec(`UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, now.UnixMilli(), ip, t.ID)
	}
	return t, true
}
