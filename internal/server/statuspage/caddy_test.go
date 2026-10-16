package statuspage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaddySync(t *testing.T) {
	data, etc := t.TempDir(), t.TempDir()
	Caddyfile, caddySite = filepath.Join(etc, "Caddyfile"), filepath.Join(etc, "fleetwatch-status.caddy")
	validateErr, reloads := error(nil), 0
	caddyValidate = func(string) ([]byte, error) { return []byte("Error: boom"), validateErr }
	caddyReload = func() ([]byte, error) { reloads++; return nil, nil }
	os.WriteFile(Caddyfile, []byte("import /etc/caddy/fleetwatch.caddy"), 0o644)
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	request := func(domains string) {
		os.WriteFile(filepath.Join(data, domainsFile), []byte(domains), 0o644)
		os.WriteFile(filepath.Join(data, requestFile), []byte("caddy\n"), 0o644)
	}

	request("status.b.com\nStatus.A.com\nstatus.b.com\n")
	if !CaddyRequested(data) {
		t.Fatal("request not seen")
	}
	if err := CaddySync(data, "127.0.0.1:8090"); err != nil {
		t.Fatal(err)
	}
	site := read(caddySite)
	if !strings.Contains(site, "status.a.com, status.b.com {") || !strings.Contains(site, "reverse_proxy 127.0.0.1:8090") {
		t.Fatalf("site: %s", site)
	}
	if got := read(Caddyfile); got != "import /etc/caddy/fleetwatch.caddy\nimport "+caddySite+"\n" || reloads != 1 {
		t.Fatalf("Caddyfile: %q, reloads %d", got, reloads)
	}
	if CaddyRequested(data) || !strings.Contains(read(filepath.Join(data, caddyResult)), `"ok":true`) {
		t.Fatal("request not cleared or result not written")
	}

	// Again with fewer domains: the import line is not added twice.
	request("status.a.com\n")
	if err := CaddySync(data, "0.0.0.0:9000"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(read(Caddyfile), "fleetwatch-status.caddy") != 1 || !strings.Contains(read(caddySite), "status.a.com {\n\tencode zstd gzip\n\treverse_proxy 127.0.0.1:9000") {
		t.Fatalf("second run: %q / %q", read(Caddyfile), read(caddySite))
	}

	// Caddy rejects the result: everything is put back as it was.
	before, beforeMain := read(caddySite), read(Caddyfile)
	os.WriteFile(Caddyfile, []byte("example.com {\n}\n"), 0o644)
	beforeMain = read(Caddyfile)
	validateErr = errors.New("exit 1")
	request("status.c.com\n")
	if err := CaddySync(data, "127.0.0.1:8090"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("validate failure: %v", err)
	}
	if read(caddySite) != before || read(Caddyfile) != beforeMain {
		t.Fatal("a rejected change was left in place")
	}
	if !strings.Contains(read(filepath.Join(data, caddyResult)), `"ok":false`) {
		t.Fatal("failure not reported")
	}
	validateErr = nil

	// Names from the unprivileged side are checked again by root.
	for _, bad := range []string{"evil.com {\n}\nother.com", "a.com;rm", "localhost", "*.example.com"} {
		request(bad)
		if err := CaddySync(data, "127.0.0.1:8090"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// No Caddy on this server: a clear error.
	os.Remove(Caddyfile)
	request("status.a.com\n")
	if err := CaddySync(data, "127.0.0.1:8090"); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("no Caddyfile: %v", err)
	}
}
