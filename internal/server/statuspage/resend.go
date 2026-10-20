package statuspage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Resend (resend.com) sends emails from your own domain: the domain is added there,
// its DNS records go in (by Fleetwatch in Cloudflare, or by hand), Resend verifies
// them, and the sender sends through Resend's SMTP with the API key as password.

var ResendAPI = "https://api.resend.com" // tests point it at a fake

type ResendRecord struct {
	Record   string `json:"record"` // SPF | DKIM | …
	Name     string `json:"name"`   // relative to the domain: "send", "resend._domainkey"
	Type     string `json:"type"`   // TXT | MX
	Value    string `json:"value"`
	Priority int    `json:"priority,omitempty"`
	Status   string `json:"status"` // not_started | pending | verified | failed
}

type ResendDomain struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Records []ResendRecord `json:"records"`
}

var resendClient = &http.Client{Timeout: 20 * time.Second}

func resendCall(ctx context.Context, key, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, ResendAPI+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := resendClient.Do(req)
	if err != nil {
		return errors.New("cannot reach Resend")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		var e struct{ Message string }
		json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = res.Status
		}
		return fmt.Errorf("Resend: %s", strings.TrimSuffix(e.Message, "."))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ResendAddDomain adds a domain to Resend (or finds it, added before) with its records.
func ResendAddDomain(ctx context.Context, key, name string) (ResendDomain, error) {
	var list struct {
		Data []ResendDomain `json:"data"`
	}
	if err := resendCall(ctx, key, http.MethodGet, "/domains", nil, &list); err != nil {
		return ResendDomain{}, err
	}
	for _, d := range list.Data {
		if strings.EqualFold(d.Name, name) {
			return ResendGetDomain(ctx, key, d.ID)
		}
	}
	var d ResendDomain
	err := resendCall(ctx, key, http.MethodPost, "/domains", map[string]string{"name": name}, &d)
	return d, err
}

func ResendGetDomain(ctx context.Context, key, id string) (ResendDomain, error) {
	var d ResendDomain
	err := resendCall(ctx, key, http.MethodGet, "/domains/"+id, nil, &d)
	return d, err
}

// ResendVerify asks Resend to check the domain's records now.
func ResendVerify(ctx context.Context, key, id string) error {
	return resendCall(ctx, key, http.MethodPost, "/domains/"+id+"/verify", nil, nil)
}

// DomainCheck asks Resend to check a domain sender's records again, and says how it stands.
func (m *Mailer) DomainCheck(ctx context.Context, id string) (ResendDomain, error) {
	m.mu.Lock()
	s, ok := m.pick(id)
	m.mu.Unlock()
	if !ok || s.ID != id || s.DomainID == "" {
		return ResendDomain{}, errors.New("this sender is not a domain sender")
	}
	key, err := m.box.Open(s.Sealed, "smtp.password")
	if err != nil {
		return ResendDomain{}, err
	}
	ResendVerify(ctx, string(key), s.DomainID) // a failed check still answers its status below
	return ResendGetDomain(ctx, string(key), s.DomainID)
}
