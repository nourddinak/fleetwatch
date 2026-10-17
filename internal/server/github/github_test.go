package github

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
)

// fakeGitHub answers the few API calls the dashboard makes, checking the app's JWT.
func fakeGitHub(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	checkJWT := func(r *http.Request) bool {
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			return false
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		return rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) == nil && strings.Contains(string(claims), `"iss":"42"`)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/app-manifests/good-code/conversions":
			json.NewEncoder(w).Encode(map[string]any{"id": 42, "slug": "fleetwatch-ops-ab12", "name": "Fleetwatch ops ab12", "html_url": "https://github.com/apps/fleetwatch-ops-ab12",
				"owner": map[string]string{"login": "nourddinak"}, "pem": keyPEM, "webhook_secret": "whsec", "client_secret": "cs"})
		case r.URL.Path == "/app/installations" && checkJWT(r):
			w.Write([]byte(`[{"id":7,"account":{"login":"nourddinak","type":"User"},"repository_selection":"selected","suspended_at":null}]`))
		case r.URL.Path == "/app/installations/7/access_tokens" && checkJWT(r):
			json.NewEncoder(w).Encode(map[string]any{"token": "ghs_x", "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/installation/repositories" && r.Header.Get("Authorization") == "token ghs_x":
			w.Write([]byte(`{"repositories":[{"full_name":"nourddinak/shop","private":true,"default_branch":"main","html_url":"https://github.com/nourddinak/shop"}]}`))
		case r.URL.Path == "/repos/nourddinak/shop/installation" && checkJWT(r):
			w.Write([]byte(`{"id":7}`))
		case r.URL.Path == "/repos/nourddinak/shop/actions/workflows/fleetwatch.yml/dispatches":
			if r.Header.Get("Authorization") != "token ghs_x" { // a token from before the permission was granted
				w.WriteHeader(403)
				w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
				return
			}
			w.WriteHeader(204)
		case r.Method == "DELETE" && r.URL.Path == "/app/installations/7" && checkJWT(r):
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
}

func TestConnectFlow(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	gh := fakeGitHub(t, key)
	defer gh.Close()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	s := New(d, box, live.NewBus())
	s.API = gh.URL
	ctx := context.Background()

	if _, _, err := s.Manifest("https://ops.example.com", "bad org!", "Nour"); err == nil {
		t.Fatal("bad organization accepted")
	}
	action, manifest, err := s.Manifest("https://ops.example.com", "", "Nour")
	if err != nil || !strings.HasPrefix(action, "https://github.com/settings/apps/new?state=") {
		t.Fatal(action, err)
	}
	var m map[string]any
	json.Unmarshal([]byte(manifest), &m)
	if m["redirect_url"] != "https://ops.example.com/api/integrations/github/created" || m["hook_attributes"].(map[string]any)["active"] != true {
		t.Fatalf("%v", m)
	}
	state := action[strings.Index(action, "state=")+6:]
	if _, _, err := s.Created(ctx, "good-code", "forged"); err == nil {
		t.Fatal("unknown state accepted")
	}
	install, by, err := s.Created(ctx, "good-code", state)
	if err != nil || by != "Nour" || install != "https://github.com/apps/fleetwatch-ops-ab12/installations/new" {
		t.Fatal(install, by, err)
	}
	if _, _, err := s.Created(ctx, "good-code", state); err == nil {
		t.Fatal("state used twice")
	}
	// The key is stored encrypted.
	var enc []byte
	d.QueryRow(`SELECT secret_enc FROM integrations`).Scan(&enc)
	if strings.Contains(string(enc), "PRIVATE KEY") {
		t.Fatal("key stored in the clear")
	}
	if err := s.Installed(ctx, 7); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Connected || len(st.Installations) != 1 || st.Installations[0].Account != "nourddinak" {
		t.Fatalf("%+v", st)
	}
	repos, err := s.Repos(ctx)
	if err != nil || len(repos) != 1 || repos[0].FullName != "nourddinak/shop" {
		t.Fatal(repos, err)
	}

	// A cached token made before permissions were granted is replaced, not trusted.
	s.mu.Lock()
	s.tokens[7] = token{"ghs_old", time.Now().Add(time.Hour)}
	s.mu.Unlock()
	if err := s.Dispatch(ctx, "nourddinak/shop", "fleetwatch.yml", "main"); err != nil {
		t.Fatal(err)
	}

	// Webhooks: only signed deliveries count.
	body := []byte(`{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"nourddinak/shop"},"head_commit":{"message":"Fix cart\n\nlong text"},"pusher":{"name":"nourddinak"}}`)
	mac := hmac.New(sha256.New, []byte("whsec"))
	mac.Write(body)
	if s.Webhook("push", "sha256=00", body) {
		t.Fatal("bad signature accepted")
	}
	if !s.Webhook("push", "sha256="+hex.EncodeToString(mac.Sum(nil)), body) {
		t.Fatal("good signature refused")
	}
	if p := s.Status().Pushes; len(p) != 1 || p[0].Message != "Fix cart" || p[0].Branch != "main" {
		t.Fatalf("%+v", p)
	}

	if app, err := s.Disconnect(ctx); err != nil || app.Slug != "fleetwatch-ops-ab12" || s.Status().Connected {
		t.Fatal(app, err)
	}
}

func TestLocalDashboardHasNoWebhook(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	s := New(d, box, live.NewBus())
	_, manifest, err := s.Manifest("http://127.0.0.1:8090", "acme", "Nour")
	if err != nil || !strings.Contains(manifest, `"active":false`) {
		t.Fatal(manifest, err)
	}
}
