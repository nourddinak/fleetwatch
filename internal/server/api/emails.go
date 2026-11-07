package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/email"
	"fleetwatch/internal/server/github"
	"fleetwatch/internal/server/valid"
)

// Emails: mail on your own domains. Owners see everything; operators only what the
// owner granted them (email.Access), checked here on every request, not in the page.

type emailHandler func(w http.ResponseWriter, r *http.Request, sess auth.Session, acc email.Access)

func (s *server) emailAccess(sess auth.Session) email.Access {
	return s.Email.For(sess.MemberID, sess.Owner(), sess.Role == auth.RoleOperator)
}

// mail runs h for someone with any access to mail.
func (s *server) mail(h emailHandler) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		acc := s.emailAccess(sess)
		if !acc.Any() {
			writeError(w, http.StatusForbidden, "forbidden", "You do not have access to email.")
			return
		}
		h(w, r, sess, acc)
	})
}

// ownerMail runs h for the owner only.
func (s *server) ownerMail(h emailHandler) http.HandlerFunc {
	return s.mail(func(w http.ResponseWriter, r *http.Request, sess auth.Session, acc email.Access) {
		if !acc.Owner {
			writeError(w, http.StatusForbidden, "forbidden", "Only the owner can do this.")
			return
		}
		h(w, r, sess, acc)
	})
}

func failMail(w http.ResponseWriter, err error) {
	if errors.Is(err, email.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	fail(w, err)
}

func denyMail(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "forbidden", "You do not have access to this.")
}

func pathInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil && n > 0
}

