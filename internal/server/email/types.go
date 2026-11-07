// Package email is mail on your own domains: Cloudflare Email Routing hands every
// message to a small Worker, which copies it to Gmail (per address) and posts the raw
// message here; here it is stored (encrypted files + SQLite metadata), searched,
// organised, and answered through the senders of Integrations → Email.
package email

import "encoding/json"

// Domain is one of your domains turned on for email.
type Domain struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ZoneID        string   `json:"zoneId"`
	CatchAll      bool     `json:"catchAll"`
	CatchAllTo    string   `json:"catchAllTo"` // address id
	SenderID      string   `json:"senderId"`   // "" = the sender matching the domain, else the default
	ForwardTo     []string `json:"forwardTo"`  // Gmail copies for addresses that follow the domain
	SpamThreshold float64  `json:"spamThreshold"`
	Checks        Checks   `json:"checks"`
	CheckedAt     int64    `json:"checkedAt"`
	CreatedAt     int64    `json:"createdAt"`
}

// Checks is how a domain's email setup stands.
type Checks struct {
	MX      Check `json:"mx"`
	SPF     Check `json:"spf"`
	DMARC   Check `json:"dmarc"`
	DKIM    Check `json:"dkim"`    // the sending provider's signature
	Routing Check `json:"routing"` // Cloudflare Email Routing on, catch-all → the Worker
}

type Check struct {
	Status string `json:"status"` // ok | missing | wrong | error | unknown
	Detail string `json:"detail,omitempty"`
}

// Address is one address on a domain. An alias (InboxID set) delivers into another
// address's inbox, keeping the address it was sent to.
type Address struct {
	ID          string   `json:"id"`
	DomainID    string   `json:"domainId"`
	Local       string   `json:"local"`
	Address     string   `json:"address"`
	DisplayName string   `json:"displayName"`
	InboxID     string   `json:"inboxId"`
	Receive     bool     `json:"receive"`
	Send        bool     `json:"send"`
	Disabled    bool     `json:"disabled"`
	ForwardMode string   `json:"forwardMode"` // domain | custom | off
	ForwardTo   []string `json:"forwardTo"`
	Notify      bool     `json:"notify"`
	Signature   string   `json:"signature"`
	CreatedAt   int64    `json:"createdAt"`
	// read-only
	Unread int   `json:"unread"`
	Total  int   `json:"total"`
	Bytes  int64 `json:"bytes"`
}

// Mailbox is the inbox mail for this address lands in.
func (a Address) Mailbox() string {
	if a.InboxID != "" {
		return a.InboxID
	}
	return a.ID
}

type Person struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Auth is what the receiving server (Cloudflare) said about the sender's authentication.
// "" = not reported (missing), unlike "fail".
type Auth struct {
	SPF   string `json:"spf,omitempty"`
	DKIM  string `json:"dkim,omitempty"`
	DMARC string `json:"dmarc,omitempty"`
}

// Message is one email as lists and the viewer show it.
type Message struct {
	ID          int64    `json:"id"`
	MailboxID   string   `json:"mailboxId"`
	DomainID    string   `json:"domainId"`
	ThreadID    int64    `json:"threadId"`
	Direction   string   `json:"direction"` // in | out
	Folder      string   `json:"folder"`
	MessageID   string   `json:"messageId"`
	InReplyTo   string   `json:"inReplyTo,omitempty"`
	References  string   `json:"references,omitempty"`
	Rcpt        string   `json:"rcpt"`
	From        Person   `json:"from"`
	To          []Person `json:"to"`
	Cc          []Person `json:"cc"`
	Bcc         []Person `json:"bcc,omitempty"`
	ReplyTo     string   `json:"replyTo,omitempty"`
	Subject     string   `json:"subject"`
	Snippet     string   `json:"snippet"`
	Size        int64    `json:"size"`
	Attachments int      `json:"attachments"`
	Unread      bool     `json:"unread"`
	Starred     bool     `json:"starred"`
	SpamScore   float64  `json:"spamScore"`
	SpamWhy     []string `json:"spamWhy"`
	Auth        Auth     `json:"auth"`
	SentAt      int64    `json:"sentAt"`
	ReceivedAt  int64    `json:"receivedAt"`
	Labels      []string `json:"labels"`
	ThreadCount int      `json:"threadCount,omitempty"`
	Outbox      *Job     `json:"outbox,omitempty"` // sent mail: how its sending stands
}

// Attachment describes one attached file.
type Attachment struct {
	Part      int    `json:"part"`
	Filename  string `json:"filename"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	ContentID string `json:"contentId,omitempty"`
	Scan      string `json:"scan"` // not_scanned | blocked (dangerous kind)
}

// Folder is a custom folder or a label.
type Folder struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // folder | label
	Name      string `json:"name"`
	Color     string `json:"color"`
	Count     int    `json:"count"`
	Unread    int    `json:"unread"`
	CreatedAt int64  `json:"createdAt"`
}

// File is an attachment uploaded for a draft.
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// Draft is a message being written.
type Draft struct {
	ID        string   `json:"id"`
	FromID    string   `json:"fromId"`
	To        []string `json:"to"`
	Cc        []string `json:"cc"`
	Bcc       []string `json:"bcc"`
	Subject   string   `json:"subject"`
	HTML      string   `json:"html"`
	ReplyToID int64    `json:"replyToId,omitempty"`
	ForwardID int64    `json:"forwardId,omitempty"`
	Files     []File   `json:"files"`
	UpdatedAt int64    `json:"updatedAt"`
}

// Job is one outgoing message in the outbox.
type Job struct {
	ID         string `json:"id"`
	MessageID  int64  `json:"messageId"`
	State      string `json:"state"` // scheduled | sending | accepted | failed | unknown
	Attempts   int    `json:"attempts"`
	SendAt     int64  `json:"sendAt"`
	NextAt     int64  `json:"nextAt"`
	LastError  string `json:"lastError,omitempty"`
	AcceptedAt int64  `json:"acceptedAt,omitempty"`
	From       string `json:"from"`
	Subject    string `json:"subject,omitempty"`
}

type Contact struct {
	ID       int64  `json:"id"`
	Address  string `json:"address"`
	Name     string `json:"name"`
	Company  string `json:"company"`
	Notes    string `json:"notes"`
	Uses     int    `json:"uses"`
	LastUsed int64  `json:"lastUsed"`
}

type SpamRule struct {
	ID        string `json:"id"`
	Scope     string `json:"scope"` // "" | domain id | address id
	Pattern   string `json:"pattern"`
	Action    string `json:"action"` // allow | block
	CreatedAt int64  `json:"createdAt"`
}

// Grant gives a team member access to a domain's or an address's mail.
type Grant struct {
	MemberID  string `json:"memberId"`
	ScopeKind string `json:"scopeKind"` // domain | address
	ScopeID   string `json:"scopeId"`
	Level     string `json:"level"` // read | send | manage
}

// Failure is an inbound message that arrived but could not be filed.
type Failure struct {
	ID         int64  `json:"id"`
	Rcpt       string `json:"rcpt"`
	MailFrom   string `json:"mailFrom"`
	Size       int64  `json:"size"`
	Error      string `json:"error"`
	ReceivedAt int64  `json:"receivedAt"`
	Kept       bool   `json:"kept"` // the raw message is on disk, so it can be retried
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func listJSON(s string) []string {
	out := []string{}
	json.Unmarshal([]byte(s), &out)
	return out
}

func peopleJSON(v []Person) string {
	if v == nil {
		v = []Person{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonPeople(s string) []Person {
	out := []Person{}
	json.Unmarshal([]byte(s), &out)
	return out
}
