// Package backup backs up a server's databases and folders with restic: it finds the
// databases, installs restic (and rclone for Google Drive) by itself, sends each item to
// the repository, keeps the copies the plan asks for, and restores one when asked.
package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/proto"
)

// Dir holds restic, rclone, the SFTP key and restored copies.
var Dir = "/var/lib/ops-agent"

const (
	resticVersion = "0.17.3"
	rcloneVersion = "1.68.2"
	tag           = "fleetwatch"
)

var running sync.Mutex // one backup or restore at a time

// ---------- finding databases ----------

// Find lists the databases on this server: PostgreSQL and MySQL/MariaDB ones (through
// their command-line tools, as their admin users) and SQLite files under the usual app
// folders.
func Find(ctx context.Context) proto.BackupFound {
	var out proto.BackupFound
	if raw, err := run(ctx, 20*time.Second, nil, "runuser", []string{"-u", "postgres", "--", "psql", "-AtXc",
		"SELECT datname, pg_database_size(datname) FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres'"}); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
			if name, size, ok := strings.Cut(l, "|"); ok {
				n, _ := strconv.ParseInt(size, 10, 64)
				out.Items = append(out.Items, proto.BackupItem{Kind: "postgres", Name: name, Size: n})
			}
		}
	}
	if raw, err := run(ctx, 20*time.Second, nil, "mysql", []string{"-N", "-B", "-e",
		"SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0) FROM information_schema.schemata s LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name " +
			"WHERE s.schema_name NOT IN ('information_schema', 'performance_schema', 'mysql', 'sys') GROUP BY s.schema_name"}, "HOME=/root"); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
			if f := strings.Split(l, "\t"); len(f) == 2 {
				n, _ := strconv.ParseInt(f[1], 10, 64)
				out.Items = append(out.Items, proto.BackupItem{Kind: "mysql", Name: f[0], Size: n})
			}
		}
	}
	out.Items = append(out.Items, sqliteFiles()...)
	if _, err := os.Stat(filepath.Join(Dir, "bin", "restic")); err == nil {
		out.Restic = resticVersion
	}
	return out
}

var sqliteRoots = []string{"/srv", "/var/www", "/opt", "/home", "/root", "/var/lib"}

