package email

import (
	"bytes"
	"html"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Incoming HTML is shown in a sandboxed iframe (no scripts, no same origin, a strict
// CSP); Sanitize is the second wall: an allowlist of tags and attributes, no event
// handlers or script URLs, and remote images held back until asked for.

type SanitizeOptions struct {
	Images bool              // load remote images
	CID    map[string]string // inline images: content id → data: URL
}

var allowedTags = map[atom.Atom]bool{}

func init() {
	for _, a := range []atom.Atom{atom.A, atom.Abbr, atom.Address, atom.B, atom.Bdi, atom.Bdo, atom.Big, atom.Blockquote, atom.Body, atom.Br, atom.Caption, atom.Center, atom.Cite,
		atom.Code, atom.Col, atom.Colgroup, atom.Dd, atom.Del, atom.Details, atom.Dfn, atom.Div, atom.Dl, atom.Dt, atom.Em, atom.Font, atom.Figcaption, atom.Figure, atom.Footer,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Head, atom.Header, atom.Hr, atom.Html, atom.I, atom.Img, atom.Ins, atom.Kbd, atom.Li, atom.Main, atom.Mark,
		atom.Ol, atom.P, atom.Pre, atom.Q, atom.S, atom.Samp, atom.Section, atom.Small, atom.Span, atom.Strike, atom.Strong, atom.Style, atom.Sub, atom.Summary, atom.Sup,
		atom.Table, atom.Tbody, atom.Td, atom.Tfoot, atom.Th, atom.Thead, atom.Time, atom.Title, atom.Tr, atom.Tt, atom.U, atom.Ul, atom.Var, atom.Wbr, atom.Article, atom.Aside, atom.Nav} {
		allowedTags[a] = true
	}
}

// Tags dropped with everything inside them.
var droppedWithContent = map[atom.Atom]bool{atom.Script: true, atom.Noscript: true, atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Applet: true,
	atom.Frame: true, atom.Frameset: true, atom.Svg: true, atom.Math: true, atom.Template: true, atom.Select: true, atom.Textarea: true, atom.Button: true, atom.Form: true}

var allowedAttrs = map[string]bool{"href": true, "src": true, "alt": true, "title": true, "width": true, "height": true, "align": true, "valign": true, "bgcolor": true,
	"border": true, "cellpadding": true, "cellspacing": true, "colspan": true, "rowspan": true, "color": true, "face": true, "size": true, "style": true, "class": true,
	"dir": true, "lang": true, "start": true, "type": true, "span": true, "datetime": true, "cite": true, "id": true, "name": true, "role": true, "summary": true, "background": false}

var reBadCSS = regexp.MustCompile(`(?i)(expression\s*\(|javascript:|vbscript:|-moz-binding|behavior\s*:|@import)`)
var reCSSURL = regexp.MustCompile(`(?i)url\s*\(\s*['"]?\s*(https?:)?//[^)]*\)`)

func safeURL(u string, img bool) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	l = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, l)
	switch {
	case strings.HasPrefix(l, "http://"), strings.HasPrefix(l, "https://"):
		return true
	case !img && (strings.HasPrefix(l, "mailto:") || strings.HasPrefix(l, "tel:") || strings.HasPrefix(l, "#")):
		return true
	case img && strings.HasPrefix(l, "data:image/") && !strings.HasPrefix(l, "data:image/svg"):
		return true
	}
	return false
}

func cleanCSS(css string, images bool) string {
	if reBadCSS.MatchString(css) {
		css = reBadCSS.ReplaceAllString(css, "")
	}
	if !images {
		css = reCSSURL.ReplaceAllString(css, "none")
	}
	return css
}

