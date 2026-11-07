package emails

import (
	"context"
	"testing"

	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/secret"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	box, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new secret box: %v", err)
	}

	tmpDir := t.TempDir()
	return NewService(d, box, nil, nil, tmpDir)
}

func TestDomainsAndAliases(t *testing.T) {
	s := newTestService(t)

	// Add domain
	dom, err := s.AddDomain(Domain{
		Domain:           "example.com",
		InboundMode:      DeliveryBoth,
		ForwardEmail:     "admin@gmail.com",
		OutboundProvider: ProviderResend,
		ResendAPIKey:     "re_test_123",
	})
	if err != nil {
		t.Fatalf("add domain: %v", err)
	}

	if dom.Domain != "example.com" || dom.ID == "" {
		t.Fatalf("unexpected domain: %+v", dom)
	}

	// Should have auto-created contact@ and noreply@ aliases
	aliases, err := s.ListAliases(dom.ID)
	if err != nil {
		t.Fatalf("list aliases: %v", err)
	}
	if len(aliases) < 2 {
		t.Fatalf("expected at least 2 default aliases, got %d", len(aliases))
	}

	// Add custom alias: support@example.com
	supp, err := s.AddAlias(Alias{
		DomainID:     dom.ID,
		LocalPart:    "support",
		DisplayName:  "Support Desk",
		DeliveryMode: DeliveryForward,
		ForwardTo:    "support-team@gmail.com",
	})
	if err != nil {
		t.Fatalf("add alias: %v", err)
	}
	if supp.FullAddress != "support@example.com" {
		t.Fatalf("expected support@example.com, got %s", supp.FullAddress)
	}

	// Find alias
	foundAlias, foundDom, err := s.FindAlias("support@example.com")
	if err != nil {
		t.Fatalf("find alias: %v", err)
	}
	if foundAlias.ID != supp.ID || foundDom.ID != dom.ID {
		t.Fatalf("mismatched alias/domain: %+v %+v", foundAlias, foundDom)
	}

	// Catch-all alias test
	_, err = s.AddAlias(Alias{
		DomainID:     dom.ID,
		LocalPart:    "*",
		DisplayName:  "Catch-All",
		DeliveryMode: DeliveryInbox,
	})
	if err != nil {
		t.Fatalf("add catch-all: %v", err)
	}

	foundCatchAll, _, err := s.FindAlias("random-user@example.com")
	if err != nil {
		t.Fatalf("find catch-all: %v", err)
	}
	if foundCatchAll.LocalPart != "*" {
		t.Fatalf("expected catch-all *, got %s", foundCatchAll.LocalPart)
	}
}

func TestInboundProcessing(t *testing.T) {
	s := newTestService(t)

	dom, err := s.AddDomain(Domain{
		Domain:       "testdomain.org",
		InboundMode:  DeliveryInbox,
		ForwardEmail: "owner@gmail.com",
	})
	if err != nil {
		t.Fatalf("add domain: %v", err)
	}

	ctx := context.Background()

	// Inbound message
	msg, err := s.ProcessInbound(ctx, InboundPayload{
		From:     "client@external.com",
		FromName: "Alice Client",
		To:       []string{"contact@testdomain.org"},
		Subject:  "Inquiry about services",
		Text:     "Hello, I would like to learn more about your services.",
		HTML:     "<p>Hello, I would like to learn more about your services.</p>",
	})
	if err != nil {
		t.Fatalf("process inbound: %v", err)
	}

	if msg.DomainID != dom.ID || msg.Folder != FolderInbox || msg.Snippet == "" {
		t.Fatalf("unexpected message: %+v", msg)
	}

	// Verify message in inbox list
	msgs, total, err := s.ListMessages(MessageFilter{Folder: FolderInbox, DomainID: dom.ID})
	if err != nil || total != 1 || len(msgs) != 1 {
		t.Fatalf("list messages failed: %v, total=%d", err, total)
	}

	// Toggle read
	if err := s.SetRead(msg.ID, true); err != nil {
		t.Fatalf("set read: %v", err)
	}

	// Toggle starred
	if err := s.SetStarred(msg.ID, true); err != nil {
		t.Fatalf("set starred: %v", err)
	}

	mCheck, _ := s.GetMessage(msg.ID)
	if !mCheck.IsRead || !mCheck.IsStarred {
		t.Fatalf("expected read and starred, got read=%v star=%v", mCheck.IsRead, mCheck.IsStarred)
	}

	// Delete to trash
	if err := s.DeleteMessage(msg.ID); err != nil {
		t.Fatalf("delete message: %v", err)
	}

	mTrash, _ := s.GetMessage(msg.ID)
	if mTrash.Folder != FolderTrash {
		t.Fatalf("expected folder trash, got %s", mTrash.Folder)
	}
}

func TestRawMimeParsing(t *testing.T) {
	raw := `From: "Jane Doe" <jane@sender.net>
To: contact@example.com
Subject: Test Email
Message-ID: <12345@sender.net>
Content-Type: text/plain; charset=utf-8

This is the plain body.
`
	parsed, err := parseRawMime(raw)
	if err != nil {
		t.Fatalf("parse raw mime: %v", err)
	}
	if parsed.From != "jane@sender.net" || parsed.FromName != "Jane Doe" || parsed.Subject != "Test Email" {
		t.Fatalf("unexpected parsed result: %+v", parsed)
	}
	if parsed.Text != "This is the plain body.\n" {
		t.Fatalf("unexpected body text: %q", parsed.Text)
	}
}

func TestWorkerScript(t *testing.T) {
	s := newTestService(t)
	script := s.WorkerScript("https://ops.example.com")
	if len(script) == 0 || s.WebhookSecret() == "" {
		t.Fatal("empty worker script or webhook secret")
	}
}

func TestOutboundValidation(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	// Missing domain
	_, err := s.Send(ctx, SendRequest{
		FromAddress: "contact@notfound.com",
		To:          []string{"user@test.com"},
		Subject:     "Hello",
	})
	if err == nil {
		t.Fatal("expected error for unconfigured domain")
	}

	// Add domain
	dom, err := s.AddDomain(Domain{
		Domain:           "mybrand.com",
		InboundMode:      DeliveryInbox,
		OutboundProvider: ProviderResend,
	})
	if err != nil {
		t.Fatalf("add domain: %v", err)
	}

	// Without API key or SMTP config
	_, err = s.Send(ctx, SendRequest{
		DomainID:    dom.ID,
		FromAddress: "contact@mybrand.com",
		To:          []string{"user@test.com"},
		Subject:     "Hello",
	})
	if err == nil {
		t.Fatal("expected error when no provider configured")
	}
}
