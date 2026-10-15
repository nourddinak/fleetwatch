// Package projects recognizes project directories by their marker files.
package projects

import (
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
	if len(p.Markers) == 0 {
		return nil
	}
	sort.Strings(p.Markers)
	p.Runtime = Runtime(p.Markers)
	_, _, p.Owner, _ = sysexec.Owner(dir)
	return p
}

// Runtime guesses the runtime from marker files.
func Runtime(markers []string) string {
	has := func(names ...string) bool {
		for _, n := range names {
			if slices.Contains(markers, n) {
				return true
			}
		}
		return false
	}
	switch {
	case has("next.config.js", "next.config.mjs", "next.config.ts"):
		return "Next.js"
	case has("bun.lockb"):
		return "Bun"
	case has("deno.json"):
		return "Deno"
	case has("package.json"):
		return "Node.js"
	case has("go.mod"):
		return "Go"
	case has("Cargo.toml"):
		return "Rust"
	case has("requirements.txt", "pyproject.toml", "Pipfile", "manage.py"):
		return "Python"
	case has("composer.json"):
		return "PHP"
	case has("Gemfile"):
		return "Ruby"
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
