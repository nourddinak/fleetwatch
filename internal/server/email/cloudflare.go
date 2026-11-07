package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	emailworker "fleetwatch/cloudflare/email-worker"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Email needs Cloudflare permissions the dashboard's DNS sign-in does not ask for, so it
// has its own API token (least privilege, see TokenPermissions), kept encrypted.
// With it Fleetwatch deploys the Worker, points each domain's catch-all at it, adds
// the Gmail destinations and fixes the DNS records.

// TokenPermissions is what the token needs, as Cloudflare's token page names them.
var TokenPermissions = []string{
	"Account · Workers Scripts · Edit",
	"Account · Email Routing Addresses · Edit",
	"Zone · Email Routing Rules · Edit",
	"Zone · DNS · Edit",
	"Zone · Zone · Read",
	"Zone · Zone Settings · Edit (turning Email Routing on)",
}

const cfKey = "email.cloudflare"

type cfSettings struct {
	Sealed     []byte `json:"sealed,omitempty"` // an API token pasted here, or:
	SignIn     bool   `json:"signIn,omitempty"` // the owner's "Sign in with Cloudflare" account
	AccountID  string `json:"accountId"`
	Account    string `json:"account"`
	Worker     string `json:"worker"`
	Secret     []byte `json:"secret,omitempty"` // sealed: the Worker signs what it posts with it
	URL        string `json:"url"`              // where the Worker posts mail
	DeployedAt int64  `json:"deployedAt"`
	EdgeBytes  int    `json:"edgeBytes"`
	Error      string `json:"error,omitempty"`
}

// CloudflareStatus is what the settings page shows (never the token).
type CloudflareStatus struct {
	Connected    bool          `json:"connected"`
	SignIn       bool          `json:"signIn"` // through the dashboard's Cloudflare sign-in (else a token)
	AccountID    string        `json:"accountId,omitempty"`
	Account      string        `json:"account,omitempty"`
	Worker       string        `json:"worker,omitempty"`
	URL          string        `json:"url,omitempty"`
	DeployedAt   int64         `json:"deployedAt,omitempty"`
	EdgeBytes    int           `json:"edgeBytes"`
	EdgeLimit    int           `json:"edgeLimit"`
	Error        string        `json:"error,omitempty"`
	Permissions  []string      `json:"permissions"`
	Destinations []Destination `json:"destinations"`
}

type Destination struct {
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
}

// edgeLimit: a Worker secret holds 5 KB.
const edgeLimit = 5 * 1024

func (s *Service) cf() cfSettings {
	var c cfSettings
	settings.Load(s.db, cfKey, &c)
	if c.Worker == "" {
		c.Worker = "fleetwatch-email"
	}
	return c
}

func (s *Service) saveCF(c cfSettings) error { return settings.Save(s.db, cfKey, c) }

