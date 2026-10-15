// Package agentbin embeds the ops-agent binaries and renders the install script.
package agentbin

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"strings"
	"sync"
	"text/template"

	"fleetwatch/internal/proto"
)

//go:embed bin install.sh.tmpl uninstall.sh ops-agent.service
var files embed.FS

// Platforms the server can hand out an agent for.
var Platforms = []string{"linux-amd64", "linux-arm64"}

// Binary returns the agent for platform ("linux-amd64"), or false if it was not built in.
func Binary(platform string) ([]byte, bool) {
	ok := false
	for _, p := range Platforms {
		ok = ok || p == platform
	}
	if !ok {
		return nil, false
	}
	b, err := files.ReadFile("bin/ops-agent-" + platform)
	return b, err == nil
}

var sums sync.Map

// SHA256 returns the hex checksum of the platform's binary.
func SHA256(platform string) (string, bool) {
	if v, ok := sums.Load(platform); ok {
		return v.(string), true
	}
	b, ok := Binary(platform)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256(b)
	s := hex.EncodeToString(sum[:])
	sums.Store(platform, s)
	return s, true
}

var script = template.Must(template.New("install.sh.tmpl").Funcs(template.FuncMap{
	// shellquote wraps s in single quotes for bash.
	"shellquote": func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" },
}).ParseFS(files, "install.sh.tmpl"))

// UninstallScript returns uninstall.sh, which removes the agent from a host.
func UninstallScript() []byte {
	b, _ := files.ReadFile("uninstall.sh")
	return b
}

// UpdateMessage tells an agent to install the agents built into this server, with
// each platform's checksum. ok is false when the build has no agent binaries.
func UpdateMessage(version string) (u proto.Update, ok bool) {
	u = proto.Update{Version: version, SHA256: map[string]string{}}
	for _, p := range Platforms {
		if sum, found := SHA256(p); found {
			u.SHA256[p] = sum
		}
	}
	return u, len(u.SHA256) > 0
}

// InstallScript renders install.sh for agents that will connect to serverURL.
func InstallScript(serverURL string) ([]byte, error) {
	unit, err := files.ReadFile("ops-agent.service")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = script.Execute(&buf, map[string]string{"URL": serverURL, "Unit": string(unit)})
	return buf.Bytes(), err
}
