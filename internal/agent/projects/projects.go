// Package projects recognizes project directories by their marker files.
package projects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// Markers are files whose presence makes a directory a project.
var Markers = []string{
	"package.json", "pnpm-lock.yaml", "yarn.lock", "package-lock.json", "bun.lockb",
	"next.config.js", "next.config.mjs", "next.config.ts",
	"requirements.txt", "pyproject.toml", "Pipfile", "manage.py",
	"Cargo.toml", "go.mod", "composer.json", "Gemfile", "deno.json",
	".git", "Dockerfile", "docker-compose.yml", "compose.yaml",
	"ecosystem.config.js", "ecosystem.config.cjs", "index.html",
}

// stopDirs are never treated as project roots when walking up from a process cwd.
var stopDirs = map[string]bool{"/": true, "/home": true, "/root": true, "/var": true, "/var/www": true, "/opt": true, "/srv": true, "/usr": true, "/usr/local": true, "/tmp": true}

const maxFiles = 30

// Detect inspects dir. It returns nil when dir has no markers.
func Detect(dir string) *proto.Project {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	p := &proto.Project{Path: dir, Markers: []string{}, Files: []string{}}
	for _, e := range entries {
		name := e.Name()
		if slices.Contains(Markers, name) {
			p.Markers = append(p.Markers, name)
		}
		if len(p.Files) < maxFiles {
			if e.IsDir() {
				name += "/"
			}
			p.Files = append(p.Files, name)
		}
	}
	// Split projects keep each half in a folder (backend/go.mod, frontend/package.json).
	subdirs := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || skipSubdirs[e.Name()] || subdirs >= maxFiles {
			continue
		}
		subdirs++
		for _, m := range subMarkers {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), m)); err == nil {
				p.Markers = append(p.Markers, e.Name()+"/"+m)
			}
		}
	}
	if len(p.Markers) == 0 {
		return nil
	}
	sort.Strings(p.Markers)
	p.Runtime = Runtime(dir, p.Markers)
	_, _, p.Owner, _ = sysexec.Owner(dir)
	return p
}

// subMarkers are looked for one folder down; skipSubdirs never hold the app itself.
var (
	subMarkers  = []string{"package.json", "go.mod", "Cargo.toml", "requirements.txt", "pyproject.toml", "composer.json", "Gemfile", "next.config.js", "next.config.mjs", "next.config.ts"}
	skipSubdirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "venv": true, "__pycache__": true}
)

// jsFrameworks maps package.json dependencies to a name, most specific first.
var jsFrameworks = [][2]string{{"next", "Next.js"}, {"nuxt", "Nuxt"}, {"@remix-run/react", "Remix"}, {"astro", "Astro"}, {"@angular/core", "Angular"}, {"svelte", "Svelte"}, {"vue", "Vue"}, {"react", "React"}}

// jsRuntime names the JavaScript side of a project: a framework when package.json
// uses one, else the runtime.
func jsRuntime(dir string, pkgDirs []string, has func(...string) bool) string {
	if has("next.config.js", "next.config.mjs", "next.config.ts") {
		return "Next.js"
	}
	for _, d := range pkgDirs {
		raw, err := os.ReadFile(filepath.Join(dir, d, "package.json"))
		if err != nil || len(raw) > 1<<20 {
			continue
		}
		var pkg struct{ Dependencies, DevDependencies map[string]any }
		if json.Unmarshal(raw, &pkg) != nil {
			continue
		}
		for _, fw := range jsFrameworks {
			if pkg.Dependencies[fw[0]] != nil || pkg.DevDependencies[fw[0]] != nil {
				return fw[1]
			}
		}
	}
	switch {
	case has("bun.lockb"):
		return "Bun"
	case len(pkgDirs) > 0:
		return "Node.js"
	case has("deno.json"):
		return "Deno"
	}
	return ""
}

// Runtime names what a project in dir is built with, from its marker files (which may
// sit one folder down, like "backend/go.mod"). A backend language next to a JavaScript
// frontend reads "Go + React".
func Runtime(dir string, markers []string) string {
	names := map[string]bool{}
	var pkgDirs []string
	for _, m := range markers {
		d, name := "", m
		if i := strings.LastIndex(m, "/"); i >= 0 {
			d, name = m[:i], m[i+1:]
		}
		names[name] = true
		if name == "package.json" {
			pkgDirs = append(pkgDirs, d)
		}
	}
	sort.Slice(pkgDirs, func(i, j int) bool { return pkgDirs[i] < pkgDirs[j] }) // top level ("") first
	has := func(ns ...string) bool {
		for _, n := range ns {
			if names[n] {
				return true
			}
		}
		return false
	}
	backend := ""
	switch {
	case has("go.mod"):
		backend = "Go"
	case has("Cargo.toml"):
		backend = "Rust"
	case has("requirements.txt", "pyproject.toml", "Pipfile", "manage.py"):
		backend = "Python"
	case has("composer.json"):
		backend = "PHP"
	case has("Gemfile"):
		backend = "Ruby"
	}
	js := jsRuntime(dir, pkgDirs, has)
	switch {
	case backend != "" && js != "":
		return backend + " + " + js
	case backend != "":
		return backend
	case js != "":
		return js
	case has("index.html"):
		return "Static"
	}
	return ""
}

// RuntimeFromExec guesses the runtime from an interpreter or executable path.
func RuntimeFromExec(exe string) string {
	base := strings.ToLower(filepath.Base(strings.Fields(exe + " x")[0]))
	switch {
	case base == "node" || base == "nodejs":
		return "Node.js"
	case base == "bun":
		return "Bun"
	case base == "deno":
		return "Deno"
	case strings.HasPrefix(base, "python"), base == "gunicorn", base == "uvicorn":
		return "Python"
	case strings.HasPrefix(base, "php"):
		return "PHP"
	case base == "java":
		return "Java"
	case base == "ruby", base == "puma", base == "bundle":
		return "Ruby"
	case base == "dotnet":
		return ".NET"
	}
	return ""
}

// FindRoot walks up from dir (at most 4 levels) to the nearest directory with markers.
func FindRoot(dir string) string {
	dir = filepath.Clean(dir)
	for d, i := dir, 0; i < 5 && !stopDirs[d]; d, i = filepath.Dir(d), i+1 {
		if Detect(d) != nil {
			return d
		}
	}
	return dir
}

// Scan lists project directories directly under each root.
func Scan(roots []string, limit int) []proto.Project {
	var out []proto.Project
	for _, root := range roots {
		root = filepath.Clean(root)
		if !filepath.IsAbs(root) {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue // ReadDir does not follow symlinks, so no loops
			}
			if p := Detect(filepath.Join(root, e.Name())); p != nil {
				out = append(out, *p)
				if len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}
