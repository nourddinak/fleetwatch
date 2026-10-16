package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/mobile"
	"fleetwatch/internal/server/oncall"
)

var pairLimit = newLimiter(10, 10*time.Minute)

// phoneRoutes: people manage their own phones in the dashboard (/api/phones), and
// the Fleetwatch Notifications app talks to /api/mobile with its device token.
func (s *server) phoneRoutes(mux *http.ServeMux) {
	failPhone := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, mobile.ErrNoDevice):
			writeError(w, http.StatusNotFound, "not_found", "Phone not found.")
		case errors.Is(err, mobile.ErrBadCode):
			writeError(w, http.StatusBadRequest, "bad_code", capitalize(err.Error())+".")
		default:
			fail(w, err)
		}
	}
	mux.HandleFunc("GET /api/phones", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		list, err := s.Mobile.Devices(sess.MemberID)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	// pairing makes a short code and the QR payload (dashboard address + code).
	mux.HandleFunc("POST /api/phones/pairing", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		code, exp, err := s.Mobile.NewPairingCode(sess.MemberID)
		if err != nil {
			fail(w, err)
			return
		}
		base := s.publicURL(r)
		qr := "fleetwatch://pair?u=" + url.QueryEscape(base) + "&c=" + url.QueryEscape(code)
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "expiresAt": exp, "url": base, "qr": qr})
	}))
	mux.HandleFunc("PATCH /api/phones/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ Name, Alerts string }
		if !decode(w, r, &in) {
			return
		}
		d, err := s.Mobile.Update(sess.MemberID, r.PathValue("id"), in.Name, in.Alerts)
		if err != nil {
			failPhone(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	}))
	mux.HandleFunc("DELETE /api/phones/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if err := s.Mobile.Remove(sess.MemberID, r.PathValue("id")); err != nil {
			failPhone(w, err)
			return
		}
		s.audit(r, "phone.remove", r.PathValue("id"), "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/phones/{id}/test", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		list, _ := s.Mobile.Devices(sess.MemberID)
		if !slices.ContainsFunc(list, func(d mobile.Device) bool { return d.ID == r.PathValue("id") }) {
			failPhone(w, mobile.ErrNoDevice)
			return
		}
		s.Mobile.ToDevice(r.PathValue("id"), mobile.Message{Kind: "test", Title: "Fleetwatch test", Body: "Notifications reach this phone.", Severity: "info"})
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
	}))

	// ---------- the app ----------

	mux.HandleFunc("POST /api/mobile/pair", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Code, Name, Platform, AppVersion string }
		if !decode(w, r, &in) {
			return
		}
		if !pairLimit.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "locked", "Too many tries. Wait a few minutes.")
			return
		}
		token, d, err := s.Mobile.Pair(in.Code, in.Name, in.Platform, in.AppVersion)
		if err != nil {
			failPhone(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "device": d, "me": s.mobileMe(d)})
	})
	mux.HandleFunc("GET /api/mobile/me", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		writeJSON(w, http.StatusOK, s.mobileMe(d))
	}))
	mux.HandleFunc("PUT /api/mobile/me", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		var in struct{ Name, Alerts string }
		if !decode(w, r, &in) {
			return
		}
		nd, err := s.Mobile.Update(d.MemberID, d.ID, in.Name, in.Alerts)
		if err != nil {
			failPhone(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.mobileMe(nd))
	}))
	mux.HandleFunc("DELETE /api/mobile/me", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		s.Mobile.Remove(d.MemberID, d.ID)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /api/mobile/test", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		s.Mobile.ToDevice(d.ID, mobile.Message{Kind: "test", Title: "Fleetwatch test", Body: "Notifications reach this phone.", Severity: "info"})
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
	}))
	mux.HandleFunc("GET /api/mobile/incidents", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		servers, _ := s.Mobile.Scope(d.MemberID)
		type item struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Type     string `json:"type"`
			Severity string `json:"severity"`
			Status   string `json:"status"`
			Server   string `json:"server"`
			FirstAt  int64  `json:"firstAt"`
			LastAt   int64  `json:"lastAt"`
			By       string `json:"by,omitempty"` // who acknowledged or muted it
		}
		out := []item{}
		if s.Incidents != nil {
			list, _ := s.Incidents.List(incidents.Filter{Status: "open"})
			for _, x := range list {
				if servers != nil && !slices.Contains(servers, x.ServerID) {
					continue
				}
				name := x.ServerID
				if rec, err := s.Fleet.Reg.Get(x.ServerID); err == nil {
					name = rec.Name
				}
				out = append(out, item{x.ID, x.Title, x.Type, x.Severity, x.Status, name, x.FirstAt, x.LastAt, x.ActedBy()})
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))
	// ack: the person on the phone is on it. Anyone who can see the server may
	// acknowledge (an on-call viewer included); it only stops alerts and paging.
	mux.HandleFunc("POST /api/mobile/incidents/{id}/ack", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		x, err := s.Incidents.Get(r.PathValue("id"))
		servers, _ := s.Mobile.Scope(d.MemberID)
		if err != nil || (servers != nil && !slices.Contains(servers, x.ServerID)) {
			writeError(w, http.StatusNotFound, "not_found", "Incident not found.")
			return
		}
		if x.Status == "active" {
			if x, err = s.Incidents.Act(x.ID, "acknowledge", "", s.mobileMe(d).Name+" (phone)"); err != nil {
				fail(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": x.Status})
	}))
	// ws streams the phone's alerts: first everything after ?after=<last id>, then new
	// ones as they come. The app reconnects by itself.
	mux.HandleFunc("GET /api/mobile/ws", s.deviceAuthed(func(w http.ResponseWriter, r *http.Request, d mobile.Device) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := c.CloseRead(r.Context())
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		wake, stop := s.Mobile.Listen(d.ID)
		defer stop()
		s.Mobile.Seen(d.ID, r.URL.Query().Get("v"))
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		send := func() bool {
			for {
				msgs := s.Mobile.Pending(d.ID, after)
				if len(msgs) == 0 {
					return true
				}
				wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := wsWriteJSON(wctx, c, map[string]any{"type": "messages", "data": msgs})
				cancel()
				if err != nil {
					return false
				}
				after = msgs[len(msgs)-1].ID
			}
		}
		if !send() {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				if !s.Mobile.Exists(d.ID) {
					c.Close(websocket.StatusPolicyViolation, "phone removed")
					return
				}
				if !send() {
					return
				}
			case <-ping.C:
				pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := c.Ping(pctx)
				cancel()
				if err != nil {
					return
				}
				s.Mobile.Seen(d.ID, "")
			}
		}
	}))
}