// sqliteFiles finds SQLite databases (by their header) up to 5 folders deep.
func sqliteFiles() []proto.BackupItem {
	var out []proto.BackupItem
	skip := []string{"node_modules", ".git", "vendor", ".cache", "proc", "docker", "containerd", "snapd", "apt", "dpkg", "ops-agent", "systemd", "ops-server",
		"PackageKit", "command-not-found", "fwupd", "ubuntu-advantage", "polkit-1", "plocate", "mlocate", "NetworkManager", "AccountsService", "update-notifier"}
	for _, root := range sqliteRoots {
		depth := strings.Count(root, "/")
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || len(out) >= 50 {
				return filepath.SkipDir
			}
			if d.IsDir() {
				if strings.Count(path, "/")-depth > 5 || (path != root && (slices.Contains(skip, d.Name()) || strings.HasPrefix(d.Name(), "."))) {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".db" && ext != ".sqlite" && ext != ".sqlite3" {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() < 512 || !d.Type().IsRegular() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			head := make([]byte, 16)
			io.ReadFull(f, head)
			f.Close()
			if string(head) == "SQLite format 3\x00" {
				out = append(out, proto.BackupItem{Kind: "sqlite", Name: path, Size: info.Size()})
			}
			return nil
		})
	}
	return out
}

// ---------- running a backup ----------

// Run backs up each item to the repository (creating it the first time), then forgets
// the copies the plan does not keep and lists the rest.
func Run(ctx context.Context, req proto.BackupRun) (res proto.BackupResult) {
	res = proto.BackupResult{RunID: req.RunID, Started: time.Now().UnixMilli(), Items: []proto.BackupItemResult{}, Snapshots: []proto.BackupSnapshot{}, ListOnly: req.ListOnly}
	defer func() { res.Finished = time.Now().UnixMilli() }()
	if !running.TryLock() {
		res.Error = "another backup or restore is still running on this server"
		return res
	}
	defer running.Unlock()
	r, cleanup, err := prepare(ctx, req.Repo)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer cleanup()
	if req.ListOnly {
		res.Snapshots, err = r.snapshots(ctx)
		if err != nil {
			res.Error = err.Error()
		}
		return res
	}
	if err := r.init(ctx); err != nil {
		res.Error = err.Error()
		return res
	}
	for _, it := range req.Items {
		ir := proto.BackupItemResult{BackupItem: it}
		ir.Snapshot, ir.Added, err = r.backup(ctx, it)
		if err != nil {
			ir.Error = err.Error()
		}
		res.Items = append(res.Items, ir)
	}
	if req.KeepDaily+req.KeepWeekly+req.KeepMonthly > 0 {
		args := []string{"forget", "--tag", tag, "--group-by", "paths", "--prune"}
		for _, k := range []struct {
			flag string
			n    int
		}{{"--keep-daily", req.KeepDaily}, {"--keep-weekly", req.KeepWeekly}, {"--keep-monthly", req.KeepMonthly}} {
			if k.n > 0 {
				args = append(args, k.flag, strconv.Itoa(k.n))
			}
		}
		if _, err := r.restic(ctx, 2*time.Hour, nil, args...); err != nil {
			res.Error = "the backup worked, but removing old copies failed: " + err.Error()
		}
	}
	res.Snapshots, _ = r.snapshots(ctx)
	return res
}

// repo runs restic against one repository.
type repo struct {
	bin  string
	env  []string
	opts []string
}

func (r repo) restic(ctx context.Context, timeout time.Duration, stdout io.Writer, args ...string) (string, error) {
	return run(ctx, timeout, stdout, r.bin, append(append([]string{}, r.opts...), args...), r.env...)
}

func (r repo) init(ctx context.Context) error {
	_, err := r.restic(ctx, 45*time.Second, nil, "cat", "config")
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "timed out"): // restic retries a storage that does not answer for many minutes
		return errors.New("cannot reach the backup storage: check its address and keys (it did not answer in 45 seconds)")
	}
	if _, err := r.restic(ctx, 3*time.Minute, nil, "init"); err != nil {
		return fmt.Errorf("cannot open or create the backup repository: %w", err)
	}
	return nil
}

// backup sends one item; databases go as dumps straight from their tools (nothing on disk).
func (r repo) backup(ctx context.Context, it proto.BackupItem) (string, int64, error) {
	args := []string{"backup", "--json", "--tag", tag, "--tag", it.Kind}
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(strings.TrimPrefix(it.Name, "/"))
	switch it.Kind {
	case "postgres":
		args = append(args, "--stdin-filename", "postgres/"+safe+".dump", "--stdin-from-command", "--", "runuser", "-u", "postgres", "--", "pg_dump", "-Fc", it.Name)
	case "mysql":
		args = append(args, "--stdin-filename", "mysql/"+safe+".sql", "--stdin-from-command", "--", "mysqldump", "--single-transaction", "--routines", "--triggers", "--databases", it.Name)
	case "sqlite":
		if _, err := exec.LookPath("sqlite3"); err == nil {
			args = append(args, "--stdin-filename", "sqlite/"+safe+".sql", "--stdin-from-command", "--", "sqlite3", "-readonly", it.Name, ".dump")
		} else { // without the sqlite3 tool: the file itself (and its write-ahead log)
			args = append(args, it.Name)
			if _, err := os.Stat(it.Name + "-wal"); err == nil {
				args = append(args, it.Name+"-wal")
			}
		}
	case "folder":
		if !filepath.IsAbs(it.Name) {
			return "", 0, errors.New("a folder must be a full path")
		}
		args = append(args, "--exclude-caches", it.Name)
	default:
		return "", 0, errors.New("unknown kind " + it.Kind)
	}
	var buf bytes.Buffer
	if _, err := r.restic(ctx, 6*time.Hour, &buf, args...); err != nil {
		return "", 0, err
	}
	var sum struct {
		Type       string `json:"message_type"`
		SnapshotID string `json:"snapshot_id"`
		Added      int64  `json:"data_added"`
	}
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var m struct {
			Type string `json:"message_type"`
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Type == "summary" {
			json.Unmarshal(sc.Bytes(), &sum)
		}
	}
	return sum.SnapshotID, sum.Added, nil
}

