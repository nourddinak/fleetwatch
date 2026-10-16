package hostcheck

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"fleetwatch/internal/proto"
)

const maxDeps = 5000

// Deps lists each app's exact dependency versions from its lock files, for the
// dashboard to look up known vulnerabilities: package-lock.json (npm), composer.lock
// (PHP), requirements.txt / Pipfile.lock / poetry.lock (Python) and go.mod (Go).
func Deps(req proto.DepsScan) proto.DepsReport {
	rep := proto.DepsReport{At: time.Now().UnixMilli(), Apps: map[string][]proto.Dep{}}
	for appID, dir := range req.Apps {
		if !filepath.IsAbs(dir) || len(rep.Apps) >= 100 {
			continue
		}
		seen := map[string]bool{}
		var list []proto.Dep
		add := func(eco, name, version string) {
			version = strings.TrimPrefix(strings.TrimSpace(version), "v")
			if name == "" || version == "" || len(list) >= maxDeps {
				return
			}
			if eco == "Go" {
				version = "v" + version // OSV wants Go versions with the v
			}
			k := eco + "|" + name + "|" + version
			if !seen[k] {
				seen[k] = true
				list = append(list, proto.Dep{Ecosystem: eco, Name: name, Version: version})
			}
		}
		npmLock(filepath.Join(dir, "package-lock.json"), add)
		composerLock(filepath.Join(dir, "composer.lock"), add)
		requirements(filepath.Join(dir, "requirements.txt"), add)
		pipfileLock(filepath.Join(dir, "Pipfile.lock"), add)
		poetryLock(filepath.Join(dir, "poetry.lock"), add)
		goMod(filepath.Join(dir, "go.mod"), add)
		if len(list) > 0 {
			rep.Apps[appID] = list
		}
	}
	return rep
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && len(b) < 64<<20 && json.Unmarshal(b, v) == nil
}

func npmLock(path string, add func(eco, name, version string)) {
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Link    bool   `json:"link"`
		} `json:"packages"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}
	if !readJSON(path, &lock) {
		return
	}
	if len(lock.Packages) > 0 { // lockfile v2/v3: "node_modules/a/node_modules/@b/c"
		for key, p := range lock.Packages {
			i := strings.LastIndex(key, "node_modules/")
			if i < 0 || p.Link {
				continue
			}
			add("npm", key[i+len("node_modules/"):], p.Version)
		}
		return
	}
	var walk func(map[string]json.RawMessage)
	walk = func(deps map[string]json.RawMessage) { // lockfile v1: nested "dependencies"
		for name, raw := range deps {
			var d struct {
				Version      string                     `json:"version"`
				Dependencies map[string]json.RawMessage `json:"dependencies"`
			}
			if json.Unmarshal(raw, &d) == nil {
				add("npm", name, d.Version)
				walk(d.Dependencies)
			}
		}
	}
	walk(lock.Dependencies)
}

func composerLock(path string, add func(eco, name, version string)) {
	var lock struct {
		Packages []struct{ Name, Version string } `json:"packages"`
	}
	if readJSON(path, &lock) {
		for _, p := range lock.Packages {
			add("Packagist", p.Name, p.Version)
		}
	}
}

var reqLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[^\]]*\])?\s*==\s*([^\s;#]+)`)

func requirements(path string, add func(eco, name, version string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := reqLine.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
			add("PyPI", strings.ToLower(m[1]), m[2])
		}
	}
}

func pipfileLock(path string, add func(eco, name, version string)) {
	var lock struct {
		Default map[string]struct {
			Version string `json:"version"`
		} `json:"default"`
	}
	if readJSON(path, &lock) {
		for name, d := range lock.Default {
			add("PyPI", strings.ToLower(name), strings.TrimPrefix(d.Version, "=="))
		}
	}
}

var poetryPkg = regexp.MustCompile(`(?m)^\[\[package\]\]\s*\nname = "([^"]+)"\s*\nversion = "([^"]+)"`)

func poetryLock(path string, add func(eco, name, version string)) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 32<<20 {
		return
	}
	for _, m := range poetryPkg.FindAllStringSubmatch(string(b), -1) {
		add("PyPI", strings.ToLower(m[1]), m[2])
	}
}

func goMod(path string, add func(eco, name, version string)) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	block := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
		switch {
		case line == "require (":
			block = true
		case block && line == ")":
			block = false
		case block:
			if f := strings.Fields(line); len(f) >= 2 {
				add("Go", f[0], f[1])
			}
		case strings.HasPrefix(line, "require "):
			if f := strings.Fields(line); len(f) >= 3 {
				add("Go", f[1], f[2])
			}
		}
	}
}
