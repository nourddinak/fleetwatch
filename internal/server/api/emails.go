package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/emails"
	"fleetwatch/internal/server/valid"
)

func (s *server) emailRoutes(mux *http.ServeMux) {
	// Inbound webhook from Cloudflare Email Worker (authenticated via secret header)
	mux.HandleFunc("POST /api/emails/inbound", s.handleEmailInbound)

	// Domains
	mux.HandleFunc("GET /api/emails/domains", s.authed(s.listEmailDomains))
	mux.HandleFunc("POST /api/emails/domains", s.authed(s.addEmailDomain))
	mux.HandleFunc("PUT /api/emails/domains/{id}", s.authed(s.updateEmailDomain))
	mux.HandleFunc("DELETE /api/emails/domains/{id}", s.authed(s.deleteEmailDomain))
	mux.HandleFunc("POST /api/emails/domains/{id}/setup-dns", s.authed(s.setupEmailDNS))

	// Cloudflare Email Worker Script & Webhook Secret
	mux.HandleFunc("GET /api/emails/worker-script", s.authed(s.emailWorkerScript))

	// Aliases
	mux.HandleFunc("GET /api/emails/aliases", s.authed(s.listEmailAliases))
	mux.HandleFunc("POST /api/emails/aliases", s.authed(s.addEmailAlias))
	mux.HandleFunc("PUT /api/emails/aliases/{id}", s.authed(s.updateEmailAlias))
	mux.HandleFunc("DELETE /api/emails/aliases/{id}", s.authed(s.deleteEmailAlias))

	// Messages & Webmail
	mux.HandleFunc("GET /api/emails/messages", s.authed(s.listEmailMessages))
	mux.HandleFunc("GET /api/emails/messages/{id}", s.authed(s.getEmailMessage))
	mux.HandleFunc("POST /api/emails/send", s.authed(s.sendEmailMessage))
	mux.HandleFunc("PUT /api/emails/messages/{id}/read", s.authed(s.setEmailMessageRead))
	mux.HandleFunc("PUT /api/emails/messages/{id}/star", s.authed(s.setEmailMessageStar))
	mux.HandleFunc("DELETE /api/emails/messages/{id}", s.authed(s.deleteEmailMessage))

	// Attachments
	mux.HandleFunc("GET /api/emails/attachments/{id}", s.authed(s.downloadEmailAttachment))
}

func (s *server) handleEmailInbound(w http.ResponseWriter, r *http.Request) {
	// Authenticate inbound secret
	secret := r.Header.Get("X-Fleetwatch-Email-Secret")
	if secret == "" {
		secret = r.URL.Query().Get("secret")
	}
	expectedSec := s.Emails.WebhookSecret()
	if expectedSec != "" && secret != expectedSec {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email webhook secret")
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 25<<20)) // 25 MB max
	if err != nil {
		fail(w, err)
		return
	}

	var payload emails.InboundPayload
	if json.Unmarshal(rawBody, &payload) != nil {
		// Fallback: raw MIME body
		payload = emails.InboundPayload{RawEmail: string(rawBody)}
	}

	msg, err := s.Emails.ProcessInbound(r.Context(), payload)
	if err != nil {
		fail(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "messageId": msg.ID})
}

// ---------------- Domains ----------------

func (s *server) listEmailDomains(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Emails.ListDomains()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) addEmailDomain(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in emails.Domain
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, valid.Error("invalid JSON body"))
		return
	}
	created, err := s.Emails.AddDomain(in)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.domain.add", created.ID, created.Domain)
	writeJSON(w, http.StatusCreated, created)
}

func (s *server) updateEmailDomain(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in emails.Domain
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, valid.Error("invalid JSON body"))
		return
	}
	in.ID = r.PathValue("id")
	if err := s.Emails.UpdateDomain(in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.domain.update", in.ID, in.Domain)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) deleteEmailDomain(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Emails.DeleteDomain(id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.domain.delete", id, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) setupEmailDNS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	owner := sess.MemberID
	if owner == "" {
		owner = "owner"
	}
	if err := s.Emails.SetupCloudflareDNS(r.Context(), owner, id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.domain.setup_dns", id, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "configured"})
}

func (s *server) emailWorkerScript(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	script := s.Emails.WorkerScript(s.publicURL(r))
	secret := s.Emails.WebhookSecret()
	writeJSON(w, http.StatusOK, map[string]string{
		"script":        script,
		"webhookSecret": secret,
		"endpoint":      strings.TrimRight(s.publicURL(r), "/") + "/api/emails/inbound",
	})
}

// ---------------- Aliases ----------------

func (s *server) listEmailAliases(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	domainID := r.URL.Query().Get("domainId")
	list, err := s.Emails.ListAliases(domainID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) addEmailAlias(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in emails.Alias
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, valid.Error("invalid JSON body"))
		return
	}
	created, err := s.Emails.AddAlias(in)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.alias.add", created.ID, created.FullAddress)
	writeJSON(w, http.StatusCreated, created)
}

func (s *server) updateEmailAlias(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in emails.Alias
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, valid.Error("invalid JSON body"))
		return
	}
	in.ID = r.PathValue("id")
	if err := s.Emails.UpdateAlias(in); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.alias.update", in.ID, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) deleteEmailAlias(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Emails.DeleteAlias(id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.alias.delete", id, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------- Messages ----------------

func (s *server) listEmailMessages(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	filter := emails.MessageFilter{
		Folder:   q.Get("folder"),
		DomainID: q.Get("domainId"),
		AliasID:  q.Get("aliasId"),
		ThreadID: q.Get("threadId"),
		Query:    q.Get("q"),
		Limit:    limit,
		Offset:   offset,
	}

	msgs, total, err := s.Emails.ListMessages(filter)
	if err != nil {
		fail(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"messages": msgs,
		"total":    total,
		"limit":    filter.Limit,
		"offset":   filter.Offset,
	})
}

func (s *server) getEmailMessage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	msg, err := s.Emails.GetMessage(id)
	if err != nil {
		fail(w, err)
		return
	}
	// Mark read automatically when opened
	if !msg.IsRead {
		s.Emails.SetRead(id, true)
		msg.IsRead = true
	}
	writeJSON(w, http.StatusOK, msg)
}

func (s *server) sendEmailMessage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in emails.SendRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, valid.Error("invalid JSON body"))
		return
	}
	msg, err := s.Emails.Send(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.send", msg.ID, "To: "+strings.Join(in.To, ", "))
	writeJSON(w, http.StatusCreated, msg)
}

func (s *server) setEmailMessageRead(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	var in struct {
		Read bool `json:"read"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if err := s.Emails.SetRead(id, in.Read); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) setEmailMessageStar(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	var in struct {
		Starred bool `json:"starred"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if err := s.Emails.SetStarred(id, in.Starred); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) deleteEmailMessage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Emails.DeleteMessage(id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "emails.message.delete", id, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) downloadEmailAttachment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	att, data, err := s.Emails.GetAttachment(id)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+att.Filename+`"`)
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
