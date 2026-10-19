package caddy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fleetwatch/internal/proto"
)

func testCert(t *testing.T, until time.Time) *tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "example.com"},
		DNSNames: []string{"example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: until}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// A renewal that works keeps the new certificate; one that does not puts the old back.
func TestRenew(t *testing.T) {
	old, fresh := testCert(t, time.Now().Add(5*24*time.Hour)), testCert(t, time.Now().Add(90*24*time.Hour))
	var serving atomic.Pointer[tls.Certificate]
	serving.Store(old)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return serving.Load(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { c.(*tls.Conn).Handshake(); c.Close() }(c)
		}
	}()
	root := t.TempDir()
	dir := filepath.Join(root, "certificates", "acme-v02.api.letsencrypt.org-directory", "example.com")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "example.com.crt"), []byte("old"), 0o600)
	works := false
	restarts := 0
	localTLS, renewWait, renewPoll, storageRoots = ln.Addr().String(), 500*time.Millisecond, 20*time.Millisecond, []string{root}
	restartCaddy = func() ([]byte, error) {
		restarts++
		if works {
			serving.Store(fresh)
		}
		return nil, nil
	}
	caddyLog = func(string) string {
		return `{"level":"error","msg":"could not get certificate from issuer","identifier":"example.com","error":"DNS problem: NXDOMAIN looking up A for example.com"}`
	}

	res := Renew(context.Background(), proto.CertRenew{RunID: "r1", Host: "example.com"})
	if res.OK || !strings.Contains(res.Error, "NXDOMAIN") || restarts != 2 {
		t.Fatal("failed renewal", res, restarts)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "example.com.crt")); err != nil || string(b) != "old" {
		t.Fatal("the old certificate is back", err)
	}

	works = true
	res = Renew(context.Background(), proto.CertRenew{RunID: "r2", Host: "example.com"})
	leaf, _ := x509.ParseCertificate(fresh.Certificate[0])
	if !res.OK || res.NotAfter != leaf.NotAfter.UnixMilli() {
		t.Fatal("renewed", res)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "certificates", "*", "*.fleetwatch-renew-*")); len(left) != 0 {
		t.Fatal("the set-aside copy is cleaned up", left)
	}
	if r := Renew(context.Background(), proto.CertRenew{RunID: "r3", Host: "other.org"}); r.OK || !strings.Contains(r.Error, "no stored certificate") {
		t.Fatal(r)
	}
}
