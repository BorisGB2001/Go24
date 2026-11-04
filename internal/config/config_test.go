package config

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// allEnvKeys lists every environment variable parsed by Load.
var allEnvKeys = []string{
	"GO24_ADDR",
	"GO24_BASE_URL",
	"GO24_DATABASE_URL",
	"GO24_SESSION_SECRET",
	"GO24_OIDC_ISSUER",
	"GO24_OIDC_CLIENT_ID",
	"GO24_OIDC_CLIENT_SECRET",
	"GO24_S3_ENDPOINT",
	"GO24_S3_ACCESS_KEY",
	"GO24_S3_SECRET_KEY",
	"GO24_S3_BUCKET",
	"GO24_S3_USE_SSL",
	"GO24_THEME_DIR",
	"GO24_LOCALES_DIR",
	"GO24_ADMIN_GROUPS",
	"GO24_GUEST_GROUPS",
	"GO24_RCLONE_RC_URL",
	"GO24_RCLONE_RC_USER",
	"GO24_RCLONE_RC_PASS",
	"GO24_AUTHENTIK_URL",
	"GO24_AUTHENTIK_TOKEN",
	"GO24_MATRIX_URL",
	"GO24_MATRIX_SERVER_NAME",
	"GO24_MATRIX_TOKEN",
	"GO24_MATRIX_ELEMENT_URL",
	"GO24_LIVEKIT_URL",
	"GO24_LIVEKIT_KEY",
	"GO24_LIVEKIT_SECRET",
	"GO24_LIVEKIT_API_URL",
	"GO24_RECORD_DIR",
	"GO24_BACKUP_DIR",
	"GO24_BACKUP_CONFIG_DIR",
	"GO24_BACKUP_HOST_DIR",
	"GO24_BACKUP_MATRIX_DIR",
	"GO24_CALL_URL",
	"GO24_ASTERISK_URL",
	"GO24_ASTERISK_SECRET",
	"GO24_TELEPHONY_DIR",
	"GO24_SIP_HOST",
	"GO24_PHONE_RECORDS",
	"GO24_WHISPER_URL",
	"GO24_TTS_URL",
	"GO24_OFFICE_URL",
	"GO24_OFFICE_SECRET",
	"GO24_OFFICE_SELF_URL",
	"GO24_GDRIVE_CLIENT_ID",
	"GO24_GDRIVE_CLIENT_SECRET",
	"GO24_DROPBOX_CLIENT_ID",
	"GO24_DROPBOX_CLIENT_SECRET",
	"GO24_ONEDRIVE_CLIENT_ID",
	"GO24_ONEDRIVE_CLIENT_SECRET",
	"GO24_MAIL_GOOGLE_CLIENT_ID",
	"GO24_MAIL_GOOGLE_CLIENT_SECRET",
	"GO24_MAIL_MICROSOFT_CLIENT_ID",
	"GO24_MAIL_MICROSOFT_CLIENT_SECRET",
	"GO24_MAIL_YANDEX_CLIENT_ID",
	"GO24_MAIL_YANDEX_CLIENT_SECRET",
}

// clearEnv resets all GO24 environment variables to avoid ambient state in tests.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, env := range os.Environ() {
		if k, _, ok := strings.Cut(env, "="); ok && strings.HasPrefix(k, "GO24_") {
			t.Setenv(k, "")
		}
	}
	for _, k := range allEnvKeys {
		t.Setenv(k, "")
	}
}

// setRequired sets the minimal valid set of required environment variables.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("GO24_DATABASE_URL", "postgres://user:pass@localhost:5432/go24_test")
	t.Setenv("GO24_SESSION_SECRET", "01234567890123456789012345678901") // 32 characters
	t.Setenv("GO24_S3_ACCESS_KEY", "minioadmin")
	t.Setenv("GO24_S3_SECRET_KEY", "minioadmin")
}

// TestEnv: fallback to default on unset or empty environment variables, and returning set values.
func TestEnv(t *testing.T) {
	const key = "GO24_TEST_ENV_HELPER"

	t.Setenv(key, "")
	if got := env(key, "default"); got != "default" {
		t.Errorf("env with empty value = %q, want default", got)
	}

	t.Setenv(key, "custom")
	if got := env(key, "default"); got != "custom" {
		t.Errorf("env with set value = %q, want custom", got)
	}

	if got := env("GO24_NONEXISTENT_KEY_12345", "fallback"); got != "fallback" {
		t.Errorf("env with unset key = %q, want fallback", got)
	}
}

