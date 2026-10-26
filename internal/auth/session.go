package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "go24_session"

type Session struct {
	UserID    string `json:"uid"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Admin     bool   `json:"adm,omitempty"`   // member of an admin group at login time
	AdminMode bool   `json:"amode,omitempty"` // admin mode switched on (only meaningful when Admin)
	// Granted: an administrator made in go24 («Настройки → Администраторы»), checked on every request and never kept
	// in the cookie, so that taking it away works at once
	Granted bool  `json:"-"`
	Expires int64 `json:"exp"`
	// SID is the session's row in user_sessions (Devices): ending it there signs this browser out; "" — a cookie
	// made before sessions were kept
	SID string `json:"sid,omitempty"`
}

// AdminActive reports whether the user is an administrator with admin mode switched on.
func (s Session) AdminActive() bool { return s.Admin && s.AdminMode }

// Sessions stores the session in an HMAC-signed cookie: no server-side state to keep.
type Sessions struct {
	secret []byte
	secure bool
	ttl    time.Duration
}

func NewSessions(secret string, secure bool) *Sessions {
	return &Sessions{secret: []byte(secret), secure: secure, ttl: 12 * time.Hour}
}

// TTL is how long a session lasts from signing in or a switch of the admin mode.
func (s *Sessions) TTL() time.Duration { return s.ttl }

func (s *Sessions) Set(w http.ResponseWriter, sess Session) error {
	sess.Expires = time.Now().Add(s.ttl).Unix()
	if sess.Granted {
		sess.Admin = false
	}
	payload, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	value := base64.RawURLEncoding.EncodeToString(payload) + "." + s.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/",
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
		Expires: time.Unix(sess.Expires, 0),
	})
	return nil
}

func (s *Sessions) Get(r *http.Request) (Session, error) {
	var sess Session
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return sess, err
	}
	data, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return sess, errors.New("malformed session")
	}
	payload, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil {
		return sess, err
	}
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return sess, errors.New("bad session signature")
	}
	if err := json.Unmarshal(payload, &sess); err != nil {
		return sess, err
	}
	if time.Now().Unix() > sess.Expires {
		return sess, errors.New("session expired")
	}
	return sess, nil
}

func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure})
}

func (s *Sessions) sign(payload []byte) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Stamp signs the parts of a link go24 gives out (not a session), with the purpose as the first part.
func (s *Sessions) Stamp(parts ...string) string {
	return s.sign([]byte("stamp\n" + strings.Join(parts, "\n")))[:32]
}

// Stamped: the stamp is go24's for the parts.
func (s *Sessions) Stamped(stamp string, parts ...string) bool {
	return hmac.Equal([]byte(stamp), []byte(s.Stamp(parts...)))
}
