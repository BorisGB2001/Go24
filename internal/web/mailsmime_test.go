package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailSMIMEHTTP drives «Настройки → Безопасность» over HTTP: a certificate file is taken (garbage is not),
// listed, offered in the editor as «Подписать»; the defaults are kept; nobody else deletes it.
func TestMailSMIMEHTTP(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := i18n.Load("")
	must(err)
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err = srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, body io.Reader, ctype string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}
	upload := func(data []byte) string {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		w.WriteField("password", "")
		fw, _ := w.CreateFormFile("file", "anna.pem")
		fw.Write(data)
		w.Close()
		_, _, loc := do(anna, "POST", "/mail/certs", &b, w.FormDataContentType())
		return loc
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(5), Subject: pkix.Name{CommonName: "Анна"}, EmailAddresses: []string{"anna@go24.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	k8, _ := x509.MarshalPKCS8PrivateKey(key)
	file := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k8})...)

	if code, page, _ := do(anna, "GET", "/mail/settings?tab=security", nil, ""); code != 200 || !strings.Contains(page, `action="/mail/certs"`) {
		t.Fatalf("security tab: %d", code)
	}
	if loc := upload([]byte("garbage")); !strings.Contains(loc, "msg=") || strings.Contains(loc, "anna@go24.test") {
		t.Fatalf("garbage: %s", loc)
	}
	if loc := upload(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); !strings.Contains(loc, "закрытого ключа") {
		t.Fatalf("no key: %s", loc)
	}
	if loc := upload(file); !strings.Contains(loc, "anna@go24.test") {
		t.Fatalf("upload: %s", loc)
	}
	certs, _ := srv.Mail.Certs(ctx, anna)
	if len(certs) != 1 {
		t.Fatalf("certs: %+v", certs)
	}
	if _, page, _ := do(anna, "GET", "/mail/settings?tab=security", nil, ""); !strings.Contains(page, certs[0].SHA1) {
		t.Fatal("the certificate is not listed")
	}
	if code, _, _ := do(anna, "POST", "/mail/settings/security", strings.NewReader("sign=1"), "application/x-www-form-urlencoded"); code != 303 {
		t.Fatalf("defaults: %d", code)
	}
	if code, page, _ := do(anna, "GET", "/mail/compose", nil, ""); code != 200 || !strings.Contains(page, `name="sign" value="1" checked`) || strings.Contains(page, `name="encrypt" value="1" checked`) {
		t.Fatalf("the editor: %d %.2000s", code, page)
	}
	do(eve, "POST", "/mail/certs/"+certs[0].ID+"/delete", nil, "")
	if list, _ := srv.Mail.Certs(ctx, anna); len(list) != 1 {
		t.Fatal("someone else's certificate deleted")
	}
	if code, _, _ := do(anna, "POST", "/mail/certs/"+certs[0].ID+"/delete", nil, ""); code != 303 {
		t.Fatalf("delete: %d", code)
	}
	if list, _ := srv.Mail.Certs(ctx, anna); len(list) != 0 {
		t.Fatal("not deleted")
	}
}
