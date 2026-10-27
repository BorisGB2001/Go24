// Package config loads go24 settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Addr    string // listen address, e.g. ":8080"
	BaseURL string // public URL of go24, used for the OIDC redirect

	DatabaseURL   string
	SessionSecret string

	// Authentik (OIDC provider)
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string

	// File storage: S3 API served by rclone (`rclone serve s3`) in front of any cloud
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	S3UseSSL    bool

	// Optional directory with theme.css (and its assets) overriding the default look
	ThemeDir string

	// Optional directory with extra locales (de-DE.json…) or overrides of built-in ones
	LocalesDir string

	// Authentik groups whose members are go24 administrators (GO24_ADMIN_GROUPS, comma-separated)
	AdminGroups []string
	// Authentik groups whose members are guests of collabs, not employees (GO24_GUEST_GROUPS, comma-separated)
	GuestGroups []string

	// rclone rcd for user-connected storages; empty URL disables the feature
	RcloneRCURL, RcloneRCUser, RcloneRCPass string
	// Authentik API (service account go24-sync): fill missing user data from go24 employee cards
	AuthentikURL, AuthentikToken string
	// Matrix messenger (Synapse): internal URL, server name, the go24 bot token, Element URL for links
	MatrixURL, MatrixServerName, MatrixToken, MatrixElementURL string
	// Calls (LiveKit, own server or LiveKit Cloud): the address browsers connect to, the API key and secret go24
	// signs room tokens with; CallURL is go24 over https (camera and microphone need a secure page), "" — BaseURL
	LiveKitURL, LiveKitKey, LiveKitSecret, CallURL string
	LiveKitAPIURL                                  string // LiveKit's own API for go24 (who is in a call), e.g. http://livekit:7880
	RecordDir                                      string // the folder LiveKit Egress records calls into, shared with go24; "" — no recording
	// Telephony (own Asterisk): its REST interface (ARI) and password, the folder of the accounts' configuration
	// shared with it, the server SIP phones register at, the folder it records the conversations into; the URL or the
	// folder of the accounts empty — no telephony
	AsteriskURL, AsteriskSecret, TelephonyDir, SIPHost, PhoneRecords string
	// Speech recognition (Whisper) for the text of voice messages; "" — none
	WhisperURL string
	// Documents online (ONLYOFFICE Document Server): its address and JWT secret as go24 reaches it, and go24 as it
	// reaches go24 (for the file and the callback); any of them empty — off
	OfficeURL, OfficeSecret, OfficeSelf string
	// «Хранилища» in the cloud services: go24's application at Google, Dropbox, Microsoft (client ID and secret);
	// empty — the service is not offered
	GDriveID, GDriveSecret, DropboxID, DropboxSecret, OneDriveID, OneDriveSecret string
	// «Почта» connected by signing in: go24's application at Google, Microsoft, Яндекс; empty — Google's and
	// Microsoft's of «Хранилища», else the service is not offered
	MailGoogleID, MailGoogleSecret, MailMicrosoftID, MailMicrosoftSecret, MailYandexID, MailYandexSecret string
	// Backups («Настройки → Резервное копирование»): the folder go24-worker writes copies into, the snapshot of the
	// configuration (deploy/ as at the start, made by backup-init) and the backups' folder as the server names it
	// (shown on the page), the data folder of Synapse (its media); BackupDir empty — no backups
	BackupDir, BackupConfigDir, BackupHostDir, BackupMatrixDir string
}

