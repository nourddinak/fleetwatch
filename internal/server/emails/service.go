package emails

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

var (
	ErrNotFound = errors.New("not found")
)

const webhookSecretKey = "emails.webhook_secret"

type Service struct {
	db      *sql.DB
	box     *secret.Box
	dns     *dns.Service
	bus     *live.Bus
	dataDir string
	mu      sync.RWMutex
}

func NewService(db *sql.DB, box *secret.Box, dnsSvc *dns.Service, bus *live.Bus, dataDir string) *Service {
	attachDir := filepath.Join(dataDir, "emails", "attachments")
	os.MkdirAll(attachDir, 0o700)
	s := &Service{
		db:      db,
		box:     box,
		dns:     dnsSvc,
		bus:     bus,
		dataDir: dataDir,
	}
	s.ensureWebhookSecret()
	return s
}

func (s *Service) ensureWebhookSecret() {
	var sec string
	if settings.Load(s.db, webhookSecretKey, &sec) != nil || sec == "" {
		b := make([]byte, 16)
		rand.Read(b)
		sec = hex.EncodeToString(b)
		settings.Save(s.db, webhookSecretKey, sec)
	}
}

func (s *Service) WebhookSecret() string {
	var sec string
	if settings.Load(s.db, webhookSecretKey, &sec) == nil && sec != "" {
		return sec
	}
	return ""
}

func (s *Service) SetWebhookSecret(sec string) error {
	sec = strings.TrimSpace(sec)
	if sec == "" {
		return valid.Error("secret cannot be empty")
	}
	return settings.Save(s.db, webhookSecretKey, sec)
}

// ----------------- Domains -----------------

func sealString(box *secret.Box, val, ad string) string {
	if box == nil || val == "" {
		return val
	}
	b := box.Seal([]byte(val), ad)
	return base64.StdEncoding.EncodeToString(b)
}

func openString(box *secret.Box, val, ad string) string {
	if box == nil || val == "" {
		return val
	}
	raw, err := base64.StdEncoding.DecodeString(val)
	if err != nil {
		return val
	}
	plain, err := box.Open(raw, ad)
	if err != nil {
		return ""
	}
	return string(plain)
}

