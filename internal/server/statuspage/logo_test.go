package statuspage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchLogo(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logo.png":
			w.Write(png)
		case "/logo.svg":
			w.Write([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`))
		case "/page.html":
			w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/big.png":
			w.Write(append(png, make([]byte, maxLogo)...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	// The test server is on 127.0.0.1: refused while only public addresses are allowed.
	if _, err := FetchLogo(ctx, srv.URL+"/logo.png"); err == nil || !strings.Contains(err.Error(), "public internet") {
		t.Fatalf("loopback fetched: %v", err)
	}
	publicOnly = false
	defer func() { publicOnly = true }()

	if got, err := FetchLogo(ctx, srv.URL+"/logo.png"); err != nil || !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("png: %v %q", err, got)
	}
	if got, err := FetchLogo(ctx, srv.URL+"/logo.svg"); err != nil || !strings.HasPrefix(got, "data:image/svg+xml;base64,") {
		t.Fatalf("svg: %v %q", err, got)
	}
	for path, want := range map[string]string{"/page.html": "PNG, JPEG", "/big.png": "256 KB", "/missing.png": "404"} {
		if _, err := FetchLogo(ctx, srv.URL+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, err := FetchLogo(ctx, "file:///etc/passwd"); err == nil {
		t.Error("file URL accepted")
	}
	// A fetched logo passes the same check as an uploaded one.
	c := Defaults()
	c.Logo, _ = FetchLogo(ctx, srv.URL+"/logo.png")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
