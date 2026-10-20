// Package backups keeps where backups go (S3-style storage, Google Drive, SFTP), each
// server's plan (what, when, how many copies), and asks the agents to run them with
// restic. Each server has its own encrypted repository at the storage.
package backups

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Agent is the first agent version that backs up.
const Agent = "v0.9.151"

var ErrNotFound = errors.New("not found")

// Dest is a place backups go. Secret (S3 secret key) and Token (Google Drive) are
// write-only; HasSecret says one is saved.
type Dest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // s3 | gdrive | sftp
	// s3: Cloudflare R2, Backblaze B2, Google Cloud Storage (HMAC keys), AWS, Wasabi…
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Region   string `json:"region,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	KeyID    string `json:"keyId,omitempty"`
	// gdrive: a folder in the Drive
	Folder string `json:"folder,omitempty"`
	// sftp
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	User      string `json:"user,omitempty"`
	Path      string `json:"path,omitempty"`
	PublicKey string `json:"publicKey,omitempty"` // read-only: goes in the target's authorized_keys

	Secret    string `json:"secret,omitempty"`
	Token     string `json:"token,omitempty"`
	HasSecret bool   `json:"hasSecret"`
	CreatedAt int64  `json:"createdAt"`
}

type storedDest struct {
	Dest
	SealedSecret   []byte `json:"sealedSecret,omitempty"` // s3 secret key, or the Drive token
	SealedPassword []byte `json:"sealedPassword"`         // the repositories' password
	SealedKey      []byte `json:"sealedKey,omitempty"`    // sftp private key
}

// Plan is what a server backs up, when and where.
type Plan struct {
	ServerID    string             `json:"serverId"`
	Enabled     bool               `json:"enabled"`
	Dest        string             `json:"dest"`
	Items       []proto.BackupItem `json:"items"`
	At          string             `json:"at"`       // "03:00", every day
	TimeZone    string             `json:"timeZone"` // of At
	KeepDaily   int                `json:"keepDaily"`
	KeepWeekly  int                `json:"keepWeekly"`
	KeepMonthly int                `json:"keepMonthly"`
	LastDay     string             `json:"lastDay,omitempty"` // date of the last scheduled run
}

// Run is one backup run as the UI shows it.
type Run struct {
	ID       string                   `json:"id"`
	ServerID string                   `json:"serverId"`
	Started  int64                    `json:"started"`
	Finished int64                    `json:"finished"`
	Status   string                   `json:"status"` // running | ok | partial | failed
	Error    string                   `json:"error,omitempty"`
	Items    []proto.BackupItemResult `json:"items"`
	Manual   bool                     `json:"manual"`
}

type Service struct {
	db        *sql.DB
	box       *secret.Box
	Incidents *incidents.Store
	Bus       *live.Bus
	Now       func() time.Time
	Name      func(serverID string) string

	Send         func(serverID, typ string, v any) error
	AgentVersion func(serverID string) string

	mu    sync.Mutex
	found map[string]proto.BackupFound // databases each server has, last scan
}

func New(d *sql.DB, box *secret.Box, inc *incidents.Store, bus *live.Bus) *Service {
	return &Service{db: d, box: box, Incidents: inc, Bus: bus, Now: time.Now, Name: func(id string) string { return id }, found: map[string]proto.BackupFound{}}
}

const (
	destsKey = "backups.dests"
	plansKey = "backups.plans"
)

func randID(prefix string, n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// ---------- places ----------

func (s *Service) stored() []storedDest {
	list := []storedDest{}
	settings.Load(s.db, destsKey, &list)
	return list
}

// Dests lists the places, without secrets.
func (s *Service) Dests() []Dest {
	out := []Dest{}
	for _, d := range s.stored() {
		x := d.Dest
		x.Secret, x.Token, x.HasSecret = "", "", len(d.SealedSecret) > 0
		out = append(out, x)
	}
	return out
}

var (
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)
	userRe   = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	folderRe = regexp.MustCompile(`^[A-Za-z0-9._/ -]{1,200}$`)
)

// SaveDest adds a place (no ID) or changes one. A new place gets its repositories'
// password, and an SFTP place its own SSH key.
func (s *Service) SaveDest(in Dest) (Dest, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 60 {
		return Dest{}, valid.Error("give the place a name (up to 60 characters)")
	}
	in.Prefix, in.Folder, in.Path = strings.Trim(in.Prefix, "/ "), strings.Trim(in.Folder, "/ "), strings.TrimRight(strings.TrimSpace(in.Path), "/")
	secretIn := ""
	switch in.Kind {
	case "s3":
		in.Endpoint = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
		if !strings.Contains(in.Endpoint, "://") {
			in.Endpoint = "https://" + in.Endpoint
		}
		u, err := url.Parse(in.Endpoint)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
			return Dest{}, valid.Error("the endpoint is the storage's address, like https://<account>.r2.cloudflarestorage.com")
		case !bucketRe.MatchString(in.Bucket):
			return Dest{}, valid.Error("enter the bucket's name")
		case in.KeyID == "" || len(in.KeyID) > 200:
			return Dest{}, valid.Error("enter the access key ID")
		case in.Prefix != "" && !folderRe.MatchString(in.Prefix):
			return Dest{}, valid.Error("the folder in the bucket may use letters, digits, - _ . and /")
		}
		secretIn = strings.TrimSpace(in.Secret)
	case "gdrive":
		if in.Folder == "" {
			in.Folder = "Fleetwatch backups"
		}
		if !folderRe.MatchString(in.Folder) {
			return Dest{}, valid.Error("the Drive folder may use letters, digits, spaces, - _ . and /")
		}
		if t := strings.TrimSpace(in.Token); t != "" {
			var tok struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			}
			if json.Unmarshal([]byte(t), &tok) != nil || tok.RefreshToken == "" {
				return Dest{}, valid.Error("paste the whole token rclone printed: {\"access_token\":…, \"refresh_token\":…}")
			}
			secretIn = t
		}
	case "sftp":
		if in.Port == 0 {
			in.Port = 22
		}
		switch {
		case !hostRe.MatchString(in.Host):
			return Dest{}, valid.Error("enter the server's address")
		case in.Port < 1 || in.Port > 65535:
			return Dest{}, valid.Error("the SSH port is 1-65535")
		case !userRe.MatchString(in.User):
			return Dest{}, valid.Error("enter the user to sign in as")
		case !strings.HasPrefix(in.Path, "/") || !folderRe.MatchString(in.Path):
			return Dest{}, valid.Error("the folder is a full path, like /backups")
		}
	default:
		return Dest{}, valid.Error("pick where backups go")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.stored()
	i := slices.IndexFunc(list, func(d storedDest) bool { return d.ID == in.ID })
	if in.ID != "" && i < 0 {
		return Dest{}, ErrNotFound
	}
	var d storedDest
	if i >= 0 {
		d = list[i]
		if d.Kind != in.Kind {
			return Dest{}, valid.Error("a place cannot change its kind; add a new one")
		}
	} else {
		if len(list) >= 20 {
			return Dest{}, valid.Error("at most 20 places")
		}
		d.ID, d.CreatedAt = randID("bd_", 6), s.Now().UnixMilli()
		pw := make([]byte, 32)
		rand.Read(pw)
		d.SealedPassword = s.box.Seal([]byte(base64.RawURLEncoding.EncodeToString(pw)), "backup.password")
		if in.Kind == "sftp" {
			pub, priv, _ := ed25519.GenerateKey(rand.Reader)
			block, err := ssh.MarshalPrivateKey(priv, "fleetwatch backups")
			if err != nil {
				return Dest{}, err
			}
			sp, _ := ssh.NewPublicKey(pub)
			d.SealedKey = s.box.Seal(pem.EncodeToMemory(block), "backup.sshkey")
			d.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " fleetwatch-backups"
		}
	}
	pub := d.PublicKey
	id, created := d.ID, d.CreatedAt
	d.Dest = in
	d.ID, d.CreatedAt, d.PublicKey = id, created, pub
	d.Secret, d.Token = "", ""
	if secretIn != "" {
		d.SealedSecret = s.box.Seal([]byte(secretIn), "backup.secret")
	}
	if in.Kind != "sftp" && len(d.SealedSecret) == 0 {
		return Dest{}, valid.Error(map[string]string{"s3": "enter the secret access key", "gdrive": "paste the Google Drive token"}[in.Kind])
	}
	if i >= 0 {
		list[i] = d
	} else {
		list = append(list, d)
	}
	if err := settings.Save(s.db, destsKey, list); err != nil {
		return Dest{}, err
	}
	out := d.Dest
	out.HasSecret = len(d.SealedSecret) > 0
	return out, nil
}

func (s *Service) DeleteDest(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.plans() {
		if p.Dest == id {
			return valid.Error("a server's plan still uses this place: change the plan first")
		}
	}
	list := s.stored()
	i := slices.IndexFunc(list, func(d storedDest) bool { return d.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	return settings.Save(s.db, destsKey, slices.Delete(list, i, i+1))
}

// Password is a place's repository password, for restoring without Fleetwatch.
func (s *Service) Password(id string) (string, error) {
	for _, d := range s.stored() {
		if d.ID == id {
			pw, err := s.box.Open(d.SealedPassword, "backup.password")
			return string(pw), err
		}
	}
	return "", ErrNotFound
}

// repo is what an agent needs to reach a server's repository at a place.
func (s *Service) repo(destID, serverID string) (proto.BackupRepo, error) {
	i := slices.IndexFunc(s.stored(), func(d storedDest) bool { return d.ID == destID })
	if i < 0 {
		return proto.BackupRepo{}, valid.Error("the plan's backup place is gone: pick another")
	}
	d := s.stored()[i]
	pw, err := s.box.Open(d.SealedPassword, "backup.password")
	if err != nil {
		return proto.BackupRepo{}, err
	}
	r := proto.BackupRepo{Password: string(pw), Env: map[string]string{}}
	var sec []byte
	if len(d.SealedSecret) > 0 {
		if sec, err = s.box.Open(d.SealedSecret, "backup.secret"); err != nil {
			return proto.BackupRepo{}, err
		}
	}
	switch d.Kind {
	case "s3":
		r.Repository = "s3:" + d.Endpoint + "/" + path.Join(d.Bucket, d.Prefix, serverID)
		r.Env["AWS_ACCESS_KEY_ID"], r.Env["AWS_SECRET_ACCESS_KEY"] = d.KeyID, string(sec)
		r.Env["AWS_DEFAULT_REGION"] = d.Region
		if d.Region == "" {
			r.Env["AWS_DEFAULT_REGION"] = "us-east-1"
		}
	case "gdrive":
		r.Repository, r.Rclone = "rclone:fw:"+path.Join(d.Folder, serverID), true
		r.Env["RCLONE_CONFIG_FW_TYPE"], r.Env["RCLONE_CONFIG_FW_SCOPE"], r.Env["RCLONE_CONFIG_FW_TOKEN"] = "drive", "drive", string(sec)
	case "sftp":
		key, err := s.box.Open(d.SealedKey, "backup.sshkey")
		if err != nil {
			return proto.BackupRepo{}, err
		}
		r.Repository, r.SSHKey, r.SSHPort = "sftp:"+d.User+"@"+d.Host+":"+path.Join(d.Path, serverID), string(key), d.Port
	}
	return r, nil
}

// ---------- plans ----------

func (s *Service) plans() map[string]Plan {
	m := map[string]Plan{}
	settings.Load(s.db, plansKey, &m)
	return m
}

func (s *Service) Plans() []Plan {
	out := []Plan{}
	for _, p := range s.plans() {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Plan) int { return strings.Compare(a.ServerID, b.ServerID) })
	return out
}

var (
	atRe       = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	dbNameRe   = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)
	snapshotRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
)

// SavePlan sets a server's plan.
func (s *Service) SavePlan(p Plan) (Plan, error) {
	if _, err := time.LoadLocation(p.TimeZone); err != nil || p.TimeZone == "" {
		p.TimeZone = "UTC"
	}
	switch {
	case !atRe.MatchString(p.At):
		return Plan{}, valid.Error("the time is HH:MM, like 03:00")
	case p.KeepDaily < 0 || p.KeepWeekly < 0 || p.KeepMonthly < 0 || p.KeepDaily > 365 || p.KeepWeekly > 520 || p.KeepMonthly > 240:
		return Plan{}, valid.Error("copies to keep: 0-365 daily, 0-520 weekly, 0-240 monthly")
	case p.KeepDaily+p.KeepWeekly+p.KeepMonthly == 0:
		return Plan{}, valid.Error("keep at least one copy")
	case len(p.Items) > 50:
		return Plan{}, valid.Error("at most 50 things per server")
	case p.Enabled && len(p.Items) == 0:
		return Plan{}, valid.Error("pick what to back up")
	}
	for _, it := range p.Items {
		ok := false
		switch it.Kind {
		case "postgres", "mysql":
			ok = dbNameRe.MatchString(it.Name)
		case "sqlite", "folder":
			ok = strings.HasPrefix(it.Name, "/") && len(it.Name) < 500 && !strings.ContainsAny(it.Name, "\n\r") && !strings.Contains(it.Name, "/../")
		}
		if !ok {
			return Plan{}, valid.Error(fmt.Sprintf("%s %q is not a valid name or full path", it.Kind, it.Name))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.ContainsFunc(s.stored(), func(d storedDest) bool { return d.ID == p.Dest }) {
		return Plan{}, valid.Error("pick where the backups go")
	}
	m := s.plans()
	p.LastDay = m[p.ServerID].LastDay
	if loc, err := time.LoadLocation(p.TimeZone); err == nil && p.LastDay == "" && s.Now().In(loc).Format("15:04") >= p.At {
		p.LastDay = s.Now().In(loc).Format("2006-01-02") // a new plan starts tomorrow, not right away
	}
	m[p.ServerID] = p
	return p, settings.Save(s.db, plansKey, m)
}

func (s *Service) DeletePlan(serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.plans()
	delete(m, serverID)
	return settings.Save(s.db, plansKey, m)
}

// ---------- running ----------

func (s *Service) ready(serverID string) error {
	if s.Send == nil {
		return errors.New("not connected")
	}
	if s.AgentVersion != nil && release.Newer(Agent, s.AgentVersion(serverID)) {
		return valid.Error("this server's agent is too old to back up; update the agent first")
	}
	return nil
}

// Scan asks a server which databases it has; the answer comes live as "backup.found".
func (s *Service) Scan(serverID string) error {
	if err := s.ready(serverID); err != nil {
		return err
	}
	if err := s.Send(serverID, proto.TypeBackupScan, struct{}{}); err != nil {
		return valid.Error("the server is not connected")
	}
	return nil
}

func (s *Service) OnFound(serverID string, data json.RawMessage) {
	var f proto.BackupFound
	if json.Unmarshal(data, &f) != nil {
		return
	}
	s.mu.Lock()
	s.found[serverID] = f
	s.mu.Unlock()
	s.Bus.Publish("backup.found", map[string]any{"serverId": serverID, "items": f.Items})
}

func (s *Service) Found() map[string]proto.BackupFound {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]proto.BackupFound{}
	for k, v := range s.found {
		out[k] = v
	}
	return out
}

// RunNow starts a server's backup (manual: from the page).
func (s *Service) RunNow(serverID string, manual bool) (string, error) {
	if err := s.ready(serverID); err != nil {
		return "", err
	}
	p, ok := s.plans()[serverID]
	if !ok || len(p.Items) == 0 {
		return "", valid.Error("set up what this server backs up first")
	}
	var running int
	s.db.QueryRow(`SELECT COUNT(*) FROM backup_runs WHERE server_id = ? AND status = 'running' AND started > ?`, serverID, s.Now().Add(-7*time.Hour).UnixMilli()).Scan(&running)
	if running > 0 {
		return "", valid.Error("a backup of this server is still running")
	}
	repo, err := s.repo(p.Dest, serverID)
	if err != nil {
		return "", err
	}
	id := randID("br_", 8)
	if err := s.Send(serverID, proto.TypeBackupRun, proto.BackupRun{RunID: id, Repo: repo, Items: p.Items, KeepDaily: p.KeepDaily, KeepWeekly: p.KeepWeekly, KeepMonthly: p.KeepMonthly}); err != nil {
		return "", valid.Error("the server is not connected")
	}
	raw, _ := json.Marshal(Run{Manual: manual, Items: []proto.BackupItemResult{}})
	s.db.Exec(`INSERT INTO backup_runs (id, server_id, started, status, data) VALUES (?, ?, ?, 'running', ?)`, id, serverID, s.Now().UnixMilli(), string(raw))
	s.Bus.Publish("backup.run", map[string]string{"serverId": serverID, "id": id, "status": "running"})
	return id, nil
}

// Refresh asks for the list of copies.
func (s *Service) Refresh(serverID string) error {
	if err := s.ready(serverID); err != nil {
		return err
	}
	p, ok := s.plans()[serverID]
	if !ok {
		return valid.Error("this server has no backup plan")
	}
	repo, err := s.repo(p.Dest, serverID)
	if err != nil {
		return err
	}
	return s.Send(serverID, proto.TypeBackupRun, proto.BackupRun{RunID: randID("bl_", 8), Repo: repo, ListOnly: true})
}

// OnResult records a run, keeps its list of copies, and opens or clears the incident.
func (s *Service) OnResult(serverID string, data json.RawMessage) {
	var r proto.BackupResult
	if json.Unmarshal(data, &r) != nil {
		return
	}
	if r.Error == "" || len(r.Snapshots) > 0 {
		raw, _ := json.Marshal(r.Snapshots)
		s.db.Exec(`INSERT INTO backup_snapshots (server_id, data, at) VALUES (?, ?, ?) ON CONFLICT (server_id) DO UPDATE SET data = excluded.data, at = excluded.at`,
			serverID, string(raw), s.Now().UnixMilli())
	}
	if r.ListOnly {
		s.Bus.Publish("backup.run", map[string]string{"serverId": serverID, "status": "listed", "error": r.Error})
		return
	}
	run := Run{ID: r.RunID, ServerID: serverID, Started: r.Started, Finished: r.Finished, Error: r.Error, Items: r.Items, Status: "ok"}
	failed := 0
	var why []string
	for _, it := range r.Items {
		if it.Error != "" {
			failed++
			why = append(why, it.Kind+" "+it.Name+": "+it.Error)
		}
	}
	switch {
	case r.Error != "" && len(r.Items) == 0, len(r.Items) > 0 && failed == len(r.Items):
		run.Status = "failed"
	case failed > 0 || r.Error != "":
		run.Status = "partial"
	}
	var old string
	if s.db.QueryRow(`SELECT data FROM backup_runs WHERE id = ?`, r.RunID).Scan(&old) == nil {
		var prev Run
		json.Unmarshal([]byte(old), &prev)
		run.Manual = prev.Manual
	}
	raw, _ := json.Marshal(run)
	s.db.Exec(`INSERT INTO backup_runs (id, server_id, started, finished, status, data) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET finished = excluded.finished, status = excluded.status, data = excluded.data`,
		r.RunID, serverID, r.Started, r.Finished, run.Status, string(raw))
	s.db.Exec(`DELETE FROM backup_runs WHERE server_id = ? AND id NOT IN (SELECT id FROM backup_runs WHERE server_id = ? ORDER BY started DESC LIMIT 100)`, serverID, serverID)
	key := "backup|" + serverID
	if run.Status == "ok" {
		s.Incidents.Clear(key, "the backup worked again")
	} else {
		if r.Error != "" {
			why = append([]string{r.Error}, why...)
		}
		s.Incidents.Ensure(incidents.Signal{Type: "backup_failed", Key: key, Severity: "critical", ServerID: serverID,
			Title: "Backup failed on " + s.Name(serverID), Sample: strings.Join(why, "\n")})
	}
	s.Bus.Publish("backup.run", map[string]string{"serverId": serverID, "id": r.RunID, "status": run.Status})
}

// Interrupted fails the runs a server had going when its agent (re)connects: the agent
// process that ran them is gone.
func (s *Service) Interrupted(serverID string) {
	raw, _ := json.Marshal(Run{Error: "the agent restarted during the backup", Items: []proto.BackupItemResult{}})
	s.db.Exec(`UPDATE backup_runs SET status = 'failed', finished = ?, data = ? WHERE server_id = ? AND status = 'running'`, s.Now().UnixMilli(), string(raw), serverID)
}

// Runs are the recent runs, newest first.
func (s *Service) Runs(allowed func(string) bool) []Run {
	out := []Run{}
	now := s.Now()
	rows, err := s.db.Query(`SELECT id, server_id, started, finished, status, data FROM backup_runs ORDER BY started DESC LIMIT 200`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r Run
		var raw string
		if rows.Scan(&r.ID, &r.ServerID, &r.Started, &r.Finished, &r.Status, &raw) != nil || (allowed != nil && !allowed(r.ServerID)) {
			continue
		}
		var d Run
		json.Unmarshal([]byte(raw), &d)
		r.Error, r.Items, r.Manual = d.Error, d.Items, d.Manual
		if r.Items == nil {
			r.Items = []proto.BackupItemResult{}
		}
		if r.Status == "running" && now.Sub(time.UnixMilli(r.Started)) > 7*time.Hour {
			r.Status, r.Error = "failed", "no answer from the server"
		}
		out = append(out, r)
	}
	return out
}

func (s *Service) Snapshots() map[string][]proto.BackupSnapshot {
	out := map[string][]proto.BackupSnapshot{}
	rows, err := s.db.Query(`SELECT server_id, data FROM backup_snapshots`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		var list []proto.BackupSnapshot
		if rows.Scan(&id, &raw) == nil && json.Unmarshal([]byte(raw), &list) == nil {
			out[id] = list
		}
	}
	return out
}

// Restore puts a copy back in a folder of its own on the server; the answer comes live
// as "backup.restored".
func (s *Service) Restore(serverID, snapshot string) (string, error) {
	if err := s.ready(serverID); err != nil {
		return "", err
	}
	if !snapshotRe.MatchString(snapshot) {
		return "", valid.Error("not a copy")
	}
	p, ok := s.plans()[serverID]
	if !ok {
		return "", valid.Error("this server has no backup plan")
	}
	repo, err := s.repo(p.Dest, serverID)
	if err != nil {
		return "", err
	}
	id := randID("bx_", 8)
	if err := s.Send(serverID, proto.TypeBackupRestore, proto.BackupRestore{RunID: id, Repo: repo, Snapshot: snapshot}); err != nil {
		return "", valid.Error("the server is not connected")
	}
	return id, nil
}

func (s *Service) OnRestored(serverID string, data json.RawMessage) {
	var r proto.BackupRestored
	if json.Unmarshal(data, &r) != nil {
		return
	}
	s.Bus.Publish("backup.restored", map[string]any{"serverId": serverID, "runId": r.RunID, "folder": r.Folder, "files": r.Files, "error": r.Error})
}

// Tick runs the plans that are due (every minute): once a day at their time.
func (s *Service) Tick() {
	now := s.Now()
	for id, p := range s.plans() {
		if !p.Enabled {
			continue
		}
		loc, err := time.LoadLocation(p.TimeZone)
		if err != nil {
			loc = time.UTC
		}
		local := now.In(loc)
		day := local.Format("2006-01-02")
		if day == p.LastDay || local.Format("15:04") < p.At {
			continue
		}
		s.mu.Lock()
		m := s.plans()
		if q, ok := m[id]; ok {
			q.LastDay = day
			m[id] = q
			settings.Save(s.db, plansKey, m)
		}
		s.mu.Unlock()
		if _, err := s.RunNow(id, false); err != nil {
			s.Incidents.Ensure(incidents.Signal{Type: "backup_failed", Key: "backup|" + id, Severity: "critical", ServerID: id,
				Title: "Backup failed on " + s.Name(id), Sample: "It could not start: " + err.Error()})
		}
	}
}