// TestSplitList: comma-separated list parsing, trimming whitespace and ignoring empty elements.
func TestSplitList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",", nil},
		{",,,", nil},
		{" , , ", nil},
		{"admins", []string{"admins"}},
		{"  admins  ", []string{"admins"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"a,,b, ,c,", []string{"a", "b", "c"}},
		{"admin-1, admin-2, guest-user", []string{"admin-1", "admin-2", "guest-user"}},
	}
	for _, c := range cases {
		got := splitList(c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("splitList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestOIDCEnabled: Authentik login is considered enabled only when issuer, client ID, and secret are all non-empty.
func TestOIDCEnabled(t *testing.T) {
	cases := []struct {
		issuer, clientID, clientSecret string
		want                           bool
	}{
		{"", "", "", false},
		{"https://auth.example.com", "", "", false},
		{"", "client-id", "", false},
		{"", "", "client-secret", false},
		{"https://auth.example.com", "client-id", "", false},
		{"https://auth.example.com", "", "client-secret", false},
		{"", "client-id", "client-secret", false},
		{"https://auth.example.com", "client-id", "client-secret", true},
	}
	for _, c := range cases {
		cfg := Config{
			OIDCIssuer:       c.issuer,
			OIDCClientID:     c.clientID,
			OIDCClientSecret: c.clientSecret,
		}
		if got := cfg.OIDCEnabled(); got != c.want {
			t.Errorf("Config{Issuer:%q, ClientID:%q, ClientSecret:%q}.OIDCEnabled() = %v, want %v",
				c.issuer, c.clientID, c.clientSecret, got, c.want)
		}
	}
}

// TestLoad_MissingRequired: missing required variables produce an error listing each missing variable.
func TestLoad_MissingRequired(t *testing.T) {
	t.Run("all missing", func(t *testing.T) {
		clearEnv(t)
		_, err := Load()
		if err == nil {
			t.Fatal("Load() should fail when required env vars are missing")
		}
		errMsg := err.Error()
		if !strings.Contains(errMsg, "missing required env:") {
			t.Errorf("error %q should contain 'missing required env:'", errMsg)
		}
		for _, name := range []string{"GO24_DATABASE_URL", "GO24_SESSION_SECRET", "GO24_S3_ACCESS_KEY", "GO24_S3_SECRET_KEY"} {
			if !strings.Contains(errMsg, name) {
				t.Errorf("error %q should mention missing variable %s", errMsg, name)
			}
		}
	})

	requiredVars := []string{
		"GO24_DATABASE_URL",
		"GO24_SESSION_SECRET",
		"GO24_S3_ACCESS_KEY",
		"GO24_S3_SECRET_KEY",
	}

	for _, omit := range requiredVars {
		t.Run("missing "+omit, func(t *testing.T) {
			clearEnv(t)
			setRequired(t)
			t.Setenv(omit, "")

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() should fail without %s", omit)
			}
			want := "missing required env: " + omit
			if err.Error() != want {
				t.Errorf("Load() err = %q, want %q", err.Error(), want)
			}
		})
	}
}

// TestLoad_SessionSecretLength: session secret must be at least 32 characters.
func TestLoad_SessionSecretLength(t *testing.T) {
	cases := []struct {
		secret  string
		wantErr bool
	}{
		{"x", true},
		{"short", true},
		{"1234567890123456789012345678901", true},                                   // 31 characters
		{"12345678901234567890123456789012", false},                                 // 32 characters
		{"123456789012345678901234567890123", false},                                // 33 characters
		{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", false}, // 64 characters
	}

	for _, c := range cases {
		t.Run(c.secret, func(t *testing.T) {
			clearEnv(t)
			setRequired(t)
			t.Setenv("GO24_SESSION_SECRET", c.secret)

			cfg, err := Load()
			if c.wantErr {
				if err == nil {
					t.Fatalf("Load() with secret len %d should fail", len(c.secret))
				}
				const want = "GO24_SESSION_SECRET must be at least 32 characters"
				if err.Error() != want {
					t.Errorf("Load() err = %q, want %q", err.Error(), want)
				}
			} else {
				if err != nil {
					t.Fatalf("Load() with secret len %d failed: %v", len(c.secret), err)
				}
				if cfg.SessionSecret != c.secret {
					t.Errorf("cfg.SessionSecret = %q, want %q", cfg.SessionSecret, c.secret)
				}
			}
		})
	}
}

// TestLoad_Defaults: minimal configuration populates expected defaults.
func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want http://localhost:8080", cfg.BaseURL)
	}
	if cfg.DatabaseURL != "postgres://user:pass@localhost:5432/go24_test" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.SessionSecret != "01234567890123456789012345678901" {
		t.Errorf("SessionSecret = %q", cfg.SessionSecret)
	}
	if cfg.S3AccessKey != "minioadmin" || cfg.S3SecretKey != "minioadmin" {
		t.Errorf("S3 keys = %q, %q", cfg.S3AccessKey, cfg.S3SecretKey)
	}
	if cfg.S3Endpoint != "rclone:8333" {
		t.Errorf("S3Endpoint = %q, want rclone:8333", cfg.S3Endpoint)
	}
	if cfg.S3Bucket != "go24" {
		t.Errorf("S3Bucket = %q, want go24", cfg.S3Bucket)
	}
	if cfg.S3UseSSL != false {
		t.Errorf("S3UseSSL = %v, want false", cfg.S3UseSSL)
	}
	if !slices.Equal(cfg.AdminGroups, []string{"go24-admins"}) {
		t.Errorf("AdminGroups = %v, want [go24-admins]", cfg.AdminGroups)
	}
	if !slices.Equal(cfg.GuestGroups, []string{"go24-guests"}) {
		t.Errorf("GuestGroups = %v, want [go24-guests]", cfg.GuestGroups)
	}
	if cfg.RcloneRCUser != "go24" {
		t.Errorf("RcloneRCUser = %q, want go24", cfg.RcloneRCUser)
	}

	// Verify all optional fields are zero-valued
	if cfg.OIDCIssuer != "" || cfg.OIDCClientID != "" || cfg.OIDCClientSecret != "" {
		t.Errorf("OIDC fields should be empty by default: %+v", cfg)
	}
	if cfg.OIDCEnabled() {
		t.Errorf("OIDCEnabled() = true, want false by default")
	}
	if cfg.ThemeDir != "" || cfg.LocalesDir != "" {
		t.Errorf("ThemeDir/LocalesDir should be empty by default")
	}
	if cfg.RcloneRCURL != "" || cfg.RcloneRCPass != "" {
		t.Errorf("RcloneRC fields should be empty by default")
	}
	if cfg.AuthentikURL != "" || cfg.AuthentikToken != "" {
		t.Errorf("Authentik fields should be empty by default")
	}
	if cfg.MatrixURL != "" || cfg.MatrixServerName != "" || cfg.MatrixToken != "" || cfg.MatrixElementURL != "" {
		t.Errorf("Matrix fields should be empty by default")
	}
	if cfg.LiveKitURL != "" || cfg.LiveKitKey != "" || cfg.LiveKitSecret != "" || cfg.LiveKitAPIURL != "" || cfg.CallURL != "" || cfg.RecordDir != "" {
		t.Errorf("LiveKit/Calls fields should be empty by default")
	}
	if cfg.AsteriskURL != "" || cfg.AsteriskSecret != "" || cfg.TelephonyDir != "" || cfg.SIPHost != "" || cfg.PhoneRecords != "" {
		t.Errorf("Telephony fields should be empty by default")
	}
	if cfg.WhisperURL != "" {
		t.Errorf("WhisperURL should be empty by default")
	}
	if cfg.TTSURL != "" {
		t.Errorf("TTSURL should be empty by default")
	}
	if cfg.OfficeURL != "" || cfg.OfficeSecret != "" || cfg.OfficeSelf != "" {
		t.Errorf("Office fields should be empty by default")
	}
	if cfg.GDriveID != "" || cfg.GDriveSecret != "" || cfg.DropboxID != "" || cfg.DropboxSecret != "" || cfg.OneDriveID != "" || cfg.OneDriveSecret != "" {
		t.Errorf("Storage OAuth fields should be empty by default")
	}
	if cfg.MailGoogleID != "" || cfg.MailGoogleSecret != "" || cfg.MailMicrosoftID != "" || cfg.MailMicrosoftSecret != "" || cfg.MailYandexID != "" || cfg.MailYandexSecret != "" {
		t.Errorf("Mail OAuth fields should be empty by default")
	}
	if cfg.BackupDir != "" || cfg.BackupConfigDir != "" || cfg.BackupHostDir != "" || cfg.BackupMatrixDir != "" {
		t.Errorf("Backup fields should be empty by default")
	}
}

// TestLoad_FullCustom: all 56 environment variables populate the corresponding Config fields.
func TestLoad_FullCustom(t *testing.T) {
	clearEnv(t)

	envMap := map[string]string{
		"GO24_ADDR":                         ":9090",
		"GO24_BASE_URL":                     "https://crm.example.com",
		"GO24_DATABASE_URL":                 "postgres://app:secret@db.internal:5432/go24_prod",
		"GO24_SESSION_SECRET":               "abcdefabcdefabcdefabcdefabcdefab", // 32 chars
		"GO24_OIDC_ISSUER":                  "https://auth.example.com/application/o/go24/",
		"GO24_OIDC_CLIENT_ID":               "oidc-client-id",
		"GO24_OIDC_CLIENT_SECRET":           "oidc-client-secret",
		"GO24_S3_ENDPOINT":                  "s3.example.com:9000",
		"GO24_S3_ACCESS_KEY":                "custom-s3-key",
		"GO24_S3_SECRET_KEY":                "custom-s3-secret",
		"GO24_S3_BUCKET":                    "custom-bucket",
		"GO24_S3_USE_SSL":                   "true",
		"GO24_THEME_DIR":                    "/var/go24/themes",
		"GO24_LOCALES_DIR":                  "/var/go24/locales",
		"GO24_ADMIN_GROUPS":                 "custom-admins, superusers",
		"GO24_GUEST_GROUPS":                 "custom-guests, external",
		"GO24_RCLONE_RC_URL":                "http://rclone.internal:5572",
		"GO24_RCLONE_RC_USER":               "rclone-custom-user",
		"GO24_RCLONE_RC_PASS":               "rclone-custom-pass",
		"GO24_AUTHENTIK_URL":                "https://auth.example.com",
		"GO24_AUTHENTIK_TOKEN":              "authentik-api-token",
		"GO24_MATRIX_URL":                   "http://synapse.internal:8008",
		"GO24_MATRIX_SERVER_NAME":           "example.com",
		"GO24_MATRIX_TOKEN":                 "matrix-bot-secret",
		"GO24_MATRIX_ELEMENT_URL":           "https://chat.example.com",
		"GO24_LIVEKIT_URL":                  "wss://livekit.example.com",
		"GO24_LIVEKIT_KEY":                  "lk-key-123",
		"GO24_LIVEKIT_SECRET":               "lk-secret-456",
		"GO24_LIVEKIT_API_URL":              "http://livekit.internal:7880",
		"GO24_RECORD_DIR":                   "/var/recordings",
		"GO24_BACKUP_DIR":                   "/var/backups",
		"GO24_BACKUP_CONFIG_DIR":            "/var/backup-config",
		"GO24_BACKUP_HOST_DIR":              "/mnt/backups-host",
		"GO24_BACKUP_MATRIX_DIR":            "/var/matrix-data",
		"GO24_CALL_URL":                     "https://calls.example.com",
		"GO24_ASTERISK_URL":                 "http://asterisk.internal:8088",
		"GO24_ASTERISK_SECRET":              "ari-password",
		"GO24_TELEPHONY_DIR":                "/etc/asterisk",
		"GO24_SIP_HOST":                     "sip.example.com",
		"GO24_PHONE_RECORDS":                "/var/spool/asterisk/records",
		"GO24_WHISPER_URL":                  "http://whisper.internal:9000",
		"GO24_TTS_URL":                      "http://piper.internal:5000",
		"GO24_OFFICE_URL":                   "http://documentserver.internal",
		"GO24_OFFICE_SECRET":                "jwt-documents-secret",
		"GO24_OFFICE_SELF_URL":              "http://go24.internal:8080",
		"GO24_GDRIVE_CLIENT_ID":             "gdrive-id",
		"GO24_GDRIVE_CLIENT_SECRET":         "gdrive-secret",
		"GO24_DROPBOX_CLIENT_ID":            "dropbox-id",
		"GO24_DROPBOX_CLIENT_SECRET":        "dropbox-secret",
		"GO24_ONEDRIVE_CLIENT_ID":           "onedrive-id",
		"GO24_ONEDRIVE_CLIENT_SECRET":       "onedrive-secret",
		"GO24_MAIL_GOOGLE_CLIENT_ID":        "mail-google-id",
		"GO24_MAIL_GOOGLE_CLIENT_SECRET":    "mail-google-secret",
		"GO24_MAIL_MICROSOFT_CLIENT_ID":     "mail-ms-id",
		"GO24_MAIL_MICROSOFT_CLIENT_SECRET": "mail-ms-secret",
		"GO24_MAIL_YANDEX_CLIENT_ID":        "mail-yandex-id",
		"GO24_MAIL_YANDEX_CLIENT_SECRET":    "mail-yandex-secret",
	}

	for k, v := range envMap {
		t.Setenv(k, v)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	want := Config{
		Addr:                ":9090",
		BaseURL:             "https://crm.example.com",
		DatabaseURL:         "postgres://app:secret@db.internal:5432/go24_prod",
		SessionSecret:       "abcdefabcdefabcdefabcdefabcdefab",
		OIDCIssuer:          "https://auth.example.com/application/o/go24/",
		OIDCClientID:        "oidc-client-id",
		OIDCClientSecret:    "oidc-client-secret",
		S3Endpoint:          "s3.example.com:9000",
		S3AccessKey:         "custom-s3-key",
		S3SecretKey:         "custom-s3-secret",
		S3Bucket:            "custom-bucket",
		S3UseSSL:            true,
		ThemeDir:            "/var/go24/themes",
		LocalesDir:          "/var/go24/locales",
		AdminGroups:         []string{"custom-admins", "superusers"},
		GuestGroups:         []string{"custom-guests", "external"},
		RcloneRCURL:         "http://rclone.internal:5572",
		RcloneRCUser:        "rclone-custom-user",
		RcloneRCPass:        "rclone-custom-pass",
		AuthentikURL:        "https://auth.example.com",
		AuthentikToken:      "authentik-api-token",
		MatrixURL:           "http://synapse.internal:8008",
		MatrixServerName:    "example.com",
		MatrixToken:         "matrix-bot-secret",
		MatrixElementURL:    "https://chat.example.com",
		LiveKitURL:          "wss://livekit.example.com",
		LiveKitKey:          "lk-key-123",
		LiveKitSecret:       "lk-secret-456",
		LiveKitAPIURL:       "http://livekit.internal:7880",
		RecordDir:           "/var/recordings",
		BackupDir:           "/var/backups",
		BackupConfigDir:     "/var/backup-config",
		BackupHostDir:       "/mnt/backups-host",
		BackupMatrixDir:     "/var/matrix-data",
		CallURL:             "https://calls.example.com",
		AsteriskURL:         "http://asterisk.internal:8088",
		AsteriskSecret:      "ari-password",
		TelephonyDir:        "/etc/asterisk",
		SIPHost:             "sip.example.com",
		PhoneRecords:        "/var/spool/asterisk/records",
		WhisperURL:          "http://whisper.internal:9000",
		TTSURL:              "http://piper.internal:5000",
		OfficeURL:           "http://documentserver.internal",
		OfficeSecret:        "jwt-documents-secret",
		OfficeSelf:          "http://go24.internal:8080",
		GDriveID:            "gdrive-id",
		GDriveSecret:        "gdrive-secret",
		DropboxID:           "dropbox-id",
		DropboxSecret:       "dropbox-secret",
		OneDriveID:          "onedrive-id",
		OneDriveSecret:      "onedrive-secret",
		MailGoogleID:        "mail-google-id",
		MailGoogleSecret:    "mail-google-secret",
		MailMicrosoftID:     "mail-ms-id",
		MailMicrosoftSecret: "mail-ms-secret",
		MailYandexID:        "mail-yandex-id",
		MailYandexSecret:    "mail-yandex-secret",
	}

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("cfg mismatch:\ngot:  %+v\nwant: %+v", cfg, want)
	}
	if !cfg.OIDCEnabled() {
		t.Errorf("OIDCEnabled() = false, want true")
	}
}

// TestLoad_TrimTrailingSlash: URLs with TrimRight have trailing slashes removed.
func TestLoad_TrimTrailingSlash(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	t.Setenv("GO24_BASE_URL", "http://localhost:8080///")
	t.Setenv("GO24_AUTHENTIK_URL", "https://auth.example.com/")
	t.Setenv("GO24_MATRIX_ELEMENT_URL", "https://element.example.com///")
	t.Setenv("GO24_CALL_URL", "https://call.example.com/")
	t.Setenv("GO24_MATRIX_URL", "http://synapse:8008/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want trailing slashes stripped", cfg.BaseURL)
	}
	if cfg.AuthentikURL != "https://auth.example.com" {
		t.Errorf("AuthentikURL = %q, want trailing slash stripped", cfg.AuthentikURL)
	}
	if cfg.MatrixElementURL != "https://element.example.com" {
		t.Errorf("MatrixElementURL = %q, want trailing slashes stripped", cfg.MatrixElementURL)
	}
	if cfg.CallURL != "https://call.example.com" {
		t.Errorf("CallURL = %q, want trailing slash stripped", cfg.CallURL)
	}
	if cfg.MatrixURL != "http://synapse:8008/" {
		t.Errorf("MatrixURL = %q, want trailing slash preserved", cfg.MatrixURL)
	}
}