type cfClient struct {
	base, token string
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

var cfHTTP = &http.Client{Timeout: 60 * time.Second}

func (c cfClient) do(ctx context.Context, method, path, ctype string, body io.Reader, out any) (cfEnvelope, error) {
	var env cfEnvelope
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return env, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := cfHTTP.Do(req)
	if err != nil {
		return env, errors.New("cannot reach Cloudflare")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	json.Unmarshal(raw, &env)
	if !env.Success {
		msg := "Cloudflare refused the request (" + res.Status + ")"
		if len(env.Errors) > 0 {
			msg = "Cloudflare: " + env.Errors[0].Message
		}
		if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusUnauthorized {
			msg += " — the sign-in or token may be missing a permission"
		}
		return env, errors.New(msg)
	}
	if out != nil {
		json.Unmarshal(env.Result, out)
	}
	return env, nil
}

func (c cfClient) call(ctx context.Context, method, path string, body, out any) (cfEnvelope, error) {
	var rd io.Reader
	ctype := ""
	if body != nil {
		b, _ := json.Marshal(body)
		rd, ctype = bytes.NewReader(b), "application/json"
	}
	return c.do(ctx, method, path, ctype, rd, out)
}

func (s *Service) client(ctx context.Context) (cfClient, cfSettings, error) {
	c := s.cf()
	switch {
	case len(c.Sealed) > 0:
		tok, err := s.box.Open(c.Sealed, "email.cloudflare")
		if err != nil {
			return cfClient{}, c, valid.Error("the saved Cloudflare token cannot be read: paste it again")
		}
		return cfClient{base: s.CloudflareAPI, token: string(tok)}, c, nil
	case c.SignIn && s.SignIn != nil:
		tok, _, err := s.SignIn(ctx)
		if err != nil {
			return cfClient{}, c, err
		}
		return cfClient{base: s.CloudflareAPI, token: tok}, c, nil
	}
	return cfClient{}, c, valid.Error("connect Cloudflare first (Emails → Settings)")
}

type Zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

func (c cfClient) zones(ctx context.Context) ([]Zone, error) {
	out := []Zone{}
	for page := 1; page <= 20; page++ {
		var zs []Zone
		env, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &zs)
		if err != nil {
			return nil, err
		}
		out = append(out, zs...)
		if page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return out, nil
}

// ConnectCloudflare saves a pasted API token after checking it can list zones.
// publicURL is where the Worker will post mail (the dashboard's public address).
func (s *Service) ConnectCloudflare(ctx context.Context, token, publicURL string) (CloudflareStatus, error) {
	token = strings.TrimSpace(token)
	if len(token) < 20 || len(token) > 300 || strings.ContainsAny(token, " \r\n") {
		return CloudflareStatus{}, valid.Error("paste the API token Cloudflare showed you")
	}
	return s.connect(ctx, token, publicURL, func(c *cfSettings) { c.Sealed, c.SignIn = s.box.Seal([]byte(token), "email.cloudflare"), false })
}

// UseSignIn sets email up through the owner's connected Cloudflare account (the
// dashboard's "Sign in with Cloudflare"): nothing to paste.
func (s *Service) UseSignIn(ctx context.Context, publicURL string) (CloudflareStatus, error) {
	if s.SignIn == nil {
		return CloudflareStatus{}, valid.Error("Sign in with Cloudflare is not available")
	}
	tok, _, err := s.SignIn(ctx)
	if err != nil {
		return CloudflareStatus{}, err
	}
	return s.connect(ctx, tok, publicURL, func(c *cfSettings) { c.Sealed, c.SignIn = nil, true })
}

func (s *Service) connect(ctx context.Context, token, publicURL string, keep func(*cfSettings)) (CloudflareStatus, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.HasPrefix(u.Hostname(), "localhost") || strings.HasPrefix(u.Hostname(), "127.") {
		return CloudflareStatus{}, valid.Error("the Worker must reach this dashboard over https on a public address: open the dashboard at its https address (or set OPS_PUBLIC_URL)")
	}
	zs, err := cfClient{base: s.CloudflareAPI, token: token}.zones(ctx)
	if err != nil {
		return CloudflareStatus{}, err
	}
	if len(zs) == 0 {
		return CloudflareStatus{}, valid.Error("Cloudflare shows no domains for this account")
	}
	c := s.cf()
	keep(&c)
	c.AccountID, c.Account, c.URL = zs[0].Account.ID, zs[0].Account.Name, strings.TrimRight(publicURL, "/")
	if len(c.Secret) == 0 {
		c.Secret = s.box.Seal([]byte(randomHex(32)), "email.inbound")
	}
	c.Error = ""
	if err := s.saveCF(c); err != nil {
		return CloudflareStatus{}, err
	}
	err = s.PushEdge(ctx)
	st, _ := s.Cloudflare(ctx)
	return st, err
}

func (s *Service) DisconnectCloudflare() error {
	c := s.cf()
	c.Sealed, c.SignIn, c.DeployedAt, c.Error = nil, false, 0, ""
	return s.saveCF(c) // the inbound secret stays: the deployed Worker keeps working
}

// InboundSecret is what the Worker signs with ("" until Cloudflare is connected or a
// manual setup generated one).
func (s *Service) InboundSecret() string {
	c := s.cf()
	if len(c.Secret) == 0 {
		return ""
	}
	b, err := s.box.Open(c.Secret, "email.inbound")
	if err != nil {
		return ""
	}
	return string(b)
}

// ManualSecret makes (or shows) the secret for a Worker deployed by hand with Wrangler.
func (s *Service) ManualSecret() (string, error) {
	if v := s.InboundSecret(); v != "" {
		return v, nil
	}
	c := s.cf()
	v := randomHex(32)
	c.Secret = s.box.Seal([]byte(v), "email.inbound")
	return v, s.saveCF(c)
}

func (s *Service) Cloudflare(ctx context.Context) (CloudflareStatus, error) {
	c := s.cf()
	st := CloudflareStatus{Connected: len(c.Sealed) > 0 || c.SignIn, SignIn: c.SignIn, AccountID: c.AccountID, Account: c.Account, Worker: c.Worker, URL: c.URL, DeployedAt: c.DeployedAt,
		EdgeBytes: c.EdgeBytes, EdgeLimit: edgeLimit, Error: c.Error, Permissions: TokenPermissions, Destinations: []Destination{}}
	if !st.Connected {
		return st, nil
	}
	cl, _, err := s.client(ctx)
	if err != nil {
		st.Error = err.Error()
		return st, nil
	}
	ds, err := cl.destinations(ctx, c.AccountID)
	if err != nil {
		st.Error = err.Error()
		return st, nil
	}
	st.Destinations = ds
	return st, nil
}

// Zones are the Cloudflare domains the token sees (to turn on for email).
func (s *Service) Zones(ctx context.Context) ([]Zone, error) {
	cl, _, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	return cl.zones(ctx)
}

func (c cfClient) destinations(ctx context.Context, account string) ([]Destination, error) {
	out := []Destination{}
	for page := 1; page <= 10; page++ {
		var list []struct {
			Email    string  `json:"email"`
			Verified *string `json:"verified"`
		}
		env, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/accounts/%s/email/routing/addresses?per_page=50&page=%d", account, page), nil, &list)
		if err != nil {
			return nil, err
		}
		for _, d := range list {
			out = append(out, Destination{Email: strings.ToLower(d.Email), Verified: d.Verified != nil && *d.Verified != ""})
		}
		if page >= env.ResultInfo.TotalPages {
			break
		}
	}
	return out, nil
}

// edgeRoutes is what the Worker knows by itself: where Gmail copies go when this
// dashboard cannot be reached. Only addresses that differ from their domain are listed.
func (s *Service) edgeRoutes() (map[string]any, []string, error) {
	doms, err := s.Domains()
	if err != nil {
		return nil, nil, err
	}
	addrs, err := s.Addresses()
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]Domain{}
	d := map[string][]string{}
	all := []string{}
	for _, x := range doms {
		byID[x.ID] = x
		d[x.Name] = x.ForwardTo
		all = append(all, x.ForwardTo...)
	}
	a := map[string][]string{}
	for _, x := range addrs {
		dom := byID[x.DomainID]
		fwd := forwardFor(x, dom)
		if x.Disabled || !x.Receive {
			fwd = []string{}
		}
		if jsonList(fwd) != jsonList(dom.ForwardTo) {
			a[x.Address] = fwd
			all = append(all, fwd...)
		}
	}
	return map[string]any{"v": 1, "d": d, "a": a}, all, nil
}

func (s *Service) edgeChanged() {
	select {
	case s.edgeSoon <- struct{}{}:
	default:
	}
}

// PushEdge deploys the Worker with the current routes, and asks Cloudflare to verify
// any new Gmail destination (it emails the address a confirmation link).
func (s *Service) PushEdge(ctx context.Context) error {
	cl, c, err := s.client(ctx)
	if err != nil {
		if len(c.Sealed) == 0 && !c.SignIn {
			return nil // not connected: a Worker deployed by hand keeps its own routes
		}
		c.Error = err.Error() // e.g. the sign-in expired: shown on the settings page
		s.saveCF(c)
		return err
	}
	routes, dests, err := s.edgeRoutes()
	if err != nil {
		return err
	}
	fail := func(err error) error {
		c.Error = err.Error()
		s.saveCF(c)
		return err
	}
	have, err := cl.destinations(ctx, c.AccountID)
	if err != nil {
		return fail(err)
	}
	known := map[string]bool{}
	for _, h := range have {
		known[h.Email] = true
	}
	for _, d := range dests {
		if !known[d] {
			known[d] = true
			if _, err := cl.call(ctx, http.MethodPost, "/accounts/"+c.AccountID+"/email/routing/addresses", map[string]string{"email": d}, nil); err != nil {
				return fail(fmt.Errorf("adding %s as a destination: %w", d, err))
			}
		}
	}
	raw, _ := json.Marshal(routes)
	note := ""
	if len(raw) > edgeLimit { // too many per-address exceptions: keep the domains' defaults
		raw, _ = json.Marshal(map[string]any{"v": 1, "d": routes["d"], "a": map[string]any{}})
		note = fmt.Sprintf("the per-address forwarding is too large for the Worker (%d bytes, %d at most): while the dashboard is down, mail is copied by domain only", len(raw), edgeLimit)
	}
	meta := map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": "2025-06-01",
		"bindings": []map[string]string{
			{"type": "plain_text", "name": "FLEETWATCH_URL", "text": c.URL + "/api/emails/inbound"},
			{"type": "secret_text", "name": "INBOUND_SECRET", "text": s.InboundSecret()},
			{"type": "secret_text", "name": "ROUTES", "text": string(raw)},
		},
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mpart, _ := mw.CreateFormField("metadata")
	json.NewEncoder(mpart).Encode(meta)
	wpart, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="worker.js"; filename="worker.js"`},
		"Content-Type":        {"application/javascript+module"},
	})
	wpart.Write(emailworker.Script)
	mw.Close()
	if _, err := cl.do(ctx, http.MethodPut, "/accounts/"+c.AccountID+"/workers/scripts/"+c.Worker, mw.FormDataContentType(), &body, nil); err != nil {
		return fail(fmt.Errorf("deploying the Worker: %w", err))
	}
	c.DeployedAt, c.EdgeBytes, c.Error = s.Now().Unix(), len(raw), note
	return s.saveCF(c)
}

// ---------- a domain's Cloudflare setup ----------

func (s *Service) zoneFor(ctx context.Context, cl cfClient, d Domain) (string, error) {
	if d.ZoneID != "" {
		return d.ZoneID, nil
	}
	zs, err := cl.zones(ctx)
	if err != nil {
		return "", err
	}
	for _, z := range zs {
		if strings.EqualFold(z.Name, d.Name) || strings.HasSuffix(strings.ToLower(d.Name), "."+strings.ToLower(z.Name)) {
			s.db.Exec(`UPDATE mail_domains SET zone_id = ? WHERE id = ?`, z.ID, d.ID)
			return z.ID, nil
		}
	}
	return "", valid.Error(d.Name + " is not in this Cloudflare account")
}

type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (c cfClient) txt(ctx context.Context, zone, name string) ([]dnsRecord, error) {
	var rs []dnsRecord
	_, err := c.call(ctx, http.MethodGet, "/zones/"+zone+"/dns_records?type=TXT&per_page=100&name="+url.QueryEscape(name), nil, &rs)
	return rs, err
}

// Setup does a domain's Cloudflare part: SPF (merged into the record you have), Email
// Routing on (MX records), the catch-all to the Worker, and a DMARC record if none.
// It answers what it did, step by step.
func (s *Service) Setup(ctx context.Context, domainID string) ([]string, error) {
	d, err := s.Domain(domainID)
	if err != nil {
		return nil, err
	}
	cl, c, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := s.zoneFor(ctx, cl, d)
	if err != nil {
		return nil, err
	}
	var did []string
	if c.DeployedAt == 0 {
		if err := s.PushEdge(ctx); err != nil {
			return did, err
		}
		did = append(did, "deployed the Worker "+c.Worker)
	}
	// SPF: merge Cloudflare's include into an existing record rather than adding a second.
	recs, err := cl.txt(ctx, zone, d.Name)
	if err != nil {
		return did, err
	}
	var spf []dnsRecord
	for _, r := range recs {
		if strings.HasPrefix(strings.ToLower(strings.Trim(r.Content, `"`)), "v=spf1") {
			spf = append(spf, r)
		}
	}
	const cfInclude = "_spf.mx.cloudflare.net"
	switch len(spf) {
	case 0:
		if _, err := cl.call(ctx, http.MethodPost, "/zones/"+zone+"/dns_records", map[string]any{"type": "TXT", "name": d.Name, "content": "v=spf1 include:" + cfInclude + " ~all", "ttl": 1}, nil); err != nil {
			return did, fmt.Errorf("adding the SPF record: %w", err)
		}
		did = append(did, "added an SPF record")
	case 1:
		merged := MergeSPF(strings.Trim(spf[0].Content, `"`), cfInclude)
		if merged != strings.Trim(spf[0].Content, `"`) {
			if _, err := cl.call(ctx, http.MethodPatch, "/zones/"+zone+"/dns_records/"+spf[0].ID, map[string]any{"content": merged}, nil); err != nil {
				return did, fmt.Errorf("updating the SPF record: %w", err)
			}
			did = append(did, "added Cloudflare to your SPF record")
		}
	default:
		return did, valid.Error(d.Name + " has more than one SPF record: merge them into one by hand first (two make receivers reject your mail)")
	}
	// Turns routing on and adds the MX records (the older /enable endpoint as a fallback).
	if _, err := cl.call(ctx, http.MethodPost, "/zones/"+zone+"/email/routing/dns", map[string]any{"name": d.Name}, nil); err != nil && !strings.Contains(strings.ToLower(err.Error()), "already") {
		if _, err2 := cl.call(ctx, http.MethodPost, "/zones/"+zone+"/email/routing/enable", map[string]any{}, nil); err2 != nil && !strings.Contains(strings.ToLower(err2.Error()), "already") {
			return did, fmt.Errorf("turning Email Routing on: %w", err)
		}
	}
	did = append(did, "turned Cloudflare Email Routing on (MX records)")
	rule := map[string]any{"name": "Fleetwatch", "enabled": true, "matchers": []map[string]string{{"type": "all"}},
		"actions": []map[string]any{{"type": "worker", "value": []string{c.Worker}}}}
	if _, err := cl.call(ctx, http.MethodPut, "/zones/"+zone+"/email/routing/rules/catch_all", rule, nil); err != nil {
		return did, fmt.Errorf("pointing the catch-all at the Worker: %w", err)
	}
	did = append(did, "sent every address of "+d.Name+" to the Worker (catch-all rule)")
	if dm, err := cl.txt(ctx, zone, "_dmarc."+d.Name); err == nil && len(dm) == 0 {
		if _, err := cl.call(ctx, http.MethodPost, "/zones/"+zone+"/dns_records", map[string]any{"type": "TXT", "name": "_dmarc." + d.Name, "content": "v=DMARC1; p=none", "ttl": 1}, nil); err == nil {
			did = append(did, "added a DMARC record (p=none: report only)")
		}
	}
	s.CheckDomain(ctx, domainID)
	return did, nil
}

