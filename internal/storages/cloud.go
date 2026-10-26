package storages

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"go24/internal/i18n"
)

// App is go24's application at a cloud service (Google Диск, Dropbox, OneDrive), registered by the administrator:
// its users connect their accounts there by signing in (OAuth), as «Подключить».
type App struct{ ClientID, Secret string }

// cloud is how a cloud service is connected: its OAuth endpoints and scopes, rclone's backend and what rclone is told.
type cloud struct {
	rclone, auth, token, host string
	scopes                    []string
	authParams                []oauth2.AuthCodeOption
	params                    map[string]string // besides the application and the token; config_* answer rclone's questions
}

// clouds are the cloud services; the addresses are replaced in tests.
var clouds = map[string]*cloud{
	// drive.file: only the files go24 puts there — what a storage needs, and no review of the application by Google
	"gdrive": {rclone: "drive", auth: "https://accounts.google.com/o/oauth2/auth", token: "https://oauth2.googleapis.com/token",
		host: "drive.google.com", scopes: []string{"https://www.googleapis.com/auth/drive.file"},
		authParams: []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")},
		params:     map[string]string{"scope": "drive.file", "config_refresh_token": "false", "config_change_team_drive": "false"}},
	"dropbox": {rclone: "dropbox", auth: "https://www.dropbox.com/oauth2/authorize", token: "https://api.dropboxapi.com/oauth2/token",
		host: "dropbox.com", authParams: []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("token_access_type", "offline")},
		params: map[string]string{"config_refresh_token": "false"}},
	"onedrive": {rclone: "onedrive", auth: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		token: "https://login.microsoftonline.com/common/oauth2/v2.0/token", host: "onedrive.live.com",
		scopes: []string{"Files.ReadWrite", "offline_access"}, params: map[string]string{"config_refresh_token": "false"}},
}

// oneDriveMe is Microsoft Graph's drive of the signed-in user (rclone needs its id and type); replaced in tests.
var oneDriveMe = "https://graph.microsoft.com/v1.0/me/drive"

// IsCloud reports whether a type is connected by signing in to a cloud service.
func IsCloud(t string) bool { return clouds[t] != nil }

// Clouds are the cloud services the administrator has registered go24's application at, in the order of the form.
func (s *Store) Clouds() []string {
	var out []string
	for _, t := range []string{"gdrive", "dropbox", "onedrive"} {
		if a := s.Apps[t]; a.ClientID != "" && a.Secret != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) oauth(t, redirect string) (*oauth2.Config, *cloud, error) {
	c := clouds[t]
	if c == nil || !slices.Contains(s.Clouds(), t) {
		return nil, nil, i18n.E("err.storage_bad_type")
	}
	return &oauth2.Config{ClientID: s.Apps[t].ClientID, ClientSecret: s.Apps[t].Secret, RedirectURL: redirect, Scopes: c.scopes,
		Endpoint: oauth2.Endpoint{AuthURL: c.auth, TokenURL: c.token}}, c, nil
}

// SignInURL is the page of the cloud service where the user lets go24 in; it comes back to redirect with the code
// and state.
func (s *Store) SignInURL(t, redirect, state string) (string, error) {
	cfg, c, err := s.oauth(t, redirect)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, c.authParams...), nil
}

// CreateCloud connects the account the user has signed in to: the code is exchanged for a token rclone keeps (and
// refreshes itself), then the storage is checked and saved as Create does.
func (s *Store) CreateCloud(ctx context.Context, userID string, admin bool, in Input, redirect, code string) (string, error) {
	if s.rc == nil {
		return "", i18n.E("err.storages_disabled")
	}
	cfg, c, err := s.oauth(in.Type, redirect)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := cfg.Exchange(ctx, code)
	if err != nil || tok.RefreshToken == "" {
		return "", i18n.E("err.storage_signin")
	}
	raw, _ := json.Marshal(oauth2.Token{AccessToken: tok.AccessToken, TokenType: tok.TokenType, RefreshToken: tok.RefreshToken, Expiry: tok.Expiry})
	params := map[string]string{"client_id": cfg.ClientID, "client_secret": cfg.ClientSecret, "token": string(raw)}
	for k, v := range c.params {
		params[k] = v
	}
	summary := c.host
	if in.Type == "onedrive" {
		var me struct {
			ID, DriveType string
			Owner         struct{ User struct{ DisplayName string } }
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", oneDriveMe, nil)
		res, err := cfg.Client(ctx, tok).Do(req)
		if err == nil {
			err = json.NewDecoder(res.Body).Decode(&me)
			res.Body.Close()
		}
		if err != nil || me.ID == "" {
			return "", i18n.E("err.storage_signin")
		}
		params["drive_id"], params["drive_type"] = me.ID, strings.ToLower(me.DriveType)
		if n := me.Owner.User.DisplayName; n != "" {
			summary += " · " + n
		}
	}
	return s.create(ctx, userID, admin, in, c.rclone, params, summary)
}