func Load() (Config, error) {
	c := Config{
		Addr:             env("GO24_ADDR", ":8080"),
		BaseURL:          strings.TrimRight(env("GO24_BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:      env("GO24_DATABASE_URL", ""),
		SessionSecret:    env("GO24_SESSION_SECRET", ""),
		OIDCIssuer:       env("GO24_OIDC_ISSUER", ""),
		OIDCClientID:     env("GO24_OIDC_CLIENT_ID", ""),
		OIDCClientSecret: env("GO24_OIDC_CLIENT_SECRET", ""),
		S3Endpoint:       env("GO24_S3_ENDPOINT", "rclone:8333"),
		S3AccessKey:      env("GO24_S3_ACCESS_KEY", ""),
		S3SecretKey:      env("GO24_S3_SECRET_KEY", ""),
		S3Bucket:         env("GO24_S3_BUCKET", "go24"),
		S3UseSSL:         env("GO24_S3_USE_SSL", "false") == "true",
		ThemeDir:         env("GO24_THEME_DIR", ""),
		LocalesDir:       env("GO24_LOCALES_DIR", ""),
		AdminGroups:      splitList(env("GO24_ADMIN_GROUPS", "go24-admins")),
		GuestGroups:      splitList(env("GO24_GUEST_GROUPS", "go24-guests")),
		RcloneRCURL:      env("GO24_RCLONE_RC_URL", ""),
		RcloneRCUser:     env("GO24_RCLONE_RC_USER", "go24"),
		RcloneRCPass:     env("GO24_RCLONE_RC_PASS", ""),
		AuthentikURL:     strings.TrimRight(env("GO24_AUTHENTIK_URL", ""), "/"),
		AuthentikToken:   env("GO24_AUTHENTIK_TOKEN", ""),
		MatrixURL:        env("GO24_MATRIX_URL", ""),
		MatrixServerName: env("GO24_MATRIX_SERVER_NAME", ""),
		MatrixToken:      env("GO24_MATRIX_TOKEN", ""),
		MatrixElementURL: strings.TrimRight(env("GO24_MATRIX_ELEMENT_URL", ""), "/"),
		LiveKitURL:       env("GO24_LIVEKIT_URL", ""),
		LiveKitKey:       env("GO24_LIVEKIT_KEY", ""),
		LiveKitSecret:    env("GO24_LIVEKIT_SECRET", ""),
		LiveKitAPIURL:    env("GO24_LIVEKIT_API_URL", ""),
		RecordDir:        env("GO24_RECORD_DIR", ""),
		BackupDir:        env("GO24_BACKUP_DIR", ""),
		BackupConfigDir:  env("GO24_BACKUP_CONFIG_DIR", ""),
		BackupHostDir:    env("GO24_BACKUP_HOST_DIR", ""),
		BackupMatrixDir:  env("GO24_BACKUP_MATRIX_DIR", ""),
		CallURL:          strings.TrimRight(env("GO24_CALL_URL", ""), "/"),
		AsteriskURL:      env("GO24_ASTERISK_URL", ""),
		AsteriskSecret:   env("GO24_ASTERISK_SECRET", ""),
		TelephonyDir:     env("GO24_TELEPHONY_DIR", ""),
		SIPHost:          env("GO24_SIP_HOST", ""),
		PhoneRecords:     env("GO24_PHONE_RECORDS", ""),
		WhisperURL:       env("GO24_WHISPER_URL", ""),
		OfficeURL:        env("GO24_OFFICE_URL", ""),
		OfficeSecret:     env("GO24_OFFICE_SECRET", ""),
		OfficeSelf:       env("GO24_OFFICE_SELF_URL", ""),
		GDriveID:         env("GO24_GDRIVE_CLIENT_ID", ""),
		GDriveSecret:     env("GO24_GDRIVE_CLIENT_SECRET", ""),
		DropboxID:        env("GO24_DROPBOX_CLIENT_ID", ""),
		DropboxSecret:    env("GO24_DROPBOX_CLIENT_SECRET", ""),
		OneDriveID:       env("GO24_ONEDRIVE_CLIENT_ID", ""),
		OneDriveSecret:   env("GO24_ONEDRIVE_CLIENT_SECRET", ""),
		// «Почта» signed in to Google, Microsoft, Яндекс
		MailGoogleID:        env("GO24_MAIL_GOOGLE_CLIENT_ID", ""),
		MailGoogleSecret:    env("GO24_MAIL_GOOGLE_CLIENT_SECRET", ""),
		MailMicrosoftID:     env("GO24_MAIL_MICROSOFT_CLIENT_ID", ""),
		MailMicrosoftSecret: env("GO24_MAIL_MICROSOFT_CLIENT_SECRET", ""),
		MailYandexID:        env("GO24_MAIL_YANDEX_CLIENT_ID", ""),
		MailYandexSecret:    env("GO24_MAIL_YANDEX_CLIENT_SECRET", ""),
	}

	var missing []string
	for name, v := range map[string]string{
		"GO24_DATABASE_URL":   c.DatabaseURL,
		"GO24_SESSION_SECRET": c.SessionSecret,
		"GO24_S3_ACCESS_KEY":  c.S3AccessKey,
		"GO24_S3_SECRET_KEY":  c.S3SecretKey,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}
	if len(c.SessionSecret) < 32 {
		return c, fmt.Errorf("GO24_SESSION_SECRET must be at least 32 characters")
	}
	return c, nil
}

// OIDCEnabled reports whether Authentik login is configured.
func (c Config) OIDCEnabled() bool {
	return c.OIDCIssuer != "" && c.OIDCClientID != "" && c.OIDCClientSecret != ""
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
