// Package sites publishes static sites on this server: it downloads a repository
// snapshot, unpacks it as a new release under /var/www/.fleetwatch/<name>/, points
// /var/www/<name> at it in one step, and gives Caddy a site file for it.
package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"fleetwatch/internal/proto"
)

// Paths and commands; tests point them elsewhere.
var (
	WWW       = "/var/www"
	Caddyfile = "/etc/caddy/Caddyfile"
	CaddyDir  = "/etc/caddy/fleetwatch-sites"
	validate  = func() ([]byte, error) {
		return exec.Command("caddy", "validate", "--config", Caddyfile, "--adapter", "caddyfile").CombinedOutput()
	}
	reload = func() ([]byte, error) { return exec.Command("systemctl", "reload", "caddy").CombinedOutput() }
	client = &http.Client{Timeout: 15 * time.Minute}
)

const (
	keepReleases = 3
	maxBytes     = 1 << 30 // a site's files, unpacked
	maxFiles     = 100_000
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9-]{2,63}$`)
)

func releases(name string) string { return filepath.Join(WWW, ".fleetwatch", name) }
func live(name string) string     { return filepath.Join(WWW, name) }
func siteFile(name string) string { return filepath.Join(CaddyDir, name+".caddy") }

// Deploy publishes one snapshot of a site.
func Deploy(ctx context.Context, req proto.SiteDeploy) proto.SiteResult {
	res := proto.SiteResult{RunID: req.RunID, Name: req.Name}
	var log bytes.Buffer
	err := deploy(ctx, req, &log, &res)
	res.OK, res.Log = err == nil, log.String()
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func deploy(ctx context.Context, req proto.SiteDeploy, log *bytes.Buffer, res *proto.SiteResult) error {
	if !nameRe.MatchString(req.Name) {
		return errors.New("invalid site name")
	}
	for _, d := range req.Domains {
		if !hostRe.MatchString(d) {
			return fmt.Errorf("invalid domain %q", d)
		}
	}
	dir := path.Clean("/" + req.Dir)[1:] // "" = the whole snapshot
	u, err := url.Parse(req.Archive)
	if err != nil || u.Scheme != "https" {
		return errors.New("the download address must be https")
	}
	if fi, err := os.Lstat(live(req.Name)); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s already exists and was not made by Fleetwatch; move it away or pick another name", live(req.Name))
	}

	// 1. Download the snapshot.
	fmt.Fprintf(log, "Downloading %s\n", u.Path)
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, req.Archive, nil)
	if req.Token != "" {
		hreq.Header.Set("Authorization", "token "+req.Token)
	}
	hreq.Header.Set("User-Agent", "ops-agent")
	resp, err := client.Do(hreq)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// 2. Unpack it as a new release.
	rel := filepath.Join(releases(req.Name), time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(rel, 0o755); err != nil {
		return err
	}
	files, size, err := unpack(resp.Body, rel, dir)
	if err != nil {
		os.RemoveAll(rel)
		return err
	}
	fmt.Fprintf(log, "Unpacked %d files (%.1f MB)\n", files, float64(size)/1e6)
	if _, err := os.Stat(filepath.Join(rel, "index.html")); err != nil {
		fmt.Fprintln(log, "Warning: there is no index.html at the top of the site")
	}
	// A Caddyfile in the site is the site's own Caddy configuration: used, never served.
	var own []byte
	for _, f := range []string{".fleetwatch.Caddyfile", "Caddyfile"} {
		if b, err := os.ReadFile(filepath.Join(rel, f)); err == nil {
			if own == nil {
				own = b
			}
			os.Remove(filepath.Join(rel, f))
		}
	}

	// 3. Switch /var/www/<name> to the new release in one step.
	tmp := filepath.Join(WWW, "."+req.Name+".next")
	os.Remove(tmp)
	if err := os.Symlink(rel, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, live(req.Name)); err != nil {
		os.Remove(tmp)
		return err
	}
	res.Release = filepath.Base(rel)
	fmt.Fprintf(log, "Live at %s → %s\n", live(req.Name), rel)
	prune(req.Name, rel)

	// 4. Caddy.
	var site string
	switch {
	case own != nil:
		site = fmt.Sprintf("# Site %s, from the Caddyfile in its repository (via Fleetwatch).\n%s\n", req.Name, own)
		res.Caddy = "repository"
	case len(req.Domains) > 0:
		site = fmt.Sprintf("# Site %s, written by Fleetwatch. Changes here are overwritten.\n%s {\n\troot * %s\n\tencode zstd gzip\n", req.Name, strings.Join(req.Domains, ", "), live(req.Name))
		if req.SPA {
			site += "\ttry_files {path} {path}/ /index.html\n"
		}
		site += "\tfile_server\n}\n"
		res.Caddy = "generated"
	default:
		fmt.Fprintln(log, "No address: files only, Caddy unchanged")
		return nil
	}
	if err := applyCaddy(req.Name, site); err != nil {
		return err
	}
	fmt.Fprintln(log, "Caddy reloaded")
	return nil
}

// unpack writes the files of a GitHub tarball (first path part is the snapshot folder)
// that are under dir into dst. Links, devices and paths leaving dst are skipped.
func unpack(r io.Reader, dst, dir string) (files int, size int64, err error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, fmt.Errorf("not a tar.gz archive: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return files, size, fmt.Errorf("archive: %w", err)
		}
		_, rest, _ := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/") // drop "owner-repo-sha/"
		if dir != "" {
			if rest != dir && !strings.HasPrefix(rest, dir+"/") {
				continue
			}
			rest = strings.TrimPrefix(strings.TrimPrefix(rest, dir), "/")
		}
		if rest == "" || path.IsAbs(rest) || slices.Contains(strings.Split(rest, "/"), "..") || strings.HasPrefix(rest, ".git/") {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rest))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, size, err
			}
		case tar.TypeReg:
			if files++; files > maxFiles {
				return files, size, errors.New("too many files for a static site")
			}
			if size += h.Size; size > maxBytes {
				return files, size, errors.New("the site is larger than 1 GB")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, size, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return files, size, err
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			f.Close()
			if err != nil {
				return files, size, err
			}
		}
	}
	if files == 0 {
		if dir != "" {
			return 0, 0, fmt.Errorf("the folder %q is empty or missing in the repository", dir)
		}
		return 0, 0, errors.New("the repository snapshot is empty")
	}
	return files, size, nil
}

// prune keeps the newest releases (and always the live one).
func prune(name, liveRel string) {
	entries, err := os.ReadDir(releases(name))
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs))) // names are timestamps
	for i, d := range dirs {
		if p := filepath.Join(releases(name), d); i >= keepReleases && p != liveRel {
			os.RemoveAll(p)
		}
	}
}

// applyCaddy writes the site file, makes sure the Caddyfile imports the site folder,
// checks the result and reloads Caddy; on a rejected configuration it puts things back.
func applyCaddy(name, site string) error {
	main, err := os.ReadFile(Caddyfile)
	if err != nil {
		return fmt.Errorf("Caddy is not installed on this server (%s is missing): sudo apt install caddy", Caddyfile)
	}
	if out, err := validate(); err != nil {
		return fmt.Errorf("the current Caddy configuration already has an error, so the site was not added: %s", lastLine(out, err))
	}
	if err := os.MkdirAll(CaddyDir, 0o755); err != nil {
		return err
	}
	old, oldErr := os.ReadFile(siteFile(name))
	if err := os.WriteFile(siteFile(name), []byte(site), 0o644); err != nil {
		return err
	}
	line := "import " + CaddyDir + "/*.caddy"
	added := false
	if !slices.ContainsFunc(strings.Split(string(main), "\n"), func(l string) bool { return strings.TrimSpace(l) == line }) {
		text := string(main)
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if err := os.WriteFile(Caddyfile, []byte(text+line+"\n"), 0o644); err != nil {
			return err
		}
		added = true
	}
	if out, err := validate(); err != nil {
		if oldErr == nil {
			os.WriteFile(siteFile(name), old, 0o644)
		} else {
			os.Remove(siteFile(name))
		}
		if added {
			os.WriteFile(Caddyfile, main, 0o644)
		}
		return fmt.Errorf("Caddy rejected the site configuration, so it was not changed: %s", lastLine(out, err))
	}
	if out, err := reload(); err != nil {
		return fmt.Errorf("could not reload Caddy: %s", lastLine(out, err))
	}
	return nil
}

// Remove takes a site down: its Caddy site, the link and every release.
func Remove(req proto.SiteRemove) proto.SiteResult {
	res := proto.SiteResult{RunID: req.RunID, Name: req.Name}
	if !nameRe.MatchString(req.Name) {
		res.Error = "invalid site name"
		return res
	}
	if _, err := os.Stat(siteFile(req.Name)); err == nil {
		os.Remove(siteFile(req.Name))
		if out, err := validate(); err == nil {
			reload()
		} else {
			res.Log = "Caddy: " + lastLine(out, err) + "\n"
		}
	}
	if fi, err := os.Lstat(live(req.Name)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		os.Remove(live(req.Name))
	}
	os.RemoveAll(releases(req.Name))
	res.OK = true
	return res
}

func lastLine(out []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return err.Error()
}