func (s *Service) ListDomains() ([]Domain, error) {
	rows, err := s.db.Query(`SELECT id, domain, cloudflare_zone_id, inbound_mode, forward_email, outbound_provider, resend_domain_id, resend_api_key, smtp_config, dkim_status, spf_status, mx_status, created_at, updated_at FROM email_domains ORDER BY domain ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Domain
	for rows.Next() {
		var d Domain
		var cfZone, fwd, resendID, resendKey, smtpRaw sql.NullString
		if err := rows.Scan(&d.ID, &d.Domain, &cfZone, &d.InboundMode, &fwd, &d.OutboundProvider, &resendID, &resendKey, &smtpRaw, &d.DKIMStatus, &d.SPFStatus, &d.MXStatus, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.CloudflareZoneID = cfZone.String
		d.ForwardEmail = fwd.String
		d.ResendDomainID = resendID.String
		if resendKey.Valid && resendKey.String != "" {
			d.HasResendKey = true
		}
		if smtpRaw.Valid && smtpRaw.String != "" {
			var smtp SMTPConfig
			if json.Unmarshal([]byte(smtpRaw.String), &smtp) == nil {
				if smtp.Password != "" {
					smtp.HasPassword = true
					smtp.Password = ""
				}
				d.SMTPConfig = &smtp
			}
		}
		out = append(out, d)
	}
	if out == nil {
		out = []Domain{}
	}
	return out, nil
}

func (s *Service) GetDomain(id string) (Domain, error) {
	row := s.db.QueryRow(`SELECT id, domain, cloudflare_zone_id, inbound_mode, forward_email, outbound_provider, resend_domain_id, resend_api_key, smtp_config, dkim_status, spf_status, mx_status, created_at, updated_at FROM email_domains WHERE id = ?`, id)
	var d Domain
	var cfZone, fwd, resendID, resendKey, smtpRaw sql.NullString
	if err := row.Scan(&d.ID, &d.Domain, &cfZone, &d.InboundMode, &fwd, &d.OutboundProvider, &resendID, &resendKey, &smtpRaw, &d.DKIMStatus, &d.SPFStatus, &d.MXStatus, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Domain{}, ErrNotFound
		}
		return Domain{}, err
	}
	d.CloudflareZoneID = cfZone.String
	d.ForwardEmail = fwd.String
	d.ResendDomainID = resendID.String
	if resendKey.Valid && resendKey.String != "" {
		d.HasResendKey = true
		d.ResendAPIKey = openString(s.box, resendKey.String, "email.resend_key")
	}
	if smtpRaw.Valid && smtpRaw.String != "" {
		var smtp SMTPConfig
		if json.Unmarshal([]byte(smtpRaw.String), &smtp) == nil {
			if smtp.Password != "" {
				smtp.HasPassword = true
				smtp.Password = openString(s.box, smtp.Password, "email.smtp_password")
			}
			d.SMTPConfig = &smtp
		}
	}
	return d, nil
}

func (s *Service) AddDomain(in Domain) (Domain, error) {
	domainName := strings.ToLower(strings.TrimSpace(in.Domain))
	if domainName == "" {
		return Domain{}, valid.Error("domain name required")
	}
	if in.InboundMode == "" {
		in.InboundMode = DeliveryBoth
	}
	if in.OutboundProvider == "" {
		in.OutboundProvider = ProviderResend
	}

	d := Domain{
		ID:               newID("edm"),
		Domain:           domainName,
		CloudflareZoneID: in.CloudflareZoneID,
		InboundMode:      in.InboundMode,
		ForwardEmail:     strings.TrimSpace(in.ForwardEmail),
		OutboundProvider: in.OutboundProvider,
		ResendDomainID:   in.ResendDomainID,
		DKIMStatus:       "pending",
		SPFStatus:        "pending",
		MXStatus:         "pending",
		CreatedAt:        nowMs(),
		UpdatedAt:        nowMs(),
	}

	var resendKeyEnc sql.NullString
	if strings.TrimSpace(in.ResendAPIKey) != "" {
		d.HasResendKey = true
		val := sealString(s.box, strings.TrimSpace(in.ResendAPIKey), "email.resend_key")
		resendKeyEnc = sql.NullString{String: val, Valid: true}
	}

	var smtpEnc sql.NullString
	if in.SMTPConfig != nil {
		cfg := *in.SMTPConfig
		if cfg.Password != "" {
			cfg.Password = sealString(s.box, cfg.Password, "email.smtp_password")
		}
		if raw, err := json.Marshal(cfg); err == nil {
			smtpEnc = sql.NullString{String: string(raw), Valid: true}
		}
	}

	_, err := s.db.Exec(`INSERT INTO email_domains (id, domain, cloudflare_zone_id, inbound_mode, forward_email, outbound_provider, resend_domain_id, resend_api_key, smtp_config, dkim_status, spf_status, mx_status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Domain, d.CloudflareZoneID, d.InboundMode, d.ForwardEmail, d.OutboundProvider, d.ResendDomainID, resendKeyEnc, smtpEnc, d.DKIMStatus, d.SPFStatus, d.MXStatus, d.CreatedAt, d.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Domain{}, valid.Error("domain already configured")
		}
		return Domain{}, err
	}

	// Auto-create default contact@ and noreply@ aliases
	s.AddAlias(Alias{
		DomainID:     d.ID,
		LocalPart:    "contact",
		DisplayName:  "Contact",
		DeliveryMode: DeliveryBoth,
		ForwardTo:    d.ForwardEmail,
		IsActive:     true,
	})
	s.AddAlias(Alias{
		DomainID:     d.ID,
		LocalPart:    "noreply",
		DisplayName:  "No Reply",
		DeliveryMode: DeliveryInbox,
		IsActive:     true,
	})

	s.publish("domains")
	return d, nil
}

