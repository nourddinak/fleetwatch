package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/statuspage"
)

// pageSummary is a status page in the list, without its logo and contents.
type pageSummary struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Title      string `json:"title"`
	Enabled    bool   `json:"enabled"`
	Domain     string `json:"domain"`
	Access     string `json:"access"`
	Components int    `json:"components"`
	Notices    int    `json:"notices"`
	Drafts     int    `json:"drafts"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// limiter allows n requests per address per window (public sign-ups, passwords).
type limiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(n int, window time.Duration) *limiter {
	return &limiter{n: n, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10000 { // forget everyone rather than grow without bound
		l.hits = map[string][]time.Time{}
	}
	var keep []time.Time
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.n {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}

var (
	unlockLimit    = newLimiter(10, 10*time.Minute)
	subscribeLimit = newLimiter(5, 10*time.Minute)
)

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if isTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// pageAccess lets a visitor see a private page, or answers 401 with just enough
// for the page to show its sign-in screen in its own colours.
func (s *server) pageAccess(w http.ResponseWriter, r *http.Request, c statuspage.Config) bool {
	locked := func(code, msg string) bool {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": code, "error": msg, "title": c.Title, "logo": c.Logo, "accent": c.Accent, "theme": c.Theme})
		return false
	}
	if c.Access != "password" && c.Access != "team" {
		return true
	}
	// Signed-in dashboard users see every page, password or not.
	if sess, err := s.Auth.Authenticate(cookieToken(r)); err == nil && !sess.MFAPending {
		return true
	}
	if c.Access == "password" {
		if ck, err := r.Cookie(statuspage.CookieName(c)); err == nil && s.Status.Unlocked(c, ck.Value) {
			return true
		}
		return locked("locked", "This page needs a password.")
	}
	return locked("team", "Only signed-in members of this team can see this page.")
}

func (s *server) statusRoutes(mux *http.ServeMux) {
	// Public: no sign-in. Returns only what the admin chose to publish.
	public := func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.Status.Find(requestHost(r), r.PathValue("slug"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "There is no status page here.")
			return
		}
		if !s.pageAccess(w, r, c) {
			return
		}
		if c.Access == "" || c.Access == "public" {
			w.Header().Set("Access-Control-Allow-Origin", "*") // embeddable in the admin's own sites
		}
		writeJSON(w, http.StatusOK, s.Status.Public(c))
	}
	mux.HandleFunc("GET /api/public/status", public)
	s.statusSetupRoutes(mux)
	mux.HandleFunc("GET /api/public/status/{slug}", public)
	s.statusPublicExtras(mux)
	s.statusAdminExtras(mux)

	mux.HandleFunc("GET /api/status-pages", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		out := []pageSummary{}
		for _, c := range s.Status.Pages() {
			n, drafts := 0, 0
			for _, g := range c.Groups {
				n += len(g.Components)
			}
			for _, x := range c.Notices {
				if x.Draft {
					drafts++
				}
			}
			access := c.Access
			if access == "" {
				access = "public"
			}
			out = append(out, pageSummary{c.ID, c.Slug, c.Title, c.Enabled, c.Domain, access, n, len(c.Notices), drafts, c.UpdatedAt})
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("GET /api/status-pages/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		c, ok := s.Status.Page(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "Status page not found.")
			return
		}
		writeJSON(w, http.StatusOK, c.Redacted())
	}))
	save := func(w http.ResponseWriter, r *http.Request, id string) {
		var c statuspage.Config
		if !decode(w, r, &c) {
			return
		}
		c.ID = id
		saved, err := s.Status.Save(c)
		if errors.Is(err, statuspage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Status page not found.")
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		state := "off"
		if saved.Enabled {
			state = "on"
		}
		s.audit(r, "statuspage.save", saved.ID, saved.Title+" ("+state+", "+saved.Access+")")
		status := http.StatusOK
		if id == "" {
			status = http.StatusCreated
		}
		writeJSON(w, status, saved.Redacted())
	}
	mux.HandleFunc("POST /api/status-pages", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { save(w, r, "") }))
	mux.HandleFunc("PUT /api/status-pages/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		save(w, r, r.PathValue("id"))
	}))
	mux.HandleFunc("DELETE /api/status-pages/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		id := r.PathValue("id")
		if err := s.Status.Delete(id); errors.Is(err, statuspage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Status page not found.")
			return
		} else if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "statuspage.delete", id, "")
		w.WriteHeader(http.StatusNoContent)
	}))
}

// statusPublicExtras: badge, unlocking password pages, and subscriptions.
func (s *server) statusPublicExtras(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/public/badge.svg", func(w http.ResponseWriter, r *http.Request) {
		c, ok := s.Status.Find(requestHost(r), r.URL.Query().Get("page"))
		if !ok || (c.Access != "" && c.Access != "public") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=120")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write(statuspage.Badge(s.Status.Public(c).Overall, r.URL.Query().Get("label")))
	})
	mux.HandleFunc("GET /api/public/widget.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write(statuspage.WidgetJS)
	})
	mux.HandleFunc("POST /api/public/unlock", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Page, Password string }
		if !decode(w, r, &in) {
			return
		}
		if !unlockLimit.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "locked", "Too many tries. Wait a few minutes.")
			return
		}
		c, ok := s.Status.Find(requestHost(r), in.Page)
		if !ok || c.Access != "password" {
			writeError(w, http.StatusNotFound, "not_found", "There is no status page here.")
			return
		}
		if !s.Status.CheckPassword(c, in.Password) {
			writeError(w, http.StatusUnauthorized, "wrong_password", "Wrong password.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: statuspage.CookieName(c), Value: s.Status.UnlockToken(c), Path: "/", MaxAge: 30 * 24 * 3600,
			HttpOnly: true, Secure: isTLS(r), SameSite: http.SameSiteLaxMode})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/public/subscribe", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Page, Kind, Target string }
		if !decode(w, r, &in) {
			return
		}
		if !subscribeLimit.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "locked", "Too many sign-ups from here. Try again later.")
			return
		}
		c, ok := s.Status.Find(requestHost(r), in.Page)
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "There is no status page here.")
			return
		}
		if !s.pageAccess(w, r, c) {
			return
		}
		if err := s.Status.Subscribe(r.Context(), c, in.Kind, in.Target, requestOrigin(r)); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/public/subscribe/confirm", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Status.Confirm(r.URL.Query().Get("t"))
		if err != nil {
			smallPage(w, http.StatusNotFound, "Link not valid", "This link is not valid any more. Sign up again on the status page.", "")
			return
		}
		smallPage(w, http.StatusOK, "Subscribed", "You will get updates about "+c.Title+".", statuspage.PageURL(c, requestOrigin(r)))
	})
	// GET asks first (mail scanners open links); POST unsubscribes, also as the
	// one-click List-Unsubscribe-Post of mail apps.
	mux.HandleFunc("GET /api/public/subscribe/remove", func(w http.ResponseWriter, r *http.Request) {
		t := r.URL.Query().Get("t")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Unsubscribe</title>%s`+
			`<main><h1>Stop updates?</h1><p>You will not get emails or webhook calls from this status page any more.</p>`+
			`<form method="post" action="/api/public/subscribe/remove?t=%s"><button>Unsubscribe</button></form></main>`, smallCSS, html.EscapeString(t))
	})
	mux.HandleFunc("POST /api/public/subscribe/remove", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Status.Unsubscribe(r.URL.Query().Get("t"))
		if err != nil {
			smallPage(w, http.StatusOK, "Already unsubscribed", "This subscription does not exist any more.", "")
			return
		}
		smallPage(w, http.StatusOK, "Unsubscribed", "You will not get updates about "+c.Title+" any more.", "")
	})
}

