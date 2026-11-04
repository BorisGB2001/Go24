package web

import (
	"net/http"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/audit"
)

// incomingHook is «Входящий вебхук» of «Разработчикам»: a ready address with the key in it,
// <go24>/rest/<employee>/<key>/<path> — a method by its name (crm.deal.list, batch; see restb24.go) or
// the same as go24 REST /api/v1/<path> with that key («.json» at the end writes methods, is dropped).
// The employee is their number, or uuid; the key must be theirs, its scopes hold as for any key.
func (s *Server) incomingHook(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, path := r.PathValue("token"), strings.TrimSuffix(r.PathValue("path"), ".json")
		keyID, _, ok := apikeys.ParseToken(token)
		var owner string
		if ok {
			var num string
			ok = s.DB.QueryRow(r.Context(), `SELECT k.user_id::text, u.num::text FROM api_keys k JOIN users u ON u.id = k.user_id
				WHERE k.key_id = $1`, keyID).Scan(&owner, &num) == nil && (owner == r.PathValue("uid") || num == r.PathValue("uid"))
		}
		if !ok || path == "" || strings.Contains(path, "..") {
			s.audit(r, audit.Event{Kind: "api_bad_key", Target: r.Method + " /rest/" + r.PathValue("uid")})
			if isPortalMethod(path) {
				s.portalFail(w, r, apikeys.ErrInvalid)
				return
			}
			s.apiFail(w, r, apikeys.ErrInvalid)
			return
		}
		if isPortalMethod(path) {
			s.portalServe(w, r, path, token, nil)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = "/api/v1/"+path, ""
		r2.RequestURI = r2.URL.RequestURI()
		r2.Header.Set("Authorization", "Bearer "+token)
		next.ServeHTTP(w, r2)
	}
}
