package api

import (
	"net/http"

	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/auth"
)

// aiRoutes: the owner's Gemini key, the live model and voice, and the monthly cap on
// call minutes (Integrations → AI).
func (s *server) aiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/integrations/ai", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		out := map[string]any{"status": s.AI.Status(), "models": []string{}, "lastCall": s.AI.LastCall()}
		if st := s.AI.Status(); st.Configured {
			if models, err := s.AI.Models(r.Context()); err == nil {
				out["models"] = models
			} else {
				out["error"] = err.Error()
			}
			if chats, err := s.AI.ChatModels(r.Context()); err == nil {
				out["chatModels"] = chats
			}
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("PUT /api/integrations/ai/key", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in struct{ Key string }
		if !decode(w, r, &in) {
			return
		}
		st, err := s.AI.SetKey(r.Context(), in.Key)
		if err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "integration.ai.connect", "", st.Config.Model)
		writeJSON(w, http.StatusOK, st)
	}))
	mux.HandleFunc("PUT /api/integrations/ai", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		var in ai.Config
		if !decode(w, r, &in) {
			return
		}
		st, err := s.AI.SetConfig(r.Context(), in)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	}))
	mux.HandleFunc("DELETE /api/integrations/ai", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Session) {
		if err := s.AI.Clear(); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "integration.ai.disconnect", "", "")
		w.WriteHeader(http.StatusNoContent)
	}))
}
