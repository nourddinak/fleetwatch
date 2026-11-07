package email

import (
	"net/url"
	"regexp"
	"strings"

	"fleetwatch/internal/server/valid"
)

// Spam is scored, never deleted: above the domain's threshold a message goes to Spam,
// where "Not spam" brings it back (and trusts the sender). Missing authentication
// counts far less than failed authentication.

var (
	reHref     = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	reTags     = regexp.MustCompile(`<[^>]+>`)
	reIPURL    = regexp.MustCompile(`(?i)https?://\d{1,3}(\.\d{1,3}){3}`)
	reShort    = regexp.MustCompile(`(?i)https?://(bit\.ly|tinyurl\.com|t\.co|goo\.gl|is\.gd|ow\.ly|cutt\.ly|rb\.gy|shorturl\.at)/`)
	reShownURL = regexp.MustCompile(`(?i)^\s*(https?://)?([a-z0-9-]+\.)+[a-z]{2,}`)
	reUrgent   = regexp.MustCompile(`(?i)(verify your account|account (will be )?(suspended|locked|closed)|confirm your password|unusual sign-?in|update your payment|wire transfer|gift card)`)
)

func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// Score rates a message; mine are the domains this dashboard receives mail for.
func Score(p Parsed, mine []string) (float64, []string) {
	score := 0.0
	why := []string{}
	add := func(n float64, reason string) {
		score += n
		why = append(why, reason)
	}
	switch p.Auth.SPF {
	case "fail":
		add(3, "SPF failed: the sending server is not allowed to send for this domain")
	case "softfail":
		add(1, "SPF soft-failed")
	}
	if p.Auth.DKIM == "fail" {
		add(2, "DKIM signature did not verify")
	}
	if p.Auth.DMARC == "fail" {
		add(4, "DMARC failed: the sender's domain says to distrust this message")
	}
	if p.Auth == (Auth{}) {
		add(0.5, "no authentication results were reported")
	}
	fromDom := domainOf(p.From.Address)
	for _, d := range mine {
		if fromDom == d && p.Auth.DMARC != "pass" && p.Auth.DKIM != "pass" && p.Auth.SPF != "pass" {
			add(4, "claims to be from your own domain "+d+" without proof")
		}
		if fromDom != d && strings.Contains(strings.ToLower(p.From.Name), d) {
			add(3, "the sender's name shows "+d+" but the address is elsewhere")
		}
	}
	body := p.HTML
	if body == "" {
		body = p.Text
	}
	if reIPURL.MatchString(body) {
		add(2, "links to a bare IP address")
	}
	if reShort.MatchString(body) {
		add(1, "uses link shorteners")
	}
	mismatch := 0
	for _, m := range reHref.FindAllStringSubmatch(p.HTML, 200) {
		shown := strings.TrimSpace(reTags.ReplaceAllString(m[2], ""))
		if !reShownURL.MatchString(shown) {
			continue
		}
		target, err := url.Parse(strings.TrimSpace(m[1]))
		if err != nil || target.Host == "" {
			continue
		}
		s := shown
		if !strings.Contains(s, "://") {
			s = "http://" + s
		}
		su, err := url.Parse(s)
		if err == nil && su.Hostname() != "" && !sameSite(su.Hostname(), target.Hostname()) {
			mismatch++
		}
	}
	if mismatch > 0 {
		add(3, "a link shows one address but goes to another")
	}
	if reUrgent.MatchString(p.Subject + " " + body) {
		add(1.5, "urgent account or payment wording, common in phishing")
	}
	for _, a := range p.Attachments {
		if scanState(a.Filename) == "blocked" {
			add(3, "carries a program or script ("+a.Filename+")")
			break
		}
	}
	return score, why
}

// sameSite treats a.example.com and b.example.com as the same site.
func sameSite(a, b string) bool {
	tail := func(h string) string {
		parts := strings.Split(strings.ToLower(strings.TrimPrefix(h, "www.")), ".")
		if len(parts) > 2 {
			parts = parts[len(parts)-2:]
		}
		return strings.Join(parts, ".")
	}
	return tail(a) == tail(b)
}

// ---------- allow / block lists ----------

func (s *Service) SpamRules() ([]SpamRule, error) {
	rows, err := s.db.Query(`SELECT id, scope, pattern, action, created_at FROM email_spam_rules ORDER BY action, pattern`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpamRule{}
	for rows.Next() {
		var r SpamRule
		rows.Scan(&r.ID, &r.Scope, &r.Pattern, &r.Action, &r.CreatedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

var rePattern = regexp.MustCompile(`^([a-z0-9._%+'-]+)?@[a-z0-9.-]+\.[a-z]{2,}$`)

func (s *Service) AddSpamRule(r SpamRule) (SpamRule, error) {
	r.Pattern = strings.ToLower(strings.TrimSpace(r.Pattern))
	if !strings.Contains(r.Pattern, "@") {
		r.Pattern = "@" + r.Pattern
	}
	if !rePattern.MatchString(r.Pattern) {
		return r, valid.Error("enter an address (someone@example.com) or a domain (@example.com)")
	}
	if r.Action != "allow" && r.Action != "block" {
		return r, valid.Error("action must be allow or block")
	}
	r.ID, r.CreatedAt = newID("esr"), s.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO email_spam_rules (id, scope, pattern, action, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (scope, pattern) DO UPDATE SET action = excluded.action`, r.ID, r.Scope, r.Pattern, r.Action, r.CreatedAt)
	return r, err
}

func (s *Service) DeleteSpamRule(id string) error {
	_, err := s.db.Exec(`DELETE FROM email_spam_rules WHERE id = ?`, id)
	return err
}

// listed says whether a sender is allowed or blocked for an address (its rules, its
// domain's, then everyone's; an address beats a domain pattern).
func (s *Service) listed(from string, a Address) string {
	from = strings.ToLower(from)
	rows, err := s.db.Query(`SELECT pattern, action FROM email_spam_rules WHERE scope IN ('', ?, ?) AND pattern IN (?, ?)`, a.ID, a.DomainID, from, "@"+domainOf(from))
	if err != nil {
		return ""
	}
	defer rows.Close()
	best, exact := "", false
	for rows.Next() {
		var pat, act string
		rows.Scan(&pat, &act)
		isExact := pat == from
		if best == "" || isExact && !exact || isExact == exact && act == "block" {
			best, exact = act, isExact
		}
	}
	return best
}
