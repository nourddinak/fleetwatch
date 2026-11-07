package email

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

// Parsed is what a raw message says. Parsing never fails outright: a broken message
// still gets stored, with whatever could be read.
type Parsed struct {
	Header      textproto.MIMEHeader
	From        Person
	To, Cc      []Person
	ReplyTo     string
	Subject     string
	MessageID   string
	InReplyTo   string
	References  []string
	Date        time.Time
	Text, HTML  string
	Attachments []Part
	Auth        Auth
}

// Part is one attachment (or inline image). Data is set when parsed with keep.
type Part struct {
	Filename  string
	Type      string
	Size      int64
	ContentID string
	Inline    bool
	Data      []byte
}

const (
	maxDepth = 8
	maxParts = 300
)

var words = &mime.WordDecoder{CharsetReader: charset.NewReaderLabel}

func decodeWords(s string) string {
	if out, err := words.DecodeHeader(s); err == nil {
		return strings.TrimSpace(out)
	}
	return strings.TrimSpace(s)
}

func parsePeople(h textproto.MIMEHeader, key string) []Person {
	out := []Person{}
	for _, v := range h.Values(key) {
		list, err := (&mail.AddressParser{WordDecoder: words}).ParseList(v)
		if err != nil { // keep what looks like addresses
			for _, m := range reBareAddr.FindAllString(v, 20) {
				out = append(out, Person{Address: strings.ToLower(m)})
			}
			continue
		}
		for _, a := range list {
			out = append(out, Person{Name: a.Name, Address: strings.ToLower(a.Address)})
		}
	}
	return out
}

var (
	reBareAddr = regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reMsgID    = regexp.MustCompile(`<[^<>\s]+>`)
)

func msgIDs(s string) []string { return reMsgID.FindAllString(s, 100) }

// Parse reads a raw message; keep holds attachment bytes too.
func Parse(raw []byte, keep bool) Parsed {
	var p Parsed
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		p.Header = textproto.MIMEHeader{}
		p.Text = string(raw[:min(len(raw), 200000)])
		p.Subject = "(this message could not be read)"
		return p
	}
	h := textproto.MIMEHeader(m.Header)
	p.Header = h
	if from := parsePeople(h, "From"); len(from) > 0 {
		p.From = from[0]
	}
	p.To, p.Cc = parsePeople(h, "To"), parsePeople(h, "Cc")
	if rt := parsePeople(h, "Reply-To"); len(rt) > 0 {
		p.ReplyTo = rt[0].Address
	}
	p.Subject = decodeWords(h.Get("Subject"))
	if ids := msgIDs(h.Get("Message-Id")); len(ids) > 0 {
		p.MessageID = ids[0]
	}
	if ids := msgIDs(h.Get("In-Reply-To")); len(ids) > 0 {
		p.InReplyTo = ids[0]
	}
	p.References = msgIDs(h.Get("References"))
	if d, err := mail.ParseDate(h.Get("Date")); err == nil {
		p.Date = d
	}
	p.Auth = parseAuth(h)
	parts := 0
	p.walk(h, m.Body, 0, keep, &parts, false)
	return p
}

func (p *Parsed) walk(h textproto.MIMEHeader, body io.Reader, depth int, keep bool, parts *int, inAlternative bool) {
	*parts++
	if depth > maxDepth || *parts > maxParts {
		return
	}
	ctype, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || ctype == "" {
		ctype, params = "text/plain", map[string]string{}
	}
	if strings.HasPrefix(ctype, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextRawPart()
			if err != nil {
				return
			}
			p.walk(part.Header, part, depth+1, keep, parts, ctype == "multipart/alternative")
		}
	}
	disp, dparams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	name := decodeWords(cmpOr(dparams["filename"], params["name"]))
	data := decodeBody(h.Get("Content-Transfer-Encoding"), body)
	cid := strings.Trim(h.Get("Content-Id"), "<> ")
	isText := ctype == "text/plain" || ctype == "text/html"
	if isText && disp != "attachment" && name == "" {
		text := toUTF8(data, params["charset"])
		if ctype == "text/html" && p.HTML == "" {
			p.HTML = text
			return
		}
		if ctype == "text/plain" && p.Text == "" {
			p.Text = text
			return
		}
		if !inAlternative { // another body part: show it below the first
			if ctype == "text/plain" {
				p.Text += "\n\n" + text
			}
			return
		}
		return
	}
	if name == "" {
		name = "attachment"
		if ctype == "message/rfc822" {
			name = "message.eml"
		} else if exts, _ := mime.ExtensionsByType(ctype); len(exts) > 0 {
			name += exts[0]
		}
	}
	at := Part{Filename: path.Base(strings.ReplaceAll(name, "\\", "/")), Type: ctype, Size: int64(len(data)), ContentID: cid, Inline: disp == "inline" || (disp == "" && cid != "")}
	if keep {
		at.Data = data
	}
	p.Attachments = append(p.Attachments, at)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func decodeBody(enc string, r io.Reader) []byte {
	lim := io.LimitReader(r, MaxMessage)
	var out []byte
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		out, _ = io.ReadAll(base64.NewDecoder(base64.StdEncoding, &b64clean{r: bufio.NewReader(lim)}))
	case "quoted-printable":
		out, _ = io.ReadAll(quotedprintable.NewReader(lim))
	default:
		out, _ = io.ReadAll(lim)
	}
	return out
}

