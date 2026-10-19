package api

import (
	"cmp"
	"context"
	"net/http"
	"strconv"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/whatsapp"
)

// whatsAppRoutes: the owner links their WhatsApp and sets how each person is reached
// (Integrations → WhatsApp); everyone may change their own number, language and quiet
// hours (Settings → Account). Who is paged, in which order and for what is On-call's.
func (s *server) whatsAppRoutes(mux *http.ServeMux) {
	type row struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		whatsapp.Person
	}
	people := func() []row {
		saved := s.WhatsApp.People()
		out := []row{}
		if s.OnCall != nil && s.OnCall.People != nil {
			for _, p := range s.OnCall.People() {
				out = append(out, row{p.ID, p.Name, saved[p.ID]})
			}
		}
		return out
	}
	mux.HandleFunc("GET /api/integrations/whatsapp", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		writeJSON(w, http.StatusOK, map[string]any{"status": s.WhatsApp.Status(), "people": people(), "languages": whatsapp.Languages, "options": s.WhatsApp.Options(), "canSpeak": whatsapp.CanSpeak(), "voice": s.WhatsApp.VoiceSetup(), "group": s.WhatsApp.Group()})
	}))
	mux.HandleFunc("PUT /api/integrations/whatsapp/options", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in whatsapp.Options
		if !decode(w, r, &in) {
			return
		}
		o, err := s.WhatsApp.SetOptions(in)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, o)
	}))
	mux.HandleFunc("POST /api/integrations/whatsapp/link", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Phone string }
		if !decode(w, r, &in) {
			return
		}
		if err := s.WhatsApp.Link(in.Phone); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.WhatsApp.Status())
	}))
	mux.HandleFunc("DELETE /api/integrations/whatsapp", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		s.WhatsApp.Unlink(r.Context())
		s.audit(r, "integration.whatsapp.disconnect", "", "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("PUT /api/integrations/whatsapp/people/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in whatsapp.Person
		if !decode(w, r, &in) {
			return
		}
		id := r.PathValue("id")
		known := false
		for _, p := range people() {
			known = known || p.ID == id
		}
		if !known {
			writeError(w, http.StatusNotFound, "not_found", "No such person.")
			return
		}
		p, err := s.WhatsApp.SetPerson(id, in)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
	test := func(w http.ResponseWriter, r *http.Request, id string) {
		p, ok := s.WhatsApp.PersonOf(id)
		if !ok {
			writeError(w, http.StatusBadRequest, "no_phone", "Save a WhatsApp number first.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := s.WhatsApp.Send(ctx, p.Phone, "✅ Fleetwatch: this is a test. On-call alerts will reach you here on WhatsApp."); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
	call := func(w http.ResponseWriter, r *http.Request, id string) {
		p, ok := s.WhatsApp.PersonOf(id)
		if !ok {
			writeError(w, http.StatusBadRequest, "no_phone", "Save a WhatsApp number first.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		ctx, cancel = context.WithTimeout(context.WithoutCancel(r.Context()), 7*time.Minute) // a test talk with the AI may run long
		defer cancel()
		answered, err := s.WhatsApp.Ring(ctx, p.Phone, 40*time.Second, s.WhatsApp.TalkerFor(id, ""))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"answered": answered})
	}
	mux.HandleFunc("POST /api/integrations/whatsapp/people/{id}/call", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		call(w, r, r.PathValue("id"))
	}))
	mux.HandleFunc("PUT /api/me/whatsapp/pin", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ Pin string }
		if !decode(w, r, &in) {
			return
		}
		if err := s.WhatsApp.SetPIN(linkKey(sess), in.Pin); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "whatsapp.pin", "", "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/me/whatsapp/call", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		call(w, r, linkKey(sess))
	}))
	mux.HandleFunc("POST /api/integrations/whatsapp/people/{id}/test", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		test(w, r, r.PathValue("id"))
	}))
	// Everyone's own settings.
	mux.HandleFunc("GET /api/me/whatsapp", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		p, _ := s.WhatsApp.PersonOf(linkKey(sess))
		writeJSON(w, http.StatusOK, map[string]any{"available": s.WhatsApp.Linked(), "person": p, "languages": whatsapp.Languages,
			"hasPin": s.WhatsApp.HasPIN(linkKey(sess)), "requirePin": s.WhatsApp.Options().RequirePIN,
			"number": s.WhatsApp.Status().Number, "canSpeak": whatsapp.CanSpeak(), "voice": s.WhatsApp.VoiceSetup()}) // whom to call to talk with the AI; voice replies need ffmpeg
	}))
	mux.HandleFunc("PUT /api/me/whatsapp", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in whatsapp.Person
		if !decode(w, r, &in) {
			return
		}
		p, err := s.WhatsApp.SetPerson(linkKey(sess), in)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
	mux.HandleFunc("POST /api/me/whatsapp/test", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		test(w, r, linkKey(sess))
	}))

	// The team group (owner only, under /api/integrations).
	phones := func() []string {
		var out []string
		for _, p := range people() {
			if p.Phone != "" {
				out = append(out, p.Phone)
			}
		}
		return out
	}
	mux.HandleFunc("POST /api/integrations/whatsapp/group", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Name string }
		if !decode(w, r, &in) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		g, missing, err := s.WhatsApp.CreateGroup(ctx, in.Name, phones())
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "whatsapp.group", "", "created "+g.Name)
		writeJSON(w, http.StatusOK, map[string]any{"group": g, "notAdded": missing})
	}))
	mux.HandleFunc("PUT /api/integrations/whatsapp/group", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct {
			Incidents string
			Answer    bool
		}
		if !decode(w, r, &in) {
			return
		}
		g, err := s.WhatsApp.SetGroupOptions(in.Incidents, in.Answer)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, g)
	}))
	mux.HandleFunc("POST /api/integrations/whatsapp/group/sync", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.WhatsApp.AddToGroup(ctx, phones()); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	mux.HandleFunc("DELETE /api/integrations/whatsapp/group", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := s.WhatsApp.ForgetGroup(ctx); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "whatsapp.group", "", "left the team group")
		w.WriteHeader(http.StatusNoContent)
	}))

	// Messages passed on between the team: the owner sees all, a member their own.
	mux.HandleFunc("GET /api/me/whatsapp/messages", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		names := map[string]string{}
		for _, p := range people() {
			names[p.ID] = p.Name
		}
		me, list := linkKey(sess), s.WhatsApp.Notes(linkKey(sess))
		if sess.Owner() {
			list = s.WhatsApp.Notes("")
		}
		type item struct {
			whatsapp.Note
			FromName  string `json:"fromName"`
			ToName    string `json:"toName"`
			CanCancel bool   `json:"canCancel"`
		}
		out := []item{}
		for _, n := range list {
			out = append(out, item{n, cmp.Or(names[n.From], "(removed)"), cmp.Or(names[n.To], "(removed)"), n.Status == "waiting" && (sess.Owner() || n.From == me)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out, "me": me})
	}))
	mux.HandleFunc("DELETE /api/me/whatsapp/messages/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		by := linkKey(sess)
		if sess.Owner() {
			by = ""
		}
		if err := s.WhatsApp.CancelNote(id, by); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// Reminders and watches asked for on WhatsApp: the owner sees all, a member their own.
	mux.HandleFunc("GET /api/me/whatsapp/reminders", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		names := map[string]string{}
		for _, p := range people() {
			names[p.ID] = p.Name
		}
		list := s.WhatsApp.Watches(linkKey(sess))
		if sess.Owner() {
			list = s.WhatsApp.Watches("")
		}
		type item struct {
			whatsapp.Watch
			Name      string `json:"name"`
			CanCancel bool   `json:"canCancel"`
		}
		out := []item{}
		for _, x := range list {
			out = append(out, item{x, cmp.Or(names[x.Person], "(removed)"), x.Status == "active"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out, "me": linkKey(sess)})
	}))
	mux.HandleFunc("DELETE /api/me/whatsapp/reminders/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		by := linkKey(sess)
		if sess.Owner() {
			by = ""
		}
		if err := s.WhatsApp.CancelWatch(id, by); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