func (r repo) snapshots(ctx context.Context) ([]proto.BackupSnapshot, error) {
	var buf bytes.Buffer
	if _, err := r.restic(ctx, 5*time.Minute, &buf, "snapshots", "--json", "--tag", tag); err != nil {
		return []proto.BackupSnapshot{}, err
	}
	var list []struct {
		ShortID string    `json:"short_id"`
		Time    time.Time `json:"time"`
		Paths   []string  `json:"paths"`
		Tags    []string  `json:"tags"`
		Summary *struct {
			Bytes int64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &list); err != nil {
		return []proto.BackupSnapshot{}, err
	}
	out := []proto.BackupSnapshot{}
	for i := len(list) - 1; i >= 0 && len(out) < 300; i-- { // newest first
		s := list[i]
		snap := proto.BackupSnapshot{ID: s.ShortID, Time: s.Time.UnixMilli(), Paths: s.Paths, Tags: s.Tags}
		if s.Summary != nil {
			snap.Size = s.Summary.Bytes
		}
		out = append(out, snap)
	}
	return out, nil
}

// prepare installs what the repository needs and builds restic's settings.
func prepare(ctx context.Context, rp proto.BackupRepo) (repo, func(), error) {
	cleanup := func() {}
	if rp.Repository == "" || rp.Password == "" {
		return repo{}, cleanup, errors.New("no backup storage set")
	}
	bin, err := tool(ctx, "restic")
	if err != nil {
		return repo{}, cleanup, err
	}
	r := repo{bin: bin, env: []string{"RESTIC_REPOSITORY=" + rp.Repository, "RESTIC_PASSWORD=" + rp.Password, "RESTIC_CACHE_DIR=" + filepath.Join(Dir, "restic-cache"), "HOME=/root"}}
	for k, v := range rp.Env {
		r.env = append(r.env, k+"="+v)
	}
	if rp.Rclone {
		rc, err := tool(ctx, "rclone")
		if err != nil {
			return repo{}, cleanup, err
		}
		r.opts = append(r.opts, "-o", "rclone.program="+rc)
	}
	if rp.SSHKey != "" {
		key := filepath.Join(Dir, "backup-ssh-key")
		if err := os.WriteFile(key, []byte(rp.SSHKey), 0o600); err != nil {
			return repo{}, cleanup, err
		}
		cleanup = func() { os.Remove(key) }
		userHost, _, _ := strings.Cut(strings.TrimPrefix(rp.Repository, "sftp:"), ":")
		port := rp.SSHPort
		if port == 0 {
			port = 22
		}
		r.opts = append(r.opts, "-o", fmt.Sprintf("sftp.command=ssh -i %s -p %d -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s -o BatchMode=yes %s -s sftp",
			key, port, filepath.Join(Dir, "backup-known-hosts"), userHost))
	}
	return r, cleanup, nil
}

// ---------- restoring ----------

// Restore puts a copy's files in a folder of their own (never over the live data).
func Restore(ctx context.Context, req proto.BackupRestore) proto.BackupRestored {
	res := proto.BackupRestored{RunID: req.RunID}
	if !running.TryLock() {
		res.Error = "another backup or restore is still running on this server"
		return res
	}
	defer running.Unlock()
	if strings.ContainsAny(req.Snapshot, "/ .") || req.Snapshot == "" {
		res.Error = "not a copy"
		return res
	}
	r, cleanup, err := prepare(ctx, req.Repo)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer cleanup()
	target := filepath.Join(Dir, "restore", req.Snapshot+"-"+time.Now().Format("20060102-150405"))
	if _, err := r.restic(ctx, 6*time.Hour, nil, "restore", req.Snapshot, "--target", target); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Folder = target
	filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && len(res.Files) < 50 {
			res.Files = append(res.Files, path)
		}
		return nil
	})
	return res
}