func (s *Service) UpdateDomain(in Domain) error {
	existing, err := s.GetDomain(in.ID)
	if err != nil {
		return err
	}

	resendKey := existing.ResendAPIKey
	if in.ResendAPIKey != "" {
		resendKey = in.ResendAPIKey
	}
	var resendKeyEnc sql.NullString
	if resendKey != "" {
		val := sealString(s.box, resendKey, "email.resend_key")
		resendKeyEnc = sql.NullString{String: val, Valid: true}
	}

	var smtpEnc sql.NullString
	if in.SMTPConfig != nil {
		cfg := *in.SMTPConfig
		if cfg.Password == "" && existing.SMTPConfig != nil {
			cfg.Password = existing.SMTPConfig.Password
		}
		if cfg.Password != "" {
			cfg.Password = sealString(s.box, cfg.Password, "email.smtp_password")
		}
		if raw, err := json.Marshal(cfg); err == nil {
			smtpEnc = sql.NullString{String: string(raw), Valid: true}
		}
	}

	_, err = s.db.Exec(`UPDATE email_domains SET inbound_mode = ?, forward_email = ?, outbound_provider = ?, resend_domain_id = ?, resend_api_key = ?, smtp_config = ?, dkim_status = ?, spf_status = ?, mx_status = ?, updated_at = ? WHERE id = ?`,
		in.InboundMode, in.ForwardEmail, in.OutboundProvider, in.ResendDomainID, resendKeyEnc, smtpEnc, in.DKIMStatus, in.SPFStatus, in.MXStatus, nowMs(), in.ID)
	if err == nil {
		s.publish("domains")
	}
	return err
}

func (s *Service) DeleteDomain(id string) error {
	_, err := s.db.Exec(`DELETE FROM email_domains WHERE id = ?`, id)
	if err == nil {
		s.publish("domains")
	}
	return err
}

// ----------------- Aliases -----------------

func (s *Service) ListAliases(domainID string) ([]Alias, error) {
	q := `SELECT a.id, a.domain_id, d.domain, a.local_part, a.full_address, a.display_name, a.delivery_mode, a.forward_to, a.is_active, a.created_at FROM email_aliases a JOIN email_domains d ON a.domain_id = d.id`
	args := []any{}
	if domainID != "" {
		q += ` WHERE a.domain_id = ?`
		args = append(args, domainID)
	}
	q += ` ORDER BY a.full_address ASC`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Alias
	for rows.Next() {
		var a Alias
		var disp, fwd sql.NullString
		var active int
		if err := rows.Scan(&a.ID, &a.DomainID, &a.DomainName, &a.LocalPart, &a.FullAddress, &disp, &a.DeliveryMode, &fwd, &active, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.DisplayName = disp.String
		a.ForwardTo = fwd.String
		a.IsActive = active == 1
		out = append(out, a)
	}
	if out == nil {
		out = []Alias{}
	}
	return out, nil
}

func (s *Service) GetAlias(id string) (Alias, error) {
	row := s.db.QueryRow(`SELECT a.id, a.domain_id, d.domain, a.local_part, a.full_address, a.display_name, a.delivery_mode, a.forward_to, a.is_active, a.created_at FROM email_aliases a JOIN email_domains d ON a.domain_id = d.id WHERE a.id = ?`, id)
	var a Alias
	var disp, fwd sql.NullString
	var active int
	if err := row.Scan(&a.ID, &a.DomainID, &a.DomainName, &a.LocalPart, &a.FullAddress, &disp, &a.DeliveryMode, &fwd, &active, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Alias{}, ErrNotFound
		}
		return Alias{}, err
	}
	a.DisplayName = disp.String
	a.ForwardTo = fwd.String
	a.IsActive = active == 1
	return a, nil
}

