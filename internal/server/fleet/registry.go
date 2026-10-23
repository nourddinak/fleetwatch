// Package fleet owns monitored servers: the registry in SQLite, agent enrollment and
// credentials, and the hub of live agent connections.
package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/valid"
)

const EnrollTokenTTL = time.Hour

var (
	ErrNotFound     = errors.New("server not found")
	ErrBadToken     = errors.New("enrollment token is invalid, used or expired")
	ErrBadCred      = errors.New("agent credential is invalid or revoked")
	NotifyOverrides = []string{"default", "all", "critical-only", "muted"}
)

// InputError is a user-correctable validation error.
type InputError = valid.Error

// Record is a server row.
type Record struct {
	ID             string
	Name           string
	Provider       string
	Notes          string
	NotifyOverride string
	Hostname       string
	OS             string
	Kernel         string
	Arch           string
	CPUCores       int
	MemTotal       uint64
	SwapTotal      uint64
	DiskTotal      uint64
	IPs            []string
	PublicIP       string
	AgentVersion   string
	Capabilities   []string
	BootTime       int64
	LastMetrics    *proto.Metrics
	CreatedAt      int64
	EnrolledAt     int64 // 0 = never
	LastSeenAt     int64
	ConnectedSince int64
}

type Event struct {
	ID       int64  `json:"id"`
	TS       int64  `json:"ts"`
	ServerID string `json:"serverId"`
	AppID    string `json:"appId,omitempty"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
}

type Registry struct {
	db  *sql.DB
	Now func() time.Time
}

func NewRegistry(d *sql.DB) *Registry { return &Registry{db: d, Now: time.Now} }

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", InputError("server name must be 1-64 characters")
	}
	if strings.ContainsFunc(name, func(r rune) bool { return r < 32 || r == 127 }) {
		return "", InputError("server name contains control characters")
	}
	return name, nil
}

// Create adds a server waiting for its agent and returns a one-time enrollment token.
func (r *Registry) Create(name, provider string) (string, string, error) {
	name, err := cleanName(name)
	if err != nil {
		return "", "", err
	}
	provider = strings.TrimSpace(provider)
	if len(provider) > 64 {
		return "", "", InputError("provider label is too long")
	}
	id := "srv_" + randHex(6)
	if _, err := r.db.Exec(`INSERT INTO servers (id, name, provider, created_at) VALUES (?, ?, ?, ?)`, id, name, provider, r.Now().UnixMilli()); err != nil {
		return "", "", err
	}
	tok, err := r.NewEnrollToken(id)
	return id, tok, err
}

// NewEnrollToken replaces any unused token for the server with a fresh one.
func (r *Registry) NewEnrollToken(serverID string) (string, error) {
	if _, err := r.Get(serverID); err != nil {
		return "", err
	}
	tok := "fwe_" + randToken(32)
	now := r.Now()
	tx, err := r.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM enrollment_tokens WHERE server_id = ? AND used_at IS NULL`, serverID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO enrollment_tokens (token_hash, server_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		sha(tok), serverID, now.UnixMilli(), now.Add(EnrollTokenTTL).UnixMilli()); err != nil {
		return "", err
	}
	return tok, tx.Commit()
}

