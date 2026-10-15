package domains

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
)

// selfSigned makes a CA-flagged certificate for names, valid until notAfter.
func selfSigned(t *testing.T, notAfter time.Time, names ...string) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0], Organization: []string{"Test CA"}},
		Issuer: pkix.Name{Organization: []string{"Test CA"}}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// tlsServer answers handshakes with the certificate registered for the SNI name.
func tlsServer(t *testing.T, certs map[string]tls.Certificate) string {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		c, ok := certs[h.ServerName]
		if !ok {
			return nil, fmt.Errorf("no cert for %s", h.ServerName)
		}
		return &c, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	return ln.Addr().String()
}

type fakeRegistry struct {
	lookups atomic.Int32
	expiry  map[string]time.Time
}

func (f *fakeRegistry) serve(t *testing.T) string {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dns.json":
			fmt.Fprintf(w, `{"services":[[["com"],["%s/rdap/"]]]}`, srv.URL)
		case strings.HasPrefix(r.URL.Path, "/rdap/domain/"):
			f.lookups.Add(1)
			name := strings.TrimPrefix(r.URL.Path, "/rdap/domain/")
			exp, ok := f.expiry[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"events":[{"eventAction":"registration","eventDate":"2020-01-01T00:00:00Z"},{"eventAction":"expiration","eventDate":%q}],
				"entities":[{"roles":["registrar"],"vcardArray":["vcard",[["version",{},"text","4.0"],["fn",{},"text","Example Registrar, Inc."]]]}]}`, exp.UTC().Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestDomainChecks(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Exec(`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'VPS', 0)`)
	store := incidents.NewStore(d, live.NewBus())
	now := time.Now()
	days := func(n int) time.Time { return now.Add(time.Duration(n)*24*time.Hour + time.Hour) }

	certs := map[string]tls.Certificate{
		"good.example.com":   selfSigned(t, days(60), "good.example.com"),
		"soon.example.com":   selfSigned(t, days(5), "soon.example.com"),
		"urgent.example.com": selfSigned(t, days(2), "urgent.example.com"),
		"wrong.example.com":  selfSigned(t, days(60), "other.example.net"),
		"cdn.example.com":    selfSigned(t, days(60), "cdn.example.com"),
	}
	addr := tlsServer(t, certs)
	roots := x509.NewCertPool()
	for _, c := range certs {
		roots.AddCert(c.Leaf)
	}
	reg := &fakeRegistry{expiry: map[string]time.Time{"example.com": days(10)}}
	regURL := reg.serve(t)

	refs := []apps.DomainRef{
		{Host: "good.example.com", ServerID: "s1"},
		{Host: "soon.example.com", ServerID: "s1"},
		{Host: "urgent.example.com", ServerID: "s1"},
		{Host: "wrong.example.com", ServerID: "s1"},
		// Public certificate fine, origin certificate on the server about to expire.
		{Host: "cdn.example.com", ServerID: "s1", Origin: &proto.CertInfo{Host: "cdn.example.com", NotAfter: days(4).UnixMilli(), Issuer: "Cloudflare Origin CA"}},
		{Host: "down.example.com", ServerID: "s1"}, // nothing answers: reported, no incident
	}
	c := New(d, nil, store, incidents.DefaultRules, nil)
	c.Source = func() []apps.DomainRef { return refs }
	c.roots = roots
	c.RDAP.Bootstrap = regURL + "/dns.json"
	c.addrFor = func(host string) string {
		if host == "down.example.com" {
			return "127.0.0.1:1"
		}
		return addr
	}
	c.Check(context.Background())

	open := map[string]incidents.Incident{}
	list, _ := store.List(incidents.Filter{Status: "open"})
	for _, x := range list {
		open[x.Title] = x
	}
	want := map[string]string{
		"SSL certificate for soon.example.com expires in 5 days":   "warning",
		"SSL certificate for urgent.example.com expires in 2 days": "critical",
		"Certificate problem on wrong.example.com":                 "critical",
		"SSL certificate for cdn.example.com expires in 4 days":    "warning",
		"Domain example.com expires in 10 days":                    "warning",
	}
	for title, sev := range want {
		if x, ok := open[title]; !ok || x.Severity != sev {
			t.Errorf("missing or wrong %q (%s): open = %v", title, sev, keys(open))
		}
	}
	if len(open) != len(want) {
		t.Errorf("unexpected incidents: %v", keys(open))
	}
	if x := open["SSL certificate for cdn.example.com expires in 4 days"]; !strings.Contains(strings.Join(x.Samples, " "), "origin") {
		t.Errorf("origin certificate not named: %v", x.Samples)
	}

	byHost := map[string]Domain{}
	for _, x := range c.List() {
		byHost[x.Host] = x
	}
	if g := byHost["good.example.com"]; g.Public.NotAfter == 0 || g.Public.Issuer != "Test CA" || g.Registered != "example.com" || g.Registrar != "Example Registrar, Inc." {
		t.Errorf("good: %+v", g)
	}
	if g := byHost["down.example.com"]; g.Public.Problem || !strings.Contains(g.Public.Error, "could not connect") {
		t.Errorf("down: %+v", g.Public)
	}
	if n := reg.lookups.Load(); n != 1 {
		t.Errorf("RDAP queried %d times for one registered domain", n)
	}

	// Renewals clear the incidents; the registration is not re-queried within a day.
	certs["soon.example.com"] = selfSigned(t, days(80), "soon.example.com")
	roots.AddCert(certs["soon.example.com"].Leaf)
	c.Check(context.Background())
	if _, ok := openTitles(store)["SSL certificate for soon.example.com expires in 5 days"]; ok {
		t.Error("renewed certificate still open")
	}
	if n := reg.lookups.Load(); n != 1 {
		t.Errorf("RDAP re-queried within a day (%d)", n)
	}
}

func openTitles(s *incidents.Store) map[string]bool {
	out := map[string]bool{}
	list, _ := s.List(incidents.Filter{Status: "open"})
	for _, x := range list {
		out[x.Title] = true
	}
	return out
}

func keys(m map[string]incidents.Incident) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
