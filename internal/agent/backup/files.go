package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"fleetwatch/internal/proto"
)

// Plain-file backups: each run is a folder named by its time (2026-10-21_030000) with
// the real files in it, so they can be opened right in Google Drive or the bucket:
// SQLite databases as .db files, PostgreSQL as .dump, MySQL as .sql, folders as they
// are. Not encrypted, and every run uploads everything again (no deduplication).

const stampLayout = "2006-01-02_150405"

// remote runs rclone against the backup's folder.
type remote struct {
	bin  string
	env  []string
	path string // fw:bucket/prefix/folder
}

func (r remote) rclone(ctx context.Context, timeout time.Duration, stdout io.Writer, args ...string) (string, error) {
	return run(ctx, timeout, stdout, r.bin, args, r.env...)
}

func prepareFiles(ctx context.Context, rp proto.BackupRepo) (remote, func(), error) {
	cleanup := func() {}
	if !strings.HasPrefix(rp.Repository, "fw:") {
		return remote{}, cleanup, errors.New("no backup storage set")
	}
	bin, err := tool(ctx, "rclone")
	if err != nil {
		return remote{}, cleanup, err
	}
	r := remote{bin: bin, path: strings.TrimRight(rp.Repository, "/"), env: []string{"HOME=/root", "RCLONE_CONFIG=/dev/null"}}
	for k, v := range rp.Env {
		r.env = append(r.env, k+"="+v)
	}
	if rp.SSHKey != "" {
		key := filepath.Join(Dir, "backup-ssh-key")
		if err := os.WriteFile(key, []byte(rp.SSHKey), 0o600); err != nil {
			return remote{}, cleanup, err
		}
		cleanup = func() { os.Remove(key) }
		r.env = append(r.env, "RCLONE_CONFIG_FW_KEY_FILE="+key)
	}
	return r, cleanup, nil
}

func runFiles(ctx context.Context, req proto.BackupRun, res proto.BackupResult) proto.BackupResult {
	r, cleanup, err := prepareFiles(ctx, req.Repo)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer cleanup()
	if req.ListOnly {
		if res.Snapshots, err = r.copies(ctx); err != nil {
			res.Error = err.Error()
		}
		return res
	}
	// A storage that does not answer fails fast (rclone retries for a long time).
	if _, err := r.rclone(ctx, 60*time.Second, nil, "mkdir", r.path); err != nil {
		res.Error = "cannot reach the backup storage: " + err.Error()
		return res
	}
	stamp := time.Now().Format(stampLayout)
	stage := filepath.Join(Dir, "files-stage", req.RunID)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		res.Error = err.Error()
		return res
	}
	defer os.RemoveAll(stage)
	used := map[string]bool{}
	name := func(n string) string { // unique file names in the run's folder
		base, ext := strings.TrimSuffix(n, filepath.Ext(n)), filepath.Ext(n)
		out := n
		for i := 2; used[out]; i++ {
			out = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		used[out] = true
		return out
	}
	var staged []int // items written to the stage folder, uploaded together
	for _, it := range req.Items {
		ir := proto.BackupItemResult{BackupItem: it}
		var err error
		switch it.Kind {
		case "postgres":
			ir.Snapshot = name("postgres-" + it.Name + ".dump")
			err = dumpTo(ctx, filepath.Join(stage, ir.Snapshot), "runuser", "-u", "postgres", "--", "pg_dump", "-Fc", it.Name)
		case "mysql":
			ir.Snapshot = name("mysql-" + it.Name + ".sql")
			err = dumpTo(ctx, filepath.Join(stage, ir.Snapshot), "mysqldump", "--single-transaction", "--routines", "--triggers", "--databases", it.Name)
		case "sqlite":
			ir.Snapshot = name(filepath.Base(it.Name))
			err = sqliteCopy(ctx, it.Name, filepath.Join(stage, ir.Snapshot))
		case "folder":
			ir.Snapshot = name(filepath.Base(it.Name)) + "/"
			if _, err = os.Stat(it.Name); err == nil {
				to := r.path + "/" + stamp + "/" + strings.TrimSuffix(ir.Snapshot, "/")
				if _, err = r.rclone(ctx, 6*time.Hour, nil, "copy", it.Name, to); err == nil {
					var size struct {
						Bytes int64 `json:"bytes"`
					}
					if out, e := r.rclone(ctx, 5*time.Minute, nil, "size", "--json", to); e == nil {
						json.Unmarshal([]byte(out), &size)
					}
					ir.Added = size.Bytes
				}
			} else {
				err = errors.New(it.Name + " does not exist")
			}
		default:
			err = errors.New("unknown kind " + it.Kind)
		}
		if err != nil {
			ir.Error, ir.Snapshot = err.Error(), ""
		} else if it.Kind != "folder" {
			staged = append(staged, len(res.Items))
			if st, e := os.Stat(filepath.Join(stage, ir.Snapshot)); e == nil {
				ir.Added = st.Size()
			}
		}
		res.Items = append(res.Items, ir)
	}
	if len(staged) > 0 {
		if _, err := r.rclone(ctx, 6*time.Hour, nil, "copy", stage, r.path+"/"+stamp); err != nil {
			for _, i := range staged {
				res.Items[i].Error, res.Items[i].Added = "upload failed: "+err.Error(), 0
			}
		}
	}
	if err := r.forget(ctx, req.KeepDaily, req.KeepWeekly, req.KeepMonthly); err != nil {
		res.Error = "the backup worked, but removing old copies failed: " + err.Error()
	}
	res.Snapshots, _ = r.copies(ctx)
	return res
}