// routingCheck asks Cloudflare whether routing is on and the catch-all reaches the Worker.
func (s *Service) routingCheck(ctx context.Context, d Domain) Check {
	cl, c, err := s.client(ctx)
	if err != nil {
		return Check{Status: "unknown", Detail: "connect Cloudflare to check routing (or check it in Cloudflare → Email Routing)"}
	}
	zone, err := s.zoneFor(ctx, cl, d)
	if err != nil {
		return Check{Status: "error", Detail: err.Error()}
	}
	var st struct {
		Enabled bool   `json:"enabled"`
		Status  string `json:"status"`
	}
	if _, err := cl.call(ctx, http.MethodGet, "/zones/"+zone+"/email/routing", nil, &st); err != nil {
		return Check{Status: "error", Detail: err.Error()}
	}
	if !st.Enabled {
		return Check{Status: "missing", Detail: "Email Routing is off for this domain"}
	}
	var rule struct {
		Enabled bool `json:"enabled"`
		Actions []struct {
			Type  string   `json:"type"`
			Value []string `json:"value"`
		} `json:"actions"`
	}
	if _, err := cl.call(ctx, http.MethodGet, "/zones/"+zone+"/email/routing/rules/catch_all", nil, &rule); err != nil {
		return Check{Status: "error", Detail: err.Error()}
	}
	if !rule.Enabled || len(rule.Actions) == 0 || rule.Actions[0].Type != "worker" || !containsStr(rule.Actions[0].Value, c.Worker) {
		return Check{Status: "wrong", Detail: "the catch-all rule does not send mail to the " + c.Worker + " Worker"}
	}
	var rules []struct {
		Enabled bool `json:"enabled"`
		Actions []struct {
			Type  string   `json:"type"`
			Value []string `json:"value"`
		} `json:"actions"`
	}
	cl.call(ctx, http.MethodGet, "/zones/"+zone+"/email/routing/rules?per_page=50", nil, &rules)
	bypass := 0
	for _, r := range rules {
		if r.Enabled && len(r.Actions) > 0 && !(r.Actions[0].Type == "worker" && containsStr(r.Actions[0].Value, c.Worker)) {
			bypass++
		}
	}
	if bypass > 0 {
		return Check{Status: "ok", Detail: fmt.Sprintf("%d older routing rules send some addresses elsewhere, past Fleetwatch", bypass)}
	}
	return Check{Status: "ok", Detail: "status " + st.Status}
}

// MergeSPF adds include:<host> to an SPF record, before its "all" (or redirect).
func MergeSPF(record, host string) string {
	parts := strings.Fields(record)
	for _, p := range parts {
		if strings.EqualFold(p, "include:"+host) {
			return record
		}
	}
	at := len(parts)
	for i, p := range parts {
		l := strings.ToLower(p)
		if strings.TrimLeft(l, "+-~?") == "all" || strings.HasPrefix(l, "redirect=") {
			at = i
			break
		}
	}
	out := append(append(append([]string{}, parts[:at]...), "include:"+host), parts[at:]...)
	return strings.Join(out, " ")
}
