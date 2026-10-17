package sites

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// LocalRoot is where the agent on the dashboard's own server publishes sites.
var LocalRoot = "/var/www"

// sandbox is the Content-Security-Policy of every file served under the dashboard's
// domain: the page gets an origin of its own (no allow-same-origin), so its scripts
// cannot use the dashboard session, read its pages or call its API as you.
const sandbox = "sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads allow-pointer-lock"

// localDir is the live release of a site published on this machine ("" when the site
// is on another server). Only Fleetwatch's own releases are served, never another
// folder that happens to have the name.
func localDir(name string) string {
	if !nameRe.MatchString(name) {
		return ""
	}
	real, err := filepath.EvalSymlinks(filepath.Join(LocalRoot, name))
	if err != nil || !strings.HasPrefix(real, filepath.Join(LocalRoot, ".fleetwatch", name)+string(filepath.Separator)) {
		return ""
	}
	return real
}

// Local reports whether a site is published on the dashboard's own server, so it is
// reachable at <dashboard>/sites/<name>/.
func (s *Service) Local(name string) bool { return localDir(name) != "" }

// Files serves /sites/<name>/… from the site's live release on this machine.
func (s *Service) Files() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/sites/")
		name, file, slash := strings.Cut(rest, "/")
		var site *Site
		for _, st := range s.List() {
			if st.Name == name {
				st := st
				site = &st
			}
		}
		dir := localDir(name)
		if site == nil || dir == "" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		if !slash {
			http.Redirect(w, r, "/sites/"+name+"/", http.StatusMovedPermanently)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", sandbox)
		h.Set("Access-Control-Allow-Origin", "*") // the sandboxed page loads its own module scripts cross-origin
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Del("X-Frame-Options")
		root, err := os.OpenRoot(dir) // nothing outside the release can be opened, whatever the path says
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		open := func(p string) (*os.File, fs.FileInfo, bool) {
			f, err := root.Open(p)
			if err != nil {
				return nil, nil, false
			}
			fi, err := f.Stat()
			if err != nil || fi.IsDir() {
				f.Close()
				return nil, nil, false
			}
			return f, fi, true
		}
		p := strings.TrimPrefix(path.Clean("/"+file), "/")
		candidates := []string{p, path.Join(p, "index.html"), p + ".html"}
		if p == "" {
			candidates = []string{"index.html"}
		}
		for _, c := range candidates {
			if f, fi, ok := open(c); ok {
				defer f.Close()
				if strings.HasSuffix(c, ".html") {
					h.Set("Cache-Control", "no-cache")
				} else {
					h.Set("Cache-Control", "public, max-age=300")
				}
				http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
				return
			}
		}
		// A single-page app routes unknown paths itself; other sites get their 404 page.
		fallback, status := "404.html", http.StatusNotFound
		if site.Plan.SPA {
			fallback, status = "index.html", http.StatusOK
		}
		if f, _, ok := open(fallback); ok {
			defer f.Close()
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-cache")
			w.WriteHeader(status)
			if r.Method == http.MethodGet {
				io.Copy(w, f)
			}
			return
		}
		http.NotFound(w, r)
	})
}
