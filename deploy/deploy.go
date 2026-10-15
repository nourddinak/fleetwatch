// Package deploy holds deployment files that ship inside ops-server, so the installer
// always writes the version that matches the binary it installed.
package deploy

import (
	_ "embed"
	"errors"
	"regexp"
	"strings"
)

//go:embed fleetwatch.caddy
var caddySite string

var domainRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)

// CaddySite returns fleetwatch.caddy for domain.
func CaddySite(domain string) (string, error) {
	if len(domain) > 253 || !domainRe.MatchString(domain) {
		return "", errors.New("invalid domain: " + domain)
	}
	return strings.Replace(caddySite, "\nops.example.com {", "\n"+domain+" {", 1), nil
}
