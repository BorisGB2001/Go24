package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/speech"
)

// TestMailDictate: «Диктовка» sends a phrase to Whisper in the language chosen, else the interface's; a language
// Whisper is not offered falls back to its own detection; no Whisper — the error says so. REST takes the body.
func TestMailDictate(t *testing.T) {
	bundle, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	type call struct{ name, lang, body string }
	heard := make(chan call, 4)
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, h, err := r.FormFile("audio_file")
		if err != nil || r.URL.Path != "/asr" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		heard <- call{h.Filename, r.URL.Query().Get("language"), string(b)}
		io.WriteString(w, " Добрый день, коллеги.\n")
	}))
	defer ws.Close()
	srv := &Server{I18n: bundle, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Speech: speech.New(ws.URL)}
	form := func(lang string) *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("audio", "dictation.webm")
		fw.Write([]byte("OPUS"))
		if lang != "" {
			mw.WriteField("lang", lang)
		}
		mw.Close()
		r := httptest.NewRequest("POST", "/mail/dictate", &buf)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Header.Set("Accept-Language", "ru-RU")
		return r
	}
	dictate := func(r *http.Request) (int, map[string]string) {
		w := httptest.NewRecorder()
		srv.mailDictate(w, r, auth.Session{UserID: "u1"})
		out := map[string]string{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for _, c := range []struct{ lang, want string }{{"", "ru"}, {"en", "en"}, {"xx", ""}} {
		code, out := dictate(form(c.lang))
		if code != http.StatusOK || out["text"] != "Добрый день, коллеги." {
			t.Fatalf("lang %q: %d %v", c.lang, code, out)
		}
		if got := <-heard; got.lang != c.want || got.name != "dictation.webm" || got.body != "OPUS" {
			t.Fatalf("lang %q: Whisper heard %+v", c.lang, got)
		}
	}
	if code, _ := dictate(httptest.NewRequest("POST", "/mail/dictate", strings.NewReader("x"))); code != http.StatusUnprocessableEntity {
		t.Fatalf("no recording: %d", code)
	}
	// REST: the body is the recording
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/mail/dictate?lang=de", strings.NewReader("WAVE"))
	if err := srv.apiMailDictate(w, r, apikeys.Principal{UserID: "u1"}); err != nil || !strings.Contains(w.Body.String(), "Добрый день") {
		t.Fatalf("REST: %v %s", err, w.Body)
	}
	if got := <-heard; got.lang != "de" || got.body != "WAVE" {
		t.Fatalf("REST: Whisper heard %+v", got)
	}
	srv.Speech = nil
	if code, out := dictate(form("")); code != http.StatusUnprocessableEntity || out["error"] != bundle.T("ru-RU", "err.speech_off") {
		t.Fatalf("no Whisper: %d %v", code, out)
	}
}
