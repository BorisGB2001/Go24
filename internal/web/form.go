package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"go24/internal/auth"
)

// Forms are sent in the background by forms.js (header X-Go24-Form), so a mistake does not wipe what the person
// typed: an error comes back as JSON {error} and the form shows it; a redirect (the usual answer to a POST)
// comes back as JSON {redirect}, which the page follows — keeping a #fragment that fetch would drop.

func fromForm(r *http.Request) bool { return r.Header.Get("X-Go24-Form") == "1" }

func formAware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fromForm(r) {
			h.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(&formWriter{ResponseWriter: w}, r)
	})
}

type formWriter struct {
	http.ResponseWriter
	redirected bool
}

func (f *formWriter) WriteHeader(code int) {
	if loc := f.Header().Get("Location"); code >= 300 && code < 400 && loc != "" {
		f.redirected = true
		f.Header().Del("Location")
		f.Header().Set("Content-Type", "application/json")
		f.ResponseWriter.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(f.ResponseWriter).Encode(map[string]string{"redirect": loc})
		return
	}
	f.ResponseWriter.WriteHeader(code)
}

func (f *formWriter) Write(b []byte) (int, error) {
	if f.redirected { // http.Redirect's own little HTML body
		return len(b), nil
	}
	return f.ResponseWriter.Write(b)
}

func (f *formWriter) Flush() {
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// clientError keeps what a page's background form got back from the server that go24 did not mean (forms.js
// reports it): the log of the container is gone after a deploy, this table stays for 30 days.
func (s *Server) clientError(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cut := func(v string, n int) string {
		if r := []rune(v); len(r) > n {
			return string(r[:n])
		}
		return v
	}
	status, _ := strconv.Atoi(r.FormValue("status"))
	page, target, body := cut(r.FormValue("page"), 500), cut(r.FormValue("url"), 500), cut(r.FormValue("body"), 500)
	s.Log.Warn("client error", "user", sess.UserID, "page", page, "url", target, "status", status, "body", body)
	if s.DB != nil {
		if _, err := s.DB.Exec(r.Context(), `WITH gone AS (DELETE FROM client_errors WHERE created_at < now() - interval '30 days')
			INSERT INTO client_errors (user_id, page, url, status, body, agent) VALUES ($1, $2, $3, $4, $5, $6)`,
			sess.UserID, page, target, status, body, cut(r.UserAgent(), 300)); err != nil {
			s.Log.Error("client error", "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
