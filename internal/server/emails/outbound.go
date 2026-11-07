package emails

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/server/valid"
)

var ResendAPI = "https://api.resend.com"

// Send sends an email via Resend or SMTP and saves it in the sent folder.
func (s *Service) Send(ctx context.Context, req SendRequest) (Message, error) {
	fromClean := extractEmailAddress(req.FromAddress)
	if fromClean == "" {
		return Message{}, valid.Error("valid from address required")
	}

	parts := strings.Split(fromClean, "@")
	if len(parts) != 2 {
		return Message{}, valid.Error("invalid from address")
	}
	domainName := parts[1]

	var domain Domain
	var err error
	if req.DomainID != "" {
		domain, err = s.GetDomain(req.DomainID)
	}
	if err != nil || domain.ID == "" {
		// Lookup by domain name
		rows, lErr := s.ListDomains()
		if lErr == nil {
			for _, d := range rows {
				if strings.EqualFold(d.Domain, domainName) {
					domain, _ = s.GetDomain(d.ID)
					break
				}
			}
		}
	}
	if domain.ID == "" {
		return Message{}, valid.Error("from domain " + domainName + " is not configured in Fleetwatch")
	}

	recipients := cleanAddresses(req.To)
	if len(recipients) == 0 {
		return Message{}, valid.Error("at least one recipient required")
	}

	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		subject = "(No Subject)"
	}

	// Resolve thread ID
	threadID := s.resolveThreadID(domain.ID, req.InReplyTo, req.References, subject)
	msgID := fmt.Sprintf("<%s@%s>", newID("msg"), domain.Domain)
	snippet := createSnippet(req.BodyText, req.BodyHTML)

	// Check delivery mechanism: Resend or SMTP
	if domain.OutboundProvider == ProviderSMTP && domain.SMTPConfig != nil && domain.SMTPConfig.Host != "" {
		if err := s.sendViaSMTP(ctx, *domain.SMTPConfig, req, msgID); err != nil {
			return Message{}, fmt.Errorf("SMTP delivery failed: %w", err)
		}
	} else if domain.ResendAPIKey != "" {
		if err := s.sendViaResend(ctx, domain.ResendAPIKey, req); err != nil {
			return Message{}, fmt.Errorf("Resend delivery failed: %w", err)
		}
	} else {
		// Check if we can fallback to SMTP or return error
		if domain.SMTPConfig != nil && domain.SMTPConfig.Host != "" {
			if err := s.sendViaSMTP(ctx, *domain.SMTPConfig, req, msgID); err != nil {
				return Message{}, fmt.Errorf("SMTP delivery failed: %w", err)
			}
		} else {
			return Message{}, valid.Error("no outbound provider configured: please set Resend API key or custom SMTP credentials for " + domain.Domain)
		}
	}

	// Save record into Sent folder
	m := Message{
		ID:               newID("em"),
		ThreadID:         threadID,
		DomainID:         domain.ID,
		AliasID:          req.AliasID,
		Direction:        DirectionOutbound,
		Folder:           FolderSent,
		FromAddress:      fromClean,
		FromName:         strings.TrimSpace(req.FromName),
		ToAddresses:      recipients,
		CcAddresses:      cleanAddresses(req.Cc),
		BccAddresses:     cleanAddresses(req.Bcc),
		Subject:          subject,
		Snippet:          snippet,
		BodyText:         req.BodyText,
		BodyHTML:         req.BodyHTML,
		MessageID:        msgID,
		InReplyTo:        req.InReplyTo,
		ReferencesHeader: req.References,
		HasAttachments:   len(req.Files) > 0,
		IsRead:           true,
		IsStarred:        false,
		ReceivedAt:       nowMs(),
	}

	toRaw, _ := json.Marshal(m.ToAddresses)
	ccRaw, _ := json.Marshal(m.CcAddresses)
	bccRaw, _ := json.Marshal(m.BccAddresses)

	var aliasIDVal any
	if m.AliasID != "" {
		aliasIDVal = m.AliasID
	}

	hasAtt := 0
	if m.HasAttachments {
		hasAtt = 1
	}

	_, err = s.db.Exec(`INSERT INTO email_messages (id, thread_id, domain_id, alias_id, direction, folder, from_address, from_name, to_addresses, cc_addresses, bcc_addresses, subject, snippet, body_text, body_html, message_id, in_reply_to, references_header, has_attachments, is_read, is_starred, received_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ThreadID, m.DomainID, aliasIDVal, m.Direction, m.Folder, m.FromAddress, m.FromName, string(toRaw), string(ccRaw), string(bccRaw), m.Subject, m.Snippet, m.BodyText, m.BodyHTML, m.MessageID, m.InReplyTo, m.ReferencesHeader, hasAtt, 1, 0, m.ReceivedAt)
	if err != nil {
		return Message{}, err
	}

	// Save attachments
	for _, f := range req.Files {
		data, err := base64.StdEncoding.DecodeString(f.DataB64)
		if err != nil {
			continue
		}
		attID := newID("att")
		path := filepath.Join(s.dataDir, "emails", "attachments", attID)
		if os.WriteFile(path, data, 0o600) == nil {
			s.db.Exec(`INSERT INTO email_attachments (id, message_id, filename, content_type, size_bytes, storage_path) VALUES (?, ?, ?, ?, ?, ?)`,
				attID, m.ID, f.Filename, f.ContentType, len(data), path)
			m.Attachments = append(m.Attachments, Attachment{
				ID:          attID,
				MessageID:   m.ID,
				Filename:    f.Filename,
				ContentType: f.ContentType,
				SizeBytes:   int64(len(data)),
			})
		}
	}

	s.publish("sent")
	return m, nil
}

// sendViaResend delivers via Resend HTTP REST API
func (s *Service) sendViaResend(ctx context.Context, apiKey string, req SendRequest) error {
	from := req.FromAddress
	if req.FromName != "" {
		from = fmt.Sprintf("%s <%s>", req.FromName, req.FromAddress)
	}

	payload := map[string]any{
		"from":    from,
		"to":      req.To,
		"subject": req.Subject,
	}
	if len(req.Cc) > 0 {
		payload["cc"] = req.Cc
	}
	if len(req.Bcc) > 0 {
		payload["bcc"] = req.Bcc
	}
	if req.BodyHTML != "" {
		payload["html"] = req.BodyHTML
	}
	if req.BodyText != "" {
		payload["text"] = req.BodyText
	}

	headers := map[string]string{}
	if req.InReplyTo != "" {
		headers["In-Reply-To"] = req.InReplyTo
	}
	if req.References != "" {
		headers["References"] = req.References
	}
	if len(headers) > 0 {
		payload["headers"] = headers
	}

	if len(req.Files) > 0 {
		var atts []map[string]any
		for _, f := range req.Files {
			atts = append(atts, map[string]any{
				"filename": f.Filename,
				"content":  f.DataB64,
			})
		}
		payload["attachments"] = atts
	}

	bodyBytes, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ResendAPI+"/emails", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return errors.New("Resend error: " + e.Message)
	}
	return nil
}

// sendViaSMTP delivers via standard SMTP relay
func (s *Service) sendViaSMTP(ctx context.Context, cfg SMTPConfig, req SendRequest, msgID string) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.Security == "tls" {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = tc
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()

	if cfg.Security == "starttls" {
		if err := client.StartTLS(tlsCfg); err != nil {
			return err
		}
	}

	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}

	if err := client.Mail(req.FromAddress); err != nil {
		return err
	}

	var allRecipients []string
	allRecipients = append(allRecipients, req.To...)
	allRecipients = append(allRecipients, req.Cc...)
	allRecipients = append(allRecipients, req.Bcc...)

	for _, to := range cleanAddresses(allRecipients) {
		if err := client.Rcpt(to); err != nil {
			return err
		}
	}

	w, err := client.Data()
	if err != nil {
		return err
	}
	defer w.Close()

	// Build raw MIME message
	mimeMsg := buildOutboundMime(req, msgID)
	_, err = w.Write(mimeMsg)
	return err
}

func buildOutboundMime(req SendRequest, msgID string) []byte {
	var b bytes.Buffer
	fromHdr := req.FromAddress
	if req.FromName != "" {
		fromHdr = fmt.Sprintf("%s <%s>", (&mail.Address{Name: req.FromName, Address: req.FromAddress}).String(), req.FromAddress)
	}

	fmt.Fprintf(&b, "From: %s\r\n", fromHdr)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(req.To, ", "))
	if len(req.Cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(req.Cc, ", "))
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", req.Subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: %s\r\n", msgID)
	if req.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", req.InReplyTo)
	}
	if req.References != "" {
		fmt.Fprintf(&b, "References: %s\r\n", req.References)
	}
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")

	if len(req.Files) == 0 {
		// Single or alternate text/html
		if req.BodyHTML != "" {
			fmt.Fprintf(&b, "Content-Type: text/html; charset=UTF-8\r\n\r\n")
			b.WriteString(req.BodyHTML)
		} else {
			fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n\r\n")
			b.WriteString(req.BodyText)
		}
		return b.Bytes()
	}

	// Multipart mixed with attachments
	mw := multipart.NewWriter(&b)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", mw.Boundary())

	// Body part
	bodyPart, _ := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": []string{"text/html; charset=UTF-8"},
	})
	if req.BodyHTML != "" {
		bodyPart.Write([]byte(req.BodyHTML))
	} else {
		bodyPart.Write([]byte(req.BodyText))
	}

	// Attachment parts
	for _, f := range req.Files {
		data, err := base64.StdEncoding.DecodeString(f.DataB64)
		if err != nil {
			continue
		}
		cType := f.ContentType
		if cType == "" {
			cType = "application/octet-stream"
		}
		part, _ := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              []string{cType},
			"Content-Disposition":       []string{fmt.Sprintf(`attachment; filename="%s"`, f.Filename)},
			"Content-Transfer-Encoding": []string{"base64"},
		})
		enc := base64.NewEncoder(base64.StdEncoding, part)
		enc.Write(data)
		enc.Close()
	}
	mw.Close()

	return b.Bytes()
}