func (s *server) emailRoutes(mux *http.ServeMux) {
	E := s.Email
	mux.HandleFunc("POST /api/emails/inbound", s.emailInbound)

	// ---------- the page ----------
	mux.HandleFunc("GET /api/emails/overview", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		doms, err := E.Domains()
		if err != nil {
			fail(w, err)
			return
		}
		addrs, err := E.Addresses()
		if err != nil {
			fail(w, err)
			return
		}
		folders, _ := E.Folders(acc)
		counts, _ := E.Counts(acc)
		type addrOut struct {
			email.Address
			CanSend   bool `json:"canSend"`
			CanManage bool `json:"canManage"`
			CanRead   bool `json:"canRead"`
		}
		outAddrs := []addrOut{}
		seenDom := map[string]bool{}
		for _, a := range addrs {
			read := acc.Mailbox(a.Mailbox(), "read")
			if !read && !acc.AddressCan(a.ID, "read") {
				continue
			}
			if !acc.AddressCan(a.ID, "manage") {
				a.ForwardTo = nil // members see where mail goes only for addresses they manage
			}
			outAddrs = append(outAddrs, addrOut{a, acc.AddressCan(a.ID, "send") && a.Send && !a.Disabled, acc.AddressCan(a.ID, "manage"), read})
			seenDom[a.DomainID] = true
		}
		type domOut struct {
			email.Domain
			CanManage bool `json:"canManage"`
		}
		outDoms := []domOut{}
		for _, d := range doms {
			if acc.Owner || seenDom[d.ID] || acc.DomainCan(d.ID, "read") {
				if !acc.Owner && !acc.DomainCan(d.ID, "manage") {
					d.ForwardTo, d.Checks = nil, email.Checks{}
				}
				outDoms = append(outDoms, domOut{d, acc.DomainCan(d.ID, "manage")})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"domains": outDoms, "addresses": outAddrs, "folders": folders, "counts": counts,
			"owner": acc.Owner, "options": E.Options(), "maxMessage": email.MaxMessage})
	}))
	mux.HandleFunc("GET /api/emails/counts", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		acc := s.emailAccess(sess)
		if !acc.Any() {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		c, err := E.Counts(acc)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "counts": c})
	}))

	// ---------- messages ----------
	mux.HandleFunc("GET /api/emails/messages", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		page, err := E.List(acc, email.Query{Folder: q.Get("folder"), Label: q.Get("label"), Mailbox: q.Get("mailbox"), Domain: q.Get("domain"),
			Search: q.Get("q"), Unread: q.Get("unread") == "1", Cursor: q.Get("cursor"), Limit: limit})
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	}))
	mux.HandleFunc("GET /api/emails/messages/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		id, ok := pathInt(r, "id")
		if !ok {
			failMail(w, email.ErrNotFound)
			return
		}
		m, err := E.Get(acc, id)
		if err != nil {
			failMail(w, err)
			return
		}
		body, err := E.Body(id, r.URL.Query().Get("images") == "1", r.URL.Query().Get("headers") == "1")
		if err != nil {
			fail(w, err)
			return
		}
		if m.Unread && r.URL.Query().Get("peek") != "1" {
			E.Act(acc, email.Action{IDs: []int64{id}, Action: "read"})
			m.Unread = false
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": m, "body": body})
	}))
	mux.HandleFunc("GET /api/emails/threads/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		id, ok := pathInt(r, "id")
		if !ok {
			failMail(w, email.ErrNotFound)
			return
		}
		ms, err := E.Thread(acc, id, r.URL.Query().Get("trash") == "1")
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ms)
	}))
	mux.HandleFunc("GET /api/emails/messages/{id}/raw", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		id, ok := pathInt(r, "id")
		if _, err := E.Get(acc, id); !ok || err != nil {
			failMail(w, email.ErrNotFound)
			return
		}
		raw, err := E.Raw(id)
		if err != nil {
			fail(w, err)
			return
		}
		download(w, fmt.Sprintf("message-%d.eml", id), "message/rfc822", raw)
	}))
	mux.HandleFunc("GET /api/emails/messages/{id}/attachments/{part}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		id, ok := pathInt(r, "id")
		part, err := strconv.Atoi(r.PathValue("part"))
		if _, gerr := E.Get(acc, id); !ok || err != nil || gerr != nil {
			failMail(w, email.ErrNotFound)
			return
		}
		p, err := E.Attachment(id, part)
		if err != nil {
			failMail(w, err)
			return
		}
		// Always a download, never shown here: the file comes from a stranger.
		download(w, p.Filename, "application/octet-stream", p.Data)
	}))
	mux.HandleFunc("POST /api/emails/messages/actions", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in email.Action
		if !decode(w, r, &in) {
			return
		}
		n, err := E.Act(acc, in)
		if err != nil {
			fail(w, err)
			return
		}
		if in.Action == "purge" {
			s.audit(r, "email.purge", "", fmt.Sprintf("%d messages deleted for good", n))
		}
		writeJSON(w, http.StatusOK, map[string]int{"changed": n})
	}))
	mux.HandleFunc("GET /api/emails/export", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		q := r.URL.Query()
		if q.Get("mailbox") != "" && !acc.Mailbox(q.Get("mailbox"), "read") {
			denyMail(w)
			return
		}
		s.audit(r, "email.export", q.Get("mailbox"), "folder "+cmpOr(q.Get("folder"), "all"))
		w.Header().Set("Content-Type", "application/mbox")
		w.Header().Set("Content-Disposition", `attachment; filename="mail-`+time.Now().Format("2006-01-02")+`.mbox"`)
		w.Header().Set("Cache-Control", "no-store")
		E.ExportMbox(acc, q.Get("mailbox"), q.Get("folder"), w)
	}))

	// ---------- writing ----------
	mux.HandleFunc("GET /api/emails/drafts", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		ds, err := E.Drafts(acc)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ds)
	}))
	saveDraft := func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in email.Draft
		if !decode(w, r, &in) {
			return
		}
		in.ID = r.PathValue("id")
		d, err := E.SaveDraft(acc, in, s.actor(r))
		if err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	}
	mux.HandleFunc("POST /api/emails/drafts", s.mail(saveDraft))
	mux.HandleFunc("PUT /api/emails/drafts/{id}", s.mail(saveDraft))
	mux.HandleFunc("GET /api/emails/drafts/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		d, err := E.Draft(acc, r.PathValue("id"))
		if err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	}))
	mux.HandleFunc("DELETE /api/emails/drafts/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		if err := E.DeleteDraft(acc, r.PathValue("id")); err != nil {
			failMail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/emails/drafts/{id}/send", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in struct {
			SendAt int64 `json:"sendAt"`
		}
		if !decode(w, r, &in) {
			return
		}
		j, err := E.Send(acc, r.PathValue("id"), in.SendAt)
		if err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, j)
	}))
	mux.HandleFunc("POST /api/emails/uploads", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		r.Body = http.MaxBytesReader(w, r.Body, email.MaxMessage)
		file, head, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Send one file, 18 MiB at most.")
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "The file is too large.")
			return
		}
		f, err := E.Upload(head.Filename, head.Header.Get("Content-Type"), data)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	}))
	mux.HandleFunc("GET /api/emails/outbox", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		js, err := E.Outbox(acc)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, js)
	}))
	mux.HandleFunc("POST /api/emails/outbox/{id}/cancel", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		draft, err := E.Cancel(acc, r.PathValue("id"))
		if err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"draftId": draft})
	}))
	mux.HandleFunc("POST /api/emails/outbox/{id}/retry", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in struct {
			Force bool `json:"force"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := E.Retry(acc, r.PathValue("id"), in.Force); err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "scheduled"})
	}))
	mux.HandleFunc("GET /api/emails/suggest", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		cs, err := E.Suggest(acc, r.URL.Query().Get("q"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cs)
	}))

	// ---------- folders and labels (anyone with mail; contents stay filtered) ----------
	mux.HandleFunc("POST /api/emails/folders", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in email.Folder
		if !decode(w, r, &in) {
			return
		}
		in.ID = ""
		f, err := E.SaveFolder(in)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	}))
	mux.HandleFunc("PUT /api/emails/folders/{id}", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		var in email.Folder
		if !decode(w, r, &in) {
			return
		}
		in.ID = r.PathValue("id")
		f, err := E.SaveFolder(in)
		if err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	}))
	mux.HandleFunc("DELETE /api/emails/folders/{id}", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		if err := E.DeleteFolder(r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// ---------- addresses ----------
	mux.HandleFunc("POST /api/emails/addresses", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in email.AddressInput
		if !decode(w, r, &in) {
			return
		}
		if !acc.DomainCan(in.DomainID, "manage") || in.InboxID != "" && !acc.Mailbox(in.InboxID, "manage") {
			denyMail(w)
			return
		}
		a, err := E.CreateAddress(in)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.address.create", a.ID, a.Address)
		writeJSON(w, http.StatusOK, a)
	}))
	mux.HandleFunc("PUT /api/emails/addresses/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in email.AddressInput
		if !decode(w, r, &in) {
			return
		}
		id := r.PathValue("id")
		if !acc.AddressCan(id, "manage") || in.InboxID != "" && !acc.Mailbox(in.InboxID, "manage") {
			denyMail(w)
			return
		}
		a, err := E.UpdateAddress(id, in)
		if err != nil {
			failMail(w, err)
			return
		}
		s.audit(r, "email.address.update", a.ID, a.Address)
		writeJSON(w, http.StatusOK, a)
	}))
	mux.HandleFunc("DELETE /api/emails/addresses/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		a, err := E.Address(r.PathValue("id"))
		if err != nil || !acc.DomainCan(a.DomainID, "manage") {
			denyMail(w)
			return
		}
		if err := E.DeleteAddress(a.ID); err != nil {
			failMail(w, err)
			return
		}
		s.audit(r, "email.address.delete", a.ID, a.Address)
		w.WriteHeader(http.StatusNoContent)
	}))

	// ---------- domains ----------
	mux.HandleFunc("POST /api/emails/domains", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		var in struct {
			Name   string `json:"name"`
			ZoneID string `json:"zoneId"`
		}
		if !decode(w, r, &in) {
			return
		}
		d, err := E.AddDomain(in.Name, in.ZoneID)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.domain.add", d.ID, d.Name)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			E.CheckDomain(ctx, d.ID)
		}()
		writeJSON(w, http.StatusOK, d)
	}))
	mux.HandleFunc("PUT /api/emails/domains/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in email.DomainUpdate
		if !decode(w, r, &in) {
			return
		}
		id := r.PathValue("id")
		if !acc.DomainCan(id, "manage") {
			denyMail(w)
			return
		}
		if !acc.Owner { // the sending setup is the owner's
			cur, err := E.Domain(id)
			if err != nil {
				failMail(w, err)
				return
			}
			in.SenderID = cur.SenderID
		}
		d, err := E.UpdateDomain(id, in)
		if err != nil {
			failMail(w, err)
			return
		}
		s.audit(r, "email.domain.update", d.ID, d.Name)
		writeJSON(w, http.StatusOK, d)
	}))
	mux.HandleFunc("DELETE /api/emails/domains/{id}", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		d, err := E.Domain(r.PathValue("id"))
		if err != nil {
			failMail(w, err)
			return
		}
		if err := E.RemoveDomain(d.ID); err != nil {
			failMail(w, err)
			return
		}
		s.audit(r, "email.domain.remove", d.ID, d.Name)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/emails/domains/{id}/check", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		if !acc.DomainCan(r.PathValue("id"), "manage") {
			denyMail(w)
			return
		}
		d, err := E.CheckDomain(r.Context(), r.PathValue("id"))
		if err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	}))
	mux.HandleFunc("POST /api/emails/domains/{id}/setup", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		steps, err := E.Setup(r.Context(), r.PathValue("id"))
		s.audit(r, "email.domain.setup", r.PathValue("id"), strings.Join(steps, "; "))
		if err != nil {
			if errors.Is(err, email.ErrNotFound) {
				failMail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"steps": steps, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
	}))

	// ---------- spam lists ----------
	mux.HandleFunc("GET /api/emails/spam-rules", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		rs, err := E.SpamRules()
		if err != nil {
			fail(w, err)
			return
		}
		out := []email.SpamRule{}
		for _, x := range rs {
			if acc.Owner || x.Scope != "" && (acc.AddressCan(x.Scope, "manage") || acc.DomainCan(x.Scope, "manage")) {
				out = append(out, x)
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /api/emails/spam-rules", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		var in email.SpamRule
		if !decode(w, r, &in) {
			return
		}
		if !acc.Owner && (in.Scope == "" || !acc.AddressCan(in.Scope, "manage") && !acc.DomainCan(in.Scope, "manage")) {
			denyMail(w)
			return
		}
		x, err := E.AddSpamRule(in)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, x)
	}))
	mux.HandleFunc("DELETE /api/emails/spam-rules/{id}", s.mail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, acc email.Access) {
		rs, _ := E.SpamRules()
		for _, x := range rs {
			if x.ID == r.PathValue("id") {
				if !acc.Owner && (x.Scope == "" || !acc.AddressCan(x.Scope, "manage") && !acc.DomainCan(x.Scope, "manage")) {
					denyMail(w)
					return
				}
				E.DeleteSpamRule(x.ID)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// ---------- owner: contacts, Cloudflare, team, settings, storage, recovery ----------
	mux.HandleFunc("GET /api/emails/contacts", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		cs, err := E.Contacts(r.URL.Query().Get("q"), 500)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cs)
	}))
	saveContact := func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		var in email.Contact
		if !decode(w, r, &in) {
			return
		}
		in.ID, _ = strconv.ParseInt(r.PathValue("id"), 10, 64)
		c, err := E.SaveContact(in)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
	mux.HandleFunc("POST /api/emails/contacts", s.ownerMail(saveContact))
	mux.HandleFunc("PUT /api/emails/contacts/{id}", s.ownerMail(saveContact))
	mux.HandleFunc("DELETE /api/emails/contacts/{id}", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		E.DeleteContact(id)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/emails/cloudflare", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		st, err := E.Cloudflare(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": st, "publicUrl": s.publicURL(r), "signIn": s.cloudflareSignIn()})
	}))
	// Sign in with Cloudflare (again) asking for the email permissions, coming back here.
	// allow: the owner added them to an older sign-in app, so ask for them from now on.
	mux.HandleFunc("POST /api/emails/cloudflare/signin-url", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		var in struct {
			Allow bool `json:"allow"`
		}
		if !decode(w, r, &in) {
			return
		}
		if s.DNS == nil {
			writeError(w, http.StatusNotFound, "not_found", "Cloudflare sign-in is not available.")
			return
		}
		if in.Allow {
			if err := s.DNS.AllowEmail(); err != nil {
				fail(w, err)
				return
			}
		}
		u, err := s.DNS.SignInURLTo(s.publicURL(r), github.OwnerKey, "#/emails?tab=settings")
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": u})
	}))
	// Email through the connected account: deploys the Worker, nothing pasted.
	mux.HandleFunc("POST /api/emails/cloudflare/signin", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		st, err := E.UseSignIn(r.Context(), s.publicURL(r))
		if err != nil && !st.Connected {
			fail(w, err)
			return
		}
		s.audit(r, "email.cloudflare.connect", st.AccountID, st.Account+" (sign-in)")
		writeJSON(w, http.StatusOK, map[string]any{"status": st, "error": errText(err)})
	}))
	mux.HandleFunc("POST /api/emails/cloudflare", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		var in struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &in) {
			return
		}
		st, err := E.ConnectCloudflare(r.Context(), in.Token, s.publicURL(r))
		if err != nil && !st.Connected {
			fail(w, err)
			return
		}
		s.audit(r, "email.cloudflare.connect", st.AccountID, st.Account)
		writeJSON(w, http.StatusOK, map[string]any{"status": st, "error": errText(err)})
	}))
	mux.HandleFunc("DELETE /api/emails/cloudflare", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		if err := E.DisconnectCloudflare(); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.cloudflare.disconnect", "", "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/emails/cloudflare/deploy", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		if err := E.PushEdge(r.Context()); err != nil {
			fail(w, err)
			return
		}
		st, _ := E.Cloudflare(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"status": st})
	}))
	mux.HandleFunc("GET /api/emails/cloudflare/zones", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		zs, err := E.Zones(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, zs)
	}))
	// A Worker deployed by hand (Wrangler) needs the secret: shown on request only.
	mux.HandleFunc("POST /api/emails/cloudflare/manual-secret", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		v, err := E.ManualSecret()
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.secret.shown", "", "")
		writeJSON(w, http.StatusOK, map[string]string{"secret": v, "url": s.publicURL(r) + "/api/emails/inbound"})
	}))
	mux.HandleFunc("GET /api/emails/grants", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		gs, err := E.Grants()
		if err != nil {
			fail(w, err)
			return
		}
		ms, _ := s.Auth.ListMembers()
		members := []map[string]string{}
		for _, m := range ms {
			if m.Role == auth.RoleOperator {
				members = append(members, map[string]string{"id": m.ID, "username": m.Username, "displayName": m.DisplayName})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": gs, "operators": members})
	}))
	mux.HandleFunc("PUT /api/emails/grants/{member}", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		var in []email.Grant
		if !decode(w, r, &in) {
			return
		}
		m, err := s.Auth.GetMember(r.PathValue("member"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "Team member not found.")
			return
		}
		if err := E.SetGrants(m.ID, m.Role, in); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.grants", m.ID, fmt.Sprintf("%s: %d grants", m.Username, len(in)))
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	}))
	mux.HandleFunc("GET /api/emails/options", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		out := map[string]any{"options": E.Options(), "senders": []any{}}
		if s.Status != nil && s.Status.Mail != nil {
			out["senders"] = s.Status.Mail.Senders()
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("PUT /api/emails/options", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		var in email.Options
		if !decode(w, r, &in) {
			return
		}
		if err := E.SaveOptions(in); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.options", "", fmt.Sprintf("trash %d days, spam %d days", in.TrashDays, in.SpamDays))
		writeJSON(w, http.StatusOK, in)
	}))
	mux.HandleFunc("GET /api/emails/storage", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		u, err := E.Usage()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"usage": u, "database": filepath.Join(s.DataDir, "ops.db"), "key": filepath.Join(s.DataDir, "secret.key")})
	}))
	mux.HandleFunc("GET /api/emails/failures", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		fs, err := E.Failures()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fs)
	}))
	mux.HandleFunc("POST /api/emails/failures/{id}/retry", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		res, err := E.RetryFailure(id)
		if err != nil {
			failMail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}))
	mux.HandleFunc("DELETE /api/emails/failures/{id}", s.ownerMail(func(w http.ResponseWriter, r *http.Request, _ auth.Session, _ email.Access) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err := E.DismissFailure(id); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "email.failure.dismiss", strconv.FormatInt(id, 10), "")
		w.WriteHeader(http.StatusNoContent)
	}))
}

// cloudflareSignIn says how far the dashboard's Cloudflare sign-in is from handling
// email: the app set up, its email permissions, an owner account granted them.
func (s *server) cloudflareSignIn() map[string]any {
	out := map[string]any{"app": false, "appEmail": false, "accounts": 0, "account": ""}
	if s.DNS == nil {
		return out
	}
	out["app"], out["appEmail"] = s.DNS.AppClientID() != "", s.DNS.AppEmail()
	n := 0
	for _, a := range s.DNS.Accounts(github.OwnerKey) {
		if a.Provider != "cloudflare" {
			continue
		}
		n++
		if dns.HasEmail(a.Scopes) && out["account"] == "" {
			out["account"] = a.Label
		}
	}
	out["accounts"] = n
	return out
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func download(w http.ResponseWriter, name, ctype string, data []byte) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Write(data)
}

// InboundSignature is what the Worker sends in X-Fw-Signature.
func InboundSignature(secret, ts, rcpt, from string, size int) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s\n%s\n%s\n%d", ts, rcpt, from, size)
	return hex.EncodeToString(mac.Sum(nil))
}

// emailInbound takes a message from the Worker. The signature (shared secret, five
// minutes) proves it is our Worker; the answer comes only once the message is stored.
func (s *server) emailInbound(w http.ResponseWriter, r *http.Request) {
	secret := s.Email.InboundSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "not_set_up", "Email is not set up.")
		return
	}
	ts, err := strconv.ParseInt(r.Header.Get("X-Fw-Timestamp"), 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		writeError(w, http.StatusUnauthorized, "bad_signature", "Stale or missing timestamp.")
		return
	}
	rcpt, from := r.Header.Get("X-Fw-Rcpt"), r.Header.Get("X-Fw-From")
	if rcpt == "" || len(rcpt) > 320 || len(from) > 320 {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing recipient.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, email.MaxMessage+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Could not read the message.")
		return
	}
	want := InboundSignature(secret, r.Header.Get("X-Fw-Timestamp"), rcpt, from, len(raw))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Fw-Signature"))) {
		writeError(w, http.StatusUnauthorized, "bad_signature", "Bad signature.")
		return
	}
	res, err := s.Email.Ingest(email.Inbound{Rcpt: rcpt, MailFrom: from}, raw)
	if err != nil {
		var input valid.Error
		if errors.As(err, &input) {
			fail(w, err)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "Could not store the message.")
		return
	}
	writeJSON(w, http.StatusOK, res)
}
