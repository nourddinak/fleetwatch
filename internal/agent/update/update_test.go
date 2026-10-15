package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	bin := []byte("#!/bin/sh\necho new agent\n")
	sum := sha256.Sum256(bin)
	good := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/download/linux-amd64" {
			http.NotFound(w, r)
			return
		}
		w.Write(bin)
	}))
	defer srv.Close()
	target := filepath.Join(t.TempDir(), "ops-agent")
	os.WriteFile(target, []byte("old"), 0o755)
	ctx := context.Background()

	if err := Apply(ctx, srv.URL, "linux-amd64", strings.Repeat("0", 64), target); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("mismatch accepted: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatal("binary replaced despite a bad checksum")
	}
	if err := Apply(ctx, srv.URL, "linux-arm64", good, target); err == nil {
		t.Fatal("404 accepted")
	}
	if err := Apply(ctx, srv.URL, "linux-amd64", "", target); err == nil {
		t.Fatal("missing checksum accepted")
	}
	if err := Apply(ctx, srv.URL, "linux-amd64", good, target); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != string(bin) {
		t.Fatal("binary not replaced")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".ops-agent-update-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}