func (s *Service) FindAlias(address string) (Alias, Domain, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	parts := strings.Split(address, "@")
	if len(parts) != 2 {
		return Alias{}, Domain{}, valid.Error("invalid email address")
	}
	local, domain := parts[0], parts[1]

	// 1. Direct match on alias full_address
	var aliasID, domID string
	err := s.db.QueryRow(`SELECT id, domain_id FROM email_aliases WHERE full_address = ? AND is_active = 1`, address).Scan(&aliasID, &domID)
	if err == nil {
		a, _ := s.GetAlias(aliasID)
		d, _ := s.GetDomain(domID)
		return a, d, nil
	}

	// 2. Catch-all match (*@domain)
	err = s.db.QueryRow(`SELECT a.id, a.domain_id FROM email_aliases a JOIN email_domains d ON a.domain_id = d.id WHERE d.domain = ? AND a.local_part = '*' AND a.is_active = 1`, domain).Scan(&aliasID, &domID)
	if err == nil {
		a, _ := s.GetAlias(aliasID)
		d, _ := s.GetDomain(domID)
		return a, d, nil
	}

	// 3. Domain match with fallback default routing
	var d Domain
	row := s.db.QueryRow(`SELECT id, domain, cloudflare_zone_id, inbound_mode, forward_email, outbound_provider, resend_domain_id, dkim_status, spf_status, mx_status, created_at, updated_at FROM email_domains WHERE domain = ?`, domain)
	var cfZone, fwd, resendID sql.NullString
	if err := row.Scan(&d.ID, &d.Domain, &cfZone, &d.InboundMode, &fwd, &d.OutboundProvider, &resendID, &d.DKIMStatus, &d.SPFStatus, &d.MXStatus, &d.CreatedAt, &d.UpdatedAt); err == nil {
		d.CloudflareZoneID = cfZone.String
		d.ForwardEmail = fwd.String
		d.ResendDomainID = resendID.String
		syntheticAlias := Alias{
			ID:           "",
			DomainID:     d.ID,
			DomainName:   d.Domain,
			LocalPart:    local,
			FullAddress:  address,
			DeliveryMode: d.InboundMode,
			ForwardTo:    d.ForwardEmail,
			IsActive:     true,
			CreatedAt:    nowMs(),
		}
		return syntheticAlias, d, nil
	}

	return Alias{}, Domain{}, ErrNotFound
}