const smallCSS = `<style>body{font:16px system-ui,sans-serif;background:#f4f3ee;color:#111;display:grid;place-items:center;min-height:100vh;margin:0}` +
	`main{background:#fff;border-radius:16px;padding:32px;max-width:420px;margin:16px}h1{font-size:20px;margin:0 0 8px}` +
	`button,a.b{display:inline-block;margin-top:16px;background:#111;color:#fff;border:0;border-radius:999px;padding:10px 18px;font:inherit;text-decoration:none;cursor:pointer}</style>`

func smallPage(w http.ResponseWriter, status int, title, msg, link string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	extra := ""
	if link != "" {
		extra = `<a class="b" href="` + html.EscapeString(link) + `">Open the status page</a>`
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>%s<main><h1>%s</h1><p>%s</p>%s</main>`,
		html.EscapeString(title), smallCSS, html.EscapeString(title), html.EscapeString(msg), extra)
}

// statusAdminExtras: notices, subscribers, reports and email settings.
func (s *server) statusAdminExtras(mux *http.ServeMux) {
	failNotice := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, statuspage.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Status page not found.")
		case errors.Is(err, statuspage.ErrNoNotice):
			writeError(w, http.StatusNotFound, "not_found", "Notice not found.")
		case errors.Is(err, statuspage.ErrNoSubscriber):
			writeError(w, http.StatusNotFound, "not_found", "Subscriber not found.")
		default:
			fail(w, err)
		}
	}
	type noticeIn struct {
		statuspage.Notice
		Notify bool `json:"notify"`
	}
	mux.HandleFunc("POST /api/status-pages/{id}/notices", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in noticeIn
		if !decode(w, r, &in) {
			return
		}
		in.Auto, in.Maint, in.Updates = "", "", nil
		n, err := s.Status.AddNotice(r.PathValue("id"), in.Notice, in.Notify)
		if err != nil {
			failNotice(w, err)
			return
		}
		s.audit(r, "statuspage.notice", r.PathValue("id"), n.Title)
		writeJSON(w, http.StatusCreated, n)
	}))
	mux.HandleFunc("PUT /api/status-pages/{id}/notices/{nid}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in noticeIn
		if !decode(w, r, &in) {
			return
		}
		in.ID = r.PathValue("nid")
		n, err := s.Status.EditNotice(r.PathValue("id"), in.Notice, in.Notify)
		if err != nil {
			failNotice(w, err)
			return
		}
		writeJSON(w, http.StatusOK, n)
	}))
	mux.HandleFunc("POST /api/status-pages/{id}/notices/{nid}/updates", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct {
			Stage, Body string
			Notify      bool
		}
		if !decode(w, r, &in) {
			return
		}
		n, err := s.Status.PostUpdate(r.PathValue("id"), r.PathValue("nid"), statuspage.NoticeUpdate{Stage: in.Stage, Body: in.Body}, in.Notify)
		if err != nil {
			failNotice(w, err)
			return
		}
		writeJSON(w, http.StatusOK, n)
	}))
	mux.HandleFunc("DELETE /api/status-pages/{id}/notices/{nid}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Status.DeleteNotice(r.PathValue("id"), r.PathValue("nid")); err != nil {
			failNotice(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("GET /api/status-pages/{id}/subscribers", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		list, err := s.Status.Subscribers(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("DELETE /api/status-pages/{id}/subscribers/{sid}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Status.RemoveSubscriber(r.PathValue("id"), r.PathValue("sid")); err != nil {
			failNotice(w, err)
			return
		}
		s.audit(r, "statuspage.subscriber.remove", r.PathValue("id"), r.PathValue("sid"))
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/status-pages/{id}/subscribers/{sid}/test", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Status.SendTest(r.PathValue("id"), r.PathValue("sid")); err != nil {
			failNotice(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "sending"})
	}))

	mux.HandleFunc("GET /api/status-pages/{id}/report", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		month := r.URL.Query().Get("month")
		rep, err := s.Status.Report(r.PathValue("id"), month)
		if err != nil {
			failNotice(w, err)
			return
		}
		if r.URL.Query().Get("format") == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="uptime-`+month+`.csv"`)
			w.Write(rep.CSV())
			return
		}
		writeJSON(w, http.StatusOK, rep)
	}))
	mux.HandleFunc("POST /api/status-pages/{id}/report/email", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Month, To string }
		if !decode(w, r, &in) {
			return
		}
		if err := s.Status.EmailReport(r.Context(), r.PathValue("id"), in.Month, in.To); err != nil {
			failNotice(w, err)
			return
		}
		s.audit(r, "statuspage.report.email", r.PathValue("id"), in.Month+" to "+in.To)
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	}))

	// Email senders (Integrations → Email): owner only, under /api/integrations.
	senders := func() any {
		used := map[string][]string{} // sender → the status pages sending through it
		def := ""
		list := s.Status.Mail.Senders()
		for _, x := range list {
			if x.Default {
				def = x.ID
			}
		}
		for _, p := range s.Status.Pages() {
			id := p.Sender
			if id == "" {
				id = def
			}
			used[id] = append(used[id], p.Title)
		}
		type item struct {
			statuspage.Sender
			UsedBy []string `json:"usedBy"`
		}
		out := []item{}
		for _, x := range list {
			out = append(out, item{x, append([]string{}, used[x.ID]...)})
		}
		return out
	}
	mux.HandleFunc("GET /api/integrations/email", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, senders())
	}))
	saveSender := func(w http.ResponseWriter, r *http.Request, id string) {
		var in statuspage.Sender
		if !decode(w, r, &in) {
			return
		}
		in.ID = id
		out, err := s.Status.Mail.SaveSender(in)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.sender", out.ID, out.Label+" <"+out.From+">")
		writeJSON(w, http.StatusOK, senders())
	}
	mux.HandleFunc("POST /api/integrations/email", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { saveSender(w, r, "") }))
	mux.HandleFunc("PUT /api/integrations/email/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { saveSender(w, r, r.PathValue("id")) }))
	mux.HandleFunc("DELETE /api/integrations/email/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.Status.Mail.DeleteSender(r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.sender.delete", r.PathValue("id"), "")
		writeJSON(w, http.StatusOK, senders())
	}))
	// A sender from your own domain, through Resend: the domain added there, its DNS
	// records put in Cloudflare when one of your accounts holds it, then verified.
	mux.HandleFunc("POST /api/integrations/email/domain", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ APIKey, Address, FromName, Label string }
		if !decode(w, r, &in) {
			return
		}
		addr, err := mail.ParseAddress(strings.TrimSpace(in.Address))
		_, domain, ok := strings.Cut(strings.ToLower(addrOf(addr)), "@")
		if err != nil || !ok || !strings.Contains(domain, ".") {
			writeError(w, http.StatusBadRequest, "invalid", "An address like noreply@yourdomain.com.")
			return
		}
		key := strings.TrimSpace(in.APIKey)
		if !strings.HasPrefix(key, "re_") {
			writeError(w, http.StatusBadRequest, "invalid", "A Resend API key starts with re_ (resend.com → API keys).")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		d, err := statuspage.ResendAddDomain(ctx, key, domain)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resend", err.Error())
			return
		}
		dnsDone, dnsNote := false, "Add these records at your DNS provider, then check again."
		if s.DNS != nil {
			recs := []dns.MailRecord{}
			for _, x := range d.Records {
				recs = append(recs, dns.MailRecord{Type: x.Type, Name: x.Name, Value: x.Value, Priority: x.Priority})
			}
			switch err := s.DNS.AddMailRecords(ctx, "owner", domain, recs); {
			case err == nil:
				dnsDone, dnsNote = true, "The records were added in Cloudflare. Resend checks them within minutes."
			case !errors.Is(err, dns.ErrNoZone):
				dnsNote = "Cloudflare refused a record (" + err.Error() + "): add them by hand, then check again."
			}
		}
		statuspage.ResendVerify(ctx, key, d.ID)
		out, err := s.Status.Mail.SaveSender(statuspage.Sender{Label: cmp.Or(strings.TrimSpace(in.Label), domain), DomainID: d.ID,
			SMTPConfig: statuspage.SMTPConfig{Host: "smtp.resend.com", Port: 465, Security: "tls", Username: "resend", Password: key, From: addrOf(addr), FromName: cmp.Or(strings.TrimSpace(in.FromName), addr.Name)}})
		if err != nil {
			fail(w, err)
			return
		}
		if d2, err := statuspage.ResendGetDomain(ctx, key, d.ID); err == nil {
			d = d2
		}
		s.audit(r, "email.domain", out.ID, out.From)
		writeJSON(w, http.StatusOK, map[string]any{"sender": out.ID, "domain": d, "dnsAdded": dnsDone, "note": dnsNote})
	}))
	// Sending it yourself (free): this server signs and delivers; DNS allows it.
	mux.HandleFunc("GET /api/integrations/email/direct/check", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, statuspage.CheckDirect(ctx))
	}))
	mux.HandleFunc("POST /api/integrations/email/direct", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Address, FromName, Label string }
		if !decode(w, r, &in) {
			return
		}
		addr, err := mail.ParseAddress(strings.TrimSpace(in.Address))
		_, domain, ok := strings.Cut(strings.ToLower(addrOf(addr)), "@")
		if err != nil || !ok || !strings.Contains(domain, ".") {
			writeError(w, http.StatusBadRequest, "invalid", "An address like noreply@yourdomain.com.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		ready := statuspage.CheckDirect(ctx)
		if ready.IP == "" {
			writeError(w, http.StatusBadGateway, "no_ip", "This server's public IPv4 address could not be found.")
			return
		}
		plan, err := statuspage.PlanDirect(ready.IP)
		if err != nil {
			fail(w, err)
			return
		}
		dnsDone, dnsNote := false, "Add these records at your DNS provider, then check again."
		if s.DNS != nil {
			recs := []dns.MailRecord{}
			for _, x := range plan.Records {
				recs = append(recs, dns.MailRecord{Type: x.Type, Name: x.Name, Value: x.Value})
			}
			switch err := s.DNS.AddMailRecords(ctx, "owner", domain, recs); {
			case err == nil:
				dnsDone, dnsNote = true, "The records were added in Cloudflare: they work within minutes."
			case !errors.Is(err, dns.ErrNoZone):
				dnsNote = "Cloudflare refused a record (" + err.Error() + "): add them by hand, then check again."
			}
		}
		out, err := s.Status.Mail.SaveSender(statuspage.Sender{Label: cmp.Or(strings.TrimSpace(in.Label), domain), Direct: true,
			SMTPConfig: statuspage.SMTPConfig{From: addrOf(addr), FromName: cmp.Or(strings.TrimSpace(in.FromName), addr.Name), Password: plan.KeyPEM}})
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.direct", out.ID, out.From)
		writeJSON(w, http.StatusOK, map[string]any{"sender": out.ID, "domain": statuspage.ResendDomain{Name: domain, Status: "pending", Records: plan.Records}, "dnsAdded": dnsDone, "note": dnsNote, "ready": ready})
	}))
	mux.HandleFunc("GET /api/integrations/email/{id}/domain", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		check := s.Status.Mail.DomainCheck
		for _, x := range s.Status.Mail.Senders() {
			if x.ID == r.PathValue("id") && x.Direct {
				check = s.Status.Mail.DirectCheck
			}
		}
		d, err := check(ctx, r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "resend", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, d)
	}))
	mux.HandleFunc("POST /api/integrations/email/{id}/test", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ To string }
		if !decode(w, r, &in) {
			return
		}
		err := s.Status.Mail.SendFrom(r.Context(), r.PathValue("id"), statuspage.Mail{To: []string{strings.TrimSpace(in.To)}, Subject: "Fleetwatch test email",
			Text: "Email sending works through this sender.\n"})
		if err != nil {
			writeError(w, http.StatusBadRequest, "smtp_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	}))
}

var titleRe = regexp.MustCompile(`<title>[^<]*</title>`)

// statusHTML is index.html marked so the UI renders that public status page.
func (s *server) statusHTML(w http.ResponseWriter, c statuspage.Config, ok bool) {
	b, err := fs.ReadFile(s.UI, "index.html")
	if err != nil || !ok {
		http.NotFound(w, nil)
		return
	}
	// The slug is validated (a-z, 0-9, dashes), so it is safe in an attribute.
	page := strings.Replace(string(b), `<div id="root">`, `<div id="root" data-page="status" data-slug="`+c.Slug+`">`, 1)
	title := "<title>" + html.EscapeString(c.Title) + "</title>"
	if c.Access == "password" || c.Access == "team" {
		title += `<meta name="robots" content="noindex" />` // private pages stay out of search engines
	}
	page = titleRe.ReplaceAllLiteralString(page, title)
	page = strings.Replace(page, `<link rel="manifest" href="/manifest.json" />`, "", 1) // not the dashboard app
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(page))
}

func requestHost(r *http.Request) string {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// statusHost serves /status and /status/<slug>, and on a page's own domain serves
// only that page: the dashboard and its API are not reachable through that name.
func (s *server) statusHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := requestHost(r)
		_, own := s.Status.ForHost(host)
		p := r.URL.Path
		slug, isStatus := strings.TrimPrefix(p, "/status/"), strings.HasPrefix(p, "/status/")
		switch {
		case p == "/status" || isStatus || (own && p == "/"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !isStatus {
				slug = ""
			}
			c, ok := s.Status.Find(host, strings.TrimSuffix(slug, "/"))
			s.statusHTML(w, c, ok)
		case !own, strings.HasPrefix(p, "/assets/"), strings.HasPrefix(p, "/api/public/"), p == "/api/health",
			p == "/icon-192.png", p == "/apple-touch-icon.png":
			next.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func addrOf(a *mail.Address) string {
	if a == nil {
		return ""
	}
	return a.Address
}
