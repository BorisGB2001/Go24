package mail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/oauth2"

	"go24/internal/i18n"
)

// Mailboxes connected by signing in to the mail service, as «Подключить» Gmail, Outlook or Яндекс and
// Outlook: the user lets go24 in at the service and gives no password; go24 signs in to IMAP and SMTP with the
// token (SASL XOAUTH2) and refreshes it itself. Microsoft takes no passwords over IMAP at all.

// OAuthApp is go24's application at a mail service, registered by the administrator.
type OAuthApp struct{ ClientID, Secret string }

// OAuthServices are the services, in the order of the form.
var OAuthServices = []string{"google", "microsoft", "yandex"}

// oauthService is how a service is signed in to: its OAuth endpoints and scopes and its servers.
type oauthService struct {
	auth, token, imap, smtp string
	smtpPort                int
	scopes                  []string
	params                  []oauth2.AuthCodeOption
	userinfo                string // where the address is asked for; "" — it is in the id_token
}

// oauthServices: the addresses are replaced in tests.
var oauthServices = map[string]*oauthService{
	"google": {auth: "https://accounts.google.com/o/oauth2/auth", token: "https://oauth2.googleapis.com/token",
		imap: "imap.gmail.com", smtp: "smtp.gmail.com", smtpPort: 465, scopes: []string{"https://mail.google.com/", "openid", "email"},
		params: []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent select_account")}},
	"microsoft": {auth: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		token: "https://login.microsoftonline.com/common/oauth2/v2.0/token", imap: "outlook.office365.com",
		smtp: "smtp.office365.com", smtpPort: 587, // STARTTLS only
		scopes: []string{"https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send",
			"offline_access", "openid", "email"},
		params: []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("prompt", "select_account")}},
	"yandex": {auth: "https://oauth.yandex.ru/authorize", token: "https://oauth.yandex.ru/token", imap: "imap.yandex.ru",
		smtp: "smtp.yandex.ru", smtpPort: 465, scopes: []string{"mail:imap_full", "mail:smtp", "login:email"},
		params: []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("force_confirm", "yes")}, userinfo: "https://login.yandex.ru/info?format=json"},
}

// The services' IMAP servers: over TLS on 993; replaced in tests.
var (
	oauthIMAPPort = 993
	oauthTLS      = true
)

// OAuthEnabled are the services the administrator has registered go24's application at.
func (s *Store) OAuthEnabled() []string {
	var out []string
	for _, k := range OAuthServices {
		if a := s.OAuth[k]; a.ClientID != "" && a.Secret != "" {
			out = append(out, k)
		}
	}
	return out
}

func (s *Store) oauthConfig(svc, redirect string) (*oauth2.Config, *oauthService, error) {
	o, a := oauthServices[svc], s.OAuth[svc]
	if o == nil || a.ClientID == "" || a.Secret == "" {
		return nil, nil, i18n.E("err.mail_oauth_off")
	}
	return &oauth2.Config{ClientID: a.ClientID, ClientSecret: a.Secret, RedirectURL: redirect, Scopes: o.scopes,
		Endpoint: oauth2.Endpoint{AuthURL: o.auth, TokenURL: o.token}}, o, nil
}

// OAuthURL is the page of the service where the user lets go24 in; it comes back to redirect with the code and state.
func (s *Store) OAuthURL(svc, redirect, state string) (string, error) {
	cfg, o, err := s.oauthConfig(svc, redirect)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, o.params...), nil
}

