// Package selfupdate replaces a running binary with a downloaded, checksum-verified build:
// the agent with the build its central server ships, the server with a GitHub release.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const maxBinary = 200 << 20

// Platform is this agent's download name, e.g. "linux-amd64".
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// Apply downloads url, checks it against wantSHA256 and atomically replaces the binary
// at target. The caller restarts afterwards.
func Apply(ctx context.Context, url, wantSHA256, target string) error {
	if len(wantSHA256) != 64 {
		return errors.New("no checksum for this platform's build")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", res.StatusCode)
	}
	// The temp file sits next to the target so the final rename stays on one filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(target), ".ops-agent-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(res.Body, maxBinary+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if n > maxBinary {
		return errors.New("download: binary too large")
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA256 {
		return fmt.Errorf("checksum mismatch (got %s…), update not installed", got[:12])
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
