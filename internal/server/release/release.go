// Package release checks for newer ops-server releases and installs one on request.
//
// The server runs as an unprivileged user and cannot replace its own binary, so an
// update is two steps:
//  1. The dashboard writes the wanted tag to DATA_DIR/update-request (Request).
//  2. A systemd path unit sees that file and starts ops-server-update.service as root,
//     which runs `ops-server self-update` (Install): it downloads that release from the
//     configured source, checks it against the release's SHA256SUMS, swaps the binary
//     and restarts ops-server.
//
// A compromised dashboard can therefore only ask for an official, checksummed release.
package release

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/selfupdate"
)

// DefaultSource is where releases come from; OPS_UPDATE_SOURCE overrides it (forks,
// mirrors) and "off" disables update checks.
const DefaultSource = "https://github.com/nourddinak/fleetwatch/releases"

// HelperUnit exists when the installer set up one-click updates.
const HelperUnit = "/etc/systemd/system/ops-server-update.path"

const (
	requestFile = "update-request"
	statusFile  = "update-status.json"
	checkEvery  = 6 * time.Hour
)

var (
	tagRe          = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	ErrNoHelper    = errors.New("one-click updates are not set up on this server: re-run the install command once")
	ErrNotNewer    = errors.New("that version is not newer than the running one")
	ErrUnavailable = errors.New("update checks are turned off (OPS_UPDATE_SOURCE=off)")
)

// Result is the outcome of the last install attempt, written by the root helper.
type Result struct {
	TS      int64  `json:"ts"`
	Version string `json:"version"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

type Status struct {
	Current    string  `json:"current"`
	Latest     string  `json:"latest"`
	Available  bool    `json:"available"`
	URL        string  `json:"url"`
	CheckedAt  int64   `json:"checkedAt"`
	CheckError string  `json:"checkError,omitempty"`
	Disabled   bool    `json:"disabled"`
	CanApply   bool    `json:"canApply"`
	Applying   bool    `json:"applying"`
	LastResult *Result `json:"lastResult,omitempty"`
}

type Checker struct {
	Source     string
	Current    string
	DataDir    string
	HelperPath string
	client     *http.Client

	mu        sync.Mutex
	latest    string
	checkedAt time.Time
	checkErr  string
}

func New(source, current, dataDir string) *Checker {
	if source == "" {
		source = DefaultSource
	}
	return &Checker{Source: strings.TrimRight(source, "/"), Current: current, DataDir: dataDir, HelperPath: HelperUnit,
		client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Checker) disabled() bool { return c.Source == "off" }

// Check asks the source for its latest release: GET <source>/latest redirects to
// <source>/tag/vX.Y.Z (no API token, no rate limit).
func (c *Checker) Check(ctx context.Context) error {
	if c.disabled() {
		return ErrUnavailable
	}
	tag, err := c.fetchLatest(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checkedAt = time.Now()
	if err != nil {
		c.checkErr = err.Error()
		return err
	}
	c.latest, c.checkErr = tag, ""
	return nil
}

func (c *Checker) fetchLatest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Source+"/latest", nil)
	if err != nil {
		return "", err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("update check failed: %w", err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if res.StatusCode/100 != 3 || !tagRe.MatchString(tag) {
		return "", fmt.Errorf("update check: unexpected answer (HTTP %d)", res.StatusCode)
	}
	return tag, nil
}

// Run checks shortly after start and then every few hours.
func (c *Checker) Run(ctx context.Context) {
	if c.disabled() {
		return
	}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Check(ctx); err != nil {
				slog.Warn("update check", "err", err)
			}
			t.Reset(checkEvery)
		}
	}
}

func (c *Checker) Status() Status {
	c.mu.Lock()
	st := Status{Current: c.Current, Latest: c.latest, CheckError: c.checkErr, Disabled: c.disabled()}
	if !c.checkedAt.IsZero() {
		st.CheckedAt = c.checkedAt.UnixMilli()
	}
	c.mu.Unlock()
	st.Available = st.Latest != "" && Newer(st.Latest, c.Current)
	if st.Latest != "" {
		st.URL = c.Source + "/tag/" + st.Latest
	}
	_, err := os.Stat(c.HelperPath)
	st.CanApply = err == nil && !st.Disabled
	_, err = os.Stat(filepath.Join(c.DataDir, requestFile))
	st.Applying = err == nil
	if raw, err := os.ReadFile(filepath.Join(c.DataDir, statusFile)); err == nil {
		var r Result
		if json.Unmarshal(raw, &r) == nil {
			st.LastResult = &r
		}
	}
	return st
}

// Request asks the root helper to install tag, which must be newer than the running
// version. The helper starts as soon as the file appears.
func (c *Checker) Request(tag string) error {
	if c.disabled() {
		return ErrUnavailable
	}
	if !tagRe.MatchString(tag) || !Newer(tag, c.Current) {
		return ErrNotNewer
	}
	if _, err := os.Stat(c.HelperPath); err != nil {
		return ErrNoHelper
	}
	tmp := filepath.Join(c.DataDir, requestFile+".tmp")
	if err := os.WriteFile(tmp, []byte(tag+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(c.DataDir, requestFile))
}

// Newer reports whether tag vX.Y.Z is newer than current ("0.9.2", "0.9.2-rc1",
// "v0.9.2"). Development builds ("dev") never report updates.
func Newer(tag, current string) bool {
	t := tagRe.FindStringSubmatch(tag)
	core, pre, _ := strings.Cut(strings.TrimPrefix(current, "v"), "-")
	c := tagRe.FindStringSubmatch("v" + core)
	if t == nil || c == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.Atoi(t[i])
		b, _ := strconv.Atoi(c[i])
		if a != b {
			return a > b
		}
	}
	return pre != "" // the release is newer than its own pre-releases
}

// Install runs as root (ops-server-update.service): it installs the requested release
// over binPath and records the outcome. The request file is always removed, so the
// path unit does not start it again.
func Install(ctx context.Context, source, dataDir, binPath string) (string, error) {
	reqPath := filepath.Join(dataDir, requestFile)
	raw, err := os.ReadFile(reqPath)
	if err != nil {
		return "", err
	}
	defer os.Remove(reqPath)
	tag := strings.TrimSpace(string(raw))
	err = install(ctx, strings.TrimRight(source, "/"), tag, binPath)
	res := Result{TS: time.Now().UnixMilli(), Version: tag, OK: err == nil}
	if err != nil {
		res.Error = err.Error()
	}
	out, _ := json.Marshal(res)
	// 0644 in the ops-owned data dir: the server reads it, only root can change it.
	if werr := os.WriteFile(filepath.Join(dataDir, statusFile), out, 0o644); werr != nil && err == nil {
		err = werr
	}
	return tag, err
}

func install(ctx context.Context, source, tag, binPath string) error {
	if source == "off" || !tagRe.MatchString(tag) {
		return fmt.Errorf("invalid update request %q", tag)
	}
	file := "ops-server-" + selfupdate.Platform()
	sum, err := checksum(ctx, source+"/download/"+tag+"/SHA256SUMS", file)
	if err != nil {
		return err
	}
	return selfupdate.Apply(ctx, source+"/download/"+tag+"/"+file, sum, binPath)
}

// checksum finds file's SHA-256 in a sha256sum-style list.
func checksum(ctx context.Context, url, file string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download checksums: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download checksums: HTTP %d", res.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(res.Body, 64<<10))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == file {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", file)
}
