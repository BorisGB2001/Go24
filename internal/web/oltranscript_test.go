package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/openlines"
	"go24/internal/speech"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestOLTranscribeHTTP: a client's voice message plays in the dialogue and in the widget; «Текст» turns it into text
// by Whisper once (kept for every operator and REST), only for someone who sees the dialogue and only of a sound.
func TestOLTranscribeHTTP(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
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
	var op, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('op', 'Оля', 'op@t') RETURNING id`).Scan(&op))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	var heard atomic.Int32
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, h, err := r.FormFile("audio_file")
		if err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		if h.Filename != "voice.ogg" || string(b) != "OggS-voice" {
			http.Error(w, "wrong file", http.StatusBadRequest)
			return
		}
		heard.Add(1)
		io.WriteString(w, " Привезите заказ завтра.\n")
	}))
	defer whisper.Close()
	bundle, err := i18n.Load("")
	must(err)
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	keys := apikeys.NewStore(pool)
	ol := openlines.NewStore(pool, crm.NewStore(pool))
	ol.Tr = func(key string, _ ...any) string { return key }
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: ol.CRM, OpenLines: ol, Storages: storages.NewStore(pool, nil, s3), Keys: keys,
		I18n: bundle, Speech: speech.New(whisper.URL),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wid, err := ol.SaveWidget(ctx, op, openlines.Widget{Name: "Сайт", Active: true, Operators: []string{op}})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(uid string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: uid}))
		return rec.Result().Cookies()[0]
	}
	opCookie, eveCookie := cookie(op), cookie(eve)
	do := func(req *http.Request, want int) []byte {
		t.Helper()
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", req.Method, req.URL.Path, res.StatusCode, want, raw)
		}
		return raw
	}
	api := hs.URL + "/w/" + wid + "/api/"
	req, _ := http.NewRequest("POST", api+"visit", strings.NewReader(`{}`))
	var v struct{ Token string }
	must(json.Unmarshal(do(req, 200), &v))
	send := func(name, data string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write([]byte(data))
		_ = mw.Close()
		req, _ := http.NewRequest("POST", api+"file", &b)
		req.Header.Set("X-Go24-Visitor", v.Token)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		do(req, 200)
	}
	send("voice.ogg", "OggS-voice")
	send("photo.png", "\x89PNG\r\n\x1a\nfake")

	req, _ = http.NewRequest("GET", api+"messages", nil)
	req.Header.Set("X-Go24-Visitor", v.Token)
	var got struct {
		Messages []struct {
			ID   int64
			File *struct{ Audio, Image bool }
		}
	}
	must(json.Unmarshal(do(req, 200), &got))
	if len(got.Messages) != 2 || got.Messages[0].File == nil || !got.Messages[0].File.Audio || got.Messages[1].File.Audio {
		t.Fatalf("the widget's messages = %+v", got)
	}
	voice, photo := strconv.FormatInt(got.Messages[0].ID, 10), strconv.FormatInt(got.Messages[1].ID, 10)
	var sid string
	must(pool.QueryRow(ctx, `SELECT id::text FROM ol_sessions`).Scan(&sid))

	page := func() string {
		req, _ := http.NewRequest("GET", hs.URL+"/openlines?s="+sid, nil)
		req.AddCookie(opCookie)
		return string(do(req, 200))
	}
	if p := page(); !strings.Contains(p, `<audio controls preload="metadata" src="/openlines/file/`+voice+`">`) ||
		!strings.Contains(p, `data-ol-text="/openlines/file/`+voice+`/text"`) || strings.Contains(p, `/openlines/file/`+photo+`/text`) {
		t.Fatal("the dialogue shows no player or no «Текст» of the voice message")
	}
	text := func(msg string, c *http.Cookie, want int) string {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/openlines/file/"+msg+"/text", nil)
		req.Header.Set("Accept", "application/json")
		req.AddCookie(c)
		var out struct{ Text string }
		_ = json.Unmarshal(do(req, want), &out)
		return out.Text
	}
	text(voice, eveCookie, 404) // not her dialogue: no text, Whisper not asked
	text(photo, opCookie, 404)  // not a sound
	if heard.Load() != 0 {
		t.Fatal("Whisper was asked without access or for a picture")
	}
	if got := text(voice, opCookie, 200); got != "Привезите заказ завтра." {
		t.Fatalf("text = %q", got)
	}
	if got := text(voice, opCookie, 200); got != "Привезите заказ завтра." || heard.Load() != 1 {
		t.Fatalf("the kept text = %q, Whisper asked %d times", got, heard.Load())
	}
	if p := page(); !strings.Contains(p, "🎤 Привезите заказ завтра.") || strings.Contains(p, `data-ol-text=`) {
		t.Fatal("the dialogue does not show the kept text")
	}
	// the text is a part of the dialogue for CoPilot, the history by e-mail, a lead or a task
	msgs, err := ol.Messages(ctx, sid, 0)
	must(err)
	if !strings.Contains(msgs[0].Text(), "📎 voice.ogg\n🎤 Привезите заказ завтра.") {
		t.Fatalf("Text() = %q", msgs[0].Text())
	}

	// REST: the kept text in the dialogue's messages and POST …/transcribe
	k, err := keys.Create(ctx, op, "k", []string{"openlines:read"}, time.Hour)
	must(err)
	rest := func(method, path string, want int) []byte {
		req, _ := http.NewRequest(method, hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k)
		return do(req, want)
	}
	if raw := string(rest("GET", "/api/v1/openlines/dialogs/"+sid+"/messages", 200)); !strings.Contains(raw, `"transcript": "Привезите заказ завтра."`) {
		t.Fatalf("REST messages = %.400s", raw)
	}
	if raw := string(rest("POST", "/api/v1/openlines/messages/"+voice+"/transcribe", 200)); !strings.Contains(raw, "Привезите заказ завтра.") {
		t.Fatalf("REST transcribe = %s", raw)
	}
	rest("POST", "/api/v1/openlines/messages/"+photo+"/transcribe", 404)

	srv.Speech = nil // no Whisper: no «Текст», the kept text stays
	if p := page(); !strings.Contains(p, "🎤 Привезите заказ завтра.") {
		t.Fatal("the kept text without Whisper")
	}
}
