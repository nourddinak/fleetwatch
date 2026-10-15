// Package projects recognizes project directories by their marker files.
package projects

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

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
		if isMarker(name) {
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
		sub, _ := os.ReadDir(filepath.Join(dir, e.Name()))
		for _, f := range sub {
			if !f.IsDir() && isSubMarker(f.Name()) {
				p.Markers = append(p.Markers, e.Name()+"/"+f.Name())
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
