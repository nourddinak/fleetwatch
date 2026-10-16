package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

// browserKeys makes keys like a browser's PushSubscription.
func browserKeys(t *testing.T) (p256dh, auth string) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	rand.Read(a)
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(a)
}

func TestPush(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(bytes.Repeat([]byte{3}, 32))

	var got atomic.Int32
	var auth, enc, urgency string
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		auth, enc, urgency = r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"), r.Header.Get("Urgency")
		w.WriteHeader(status)
	}))
	defer srv.Close()

	s := New(d, box)
	s.client = srv.Client()
	pub, err := s.PublicKey()
	if err != nil || pub == "" {
		t.Fatalf("key: %q %v", pub, err)
	}
	// The key pair is stored (private part encrypted) and reused after a restart.
	if again, _ := New(d, box).PublicKey(); again != pub {
		t.Fatal("VAPID key changed across restarts")
	}

	if err := s.Send(context.Background(), Message{Title: "x"}); err == nil {
		t.Fatal("send with no devices succeeded")
	}
	p256dh, a := browserKeys(t)
	for _, bad := range [][3]string{{"http://insecure/x", p256dh, a}, {srv.URL + "/s", "short", a}, {srv.URL + "/s", p256dh, "short"}} {
		if _, err := s.Subscribe(bad[0], bad[1], bad[2], "test", ""); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if _, err := s.Subscribe(srv.URL+"/push/1", p256dh, a, "Chrome on Android", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Subscribe(srv.URL+"/push/1", p256dh, a, "Chrome on Android", ""); err != nil || s.Count("") != 1 {
		t.Fatalf("resubscribing duplicated the device: %d %v", s.Count(""), err)
	}

	if err := s.Send(context.Background(), Message{Title: "api is offline", Body: "…", Severity: "critical"}); err != nil {
		t.Fatal(err)
	}
	if got.Load() != 1 || !strings.HasPrefix(auth, "vapid t=") || !strings.Contains(auth, "k="+pub) || enc != "aes128gcm" || urgency != "high" {
		t.Fatalf("push request: auth %q encoding %q urgency %q", auth, enc, urgency)
	}
	devs, _ := s.Devices("")
	if len(devs) != 1 || devs[0].LastOKAt == 0 {
		t.Fatalf("devices: %+v", devs)
	}

	// The push service says the subscription is gone: the device is dropped.
	status = http.StatusGone
	if err := s.Send(context.Background(), Message{Title: "x"}); err == nil {
		t.Fatal("send to a gone device reported success")
	}
	if s.Count("") != 0 {
		t.Fatal("gone device kept")
	}
}

func TestDevicesPerPerson(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	s := New(d, box)
	key := base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...))
	auth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	s.Subscribe("https://push.example/owner", key, auth, "owner phone", "")
	s.Subscribe("https://push.example/ops", key, auth, "ops phone", "mem_1")
	if s.Count("") != 1 || s.Count("mem_1") != 1 {
		t.Fatalf("counts: %d %d", s.Count(""), s.Count("mem_1"))
	}
	if list, _ := s.Devices("mem_1"); len(list) != 1 || list[0].Device != "ops phone" {
		t.Fatalf("member devices: %+v", list)
	}
	s.Unsubscribe("https://push.example/owner", "mem_1") // not theirs
	if s.Count("") != 1 {
		t.Fatal("a member removed the owner's phone")
	}
}