// ---------- tools ----------

var toolMu sync.Mutex

// tool finds restic or rclone, or downloads the release from GitHub (checked against
// the release's SHA256SUMS) into Dir/bin.
func tool(ctx context.Context, name string) (string, error) {
	toolMu.Lock()
	defer toolMu.Unlock()
	own := filepath.Join(Dir, "bin", name)
	if _, err := os.Stat(own); err == nil {
		return own, nil
	}
	if p, err := exec.LookPath(name); err == nil && name == "rclone" { // restic: our own, for --stdin-from-command (0.17+)
		return p, nil
	}
	arch := runtime.GOARCH
	var url, sums, file string
	switch name {
	case "restic":
		file = fmt.Sprintf("restic_%s_linux_%s.bz2", resticVersion, arch)
		url = "https://github.com/restic/restic/releases/download/v" + resticVersion + "/" + file
		sums = "https://github.com/restic/restic/releases/download/v" + resticVersion + "/SHA256SUMS"
	case "rclone":
		file = fmt.Sprintf("rclone-v%s-linux-%s.zip", rcloneVersion, arch)
		url = "https://github.com/rclone/rclone/releases/download/v" + rcloneVersion + "/" + file
		sums = "https://github.com/rclone/rclone/releases/download/v" + rcloneVersion + "/SHA256SUMS"
	default:
		return "", errors.New("unknown tool " + name)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	sumList, err := download(ctx, sums, 1<<20)
	if err != nil {
		return "", fmt.Errorf("installing %s: %w", name, err)
	}
	want := ""
	for _, l := range strings.Split(string(sumList), "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("installing %s: %s is not in its checksums", name, file)
	}
	data, err := download(ctx, url, 200<<20)
	if err != nil {
		return "", fmt.Errorf("installing %s: %w", name, err)
	}
	if h := sha256.Sum256(data); hex.EncodeToString(h[:]) != want {
		return "", fmt.Errorf("installing %s: the download does not match its checksum", name)
	}
	var bin []byte
	if name == "restic" {
		bin, err = io.ReadAll(io.LimitReader(bzip2.NewReader(bytes.NewReader(data)), 300<<20))
	} else {
		bin, err = fromZip(data, "rclone")
	}
	if err != nil {
		return "", fmt.Errorf("installing %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		return "", err
	}
	tmp := own + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	return own, os.Rename(tmp, own)
}

func download(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, max))
}

func fromZip(data []byte, name string) ([]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range z.File {
		if filepath.Base(f.Name) == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 300<<20))
		}
	}
	return nil, errors.New(name + " is not in the download")
}

// run runs a command with extra environment; its output goes to stdout when given, and
// the last of its error output makes the error.
func run(ctx context.Context, timeout time.Duration, stdout io.Writer, name string, args []string, env ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil && !filepath.IsAbs(name) {
		return "", fmt.Errorf("%s is not installed", name)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}, env...)
	var out bytes.Buffer
	if stdout != nil {
		cmd.Stdout = stdout
	} else {
		cmd.Stdout = &limited{&out, 1 << 20}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limited{&stderr, 64 << 10}
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), fmt.Errorf("%s timed out after %s", filepath.Base(name), timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if lines := strings.Split(msg, "\n"); len(lines) > 4 {
			msg = strings.Join(lines[len(lines)-4:], "\n")
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

type limited struct {
	b   *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
