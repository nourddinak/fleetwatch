// Package config reads and writes the agent's /etc/ops-agent/config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const DefaultPath = "/etc/ops-agent/config.json"

type Config struct {
	ServerURL  string `json:"serverUrl"`
	ServerID   string `json:"serverId"`
	Credential string `json:"credential"`
	// AllowCustomCommands is the local safety switch: false makes this VPS refuse
	// admin-defined commands even if the central server asks.
	AllowCustomCommands bool `json:"allowCustomCommands"`
}

func Load(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.ServerURL == "" || c.Credential == "" {
		return c, fmt.Errorf("%s: not enrolled (run ops-agent enroll)", path)
	}
	return c, nil
}

// Save writes the config atomically with mode 0600 (it holds the agent credential).
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CheckServerURL normalizes the server URL and requires https, except for loopback
// addresses used in development.
func CheckServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" {
		return "", errors.New("server URL must look like https://ops.example.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("server URL must not contain credentials, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("server URL must use https (plain http is only allowed for localhost)")
		}
	default:
		return "", errors.New("server URL must use https")
	}
	return u.String(), nil
}