// b64clean drops whitespace and junk, which real mail puts inside base64 bodies.
type b64clean struct{ r *bufio.Reader }

func (b *b64clean) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		c, err := b.r.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' {
			p[n] = c
			n++
		}
	}
	return n, nil
}

func toUTF8(b []byte, cs string) string {
	cs = strings.ToLower(strings.TrimSpace(cs))
	if cs == "" || cs == "utf-8" || cs == "us-ascii" || cs == "utf8" {
		return strings.ToValidUTF8(string(b), "�")
	}
	r, err := charset.NewReaderLabel(cs, bytes.NewReader(b))
	if err != nil {
		return strings.ToValidUTF8(string(b), "�")
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return strings.ToValidUTF8(string(b), "�")
	}
	return string(out)
}

// parseAuth reads the authentication results the receiving server added. Only the
// topmost header counts: Cloudflare adds its own above whatever the sender wrote, and
// lower ones can be forged.
var reAuthItem = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc)\s*=\s*([a-z]+)`)

func parseAuth(h textproto.MIMEHeader) Auth {
	var a Auth
	for _, key := range []string{"Authentication-Results", "Arc-Authentication-Results"} {
		vals := h.Values(key)
		if len(vals) == 0 {
			continue
		}
		top := vals[0]
		if key == "Arc-Authentication-Results" && !strings.Contains(strings.ToLower(top), "cloudflare") {
			continue
		}
		for _, m := range reAuthItem.FindAllStringSubmatch(top, -1) {
			v := strings.ToLower(m[2])
			switch strings.ToLower(m[1]) {
			case "spf":
				if a.SPF == "" {
					a.SPF = v
				}
			case "dkim":
				if a.DKIM == "" || a.DKIM != "pass" && v == "pass" { // any passing signature counts
					a.DKIM = v
				}
			case "dmarc":
				if a.DMARC == "" {
					a.DMARC = v
				}
			}
		}
		if a != (Auth{}) {
			return a
		}
	}
	return a
}

// snippet is the start of the text, on one line.
func snippet(p Parsed) string {
	t := p.Text
	if strings.TrimSpace(t) == "" && p.HTML != "" {
		t = HTMLToText(p.HTML)
	}
	t = strings.Join(strings.Fields(t), " ")
	if r := []rune(t); len(r) > 180 {
		t = string(r[:180]) + "…"
	}
	return t
}

var reSubjectPrefix = regexp.MustCompile(`(?i)^\s*((re|fwd?|aw|wg|sv|tr|rv)\s*(\[\d+\])?\s*:\s*)+`)

// normSubject is the subject without Re:/Fwd: prefixes, for the threading fallback.
func normSubject(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(reSubjectPrefix.ReplaceAllString(s, "")), " "))
}

// dangerous kinds are never opened in the browser: shown, downloadable, flagged.
var dangerous = map[string]bool{".exe": true, ".scr": true, ".bat": true, ".cmd": true, ".com": true, ".pif": true, ".js": true, ".jse": true, ".vbs": true, ".vbe": true,
	".wsf": true, ".msi": true, ".ps1": true, ".jar": true, ".hta": true, ".lnk": true, ".iso": true, ".img": true, ".dll": true, ".reg": true, ".cpl": true, ".apk": true, ".app": true}

func scanState(name string) string {
	if dangerous[strings.ToLower(path.Ext(name))] {
		return "blocked"
	}
	return "not_scanned"
}