// TestLoad_S3UseSSL: boolean parsing strictly checks for "true".
func TestLoad_S3UseSSL(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"TRUE", false},
		{"True", false},
		{"1", false},
		{"yes", false},
		{"", false},
	}
	for _, c := range cases {
		clearEnv(t)
		setRequired(t)
		t.Setenv("GO24_S3_USE_SSL", c.val)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() err with GO24_S3_USE_SSL=%q: %v", c.val, err)
		}
		if cfg.S3UseSSL != c.want {
			t.Errorf("GO24_S3_USE_SSL=%q -> S3UseSSL = %v, want %v", c.val, cfg.S3UseSSL, c.want)
		}
	}
}

// TestLoad_GroupsParsing: comma-separated values with surrounding whitespace are cleaned.
func TestLoad_GroupsParsing(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	t.Setenv("GO24_ADMIN_GROUPS", " group1 , group2,group3 ")
	t.Setenv("GO24_GUEST_GROUPS", "guests-only")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	wantAdmin := []string{"group1", "group2", "group3"}
	if !slices.Equal(cfg.AdminGroups, wantAdmin) {
		t.Errorf("AdminGroups = %v, want %v", cfg.AdminGroups, wantAdmin)
	}

	wantGuest := []string{"guests-only"}
	if !slices.Equal(cfg.GuestGroups, wantGuest) {
		t.Errorf("GuestGroups = %v, want %v", cfg.GuestGroups, wantGuest)
	}
}

// TestLoad_GroupsEmptyOverride: explicit whitespace or commas clear the groups.
func TestLoad_GroupsEmptyOverride(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	t.Setenv("GO24_ADMIN_GROUPS", " , ")
	t.Setenv("GO24_GUEST_GROUPS", ",")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if len(cfg.AdminGroups) != 0 {
		t.Errorf("AdminGroups = %v, want empty", cfg.AdminGroups)
	}
	if len(cfg.GuestGroups) != 0 {
		t.Errorf("GuestGroups = %v, want empty", cfg.GuestGroups)
	}
}

// TestLoad_GroupsDefaultWhenEmpty: empty string falls back to the default group.
func TestLoad_GroupsDefaultWhenEmpty(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	t.Setenv("GO24_ADMIN_GROUPS", "")
	t.Setenv("GO24_GUEST_GROUPS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if !slices.Equal(cfg.AdminGroups, []string{"go24-admins"}) {
		t.Errorf("AdminGroups = %v, want default [go24-admins]", cfg.AdminGroups)
	}
	if !slices.Equal(cfg.GuestGroups, []string{"go24-guests"}) {
		t.Errorf("GuestGroups = %v, want default [go24-guests]", cfg.GuestGroups)
	}
}
