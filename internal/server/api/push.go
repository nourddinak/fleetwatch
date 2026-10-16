package api

import (
	"net/http"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/push"
)

func (s *server) pushRoutes(mux *http.ServeMux) {
	// test sends a test push to the signed-in person's own devices.
	mux.HandleFunc("POST /api/push/test", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if err := s.Push.SendTo(r.Context(), sess.MemberID, push.Message{Title: "Fleetwatch test", Body: "Push notifications work on this device.", Severity: "info"}); err != nil {
			writeJSON(w, http.StatusOK, map[string]string{"result": "failed", "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"result": "delivered"})
	}))
	mux.HandleFunc("GET /api/push/key", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		key, err := s.Push.PublicKey()
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"publicKey": key})
	}))
	mux.HandleFunc("GET /api/push/devices", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		list, err := s.Push.Devices(sess.MemberID)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	// subscribe registers this browser (its PushSubscription JSON).
	mux.HandleFunc("POST /api/push/subscribe", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		// The browser's PushSubscription.toJSON() shape, field for field.
		var in struct {
			Endpoint       string
			ExpirationTime *int64 // always null in practice; accepted and ignored
			Keys           struct{ P256dh, Auth string }
		}
		if !decode(w, r, &in) {
			return
		}
		d, err := s.Push.Subscribe(in.Endpoint, in.Keys.P256dh, in.Keys.Auth, r.UserAgent(), sess.MemberID)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "push.subscribe", d.ID, "")
		writeJSON(w, http.StatusOK, d)
	}))
	mux.HandleFunc("POST /api/push/unsubscribe", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		var in struct{ Endpoint string }
		if !decode(w, r, &in) {
			return
		}
		if err := s.Push.Unsubscribe(in.Endpoint, sess.MemberID); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	mux.HandleFunc("DELETE /api/push/devices/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if err := s.Push.Unsubscribe(r.PathValue("id"), sess.MemberID); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "push.remove", r.PathValue("id"), "")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
}
