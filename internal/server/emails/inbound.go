package emails

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"fleetwatch/internal/server/valid"
)

var reCleanSubject = regexp.MustCompile(`(?i)^(re|fwd|fw):\s*`)

// ProcessInbound handles an incoming email from Cloudflare Email Worker or direct webhook.
func (s *Service) ProcessInbound(ctx context.Context, p InboundPayload) (Message, error) {
	// If raw MIME email is supplied, parse it
	if p.RawEmail != "" && p.Subject == "" && len(p.To) == 0 {
		parsed, err := parseRawMime(p.RawEmail)
		if err == nil {
			p.From = parsed.From
			p.FromName = parsed.FromName
			p.To = parsed.To
			p.Cc = parsed.Cc
			p.Subject = parsed.Subject
			p.Text = parsed.Text
			p.HTML = parsed.HTML
			p.MessageID = parsed.MessageID
			p.InReplyTo = parsed.InReplyTo
			p.References = parsed.References
			if len(parsed.Attachments) > 0 {
				p.Attachments = append(p.Attachments, parsed.Attachments...)
			}
		}
	}

	fromAddr := strings.TrimSpace(p.From)
	if fromAddr == "" {
		return Message{}, valid.Error("missing from address")
	}

	var targetTo string
	if len(p.To) > 0 {
		targetTo = p.To[0]
	}
	targetTo = extractEmailAddress(targetTo)

	alias, domain, err := s.FindAlias(targetTo)
	if err != nil {
		// Domain not configured in Fleetwatch
		return Message{}, fmt.Errorf("recipient %s does not match any configured domain: %w", targetTo, err)
	}

	subject := strings.TrimSpace(p.Subject)
	if subject == "" {
		subject = "(No Subject)"
	}

	// Resolve thread ID
	threadID := s.resolveThreadID(domain.ID, p.InReplyTo, p.References, subject)

	msgID := p.MessageID
	if msgID == "" {
		msgID = fmt.Sprintf("<%s@%s>", newID("msg"), domain.Domain)
	}

	snippet := createSnippet(p.Text, p.HTML)

	m := Message{
		ID:               newID("em"),
		ThreadID:         threadID,
		DomainID:         domain.ID,
		AliasID:          alias.ID,
		Direction:        DirectionInbound,
		Folder:           FolderInbox,
		FromAddress:      extractEmailAddress(fromAddr),
		FromName:         strings.TrimSpace(p.FromName),
		ToAddresses:      cleanAddresses(p.To),
		CcAddresses:      cleanAddresses(p.Cc),
		Subject:          subject,
		Snippet:          snippet,
		BodyText:         p.Text,
		BodyHTML:         p.HTML,
		MessageID:        msgID,
		InReplyTo:        p.InReplyTo,
		ReferencesHeader: p.References,
		HasAttachments:   len(p.Attachments) > 0,
		IsRead:           false,
		IsStarred:        false,
		ReceivedAt:       nowMs(),
	}

	toRaw, _ := json.Marshal(m.ToAddresses)
	ccRaw, _ := json.Marshal(m.CcAddresses)

	var aliasIDVal any
	if m.AliasID != "" {
		aliasIDVal = m.AliasID
	}

	hasAtt := 0
	if m.HasAttachments {
		hasAtt = 1
	}

	// Save message record
	_, err = s.db.Exec(`INSERT INTO email_messages (id, thread_id, domain_id, alias_id, direction, folder, from_address, from_name, to_addresses, cc_addresses, reply_to, subject, snippet, body_text, body_html, message_id, in_reply_to, references_header, has_attachments, is_read, is_starred, received_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ThreadID, m.DomainID, aliasIDVal, m.Direction, m.Folder, m.FromAddress, m.FromName, string(toRaw), string(ccRaw), p.From, m.Subject, m.Snippet, m.BodyText, m.BodyHTML, m.MessageID, m.InReplyTo, m.ReferencesHeader, hasAtt, 0, 0, m.ReceivedAt)
	if err != nil {
		return Message{}, err
	}

	// Process and store attachments
	for _, att := range p.Attachments {
		if att.Filename == "" {
			att.Filename = "attachment"
		}
		rawBytes, err := base64.StdEncoding.DecodeString(att.DataB64)
		if err != nil {
			continue
		}
		attID := newID("att")
		storagePath := filepath.Join(s.dataDir, "emails", "attachments", attID)
		if err := os.WriteFile(storagePath, rawBytes, 0o600); err == nil {
			s.db.Exec(`INSERT INTO email_attachments (id, message_id, filename, content_type, size_bytes, storage_path) VALUES (?, ?, ?, ?, ?, ?)`,
				attID, m.ID, att.Filename, att.ContentType, len(rawBytes), storagePath)
			m.Attachments = append(m.Attachments, Attachment{
				ID:          attID,
				MessageID:   m.ID,
				Filename:    att.Filename,
				ContentType: att.ContentType,
				SizeBytes:   int64(len(rawBytes)),
			})
		}
	}

	// Auto-forward to Gmail if alias is configured for forward or both
	destForward := alias.ForwardTo
	if destForward == "" {
		destForward = domain.ForwardEmail
	}
	if destForward != "" && (alias.DeliveryMode == DeliveryForward || alias.DeliveryMode == DeliveryBoth) {
		go func() {
			fwdReq := SendRequest{
				DomainID:    domain.ID,
				FromAddress: "forward@" + domain.Domain,
				FromName:    "Fleetwatch Forwarder",
				To:          []string{destForward},
				Subject:     "[FWD] " + m.Subject + " (from " + m.FromAddress + ")",
				BodyText:    fmt.Sprintf("--- Forwarded message from %s to %s ---\n\n%s", m.FromAddress, targetTo, m.BodyText),
				BodyHTML:    m.BodyHTML,
			}
			s.Send(context.Background(), fwdReq)
		}()
	}

	// If delivery mode is forward-only, we can archive or remove from primary inbox if requested
	if alias.DeliveryMode == DeliveryForward {
		// keep in database for search/record but marked read
		s.SetRead(m.ID, true)
	}

	s.publish("inbox")
	return m, nil
}

func (s *Service) resolveThreadID(domainID, inReplyTo, references, subject string) string {
	// 1. Try finding parent message by In-Reply-To
	if inReplyTo != "" {
		var threadID string
		if s.db.QueryRow(`SELECT thread_id FROM email_messages WHERE message_id = ?`, inReplyTo).Scan(&threadID) == nil {
			return threadID
		}
	}

	// 2. Try references
	if references != "" {
		parts := strings.Fields(references)
		for _, ref := range parts {
			var threadID string
			if s.db.QueryRow(`SELECT thread_id FROM email_messages WHERE message_id = ?`, ref).Scan(&threadID) == nil {
				return threadID
			}
		}
	}

	// 3. Fallback: normalize subject hash
	cleanSub := strings.TrimSpace(reCleanSubject.ReplaceAllString(subject, ""))
	cleanSub = strings.ToLower(cleanSub)
	h := sha256.Sum256([]byte(domainID + ":" + cleanSub))
	return "th_" + hex.EncodeToString(h[:8])
}

func extractEmailAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if a, err := mail.ParseAddress(raw); err == nil && a.Address != "" {
		return strings.ToLower(a.Address)
	}
	// Fallback: extract substring inside <...>
	if start := strings.Index(raw, "<"); start >= 0 {
		if end := strings.Index(raw[start:], ">"); end > 0 {
			return strings.ToLower(strings.TrimSpace(raw[start+1 : start+end]))
		}
	}
	return strings.ToLower(raw)
}

func cleanAddresses(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range list {
		clean := extractEmailAddress(a)
		if clean != "" && !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out
}

func createSnippet(text, html string) string {
	src := text
	if src == "" {
		// strip html tags
		re := regexp.MustCompile(`<[^>]*>`)
		src = re.ReplaceAllString(html, " ")
	}
	fields := strings.Fields(src)
	joined := strings.Join(fields, " ")
	if len(joined) > 160 {
		return joined[:157] + "…"
	}
	return joined
}

// parseRawMime decodes a standard RFC 822 MIME message
func parseRawMime(raw string) (InboundPayload, error) {
	r := strings.NewReader(raw)
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return InboundPayload{}, err
	}

	res := InboundPayload{
		From:       msg.Header.Get("From"),
		Subject:    msg.Header.Get("Subject"),
		MessageID:  msg.Header.Get("Message-ID"),
		InReplyTo:  msg.Header.Get("In-Reply-To"),
		References: msg.Header.Get("References"),
	}

	if fromAddr, err := mail.ParseAddress(res.From); err == nil {
		res.From = fromAddr.Address
		res.FromName = fromAddr.Name
	}

	if toList, err := msg.Header.AddressList("To"); err == nil {
		for _, a := range toList {
			res.To = append(res.To, a.Address)
		}
	}
	if ccList, err := msg.Header.AddressList("Cc"); err == nil {
		for _, a := range ccList {
			res.Cc = append(res.Cc, a.Address)
		}
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		// Plain single part
		body, _ := io.ReadAll(msg.Body)
		if strings.Contains(mediaType, "html") {
			res.HTML = string(body)
		} else {
			res.Text = string(body)
		}
		return res, nil
	}

	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		pType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		pData, _ := io.ReadAll(part)
		if part.FileName() != "" {
			res.Attachments = append(res.Attachments, InboundAttachment{
				Filename:    part.FileName(),
				ContentType: pType,
				DataB64:     base64.StdEncoding.EncodeToString(pData),
			})
		} else if strings.Contains(pType, "html") && res.HTML == "" {
			res.HTML = string(pData)
		} else if strings.Contains(pType, "text") && res.Text == "" {
			res.Text = string(pData)
		}
	}

	return res, nil
}