func (s *Service) AddAlias(in Alias) (Alias, error) {
	local := strings.ToLower(strings.TrimSpace(in.LocalPart))
	if local == "" {
		return Alias{}, valid.Error("alias local-part required (e.g. 'contact')")
	}
	dom, err := s.GetDomain(in.DomainID)
	if err != nil {
		return Alias{}, valid.Error("domain not found")
	}

	full := local + "@" + dom.Domain
	if local == "*" {
		full = "*@" + dom.Domain
	}

	mode := in.DeliveryMode
	if mode == "" {
		mode = DeliveryInbox
	}

	fwd := strings.TrimSpace(in.ForwardTo)
	if (mode == DeliveryForward || mode == DeliveryBoth) && fwd == "" {
		fwd = dom.ForwardEmail
	}

	a := Alias{
		ID:           newID("al"),
		DomainID:     dom.ID,
		DomainName:   dom.Domain,
		LocalPart:    local,
		FullAddress:  full,
		DisplayName:  strings.TrimSpace(in.DisplayName),
		DeliveryMode: mode,
		ForwardTo:    fwd,
		IsActive:     true,
		CreatedAt:    nowMs(),
	}

	_, err = s.db.Exec(`INSERT INTO email_aliases (id, domain_id, local_part, full_address, display_name, delivery_mode, forward_to, is_active, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.DomainID, a.LocalPart, a.FullAddress, a.DisplayName, a.DeliveryMode, a.ForwardTo, 1, a.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Alias{}, valid.Error("alias already exists")
		}
		return Alias{}, err
	}

	s.publish("aliases")
	return a, nil
}

func (s *Service) UpdateAlias(in Alias) error {
	existing, err := s.GetAlias(in.ID)
	if err != nil {
		return err
	}
	mode := in.DeliveryMode
	if mode == "" {
		mode = existing.DeliveryMode
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err = s.db.Exec(`UPDATE email_aliases SET display_name = ?, delivery_mode = ?, forward_to = ?, is_active = ? WHERE id = ?`,
		strings.TrimSpace(in.DisplayName), mode, strings.TrimSpace(in.ForwardTo), active, in.ID)
	if err == nil {
		s.publish("aliases")
	}
	return err
}

func (s *Service) DeleteAlias(id string) error {
	_, err := s.db.Exec(`DELETE FROM email_aliases WHERE id = ?`, id)
	if err == nil {
		s.publish("aliases")
	}
	return err
}

// ----------------- Messages -----------------

type MessageFilter struct {
	Folder   string // inbox | sent | trash
	DomainID string
	AliasID  string
	ThreadID string
	Query    string
	Limit    int
	Offset   int
}

func (s *Service) ListMessages(f MessageFilter) ([]Message, int, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	if f.Folder == "" {
		f.Folder = FolderInbox
	}

	where := []string{"folder = ?"}
	args := []any{f.Folder}

	if f.DomainID != "" {
		where = append(where, "domain_id = ?")
		args = append(args, f.DomainID)
	}
	if f.AliasID != "" {
		where = append(where, "alias_id = ?")
		args = append(args, f.AliasID)
	}
	if f.ThreadID != "" {
		where = append(where, "thread_id = ?")
		args = append(args, f.ThreadID)
	}
	if f.Query != "" {
		where = append(where, "(subject LIKE ? OR from_address LIKE ? OR snippet LIKE ?)")
		pat := "%" + f.Query + "%"
		args = append(args, pat, pat, pat)
	}

	clause := strings.Join(where, " AND ")

	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM email_messages WHERE `+clause, args...).Scan(&total)

	q := fmt.Sprintf(`SELECT id, thread_id, domain_id, alias_id, direction, folder, from_address, from_name, to_addresses, cc_addresses, bcc_addresses, reply_to, subject, snippet, message_id, has_attachments, is_read, is_starred, received_at FROM email_messages WHERE %s ORDER BY received_at DESC LIMIT %d OFFSET %d`, clause, f.Limit, f.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		var aliasID, fromName, toRaw, ccRaw, bccRaw, replyTo, snippet, msgID sql.NullString
		var hasAtt, read, star int
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.DomainID, &aliasID, &m.Direction, &m.Folder, &m.FromAddress, &fromName, &toRaw, &ccRaw, &bccRaw, &replyTo, &m.Subject, &snippet, &msgID, &hasAtt, &read, &star, &m.ReceivedAt); err != nil {
			return nil, 0, err
		}
		m.AliasID = aliasID.String
		m.FromName = fromName.String
		m.Snippet = snippet.String
		m.MessageID = msgID.String
		m.HasAttachments = hasAtt == 1
		m.IsRead = read == 1
		m.IsStarred = star == 1
		if toRaw.Valid {
			json.Unmarshal([]byte(toRaw.String), &m.ToAddresses)
		}
		if ccRaw.Valid {
			json.Unmarshal([]byte(ccRaw.String), &m.CcAddresses)
		}
		out = append(out, m)
	}
	if out == nil {
		out = []Message{}
	}
	return out, total, nil
}