// dumpTo writes a database tool's output to a file.
func dumpTo(ctx context.Context, file, name string, args ...string) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = run(ctx, 6*time.Hour, f, name, args, "HOME=/root")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// sqliteCopy makes a consistent copy with sqlite3's .backup when it is installed, or
// copies the file (with its write-ahead log, which SQLite reads back in).
func sqliteCopy(ctx context.Context, src, dst string) error {
	if _, err := exec.LookPath("sqlite3"); err == nil {
		_, err := run(ctx, time.Hour, nil, "sqlite3", []string{"-readonly", src, ".backup '" + strings.ReplaceAll(dst, "'", "''") + "'"})
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if _, err := os.Stat(src + "-wal"); err == nil {
		return copyFile(src+"-wal", dst+"-wal")
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copies lists the run folders, newest first, with their files and sizes.
func (r remote) copies(ctx context.Context) ([]proto.BackupSnapshot, error) {
	var buf strings.Builder
	if _, err := r.rclone(ctx, 5*time.Minute, &buf, "lsjson", "-R", "--files-only", "--no-mimetype", r.path); err != nil {
		if strings.Contains(err.Error(), "directory not found") {
			return []proto.BackupSnapshot{}, nil
		}
		return []proto.BackupSnapshot{}, err
	}
	var files []struct {
		Path string `json:"Path"`
		Size int64  `json:"Size"`
	}
	if err := json.Unmarshal([]byte(buf.String()), &files); err != nil {
		return []proto.BackupSnapshot{}, err
	}
	by := map[string]*proto.BackupSnapshot{}
	for _, f := range files {
		dir, rest, ok := strings.Cut(f.Path, "/")
		t, err := time.ParseInLocation(stampLayout, dir, time.Local)
		if !ok || err != nil {
			continue // not one of our run folders
		}
		s := by[dir]
		if s == nil {
			s = &proto.BackupSnapshot{ID: dir, Time: t.UnixMilli(), Paths: []string{}, Tags: []string{"files"}}
			by[dir] = s
		}
		s.Size += f.Size
		top, _, isDir := strings.Cut(rest, "/")
		if isDir {
			top += "/"
		}
		if !slices.Contains(s.Paths, top) && len(s.Paths) < 50 {
			s.Paths = append(s.Paths, top)
		}
	}
	out := []proto.BackupSnapshot{}
	for _, s := range by {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

// forget deletes the run folders the plan does not keep: the newest copy of each of the
// last N days, weeks and months (like restic's --keep-daily/weekly/monthly).
func (r remote) forget(ctx context.Context, daily, weekly, monthly int) error {
	list, err := r.copies(ctx)
	if err != nil || daily+weekly+monthly == 0 {
		return err
	}
	keep := keepSet(list, daily, weekly, monthly)
	for _, s := range list {
		if !keep[s.ID] {
			if _, err := r.rclone(ctx, 30*time.Minute, nil, "purge", r.path+"/"+s.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// keepSet picks the copies to keep from a newest-first list.
func keepSet(list []proto.BackupSnapshot, daily, weekly, monthly int) map[string]bool {
	keep := map[string]bool{}
	for _, rule := range []struct {
		n   int
		key func(time.Time) string
	}{
		{daily, func(t time.Time) string { return t.Format("2006-01-02") }},
		{weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprint(y, "-", w) }},
		{monthly, func(t time.Time) string { return t.Format("2006-01") }},
	} {
		seen := map[string]bool{}
		for _, s := range list {
			k := rule.key(time.UnixMilli(s.Time))
			if len(seen) >= rule.n {
				break
			}
			if !seen[k] {
				seen[k], keep[s.ID] = true, true
			}
		}
	}
	return keep
}

func restoreFiles(ctx context.Context, req proto.BackupRestore, res proto.BackupRestored) proto.BackupRestored {
	r, cleanup, err := prepareFiles(ctx, req.Repo)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer cleanup()
	target := filepath.Join(Dir, "restore", req.Snapshot+"-"+time.Now().Format("150405"))
	if _, err := r.rclone(ctx, 6*time.Hour, nil, "copy", r.path+"/"+req.Snapshot, target); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Folder = target
	filepath.WalkDir(target, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && len(res.Files) < 50 {
			res.Files = append(res.Files, path)
		}
		return nil
	})
	return res
}
