package dns

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

// People sign in with Cloudflare (OAuth, authorization code with PKCE) through an OAuth
// client the owner creates once in their Cloudflare account; every account it connects
// keeps its own tokens, refreshed as they expire.

// CallbackPath is where Cloudflare sends people back after they sign in.
const CallbackPath = "/api/integrations/cloudflare/callback"

// Scopes the OAuth client must be created with (Cloudflare refuses a sign-in that asks
// for more than the client has).
var Scopes = []string{"offline_access", "zone.read", "dns.write", "cache.purge", "zone-settings.write", "firewall-services.write"}

const appAD = "dns.cloudflare-app"

type appKeys struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

type tokens struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"` // unix seconds
}

type signIn struct {
	owner, verifier, redirect string
	expires                   time.Time
}

func (s *Service) app() (appKeys, bool) {
	var enc []byte
	var k appKeys
	if s.db.QueryRow(`SELECT secret_enc FROM integrations WHERE provider = 'cloudflare'`).Scan(&enc) != nil {
		return k, false
	}
	raw, err := s.box.Open(enc, appAD)
	if err != nil || json.Unmarshal(raw, &k) != nil {
		return k, false
	}
	return k, k.ClientID != ""
}

// AppClientID is the OAuth client's ID ("" until the owner sets it up).
func (s *Service) AppClientID() string {
	k, _ := s.app()
	return k.ClientID
}

// basic signs a request to Cloudflare's token endpoints as the client, the way the
// dashboard's client form defaults to ("Client Secret Basic"; both parts form-encoded).
func (k appKeys) basic(req *http.Request) {
	req.SetBasicAuth(url.QueryEscape(k.ClientID), url.QueryEscape(k.ClientSecret))
}

// SetApp keeps the OAuth client the owner created in Cloudflare.
func (s *Service) SetApp(clientID, secret string) error {
	clientID, secret = strings.TrimSpace(clientID), strings.TrimSpace(secret)
	if clientID == "" || secret == "" || len(clientID) > 200 || len(secret) > 500 {
		return valid.Error("paste both the client ID and the client secret")
	}
	raw, _ := json.Marshal(appKeys{clientID, secret})
	_, err := s.db.Exec(`INSERT INTO integrations (provider, public, secret_enc, created_at) VALUES ('cloudflare', '{}', ?, ?)
		ON CONFLICT (provider) DO UPDATE SET secret_enc = excluded.secret_enc`, s.box.Seal(raw, appAD), s.Now().UnixMilli())
	s.publish("")
	return err
}

// AutoApp creates the OAuth client in the owner's Cloudflare account with a one-time API
// token (OAuth Clients Write), with everything the sign-in needs, and keeps its ID and
// secret. The token itself is not kept. It answers the account's name.
func (s *Service) AutoApp(ctx context.Context, token, publicURL string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 300 {
		return "", valid.Error("paste the API token")
	}
	c := cloudflare{token: token}
	var accounts []struct{ ID, Name string }
	if _, err := c.call(ctx, http.MethodGet, "/accounts?per_page=50", nil, &accounts); err != nil {
		return "", valid.Error("the token does not work: " + err.Error())
	}
	if len(accounts) == 0 {
		return "", valid.Error("the token cannot see any Cloudflare account: give it your account")
	}
	acc := accounts[0]
	body := map[string]any{"client_name": "Fleetwatch", "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": "client_secret_basic",
		"redirect_uris": []string{publicURL + CallbackPath}, "scopes": Scopes}
	if strings.HasPrefix(publicURL, "https://") {
		body["client_uri"] = publicURL
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ID           string `json:"id"`
		ClientSecret string `json:"client_secret"`
		Secret       string `json:"secret"`
	}
	path := "/accounts/" + acc.ID + "/oauth_clients"
	_, err := c.call(ctx, http.MethodPost, path, body, &out)
	if err != nil && strings.Contains(err.Error(), "offline") { // in case a client may not list offline access itself
		body["scopes"] = slices.DeleteFunc(slices.Clone(Scopes), func(sc string) bool { return sc == "offline_access" })
		_, err = c.call(ctx, http.MethodPost, path, body, &out)
	}
	if err != nil {
		return "", valid.Error("Cloudflare did not create the sign-in app: " + err.Error())
	}
	id, secret := cmp.Or(out.ClientID, out.ID), cmp.Or(out.ClientSecret, out.Secret)
	if id == "" || secret == "" {
		return "", valid.Error("Cloudflare made the sign-in app but did not send its ID and secret: set it up by hand with a new secret (Manage account → OAuth clients)")
	}
	return acc.Name, s.SetApp(id, secret)
}

// ClearApp forgets the OAuth client; accounts already connected stop working.
func (s *Service) ClearApp() error {
	_, err := s.db.Exec(`DELETE FROM integrations WHERE provider = 'cloudflare'`)
	s.publish("")
	return err
}

