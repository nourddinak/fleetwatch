package projects

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// shells only start something else; their process says nothing about the app.
var shells = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "env": true, "su": true, "sudo": true}

// ExecRuntime identifies what a process runs. Interpreters (node, python, …) are
// recognized by name, from the command line or the running executable; compiled
// programs by inspecting the executable itself. "" means unknown (project markers decide).
func ExecRuntime(cmdline string, pid int) string {
	if rt := RuntimeFromExec(cmdline); rt != "" {
		return rt
	}
	path := strings.Fields(cmdline + " x")[0]
	if !filepath.IsAbs(path) {
		path = ""
	}
	if pid > 0 {
		proc := fmt.Sprintf("/proc/%d/exe", pid)
		if target, err := os.Readlink(proc); err == nil {
			if rt := RuntimeFromExec(target); rt != "" {
				return rt
			}
			path = proc
			if shells[filepath.Base(target)] {
				return ""
			}
		}
	}
	if path == "" || shells[filepath.Base(path)] {
		return ""
	}
	return BinaryRuntime(path)
}

type binKey struct {
	path  string
	size  int64
	mtime int64
}

var binCache sync.Map // binKey → runtime; executables rarely change, inventories run every 30s

// BinaryRuntime returns "Go" or "Rust" for executables built with them, "Binary" for
// other native programs and "" for anything that is not an ELF executable (scripts).
func BinaryRuntime(path string) string {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	key := binKey{path, fi.Size(), fi.ModTime().UnixNano()}
	if target, err := os.Readlink(path); err == nil {
		key.path = target // /proc/<pid>/exe: cache by the real file, not the pid
	}
	if v, ok := binCache.Load(key); ok {
		return v.(string)
	}
	rt := ""
	if _, err := buildinfo.ReadFile(path); err == nil {
		rt = "Go"
	} else if f, err := elf.Open(path); err == nil {
		rt = "Binary"
		// Rust binaries embed rustc source paths in their panic messages.
		if s := f.Section(".rodata"); s != nil && s.Size < 64<<20 {
			if data, err := s.Data(); err == nil && bytes.Contains(data, []byte("/rustc/")) {
				rt = "Rust"
			}
		}
		f.Close()
	}
	binCache.Store(key, rt)
	return rt
}
