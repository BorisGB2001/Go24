package storages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/storage"
)

// TestCloudSignIn: Google Диск, Dropbox and OneDrive are offered once their application is set; signing in sends
// the user to the service with go24's application, the code coming back becomes the token rclone keeps (OneDrive's
// drive found by Microsoft Graph); a code the service refuses connects nothing.
func TestCloudSignIn(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('a', 'A') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}

	// the service: the token for the code "good", the drive of the user at Graph
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good" || r.Form.Get("redirect_uri") != "https://go24.test/back" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","token_type":"Bearer","expires_in":3600}`))
		case "/me/drive":
			if r.Header.Get("Authorization") != "Bearer AT" {
				http.Error(w, "", 401)
				return
			}
			w.Write([]byte(`{"id":"D1","driveType":"personal","owner":{"user":{"displayName":"Анна"}}}`))
		}
	}))
	defer svc.Close()
	for _, c := range clouds {
		c.auth, c.token = svc.URL+"/auth", svc.URL+"/token"
	}
	oneDriveMe = svc.URL + "/me/drive"
	// rclone: what config/create was told
	var created []map[string]any
	rc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path == "/config/create" {
			created = append(created, in)
		}
		w.Write([]byte(`{}`))
	}))
	defer rc.Close()

	s := NewStore(pool, storage.NewRC(rc.URL, "go24", "x"), nil)
	if got := s.Clouds(); len(got) != 0 {
		t.Fatalf("no applications, yet offered %v", got)
	}
	s.Apps = map[string]App{"gdrive": {"G", "GS"}, "dropbox": {"B", ""}, "onedrive": {"M", "MS"}}
	if got := strings.Join(s.Clouds(), " "); got != "gdrive onedrive" {
		t.Fatalf("offered %q", got)
	}
	if _, err := s.SignInURL("dropbox", "https://go24.test/back", "st"); key(err) != "err.storage_bad_type" {
		t.Fatalf("a service without its application: %v", err)
	}
	to, err := s.SignInURL("gdrive", "https://go24.test/back", "st")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(to)
	q := u.Query()
	if q.Get("client_id") != "G" || q.Get("state") != "st" || q.Get("redirect_uri") != "https://go24.test/back" ||
		q.Get("access_type") != "offline" || !strings.Contains(q.Get("scope"), "drive.file") {
		t.Fatalf("sign-in address: %s", to)
	}

	in := Input{Name: "Мой Google", Type: "gdrive", Root: "go24"}
	if _, err := s.CreateCloud(ctx, uid, false, in, "https://go24.test/back", "bad"); key(err) != "err.storage_signin" || len(created) != 0 {
		t.Fatalf("a refused code: %v, %d remotes", err, len(created))
	}
	shared := in
	shared.Shared = true
	if _, err := s.CreateCloud(ctx, uid, false, shared, "https://go24.test/back", "good"); err != ErrForbidden {
		t.Fatalf("a shared storage by a user: %v", err)
	}
	created = nil
	if _, err := s.CreateCloud(ctx, uid, false, in, "https://go24.test/back", "good"); err != nil {
		t.Fatal(err)
	}
	p := created[0]["parameters"].(map[string]any)
	var tok struct{ Access_token, Refresh_token string }
	_ = json.Unmarshal([]byte(p["token"].(string)), &tok)
	if created[0]["type"] != "drive" || p["client_id"] != "G" || p["client_secret"] != "GS" || p["scope"] != "drive.file" ||
		tok.Access_token != "AT" || tok.Refresh_token != "RT" {
		t.Fatalf("rclone was told %v", created[0])
	}

	created = nil
	if _, err := s.CreateCloud(ctx, uid, false, Input{Name: "OneDrive", Type: "onedrive"}, "https://go24.test/back", "good"); err != nil {
		t.Fatal(err)
	}
	p = created[0]["parameters"].(map[string]any)
	if created[0]["type"] != "onedrive" || p["drive_id"] != "D1" || p["drive_type"] != "personal" {
		t.Fatalf("rclone was told %v", created[0])
	}
	list, _ := s.List(ctx, uid, false)
	if len(list) != 2 || list[1].Summary != "onedrive.live.com · Анна" && list[0].Summary != "onedrive.live.com · Анна" {
		t.Fatalf("storages: %+v", list)
	}
}
