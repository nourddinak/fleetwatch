package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestEmailAPI(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)

	// 1. Add domain
	st, res := c.do("POST", "/api/emails/domains", map[string]any{
		"domain":           "company.com",
		"inboundMode":      "both",
		"forwardEmail":     "mygmail@gmail.com",
		"outboundProvider": "resend",
		"resendApiKey":     "re_test_key_123",
	})
	if st != 201 {
		t.Fatalf("add email domain failed: %d %+v", st, res)
	}
	domID, _ := res["id"].(string)
	if domID == "" {
		t.Fatalf("missing domain id: %+v", res)
	}

	// 2. List domains
	list := c.list("/api/emails/domains")
	if len(list) != 1 {
		t.Fatalf("expected 1 domain, got %d", len(list))
	}

	// 3. Worker script
	st, scriptRes := c.do("GET", "/api/emails/worker-script", nil)
	if st != 200 || scriptRes["script"] == "" || scriptRes["webhookSecret"] == "" {
		t.Fatalf("worker-script endpoint failed: %d %+v", st, scriptRes)
	}
	webhookSecret := scriptRes["webhookSecret"].(string)

	// 4. List auto-created aliases
	aliases := c.list("/api/emails/aliases?domainId=" + domID)
	if len(aliases) < 2 {
		t.Fatalf("expected at least 2 default aliases, got %d", len(aliases))
	}

	// 5. Add custom alias
	st, aliasRes := c.do("POST", "/api/emails/aliases", map[string]any{
		"domainId":     domID,
		"localPart":    "billing",
		"displayName":  "Billing Support",
		"deliveryMode": "inbox",
	})
	if st != 201 {
		t.Fatalf("add alias failed: %d %+v", st, aliasRes)
	}
	aliasID, _ := aliasRes["id"].(string)

	// 6. Test Inbound Webhook (simulating Cloudflare Email Worker)
	inboundBody, _ := json.Marshal(map[string]any{
		"from":      "supplier@vendor.com",
		"fromName":  "Bob Vendor",
		"to":        []string{"billing@company.com"},
		"subject":   "Invoice #1004",
		"text":      "Please find your invoice attached.",
		"html":      "<p>Please find your invoice attached.</p>",
		"messageId": "<inv-1004@vendor.com>",
	})

	req, _ := http.NewRequest("POST", srv.URL+"/api/emails/inbound", bytes.NewReader(inboundBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Fleetwatch-Email-Secret", webhookSecret)
	inboundResp, err := srv.Client().Do(req)
	if err != nil || inboundResp.StatusCode != 201 {
		t.Fatalf("inbound webhook failed: %v, status=%v", err, inboundResp.StatusCode)
	}

	// 7. List messages
	st, msgListRes := c.do("GET", "/api/emails/messages?folder=inbox", nil)
	if st != 200 {
		t.Fatalf("list messages failed: %d", st)
	}
	msgs, _ := msgListRes["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in inbox, got %d", len(msgs))
	}
	firstMsg := msgs[0].(map[string]any)
	msgID := firstMsg["id"].(string)

	// 8. Get message detail
	st, detail := c.do("GET", "/api/emails/messages/"+msgID, nil)
	if st != 200 || detail["subject"] != "Invoice #1004" {
		t.Fatalf("get message detail failed: %d %+v", st, detail)
	}

	// 9. Star & Read status
	c.do("PUT", "/api/emails/messages/"+msgID+"/star", map[string]bool{"starred": true})
	c.do("PUT", "/api/emails/messages/"+msgID+"/read", map[string]bool{"read": true})

	// 10. Delete message (move to trash)
	st, _ = c.do("DELETE", "/api/emails/messages/"+msgID, nil)
	if st != 200 {
		t.Fatalf("delete message failed: %d", st)
	}

	// Check it is now in trash
	st, trashRes := c.do("GET", "/api/emails/messages?folder=trash", nil)
	trashMsgs, _ := trashRes["messages"].([]any)
	if len(trashMsgs) != 1 {
		t.Fatalf("expected 1 message in trash, got %d", len(trashMsgs))
	}

	// 11. Delete alias
	st, _ = c.do("DELETE", "/api/emails/aliases/"+aliasID, nil)
	if st != 200 {
		t.Fatalf("delete alias failed: %d", st)
	}

	// 12. Delete domain
	st, _ = c.do("DELETE", "/api/emails/domains/"+domID, nil)
	if st != 200 {
		t.Fatalf("delete domain failed: %d", st)
	}
}
