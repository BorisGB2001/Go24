package mail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
	"github.com/emersion/go-sasl"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"go24/internal/db"
	"go24/internal/i18n"
)

// xoauth2Server checks a token the test lets in and signs in as the memory backend's only user.
type xoauth2Server struct {
	ok   func(user, token string) bool
	done func() error
}

func (x xoauth2Server) Next(resp []byte) ([]byte, bool, error) {
	var user, token string
	for _, kv := range strings.Split(string(resp), "\x01") {
		if v, ok := strings.CutPrefix(kv, "user="); ok {
			user = v
		}
		if v, ok := strings.CutPrefix(kv, "auth=Bearer "); ok {
			token = v
		}
	}
	if !x.ok(user, token) {
		return nil, true, errors.New("invalid credentials")
	}
	return nil, true, x.done()
}

// TestOAuthMailbox: a mailbox connected by signing in to the service — the code is exchanged at the token endpoint,
// the address comes from the id_token, go24 signs in to IMAP with XOAUTH2; a token that ran out is refreshed (the new
// refresh token kept), a revoked one asks to sign in again, signing in again keeps the mailbox.
func TestOAuthMailbox(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// the service: its token endpoint and its IMAP server
	var mu sync.Mutex
	valid := map[string]bool{}   // access tokens IMAP lets in
	refresh := map[string]bool{} // refresh tokens the endpoint takes
	n := 0
	idToken := "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"Anna@Outlook.test","preferred_username":"anna"}`)) + ".y"
	oauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		r.ParseForm()
		if r.Form.Get("client_id") != "cid" && !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") == "good":
		case r.Form.Get("grant_type") == "refresh_token" && refresh[r.Form.Get("refresh_token")]:
			delete(refresh, r.Form.Get("refresh_token")) // Microsoft hands out a new one
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		n++
		access, rt := "access-"+string(rune('0'+n)), "refresh-"+string(rune('0'+n))
		valid[access], refresh[rt] = true, true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": rt, "token_type": "Bearer",
			"expires_in": 3600, "id_token": idToken})
	}))
	defer oauthSrv.Close()

	be := memory.New()
	imapSrv := server.New(be)
	imapSrv.AllowInsecureAuth = true
	imapSrv.EnableAuth("XOAUTH2", func(conn server.Conn) sasl.Server {
		return xoauth2Server{
			ok: func(user, token string) bool {
				mu.Lock()
				defer mu.Unlock()
				return user == "anna@outlook.test" && valid[token]
			},
			done: func() error {
				u, err := be.Login(nil, "username", "password")
				if err != nil {
					return err
				}
				c := conn.Context()
				c.State, c.User = imap.AuthenticatedState, u
				return nil
			}}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	go imapSrv.Serve(ln)
	defer imapSrv.Close()

	saved, savedPort, savedTLS := *oauthServices["microsoft"], oauthIMAPPort, oauthTLS
	defer func() { *oauthServices["microsoft"], oauthIMAPPort, oauthTLS = saved, savedPort, savedTLS }()
	oauthServices["microsoft"].auth, oauthServices["microsoft"].token = oauthSrv.URL+"/authorize", oauthSrv.URL+"/token"
	oauthServices["microsoft"].imap = "127.0.0.1"
	oauthIMAPPort, oauthTLS = ln.Addr().(*net.TCPAddr).Port, false

	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	s := NewStore(pool, "test-secret")
	var ie *i18n.Error
	if _, err := s.OAuthURL("microsoft", "https://go24.test/mail/oauth", "st"); !errors.As(err, &ie) || ie.Key != "err.mail_oauth_off" {
		t.Fatalf("no application: %v", err)
	}
	s.OAuth = map[string]OAuthApp{"microsoft": {ClientID: "cid", Secret: "cs"}, "google": {ClientID: "g"}} // google: no secret
	if got := s.OAuthEnabled(); len(got) != 1 || got[0] != "microsoft" {
		t.Fatalf("enabled = %v", got)
	}
	to, err := s.OAuthURL("microsoft", "https://go24.test/mail/oauth", "st")
	must(err)
	for _, want := range []string{oauthSrv.URL + "/authorize?", "client_id=cid", "state=st", "IMAP.AccessAsUser.All", "offline_access",
		"redirect_uri=https%3A%2F%2Fgo24.test%2Fmail%2Foauth"} {
		if !strings.Contains(to, want) {
			t.Fatalf("sign-in URL %s: no %s", to, want)
		}
	}

	if _, err := s.AddOAuth(ctx, anna, "microsoft", "https://go24.test/mail/oauth", "bad"); !errors.As(err, &ie) || ie.Key != "err.mail_oauth_signin" {
		t.Fatalf("a bad code: %v", err)
	}
	id, err := s.AddOAuth(ctx, anna, "microsoft", "https://go24.test/mail/oauth", "good")
	must(err)
	b, err := s.Mailbox(ctx, anna, id)
	must(err)
	if b.Email != "anna@outlook.test" || b.OAuth != "microsoft" || b.Login != "anna@outlook.test" || b.SMTPHost != "smtp.office365.com" || b.SMTPPort != 587 {
		t.Fatalf("mailbox = %+v", b)
	}
	signedIn := func() error {
		return s.remote(ctx, id, func(c *client.Client, a account) error {
			_, err := c.Select("INBOX", true)
			return err
		})
	}
	must(signedIn()) // the token is fresh: no refresh
	if n != 1 {
		t.Fatalf("refreshed a fresh token: %d", n)
	}

	// the token ran out: refreshed, the new refresh token kept
	a, err := s.account(ctx, id)
	must(err)
	var tok oauth2.Token
	must(json.Unmarshal([]byte(a.password), &tok))
	tok.Expiry = time.Now().Add(-time.Minute)
	sealed, err := s.sealToken(&tok)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE mailboxes SET secret = $2 WHERE id = $1`, id, sealed)
	must(err)
	must(signedIn())
	a, err = s.account(ctx, id)
	must(err)
	must(json.Unmarshal([]byte(a.password), &tok))
	if n != 2 || tok.AccessToken != "access-2" || tok.RefreshToken != "refresh-2" {
		t.Fatalf("after refreshing: n = %d, token = %+v", n, tok)
	}

	// access revoked: the mailbox asks to sign in again, and signing in keeps it (and clears its error)
	mu.Lock()
	clear(valid)
	clear(refresh)
	mu.Unlock()
	if err := signedIn(); !errors.As(err, &ie) || ie.Key != "err.mail_oauth_again" {
		t.Fatalf("revoked access token: %v", err)
	}
	tok.Expiry = time.Now().Add(-time.Minute)
	sealed, _ = s.sealToken(&tok)
	_, err = pool.Exec(ctx, `UPDATE mailboxes SET secret = $2, error = 'err.mail_oauth_again' WHERE id = $1`, id, sealed)
	must(err)
	if err := signedIn(); !errors.As(err, &ie) || ie.Key != "err.mail_oauth_again" {
		t.Fatalf("revoked refresh token: %v", err)
	}
	again, err := s.AddOAuth(ctx, anna, "microsoft", "https://go24.test/mail/oauth", "good")
	must(err)
	if again != id {
		t.Fatalf("signed in again: a new mailbox %s, was %s", again, id)
	}
	b, _ = s.Mailbox(ctx, anna, id)
	if b.Error != "" {
		t.Fatalf("the error stays: %q", b.Error)
	}
	must(signedIn())

	// the application taken away: the mailbox says so
	s.OAuth = nil
	a, _ = s.account(ctx, id)
	if err := s.authorize(ctx, &a); !errors.As(err, &ie) || ie.Key != "err.mail_oauth_off" {
		t.Fatalf("no application: %v", err)
	}
}

// TestOAuthEmail: the address from the id_token (email, else preferred_username) or Яндекс's userinfo.
func TestOAuthEmail(t *testing.T) {
	jwt := func(claims string) *oauth2.Token {
		return (&oauth2.Token{AccessToken: "a"}).WithExtra(map[string]any{
			"id_token": "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"})
	}
	for claims, want := range map[string]string{
		`{"email":"a@b.test"}`:                        "a@b.test",
		`{"preferred_username":"Boss@Corp.test"}`:     "boss@corp.test",
		`{"email":"","preferred_username":"no-mail"}`: "",
	} {
		got, err := oauthEmail(context.Background(), oauthServices["google"], jwt(claims))
		if got != want || (want == "") != (err != nil) {
			t.Fatalf("%s: %q, %v", claims, got, err)
		}
	}
	ya := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "OAuth ya-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"login":"ivan","default_email":"ivan@yandex.ru"}`))
	}))
	defer ya.Close()
	o := *oauthServices["yandex"]
	o.userinfo = ya.URL
	if got, err := oauthEmail(context.Background(), &o, &oauth2.Token{AccessToken: "ya-token"}); err != nil || got != "ivan@yandex.ru" {
		t.Fatalf("yandex: %q, %v", got, err)
	}
	if _, err := oauthEmail(context.Background(), &o, &oauth2.Token{AccessToken: "other"}); err == nil {
		t.Fatal("yandex: a refused token gave an address")
	}
}

// TestXOAuth2: what IMAP and SMTP are told.
func TestXOAuth2(t *testing.T) {
	mech, ir, _ := xoauth2{"a@b.test", "tok"}.Start()
	if mech != "XOAUTH2" || string(ir) != "user=a@b.test\x01auth=Bearer tok\x01\x01" {
		t.Fatalf("imap: %s %q", mech, ir)
	}
	var auth smtp.Auth = smtpXOAuth2{xoauth2{"a@b.test", "tok"}}
	mech, ir, _ = auth.Start(&smtp.ServerInfo{Name: "smtp.test", TLS: true})
	if mech != "XOAUTH2" || string(ir) != "user=a@b.test\x01auth=Bearer tok\x01\x01" {
		t.Fatalf("smtp: %s %q", mech, ir)
	}
	if next, err := auth.Next([]byte(`{"status":"401"}`), true); err != nil || next == nil || len(next) != 0 {
		t.Fatalf("smtp error challenge: %q, %v", next, err)
	}
}