func (s *Service) GetMessage(id string) (Message, error) {
	row := s.db.QueryRow(`SELECT id, thread_id, domain_id, alias_id, direction, folder, from_address, from_name, to_addresses, cc_addresses, bcc_addresses, reply_to, subject, snippet, body_text, body_html, message_id, in_reply_to, references_header, has_attachments, is_read, is_starred, received_at FROM email_messages WHERE id = ?`, id)

	var m Message
	var aliasID, fromName, toRaw, ccRaw, bccRaw, replyTo, snippet, bodyText, bodyHTML, msgID, inReplyTo, refs sql.NullString
	var hasAtt, read, star int
	if err := row.Scan(&m.ID, &m.ThreadID, &m.DomainID, &aliasID, &m.Direction, &m.Folder, &m.FromAddress, &fromName, &toRaw, &ccRaw, &bccRaw, &replyTo, &m.Subject, &snippet, &bodyText, &bodyHTML, &msgID, &inReplyTo, &refs, &hasAtt, &read, &star, &m.ReceivedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Message{}, ErrNotFound
		}
		return Message{}, err
	}
	m.AliasID = aliasID.String
	m.FromName = fromName.String
	m.Snippet = snippet.String
	m.BodyText = bodyText.String
	m.BodyHTML = bodyHTML.String
	m.MessageID = msgID.String
	m.InReplyTo = inReplyTo.String
	m.ReferencesHeader = refs.String
	m.HasAttachments = hasAtt == 1
	m.IsRead = read == 1
	m.IsStarred = star == 1
	if toRaw.Valid {
		json.Unmarshal([]byte(toRaw.String), &m.ToAddresses)
	}
	if ccRaw.Valid {
		json.Unmarshal([]byte(ccRaw.String), &m.CcAddresses)
	}
	if bccRaw.Valid {
		json.Unmarshal([]byte(bccRaw.String), &m.BccAddresses)
	}

	// Fetch attachments
	attRows, err := s.db.Query(`SELECT id, message_id, filename, content_type, size_bytes, storage_path FROM email_attachments WHERE message_id = ?`, m.ID)
	if err == nil {
		defer attRows.Close()
		for attRows.Next() {
			var a Attachment
			if err := attRows.Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.SizeBytes, &a.StoragePath); err == nil {
				m.Attachments = append(m.Attachments, a)
			}
		}
	}
	return m, nil
}

func (s *Service) SetRead(id string, read bool) error {
	v := 0
	if read {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE email_messages SET is_read = ? WHERE id = ?`, v, id)
	return err
}

func (s *Service) SetStarred(id string, star bool) error {
	v := 0
	if star {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE email_messages SET is_starred = ? WHERE id = ?`, v, id)
	return err
}

func (s *Service) SetFolder(id, folder string) error {
	_, err := s.db.Exec(`UPDATE email_messages SET folder = ? WHERE id = ?`, folder, id)
	if err == nil {
		s.publish("messages")
	}
	return err
}

func (s *Service) DeleteMessage(id string) error {
	// If in trash, delete permanently; otherwise move to trash
	var curFolder string
	if err := s.db.QueryRow(`SELECT folder FROM email_messages WHERE id = ?`, id).Scan(&curFolder); err != nil {
		return err
	}
	var execErr error
	if curFolder == FolderTrash {
		// Clean attachments
		rows, err := s.db.Query(`SELECT storage_path FROM email_attachments WHERE message_id = ?`, id)
		if err == nil {
			for rows.Next() {
				var p string
				if rows.Scan(&p) == nil && p != "" {
					os.Remove(p)
				}
			}
			rows.Close()
		}
		s.db.Exec(`DELETE FROM email_attachments WHERE message_id = ?`, id)
		_, execErr = s.db.Exec(`DELETE FROM email_messages WHERE id = ?`, id)
	} else {
		_, execErr = s.db.Exec(`UPDATE email_messages SET folder = ? WHERE id = ?`, FolderTrash, id)
	}
	if execErr == nil {
		s.publish("messages")
	}
	return execErr
}

func (s *Service) GetAttachment(id string) (Attachment, []byte, error) {
	var a Attachment
	err := s.db.QueryRow(`SELECT id, message_id, filename, content_type, size_bytes, storage_path FROM email_attachments WHERE id = ?`, id).Scan(&a.ID, &a.MessageID, &a.Filename, &a.ContentType, &a.SizeBytes, &a.StoragePath)
	if err != nil {
		return Attachment{}, nil, ErrNotFound
	}
	data, err := os.ReadFile(a.StoragePath)
	if err != nil {
		return Attachment{}, nil, err
	}
	return a, data, nil
}

func (s *Service) publish(topic string) {
	if s.bus != nil {
		s.bus.Publish("emails."+topic, map[string]string{"topic": topic, "ts": fmt.Sprint(nowMs())})
	}
}
