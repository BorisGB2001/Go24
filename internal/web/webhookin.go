package web

import (
	"net/http"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/audit"
)

// incomingHook is «Входящий вебхук» of «Разработчикам»: a ready address with the key in it,
// <go24>/rest/<employee>/<key>/<path> — the same as go24 REST /api/v1/<path> with that key («.json» at the end, as
// the portal writes methods, is dropped). The key must be the employee's; its scopes hold as for any key.
func (s *Server) incomingHook(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, path := r.PathValue("token"), strings.TrimSuffix(r.PathValue("path"), ".json")
		keyID, _, ok := apikeys.ParseToken(token)
		var owner string
		if ok {
			ok = s.DB.QueryRow(r.Context(), `SELECT user_id::text FROM api_keys WHERE key_id = $1`, keyID).Scan(&owner) == nil &&
				owner == r.PathValue("uid")
		}
		if !ok || path == "" || strings.Contains(path, "..") {
			s.audit(r, audit.Event{Kind: "api_bad_key", Target: r.Method + " /rest/" + r.PathValue("uid")})
			s.apiFail(w, r, apikeys.ErrInvalid)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = "/api/v1/"+path, ""
		r2.RequestURI = r2.URL.RequestURI()
		r2.Header.Set("Authorization", "Bearer "+token)
		next.ServeHTTP(w, r2)
	}
}