// Enroll exchanges a one-time token for a permanent credential. Earlier credentials of
// the same server are revoked, so re-pairing a server cuts off its old agent.
func (r *Registry) Enroll(token string) (serverID, credential string, err error) {
	now := r.Now().UnixMilli()
	tx, err := r.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE enrollment_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`, now, sha(token), now)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", "", ErrBadToken
	}
	if err := tx.QueryRow(`SELECT server_id FROM enrollment_tokens WHERE token_hash = ?`, sha(token)).Scan(&serverID); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`UPDATE agent_credentials SET revoked_at = ? WHERE server_id = ? AND revoked_at IS NULL`, now, serverID); err != nil {
		return "", "", err
	}
	id, secretPart := randHex(8), randToken(32)
	if _, err := tx.Exec(`INSERT INTO agent_credentials (id, server_id, secret_hash, created_at) VALUES (?, ?, ?, ?)`, id, serverID, sha(secretPart), now); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`UPDATE servers SET enrolled_at = ? WHERE id = ?`, now, serverID); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	r.AddEvent(serverID, "agent", "Agent enrolled")
	return serverID, "fwa." + id + "." + secretPart, nil
}

// Authenticate resolves an agent credential to its server.
func (r *Registry) Authenticate(credential string) (string, error) {
	parts := strings.Split(credential, ".")
	if len(parts) != 3 || parts[0] != "fwa" || parts[1] == "" || parts[2] == "" {
		return "", ErrBadCred
	}
	var serverID, hash string
	var revoked sql.NullInt64
	err := r.db.QueryRow(`SELECT server_id, secret_hash, revoked_at FROM agent_credentials WHERE id = ?`, parts[1]).Scan(&serverID, &hash, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrBadCred
	}
	if err != nil {
		return "", err
	}
	if revoked.Valid || subtle.ConstantTimeCompare([]byte(hash), []byte(sha(parts[2]))) != 1 {
		return "", ErrBadCred
	}
	r.db.Exec(`UPDATE agent_credentials SET last_used_at = ? WHERE id = ?`, r.Now().UnixMilli(), parts[1])
	return serverID, nil
}

// RevokeCredentials invalidates every credential and pending token of a server.
func (r *Registry) RevokeCredentials(serverID string) error {
	now := r.Now().UnixMilli()
	if _, err := r.db.Exec(`UPDATE agent_credentials SET revoked_at = ? WHERE server_id = ? AND revoked_at IS NULL`, now, serverID); err != nil {
		return err
	}
	_, err := r.db.Exec(`DELETE FROM enrollment_tokens WHERE server_id = ? AND used_at IS NULL`, serverID)
	if err == nil {
		r.AddEvent(serverID, "agent", "Agent credential revoked")
	}
	return err
}

const recordCols = `id, name, provider, notes, notify_override, hostname, os, kernel, arch, cpu_cores, mem_total, swap_total, disk_total,
	ips, public_ip, agent_version, capabilities, boot_time, last_metrics, created_at, enrolled_at, last_seen_at, connected_since`

func scanRecord(sc interface{ Scan(...any) error }) (Record, error) {
	var x Record
	var ips, caps, lastMetrics string
	var enrolled, seen, since sql.NullInt64
	err := sc.Scan(&x.ID, &x.Name, &x.Provider, &x.Notes, &x.NotifyOverride, &x.Hostname, &x.OS, &x.Kernel, &x.Arch, &x.CPUCores,
		&x.MemTotal, &x.SwapTotal, &x.DiskTotal, &ips, &x.PublicIP, &x.AgentVersion, &caps, &x.BootTime, &lastMetrics,
		&x.CreatedAt, &enrolled, &seen, &since)
	if err != nil {
		return x, err
	}
	json.Unmarshal([]byte(ips), &x.IPs)
	json.Unmarshal([]byte(caps), &x.Capabilities)
	if lastMetrics != "" {
		var m proto.Metrics
		if json.Unmarshal([]byte(lastMetrics), &m) == nil {
			x.LastMetrics = &m
		}
	}
	x.EnrolledAt, x.LastSeenAt, x.ConnectedSince = enrolled.Int64, seen.Int64, since.Int64
	return x, nil
}

func (r *Registry) Get(id string) (Record, error) {
	x, err := scanRecord(r.db.QueryRow(`SELECT `+recordCols+` FROM servers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

func (r *Registry) List() ([]Record, error) {
	rows, err := r.db.Query(`SELECT ` + recordCols + ` FROM servers ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		x, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Patch holds the admin-editable fields; nil means unchanged.
type Patch struct {
	Name           *string `json:"name"`
	Provider       *string `json:"provider"`
	Notes          *string `json:"notes"`
	NotifyOverride *string `json:"notifyOverride"`
}

func (r *Registry) Update(id string, p Patch) error {
	x, err := r.Get(id)
	if err != nil {
		return err
	}
	if p.Name != nil {
		if x.Name, err = cleanName(*p.Name); err != nil {
			return err
		}
	}
	if p.Provider != nil {
		if x.Provider = strings.TrimSpace(*p.Provider); len(x.Provider) > 64 {
			return InputError("provider label is too long")
		}
	}
	if p.Notes != nil {
		if x.Notes = *p.Notes; len(x.Notes) > 4000 {
			return InputError("notes are too long")
		}
	}
	if p.NotifyOverride != nil {
		ok := false
		for _, v := range NotifyOverrides {
			ok = ok || v == *p.NotifyOverride
		}
		if !ok {
			return InputError("unknown notification override")
		}
		x.NotifyOverride = *p.NotifyOverride
	}
	_, err = r.db.Exec(`UPDATE servers SET name = ?, provider = ?, notes = ?, notify_override = ? WHERE id = ?`, x.Name, x.Provider, x.Notes, x.NotifyOverride, id)
	return err
}

// Delete removes the server and (via cascades) its credentials, metrics and events.
func (r *Registry) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyHello stores what a (re)connecting agent reports. It reports whether the host
// rebooted since the previous connection.
func (r *Registry) ApplyHello(id string, h proto.Hello, publicIP string) (rebooted bool, err error) {
	prev, err := r.Get(id)
	if err != nil {
		return false, err
	}
	ips, _ := json.Marshal(h.IPs)
	caps, _ := json.Marshal(h.Capabilities)
	now := r.Now().UnixMilli()
	_, err = r.db.Exec(`UPDATE servers SET hostname = ?, os = ?, kernel = ?, arch = ?, cpu_cores = ?, mem_total = ?, swap_total = ?, disk_total = ?,
		ips = ?, public_ip = ?, agent_version = ?, capabilities = ?, boot_time = ?, last_seen_at = ?, connected_since = ? WHERE id = ?`,
		trunc(h.Hostname, 255), trunc(h.OS, 120), trunc(h.Kernel, 120), trunc(h.Arch, 32), h.CPUCores, h.MemTotal, h.SwapTotal, h.DiskTotal,
		string(ips), publicIP, trunc(h.AgentVersion, 64), string(caps), h.BootTime, now, now, id)
	// Boot time jitters by a second between reads, so only count real changes.
	return prev.BootTime != 0 && abs(h.BootTime-prev.BootTime) > 30, err
}

func (r *Registry) RecordSample(id string, m proto.Metrics) error {
	b, _ := json.Marshal(m)
	_, err := r.db.Exec(`UPDATE servers SET last_metrics = ?, last_seen_at = ? WHERE id = ?`, string(b), r.Now().UnixMilli(), id)
	return err
}

func (r *Registry) TouchSeen(id string) error {
	_, err := r.db.Exec(`UPDATE servers SET last_seen_at = ? WHERE id = ?`, r.Now().UnixMilli(), id)
	return err
}

func (r *Registry) MarkDisconnected(id string) error {
	_, err := r.db.Exec(`UPDATE servers SET last_seen_at = ?, connected_since = NULL WHERE id = ?`, r.Now().UnixMilli(), id)
	return err
}

func (r *Registry) AddEvent(serverID, kind, text string) {
	r.db.Exec(`INSERT INTO events (ts, server_id, kind, text) VALUES (?, ?, ?, ?)`, r.Now().UnixMilli(), serverID, kind, text)
}

func (r *Registry) Events(serverID string, limit int) ([]Event, error) {
	rows, err := r.db.Query(`SELECT id, ts, COALESCE(server_id, ''), COALESCE(app_id, ''), kind, text FROM events WHERE server_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.ServerID, &e.AppID, &e.Kind, &e.Text); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