// AddOAuth connects the mailbox the user has signed in to: the code is exchanged for a token, go24 signs in to IMAP
// with it, and the mailbox is kept. A mailbox of the user with that address already there is signed in again (its
// access was taken back or ran out) and keeps its letters.
func (s *Store) AddOAuth(ctx context.Context, userID, svc, redirect, code string) (string, error) {
	cfg, o, err := s.oauthConfig(svc, redirect)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := cfg.Exchange(ctx, code)
	if err != nil || tok.RefreshToken == "" {
		return "", i18n.E("err.mail_oauth_signin")
	}
	email, err := oauthEmail(ctx, o, tok)
	if err != nil {
		return "", err
	}
	a := account{Mailbox: Mailbox{Email: email, Login: email, IMAPHost: o.imap, IMAPPort: oauthIMAPPort, SMTPHost: o.smtp,
		SMTPPort: o.smtpPort, TLS: oauthTLS}, oauth: svc, token: tok.AccessToken}
	c, err := dialIMAP(a.IMAPHost, a.IMAPPort, a.TLS)
	if err != nil {
		return "", i18n.E("err.mail_connect", err.Error())
	}
	err = imapLogin(c, a)
	c.Logout()
	if err != nil {
		return "", i18n.E("err.mail_oauth_imap", clip(err.Error(), 300))
	}
	secret, err := s.sealToken(tok)
	if err != nil {
		return "", err
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO mailboxes (user_id, email, imap_host, imap_port, smtp_host, smtp_port, tls, login, secret, oauth)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (user_id, email) DO UPDATE SET imap_host = $3, imap_port = $4, smtp_host = $5, smtp_port = $6, tls = $7,
			login = $8, secret = $9, oauth = $10, error = ''
		RETURNING id`, userID, a.Email, a.IMAPHost, a.IMAPPort, a.SMTPHost, a.SMTPPort, a.TLS, a.Login, secret, svc).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return "", i18n.E("err.mail_email")
	}
	return id, err
}

// oauthEmail is the address of the account signed in to: from the service's userinfo or the id_token's claims.
func oauthEmail(ctx context.Context, o *oauthService, tok *oauth2.Token) (string, error) {
	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		DefaultEmail      string `json:"default_email"`
	}
	if o.userinfo != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", o.userinfo, nil)
		req.Header.Set("Authorization", "OAuth "+tok.AccessToken) // Яндекс's own scheme
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			if res.StatusCode != http.StatusOK {
				err = errors.New(res.Status)
			} else {
				err = json.NewDecoder(res.Body).Decode(&claims)
			}
			res.Body.Close()
		}
		if err != nil {
			return "", i18n.E("err.mail_oauth_signin")
		}
	} else if idt, _ := tok.Extra("id_token").(string); idt != "" {
		// the id_token came from the token endpoint itself over TLS: its claims are read without checking the signature
		if parts := strings.Split(idt, "."); len(parts) == 3 {
			if raw, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
				_ = json.Unmarshal(raw, &claims)
			}
		}
	}
	for _, e := range []string{claims.DefaultEmail, claims.Email, claims.PreferredUsername} {
		if a, err := mail.ParseAddress(strings.TrimSpace(e)); err == nil && a.Address == strings.TrimSpace(e) {
			return strings.ToLower(a.Address), nil
		}
	}
	return "", i18n.E("err.mail_oauth_email")
}

func (s *Store) sealToken(tok *oauth2.Token) ([]byte, error) {
	raw, err := json.Marshal(oauth2.Token{AccessToken: tok.AccessToken, TokenType: tok.TokenType, RefreshToken: tok.RefreshToken,
		Expiry: tok.Expiry})
	if err != nil {
		return nil, err
	}
	return s.seal(string(raw))
}

// authorize gives a mailbox connected by signing in a fresh access token, refreshing it when it runs out (a new
// refresh token the service hands out is kept); a mailbox with a password needs none.
func (s *Store) authorize(ctx context.Context, a *account) error {
	if a.oauth == "" || a.token != "" {
		return nil
	}
	cfg, _, err := s.oauthConfig(a.oauth, "")
	if err != nil {
		return err
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(a.password), &tok); err != nil {
		return i18n.E("err.mail_oauth_again")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	fresh, err := cfg.TokenSource(ctx, &tok).Token()
	if err != nil {
		return i18n.E("err.mail_oauth_again")
	}
	if fresh.AccessToken != tok.AccessToken || fresh.RefreshToken != tok.RefreshToken {
		secret, err := s.sealToken(fresh)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, `UPDATE mailboxes SET secret = $2 WHERE id::text = $1`, a.ID, secret); err != nil {
			return err
		}
	}
	a.token = fresh.AccessToken
	return nil
}

// xoauth2 is SASL XOAUTH2 (Google's and Microsoft's) for IMAP and SMTP.
type xoauth2 struct{ user, token string }

func (x xoauth2) ir() []byte {
	return []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01")
}

// Start, Next — sasl.Client (IMAP): a challenge is the server's error in JSON; the empty answer lets it say NO.
func (x xoauth2) Start() (string, []byte, error)        { return "XOAUTH2", x.ir(), nil }
func (x xoauth2) Next(challenge []byte) ([]byte, error) { return []byte{}, nil }

// imapLogin signs in to the IMAP server: with the token or the password.
func imapLogin(c *client.Client, a account) error {
	if a.oauth != "" {
		return c.Authenticate(xoauth2{a.Login, a.token})
	}
	return c.Login(a.Login, a.password)
}

// smtpXOAuth2 is the same for net/smtp.
type smtpXOAuth2 struct{ xoauth2 }

func (x smtpXOAuth2) Start(*smtp.ServerInfo) (string, []byte, error) { return "XOAUTH2", x.ir(), nil }
func (x smtpXOAuth2) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte{}, nil
	}
	return nil, nil
}
