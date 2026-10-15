package fleet

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
)

func newRegistry(t *testing.T) (*Registry, *time.Time) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	now := time.Unix(1_700_000_000, 0)
	r := NewRegistry(d)
	r.Now = func() time.Time { return now }
	return r, &now
}

func TestEnrollmentLifecycle(t *testing.T) {
	r, now := newRegistry(t)
	if _, _, err := r.Create("  ", ""); err == nil {
		t.Fatal("blank name accepted")
	}
	if _, _, err := r.Create("bad\x1bname", ""); err == nil {
		t.Fatal("control characters accepted")
	}
	id, tok, err := r.Create("VPS-US-01", "Hetzner")
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := r.Get(id); rec.EnrolledAt != 0 {
		t.Fatal("new server should be pending")
	}
	if _, _, err := r.Enroll("fwe_wrong"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("wrong token: %v", err)
	}
	sid, cred, err := r.Enroll(tok)
	if err != nil || sid != id || !strings.HasPrefix(cred, "fwa.") {
		t.Fatalf("enroll: %s %s %v", sid, cred, err)
	}
	if _, _, err := r.Enroll(tok); !errors.Is(err, ErrBadToken) {
		t.Fatal("token reused")
	}
	if got, err := r.Authenticate(cred); err != nil || got != id {
		t.Fatalf("authenticate: %s %v", got, err)
	}
	for _, bad := range []string{"", "fwa", "fwa..", cred + "x", "fwa.nope." + strings.Split(cred, ".")[2], strings.Replace(cred, "fwa.", "xxx.", 1)} {
		if _, err := r.Authenticate(bad); !errors.Is(err, ErrBadCred) {
			t.Errorf("credential %q accepted", bad)
		}
	}

	// Re-pairing revokes the old credential.
	tok2, _ := r.NewEnrollToken(id)
	_, cred2, err := r.Enroll(tok2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Authenticate(cred); !errors.Is(err, ErrBadCred) {
		t.Fatal("old credential survived re-pairing")
	}
	if err := r.RevokeCredentials(id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Authenticate(cred2); !errors.Is(err, ErrBadCred) {
		t.Fatal("revoked credential still works")
	}

	// Tokens expire after an hour, and a new token replaces the unused old one.
	tok3, _ := r.NewEnrollToken(id)
	tok4, _ := r.NewEnrollToken(id)
	if _, _, err := r.Enroll(tok3); !errors.Is(err, ErrBadToken) {
		t.Fatal("superseded token still valid")
	}
	*now = now.Add(EnrollTokenTTL + time.Second)
	if _, _, err := r.Enroll(tok4); !errors.Is(err, ErrBadToken) {
		t.Fatal("expired token accepted")
	}
}

func TestHelloRebootAndUpdate(t *testing.T) {
	r, _ := newRegistry(t)
	id, _, _ := r.Create("a", "")
	h := proto.Hello{Hostname: "host", OS: "Ubuntu 24.04", BootTime: 1000, IPs: []string{"10.0.0.2"}}
	if rebooted, err := r.ApplyHello(id, h, "203.0.113.4"); err != nil || rebooted {
		t.Fatalf("first hello: %v %v", rebooted, err)
	}
	h.BootTime = 1001 // jitter, not a reboot
	if rebooted, _ := r.ApplyHello(id, h, ""); rebooted {
		t.Fatal("1s jitter counted as reboot")
	}
	h.BootTime = 5000
	if rebooted, _ := r.ApplyHello(id, h, ""); !rebooted {
		t.Fatal("reboot not detected")
	}
	long := strings.Repeat("x", 65)
	bad := "loud"
	for _, p := range []Patch{{Name: &long}, {NotifyOverride: &bad}} {
		if err := r.Update(id, p); err == nil {
			t.Errorf("invalid patch accepted: %+v", p)
		}
	}
	name := "renamed"
	if err := r.Update(id, Patch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := r.Get(id); rec.Name != "renamed" || rec.Hostname != "host" {
		t.Fatalf("record: %+v", rec)
	}
	if err := r.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted server still present")
	}
}
