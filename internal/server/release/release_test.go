package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fleetwatch/internal/selfupdate"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v0.9.3", "0.9.2", true}, {"v0.10.0", "0.9.9", true}, {"v1.0.0", "0.9.2", true},
		{"v0.9.2", "0.9.2", false}, {"v0.9.1", "0.9.2", false}, {"v0.9.2", "0.9.2-rc1", true},
		{"v0.9.2", "v0.9.2", false}, {"v0.9.3", "dev", false}, {"latest", "0.9.2", false},
		{"0.9.46", "0.9.45", true}, {"0.9.46", "v0.9.46", false}, // minimums written without the v
	}
	for _, c := range cases {
		if got := Newer(c.tag, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.tag, c.cur, got)
		}
	}
}

// fakeReleases serves /latest (redirect), /download/<tag>/SHA256SUMS and the binary.
func fakeReleases(t *testing.T, tag string, bin []byte, badSum bool) *httptest.Server {
	sum := sha256.Sum256(bin)
	s := hex.EncodeToString(sum[:])
	if badSum {
		s = strings.Repeat("0", 64)
	}
	file := "ops-server-" + selfupdate.Platform()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
		case "/download/" + tag + "/SHA256SUMS":
			w.Write([]byte(s + "  " + file + "\n"))
		case "/download/" + tag + "/" + file:
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckRequestInstall(t *testing.T) {
	bin := []byte("new ops-server")
	srv := fakeReleases(t, "v0.9.3", bin, false)
	dir := t.TempDir()
	c := New(srv.URL, "0.9.2", dir)
	c.HelperPath = filepath.Join(dir, "no-helper")
	ctx := context.Background()

	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	st := c.Status()
	if !st.Available || st.Latest != "v0.9.3" || st.CanApply || st.URL != srv.URL+"/tag/v0.9.3" {
		t.Fatalf("status: %+v", st)
	}
	if err := c.Request("v0.9.3", false); err != ErrNoHelper {
		t.Fatalf("request without helper: %v", err)
	}
	os.WriteFile(c.HelperPath, nil, 0o644)
	for _, bad := range []string{"v0.9.2", "v0.9.1", "../../x", "v1.0.0; rm -rf /"} {
		if err := c.Request(bad, false); err == nil {
			t.Fatalf("request %q accepted", bad)
		}
	}
	if err := c.Request("v0.9.3", false); err != nil || !c.Status().Applying {
		t.Fatalf("request: %v", err)
	}

	target := filepath.Join(dir, "ops-server")
	os.WriteFile(target, []byte("old"), 0o755)
	tag, err := Install(ctx, srv.URL, dir, target)
	if err != nil || tag != "v0.9.3" {
		t.Fatalf("install: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != string(bin) {
		t.Fatal("binary not replaced")
	}
	st = c.Status()
	if st.Applying || st.LastResult == nil || !st.LastResult.OK || st.LastResult.Version != "v0.9.3" {
		t.Fatalf("after install: %+v %+v", st, st.LastResult)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	srv := fakeReleases(t, "v0.9.3", []byte("tampered"), true)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, requestFile), []byte("v0.9.3\n"), 0o644)
	target := filepath.Join(dir, "ops-server")
	os.WriteFile(target, []byte("old"), 0o755)
	if _, err := Install(context.Background(), srv.URL, dir, target); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatal("binary replaced despite a bad checksum")
	}
	if _, err := os.Stat(filepath.Join(dir, requestFile)); !os.IsNotExist(err) {
		t.Fatal("request file left behind (the path unit would loop)")
	}
	c := New(srv.URL, "0.9.2", dir)
	if r := c.Status().LastResult; r == nil || r.OK || r.Error == "" {
		t.Fatalf("failure not recorded: %+v", r)
	}
}

func TestDisabled(t *testing.T) {
	c := New("off", "0.9.2", t.TempDir())
	if err := c.Check(context.Background()); err != ErrUnavailable || !c.Status().Disabled || c.Request("v9.9.9", false) != ErrUnavailable {
		t.Fatal("off must disable checks and requests")
	}
}

func TestLivePushAndRefresh(t *testing.T) {
	var hits atomic.Int32
	tag := "v0.9.3"
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		defer mu.Unlock()
		http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "0.9.2", t.TempDir())
	pushed := make(chan Status, 4)
	c.OnChange = func(st Status) { pushed <- st }
	ctx := context.Background()

	c.Check(ctx)
	if st := <-pushed; st.Latest != "v0.9.3" || !st.Available {
		t.Fatalf("first push: %+v", st)
	}
	c.Check(ctx) // same release: nothing to push
	select {
	case st := <-pushed:
		t.Fatalf("pushed without a change: %+v", st)
	default:
	}

	// Refresh is a no-op while the last check is fresh...
	before := hits.Load()
	c.Refresh()
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != before {
		t.Fatal("refresh re-checked a fresh result")
	}
	// ...and re-checks once it is stale, pushing the new release.
	mu.Lock()
	tag = "v0.9.4"
	mu.Unlock()
	c.mu.Lock()
	c.checkedAt = time.Now().Add(-3 * time.Minute)
	c.mu.Unlock()
	c.Refresh()
	c.Refresh() // concurrent refreshes collapse into one check
	select {
	case st := <-pushed:
		if st.Latest != "v0.9.4" {
			t.Fatalf("refresh push: %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale refresh did not check")
	}
	time.Sleep(100 * time.Millisecond)
	if n := hits.Load() - before; n != 1 {
		t.Fatalf("refresh made %d requests, want 1", n)
	}
}

func TestAgentsFollowTheUpdate(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	os.WriteFile(helper, nil, 0o644)
	old := New("http://x", "0.9.2", dir)
	old.HelperPath = helper
	note := filepath.Join(dir, agentsFile)

	if err := old.Request("v0.9.3", true); err != nil {
		t.Fatal(err)
	}
	// The old version restarting (say the update failed) must not act on the note...
	if New("http://x", "0.9.2", dir).AgentsFollow() {
		t.Fatal("old version followed an update it did not install")
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatal("fresh note dropped too early")
	}
	// ...the new version does, once.
	if !New("http://x", "0.9.3", dir).AgentsFollow() {
		t.Fatal("new version ignored the note")
	}
	if New("http://x", "0.9.3", dir).AgentsFollow() {
		t.Fatal("note acted on twice")
	}

	// A note for an update that never happened expires.
	old.Request("v0.9.3", true)
	past := time.Now().Add(-2 * agentsStale)
	os.Chtimes(note, past, past)
	New("http://x", "0.9.2", dir).AgentsFollow()
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Fatal("stale note kept")
	}

	// Updating without "agents too" clears an earlier note.
	old.Request("v0.9.3", true)
	old.Request("v0.9.3", false)
	if New("http://x", "0.9.3", dir).AgentsFollow() {
		t.Fatal("agents updated although not asked")
	}
}
