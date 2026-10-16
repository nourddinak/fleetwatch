package statuspage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"fleetwatch/internal/server/valid"
)

// publicOnly refuses addresses inside the server's own networks; tests turn it off
// to reach their local test server.
var publicOnly = true

var errPrivate = errors.New("private address")

// sharedNAT is 100.64.0.0/10 (carrier NAT, also Tailscale): not public either.
var sharedNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// FetchLogo downloads an image once and returns it as a data URL, stored like an
// uploaded logo: visitors never load it from the other site, and the page keeps
// working if the original goes away. Only public addresses are fetched, so the
// address cannot be used to probe the server's own network.
func FetchLogo(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", valid.Error("logo link must be an http(s) URL")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if publicOnly && (ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || sharedNAT.Contains(ip)) {
			return errPrivate
		}
		return nil
	}}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil, TLSHandshakeTimeout: 5 * time.Second},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Accept", "image/*")
	res, err := client.Do(req)
	if errors.Is(err, errPrivate) {
		return "", valid.Error("that address is not on the public internet")
	}
	if err != nil {
		return "", valid.Error("could not download the logo")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", valid.Error(fmt.Sprintf("the logo address answered %d", res.StatusCode))
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxLogo+1))
	if err != nil {
		return "", valid.Error("could not download the logo")
	}
	if len(b) > maxLogo {
		return "", valid.Error("logo must be at most 256 KB")
	}
	kind := http.DetectContentType(b)
	if i := strings.IndexByte(kind, ';'); i > 0 {
		kind = kind[:i]
	}
	if strings.HasPrefix(kind, "text/") && bytes.Contains(b[:min(len(b), 1024)], []byte("<svg")) {
		kind = "image/svg+xml" // SVG is text; shown as an image it cannot run scripts
	}
	switch kind {
	case "image/png", "image/jpeg", "image/webp", "image/svg+xml":
	default:
		return "", valid.Error("logo must be a PNG, JPEG, WebP or SVG image")
	}
	return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}