// Sanitize returns safe HTML.
func Sanitize(in string, o SanitizeOptions) string {
	z := xhtml.NewTokenizer(strings.NewReader(in))
	var b bytes.Buffer
	skip := 0 // inside a dropped-with-content element
	var skipTag atom.Atom
	inStyle := false
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		t := z.Token()
		switch tt {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			if skip > 0 {
				if t.DataAtom == skipTag && tt == xhtml.StartTagToken {
					skip++
				}
				continue
			}
			if droppedWithContent[t.DataAtom] {
				if tt == xhtml.StartTagToken {
					skip, skipTag = 1, t.DataAtom
				}
				continue
			}
			if !allowedTags[t.DataAtom] {
				continue
			}
			if t.DataAtom == atom.Style {
				inStyle = tt == xhtml.StartTagToken
			}
			b.WriteByte('<')
			b.WriteString(t.DataAtom.String())
			isImg := t.DataAtom == atom.Img
			for _, a := range t.Attr {
				k := strings.ToLower(a.Key)
				if !allowedAttrs[k] || a.Namespace != "" {
					continue
				}
				v := a.Val
				switch k {
				case "href":
					if !safeURL(v, false) {
						continue
					}
				case "src":
					if isImg && strings.HasPrefix(strings.ToLower(v), "cid:") {
						if d, ok := o.CID[strings.Trim(v[4:], "<> ")]; ok {
							v = d
						} else {
							continue
						}
					} else if !safeURL(v, isImg) {
						continue
					} else if isImg && !o.Images && !strings.HasPrefix(strings.ToLower(v), "data:") {
						k = "data-fw-src" // held back: "Load images" shows them
					}
				case "style":
					v = cleanCSS(v, o.Images)
				}
				b.WriteByte(' ')
				b.WriteString(k)
				b.WriteString(`="`)
				b.WriteString(html.EscapeString(v))
				b.WriteByte('"')
			}
			if t.DataAtom == atom.A {
				b.WriteString(` target="_blank" rel="noopener noreferrer nofollow"`)
			}
			if tt == xhtml.SelfClosingTagToken {
				b.WriteString(" /")
			}
			b.WriteByte('>')
		case xhtml.EndTagToken:
			if skip > 0 {
				if t.DataAtom == skipTag {
					skip--
				}
				continue
			}
			if !allowedTags[t.DataAtom] {
				continue
			}
			if t.DataAtom == atom.Style {
				inStyle = false
			}
			b.WriteString("</" + t.DataAtom.String() + ">")
		case xhtml.TextToken:
			if skip > 0 {
				continue
			}
			if inStyle {
				b.WriteString(strings.ReplaceAll(cleanCSS(t.Data, o.Images), "</", "<\\/"))
			} else {
				b.WriteString(html.EscapeString(t.Data))
			}
		}
	}
	return b.String()
}

var reBlank = regexp.MustCompile(`\n{3,}`)

// HTMLToText is the plain-text version of HTML: for snippets, search and the text part
// of what you send.
func HTMLToText(in string) string {
	z := xhtml.NewTokenizer(strings.NewReader(in))
	var b strings.Builder
	skip := 0
	var href string
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		t := z.Token()
		switch tt {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			switch t.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Title:
				if tt == xhtml.StartTagToken {
					skip++
				}
			case atom.Br:
				b.WriteString("\n")
			case atom.P, atom.Div, atom.Tr, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Blockquote, atom.Table, atom.Ul, atom.Ol:
				b.WriteString("\n")
			case atom.Li:
				b.WriteString("\n- ")
			case atom.A:
				href = ""
				for _, a := range t.Attr {
					if a.Key == "href" && strings.HasPrefix(a.Val, "http") {
						href = a.Val
					}
				}
			}
		case xhtml.EndTagToken:
			switch t.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Title:
				if skip > 0 {
					skip--
				}
			case atom.P, atom.Div, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Blockquote:
				b.WriteString("\n")
			case atom.A:
				if href != "" {
					b.WriteString(" (" + href + ")")
					href = ""
				}
			}
		case xhtml.TextToken:
			if skip == 0 {
				b.WriteString(strings.Join(strings.Fields(t.Data), " "))
				if strings.HasSuffix(t.Data, " ") || strings.HasSuffix(t.Data, "\n") {
					b.WriteString(" ")
				}
			}
		}
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// TextToHTML shows plain text as HTML (escaped, line breaks kept).
func TextToHTML(t string) string {
	return `<div style="white-space:pre-wrap;font-family:inherit">` + html.EscapeString(t) + `</div>`
}
