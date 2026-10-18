// Package envfile reads and writes an app's .env files, only inside the app's own
// folder and only files named .env or .env.<something>.
package envfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

const maxSize = 256 << 10

var nameRe = regexp.MustCompile(`^\.env(\.[A-Za-z0-9_-]{1,40})?$`)

// Handle answers one request: the list of .env files in the folder, plus the content
// of the one asked for (after writing it, for a write).
func Handle(req proto.EnvFile) proto.EnvFileResult {
	res := proto.EnvFileResult{RunID: req.RunID, Name: req.Name}
	if err := handle(req, &res); err != nil {
		res.Error = err.Error()
	}
	return res
}

func handle(req proto.EnvFile, res *proto.EnvFileResult) error {
	if !filepath.IsAbs(req.Dir) {
		return errors.New("the app has no folder on the server")
	}
	dir, err := filepath.EvalSymlinks(req.Dir)
	if err != nil {
		return errors.New("the app's folder does not exist on the server")
	}
	if req.Name != "" && !nameRe.MatchString(req.Name) {
		return errors.New("only files named .env or .env.something can be opened")
	}
	root, err := os.OpenRoot(dir) // nothing outside the folder can be reached
	if err != nil {
		return err
	}
	defer root.Close()

	if req.Write {
		if len(req.Content) > maxSize {
			return errors.New("the file is larger than 256 KB")
		}
		if err := write(root, dir, req.Name, req.Content); err != nil {
			return err
		}
	}
	res.Files = list(dir)
	if req.Name == "" {
		return nil
	}
	fi, err := root.Lstat(req.Name)
	if errors.Is(err, os.ErrNotExist) {
		return nil // not there yet: it can be created
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New(req.Name + " is not a regular file")
	}
	if fi.Size() > maxSize {
		return errors.New(req.Name + " is larger than 256 KB")
	}
	f, err := root.Open(req.Name)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	res.Exists, res.Content = true, string(b)
	return err
}

// write keeps the file's owner and permissions (it is rewritten in place) and the
// previous content as <name>.bak; a new file belongs to the folder's owner, mode 600.
func write(root *os.Root, dir, name, content string) error {
	if name == "" {
		return errors.New("which file?")
	}
	fi, err := root.Lstat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if uid, gid, _, ok := sysexec.Owner(dir); ok {
			f.Chown(uid, gid)
		}
		_, err = f.WriteString(content)
		return errors.Join(err, f.Close())
	case err != nil:
		return err
	case !fi.Mode().IsRegular():
		return errors.New(name + " is not a regular file")
	}
	old, err := root.ReadFile(name)
	if err != nil {
		return err
	}
	if err := root.WriteFile(name+".bak", old, fi.Mode().Perm()&0o600); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	return errors.Join(err, f.Close())
}

// list names the .env files of the folder (regular files only, backups left out).
func list(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, ".env*"))
	out := []string{}
	for _, m := range matches {
		n := filepath.Base(m)
		if fi, err := os.Lstat(m); err == nil && fi.Mode().IsRegular() && nameRe.MatchString(n) && filepath.Ext(n) != ".bak" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
