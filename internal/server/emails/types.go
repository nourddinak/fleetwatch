package emails

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Inbound delivery modes.
const (
	DeliveryInbox   = "inbox"
	DeliveryForward = "forward"
	DeliveryBoth    = "both"
)

// Folders.
const (
	FolderInbox = "inbox"
	FolderSent  = "sent"
	FolderTrash = "trash"
)

// Directions.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// Providers.
const (
	ProviderResend = "resend"
	ProviderSMTP   = "smtp"
)

// Domain is a configured domain for receiving and sending emails.
type Domain struct {
	ID               string      `json:"id"`
	Domain           string      `json:"domain"`
	CloudflareZoneID string      `json:"cloudflareZoneId,omitempty"`
	InboundMode      string      `json:"inboundMode"` // worker | forward | both
	ForwardEmail     string      `json:"forwardEmail,omitempty"`
	OutboundProvider string      `json:"outboundProvider"` // resend | smtp
	ResendDomainID   string      `json:"resendDomainId,omitempty"`
	ResendAPIKey     string      `json:"resendApiKey,omitempty"` // write-only
	HasResendKey     bool        `json:"hasResendKey"`
	SMTPConfig       *SMTPConfig `json:"smtpConfig,omitempty"`
	DKIMStatus       string      `json:"dkimStatus"`
	SPFStatus        string      `json:"spfStatus"`
	MXStatus         string      `json:"mxStatus"`
	CreatedAt        int64       `json:"createdAt"`
	UpdatedAt        int64       `json:"updatedAt"`
}

// SMTPConfig holds custom SMTP credentials.
type SMTPConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"` // starttls | tls | none
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	HasPassword bool   `json:"hasPassword"`
	From        string `json:"from"`
	FromName    string `json:"fromName,omitempty"`
}

// Alias is a configured email address (e.g. contact@example.com) or catch-all (*@example.com).
type Alias struct {
	ID           string `json:"id"`
	DomainID     string `json:"domainId"`
	DomainName   string `json:"domainName,omitempty"`
	LocalPart    string `json:"localPart"`
	FullAddress  string `json:"fullAddress"`
	DisplayName  string `json:"displayName,omitempty"`
	DeliveryMode string `json:"deliveryMode"` // inbox | forward | both
	ForwardTo    string `json:"forwardTo,omitempty"`
	IsActive     bool   `json:"isActive"`
	CreatedAt    int64  `json:"createdAt"`
}

// Message is an email stored in Fleetwatch.
type Message struct {
	ID               string       `json:"id"`
	ThreadID         string       `json:"threadId"`
	DomainID         string       `json:"domainId"`
	AliasID          string       `json:"aliasId,omitempty"`
	Direction        string       `json:"direction"` // inbound | outbound
	Folder           string       `json:"folder"`    // inbox | sent | trash
	FromAddress      string       `json:"fromAddress"`
	FromName         string       `json:"fromName,omitempty"`
	ToAddresses      []string     `json:"toAddresses"`
	CcAddresses      []string     `json:"ccAddresses,omitempty"`
	BccAddresses     []string     `json:"bccAddresses,omitempty"`
	ReplyTo          string       `json:"replyTo,omitempty"`
	Subject          string       `json:"subject"`
	Snippet          string       `json:"snippet,omitempty"`
	BodyText         string       `json:"bodyText,omitempty"`
	BodyHTML         string       `json:"bodyHtml,omitempty"`
	MessageID        string       `json:"messageId,omitempty"`
	InReplyTo        string       `json:"inReplyTo,omitempty"`
	ReferencesHeader string       `json:"references,omitempty"`
	HasAttachments   bool         `json:"hasAttachments"`
	IsRead           bool         `json:"isRead"`
	IsStarred        bool         `json:"isStarred"`
	ReceivedAt       int64        `json:"receivedAt"`
	Attachments      []Attachment `json:"attachments,omitempty"`
}

// Attachment is metadata about a file attached to a message.
type Attachment struct {
	ID          string `json:"id"`
	MessageID   string `json:"messageId"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	StoragePath string `json:"-"`
}

// SendRequest is what the UI submits to send a message or reply.
type SendRequest struct {
	DomainID    string       `json:"domainId"`
	AliasID     string       `json:"aliasId,omitempty"`
	FromAddress string       `json:"fromAddress"`
	FromName    string       `json:"fromName,omitempty"`
	To          []string     `json:"to"`
	Cc          []string     `json:"cc,omitempty"`
	Bcc         []string     `json:"bcc,omitempty"`
	Subject     string       `json:"subject"`
	BodyText    string       `json:"bodyText,omitempty"`
	BodyHTML    string       `json:"bodyHtml,omitempty"`
	InReplyTo   string       `json:"inReplyTo,omitempty"`
	References  string       `json:"references,omitempty"`
	Files       []UploadFile `json:"files,omitempty"`
}

// UploadFile holds raw attachment data from a compose action.
type UploadFile struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	DataB64     string `json:"dataBase64"`
}

// InboundPayload represents the JSON payload posted by a Cloudflare Email Worker.
type InboundPayload struct {
	Secret      string              `json:"secret,omitempty"`
	From        string              `json:"from"`
	FromName    string              `json:"fromName,omitempty"`
	To          []string            `json:"to"`
	Cc          []string            `json:"cc,omitempty"`
	Subject     string              `json:"subject"`
	Text        string              `json:"text,omitempty"`
	HTML        string              `json:"html,omitempty"`
	MessageID   string              `json:"messageId,omitempty"`
	InReplyTo   string              `json:"inReplyTo,omitempty"`
	References  string              `json:"references,omitempty"`
	RawEmail    string              `json:"rawEmail,omitempty"` // RFC 822 MIME raw text fallback
	Attachments []InboundAttachment `json:"attachments,omitempty"`
}

// InboundAttachment is an attachment carried by the inbound webhook.
type InboundAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	DataB64     string `json:"dataBase64"` // base64 encoded payload
}

func newID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