func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// SignInURL starts a sign-in for owner ("owner" or a member ID).
func (s *Service) SignInURL(publicURL, owner string) (string, error) {
	k, ok := s.app()
	if !ok {
		return "", valid.Error("Cloudflare is not set up yet: the dashboard owner adds it in Integrations")
	}
	state, verifier := random(16), random(32)
	sum := sha256.Sum256([]byte(verifier))
	redirect := publicURL + CallbackPath
	s.mu.Lock()
	for st, p := range s.pending {
		if s.Now().After(p.expires) {
			delete(s.pending, st)
		}
	}
	s.pending[state] = signIn{owner, verifier, redirect, s.Now().Add(15 * time.Minute)}
	s.mu.Unlock()
	q := url.Values{"response_type": {"code"}, "client_id": {k.ClientID}, "redirect_uri": {redirect}, "scope": {strings.Join(Scopes, " ")},
		"state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	return CloudflareWeb + "/oauth2/auth?" + q.Encode(), nil
}

// tokenCall asks Cloudflare's token endpoint for tokens.
func (s *Service) tokenCall(ctx context.Context, k appKeys, form url.Values) (tokens, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, CloudflareWeb+"/oauth2/token", strings.NewReader(form.Encode()))
	k.basic(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return tokens{}, fmt.Errorf("cannot reach Cloudflare: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.AccessToken == "" {
		msg := out.Description
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return tokens{}, errors.New("Cloudflare: " + msg)
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	return tokens{out.AccessToken, out.RefreshToken, s.Now().Unix() + out.ExpiresIn}, nil
}

// FinishSignIn trades the code Cloudflare sent back for tokens and keeps the account.
// Signing in again with an account already connected renews it instead. Once the state
// checks out, the answer names whose sign-in it was, even on an error.
func (s *Service) FinishSignIn(ctx context.Context, code, state string) (Account, error) {
	s.mu.Lock()
	p, ok := s.pending[state]
	delete(s.pending, state)
	s.mu.Unlock()
	if !ok || s.Now().After(p.expires) {
		return Account{}, valid.Error("this Cloudflare sign-in was not started here, or took too long; start again")
	}
	who := Account{Owner: p.owner}
	if code == "" || len(code) > 2000 {
		return who, valid.Error("Cloudflare did not send a code")
	}
	k, ok := s.app()
	if !ok {
		return who, valid.Error("Cloudflare is not set up")
	}
	t, err := s.tokenCall(ctx, k, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.redirect}, "code_verifier": {p.verifier}})
	if err != nil {
		return who, err
	}
	zones, err := cloudflare{token: t.Access}.Zones(ctx)
	if err != nil {
		return who, err
	}
	a := Account{ID: "dns_" + random(8), Owner: p.owner, Provider: "cloudflare", Label: "Cloudflare", Zones: zones, CreatedAt: s.Now().UnixMilli()}
	for _, old := range s.Accounts(p.owner) { // the same Cloudflare account again
		if slices.ContainsFunc(old.Zones, func(z Zone) bool { return slices.ContainsFunc(zones, func(n Zone) bool { return n.ID == z.ID }) }) {
			old.Zones, old.Error = zones, ""
			a = old
			break
		}
	}
	if len(zones) > 0 {
		a.Label = zones[0].Name
		if len(zones) > 1 {
			a.Label += fmt.Sprintf(" and %d more", len(zones)-1)
		}
	}
	raw, _ := json.Marshal(t)
	data, _ := json.Marshal(a)
	if _, err := s.db.Exec(`INSERT INTO dns_accounts (id, owner, data, secret_enc, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET data = excluded.data, secret_enc = excluded.secret_enc`,
		a.ID, a.Owner, string(data), s.box.Seal(raw, secretAD+a.ID), a.CreatedAt); err != nil {
		return who, err
	}
	s.publish(a.Owner)
	return a, nil
}

// token gives an account's access token, refreshing it first when it is about to expire.
// Cloudflare hands out a new refresh token each time, so refreshes go one at a time.
func (s *Service) token(ctx context.Context, a *Account, enc []byte) (string, error) {
	var t tokens
	if plain, err := s.box.Open(enc, secretAD+a.ID); err != nil || json.Unmarshal(plain, &t) != nil {
		return "", errors.New("the account's sign-in cannot be read")
	}
	if s.Now().Unix() < t.Expires-60 {
		return t.Access, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.QueryRow(`SELECT secret_enc FROM dns_accounts WHERE id = ?`, a.ID).Scan(&enc) == nil { // another call may have refreshed it
		if plain, err := s.box.Open(enc, secretAD+a.ID); err == nil {
			json.Unmarshal(plain, &t)
		}
	}
	if s.Now().Unix() < t.Expires-60 {
		return t.Access, nil
	}
	k, ok := s.app()
	if !ok || t.Refresh == "" {
		return "", errors.New("the Cloudflare sign-in has expired: sign in with Cloudflare again")
	}
	nt, err := s.tokenCall(ctx, k, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	if err != nil {
		a.Error = "the Cloudflare sign-in has expired: sign in with Cloudflare again"
		s.save(*a)
		return "", errors.New(a.Error)
	}
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	raw, _ := json.Marshal(nt)
	s.db.Exec(`UPDATE dns_accounts SET secret_enc = ? WHERE id = ?`, s.box.Seal(raw, secretAD+a.ID), a.ID)
	return nt.Access, nil
}

// revoke signs an account out of Cloudflare (best effort).
func (s *Service) revoke(id string, enc []byte) {
	var t tokens
	k, ok := s.app()
	plain, err := s.box.Open(enc, secretAD+id)
	if !ok || err != nil || json.Unmarshal(plain, &t) != nil || t.Refresh == "" {
		return
	}
	form := url.Values{"token": {t.Refresh}, "token_type_hint": {"refresh_token"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, CloudflareWeb+"/oauth2/revoke", strings.NewReader(form.Encode()))
	k.basic(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}