// deviceAuthed lets the app in with its device token (Authorization: Bearer fwm_…).
func (s *server) deviceAuthed(h func(w http.ResponseWriter, r *http.Request, d mobile.Device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		d, err := s.Mobile.Authenticate(strings.TrimSpace(tok))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "This phone is not connected any more. Pair it again.")
			return
		}
		h(w, r, d)
	}
}

type mobileMe struct {
	Device  mobile.Device `json:"device"`
	Name    string        `json:"name"`
	Role    string        `json:"role"`
	OnCall  bool          `json:"onCall"`
	Version string        `json:"version"`
}

func (s *server) mobileMe(d mobile.Device) mobileMe {
	me := mobileMe{Device: d, Role: auth.RoleOwner, Version: s.Version}
	if d.MemberID == "" {
		if p, err := s.Auth.Profile(); err == nil {
			me.Name = firstNonEmpty(p.DisplayName, p.Username)
		}
	} else if m, err := s.Auth.GetMember(d.MemberID); err == nil {
		me.Name, me.Role = firstNonEmpty(m.DisplayName, m.Username), m.Role
	}
	if s.OnCall != nil {
		person := d.MemberID
		if person == "" {
			person = oncall.Owner
		}
		c := s.OnCall.Config()
		me.OnCall = c.Enabled && c.OnCall(time.Now()) == person
	}
	return me
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
