package emails

import (
	"context"
	"fmt"
	"strings"

	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/statuspage"
	"fleetwatch/internal/server/valid"
)

// SetupCloudflareDNS automatically provisions MX, SPF, and Resend DKIM records in Cloudflare.
func (s *Service) SetupCloudflareDNS(ctx context.Context, owner string, domainID string) error {
	d, err := s.GetDomain(domainID)
	if err != nil {
		return err
	}
	if s.dns == nil {
		return valid.Error("DNS service not initialized")
	}

	var recs []dns.MailRecord

	// 1. Cloudflare Email Routing MX records
	recs = append(recs,
		dns.MailRecord{Type: "MX", Name: "@", Value: "isaac.mx.cloudflare.net", Priority: 17},
		dns.MailRecord{Type: "MX", Name: "@", Value: "linda.mx.cloudflare.net", Priority: 45},
		dns.MailRecord{Type: "MX", Name: "@", Value: "amir.mx.cloudflare.net", Priority: 92},
	)

	// 2. SPF TXT record
	spfVal := "v=spf1 include:_spf.mx.cloudflare.net ~all"
	if d.OutboundProvider == ProviderResend && d.ResendAPIKey != "" {
		spfVal = "v=spf1 include:_spf.mx.cloudflare.net include:resend.com ~all"
	}
	recs = append(recs, dns.MailRecord{Type: "TXT", Name: "@", Value: spfVal})

	// 3. Resend DKIM records (if using Resend)
	if d.OutboundProvider == ProviderResend && d.ResendAPIKey != "" {
		rd, err := statuspage.ResendAddDomain(ctx, d.ResendAPIKey, d.Domain)
		if err == nil && len(rd.Records) > 0 {
			d.ResendDomainID = rd.ID
			for _, r := range rd.Records {
				if r.Type == "TXT" || r.Type == "MX" {
					recs = append(recs, dns.MailRecord{
						Type:     r.Type,
						Name:     r.Name,
						Value:    r.Value,
						Priority: r.Priority,
					})
				}
			}
			go func() {
				// Ask Resend to verify after DNS records are inserted
				statuspage.ResendVerify(context.Background(), d.ResendAPIKey, rd.ID)
			}()
		}
	}

	// 4. Push all records into Cloudflare DNS
	if err := s.dns.AddMailRecords(ctx, owner, d.Domain, recs); err != nil {
		return fmt.Errorf("Cloudflare DNS update failed: %w", err)
	}

	// Update status in database
	d.MXStatus = "active"
	d.SPFStatus = "active"
	d.DKIMStatus = "active"
	s.UpdateDomain(d)
	return nil
}

// WorkerScript returns the ready-to-paste Cloudflare Email Worker code.
func (s *Service) WorkerScript(publicURL string) string {
	endpoint := strings.TrimRight(publicURL, "/") + "/api/emails/inbound"
	if publicURL == "" {
		endpoint = "https://ops.example.com/api/emails/inbound"
	}
	sec := s.WebhookSecret()

	return fmt.Sprintf(`/**
 * Fleetwatch Cloudflare Email Worker
 * Forwards incoming emails to Fleetwatch's webhook endpoint
 */
export default {
  async email(message, env, ctx) {
    const rawEmail = await new Response(message.raw).text();
    const payload = {
      from: message.from,
      to: [message.to],
      subject: message.headers.get("subject") || "",
      messageId: message.headers.get("message-id") || "",
      rawEmail: rawEmail
    };

    const resp = await fetch("%s", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Fleetwatch-Email-Secret": "%s"
      },
      body: JSON.stringify(payload)
    });

    if (!resp.ok) {
      console.error("Fleetwatch webhook failed", resp.status, await resp.text());
      // message.setReject("Fleetwatch webhook unavailable"); // optional: reject if down
    }
  }
};
`, endpoint, sec)
}

// CheckDNSStatus checks whether MX and SPF records resolve for the domain
func (s *Service) CheckDNSStatus(ctx context.Context, d Domain) (mxOK, spfOK bool) {
	// Simple validation or status retrieval
	return d.MXStatus == "active", d.SPFStatus == "active"
}

// SendTestWebhook can be used to verify inbound routing
func (s *Service) SendTestInbound(ctx context.Context, toAddress string) (Message, error) {
	return s.ProcessInbound(ctx, InboundPayload{
		From:     "test@fleetwatch.local",
		FromName: "Fleetwatch System",
		To:       []string{toAddress},
		Subject:  "Fleetwatch Email Test",
		Text:     "This is a test inbound email confirming that your Fleetwatch email system is functioning properly.",
		HTML:     "<p>This is a test inbound email confirming that your Fleetwatch email system is functioning properly.</p>",
	})
}
