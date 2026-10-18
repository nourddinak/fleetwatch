// Package envfile reads and writes an app's .env files and web server files, only inside
// the app's own folder, and the server's own Caddy and Nginx configuration.
package envfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fleetwatch/internal/agent/sites"
	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

const maxSize = 256 << 10

var (
	nameRe  = regexp.MustCompile(`^(\.env(\.[A-Za-z0-9_-]{1,40})?|Caddyfile|nginx\.conf)$`)
	nginxRe = regexp.MustCompile(`^(nginx\.conf|sites-available/[A-Za-z0-9][A-Za-z0-9._-]{0,80}|conf\.d/[A-Za-z0-9][A-Za-z0-9._-]{0,80}\.conf)$`)
	// NginxDir is where Nginx keeps its configuration; tests point it elsewhere.
	NginxDir = "/etc/nginx"
	envRe    = regexp.MustCompile(`^\.env(\.[A-Za-z0-9_-]{1,40})?$`)
	// Checked and reloaded after the server's Caddyfile is saved; tests replace it.
	checkCaddy = sites.CheckAndReload
	checkNginx = func() error {
		if out, err := exec.Command("nginx", "-t").CombinedOutput(); err != nil {
			return fmt.Errorf("Nginx rejected the configuration: %s", lastLine(out, err))
		}
		if out, err := exec.Command("systemctl", "reload", "nginx").CombinedOutput(); err != nil {
			return fmt.Errorf("could not reload Nginx: %s", lastLine(out, err))
		}
		return nil
	}
)

func lastLine(out []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}

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
	if req.Caddy {
		req.Web = "caddy"
	}
	// A web server's own files: the agent decides where they are, whatever the request says.
	var check func() error
	switch req.Web {
	case "caddy":
		req.Dir, req.Name = filepath.Dir(sites.Caddyfile), filepath.Base(sites.Caddyfile)
		if _, err := os.Stat(sites.Caddyfile); err != nil {
			return errors.New("Caddy is not set up on this server (" + sites.Caddyfile + " is missing)")
		}
		check = checkCaddy
	case "nginx":
		req.Dir = NginxDir
		if req.Name == "" {
			req.Name = "nginx.conf"
		}
		if _, err := os.Stat(filepath.Join(NginxDir, "nginx.conf")); err != nil {
			return errors.New("Nginx is not set up on this server (" + NginxDir + "/nginx.conf is missing)")
		}
		if !nginxRe.MatchString(req.Name) {
			return errors.New("only nginx.conf, sites-available/<name> and conf.d/<name>.conf can be opened")
		}
		check = checkNginx
	case "":
	default:
		return errors.New("unknown web server")
	}
	res.Name = req.Name
	if !filepath.IsAbs(req.Dir) {
		return errors.New("the app has no folder on the server")
	}
	dir, err := filepath.EvalSymlinks(req.Dir)
	if err != nil {
		return errors.New("the app's folder does not exist on the server")
	}
	if req.Web == "" && req.Name != "" && !nameRe.MatchString(req.Name) {
		return errors.New("only .env files and the Caddyfile can be opened")
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
		old, readErr := root.ReadFile(req.Name)
		if err := write(root, dir, req.Name, req.Content); err != nil {
			return err
		}
		if check != nil {
			if err := check(); err != nil {
				// The web server keeps running on what it had.
				if readErr == nil {
					root.WriteFile(req.Name, old, 0o644)
				} else {
					root.Remove(req.Name)
				}
				check()
				return err
			}
		}
	}
	switch req.Web {
	case "nginx":
		res.Files = listNginx()
	case "caddy":
		res.Files = []string{}
	default:
		res.Files = list(dir)
	}
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
		if fi, err := os.Lstat(m); err == nil && fi.Mode().IsRegular() && envRe.MatchString(n) && filepath.Ext(n) != ".bak" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// listNginx names Nginx's configuration files: nginx.conf, sites-available and conf.d.
func listNginx() []string {
	out := []string{"nginx.conf"}
	for _, pat := range []string{"sites-available/*", "conf.d/*.conf"} {
		matches, _ := filepath.Glob(filepath.Join(NginxDir, pat))
		for _, m := range matches {
			rel, _ := filepath.Rel(NginxDir, m)
			rel = filepath.ToSlash(rel)
			if fi, err := os.Lstat(m); err == nil && fi.Mode().IsRegular() && nginxRe.MatchString(rel) && filepath.Ext(rel) != ".bak" {
				out = append(out, rel)
			}
		}
	}
	return out
}
