package web

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/speech"
)

// piperWAV is what Piper gives: 16-bit mono 22050 Hz, here half a second of silence.
func piperWAV() []byte {
	var b bytes.Buffer
	n := 22050 / 2 * 2
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+n))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, []uint32{16})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 1})
	binary.Write(&b, binary.LittleEndian, []uint32{22050, 44100})
	binary.Write(&b, binary.LittleEndian, []uint16{2, 16})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(n))
	b.Write(make([]byte, n))
	return b.Bytes()
}

// TestTTS: «Озвучить текст» gives the text spoken by Piper as a WAV Asterisk plays (8 kHz); no text, a text too long,
// Piper failing or no synthesis at all — a message the person reads; the REST gives the voices and the same WAV.
func TestTTS(t *testing.T) {
	var got map[string]any
	piper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		if got["text"] == "сбой" {
			w.WriteHeader(500)
			return
		}
		w.Write(piperWAV())
	}))
	defer piper.Close()
	bundle, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{I18n: bundle, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), TTS: speech.NewSynth(piper.URL)}
	post := func(form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/telephony/tts", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept-Language", "ru")
		w := httptest.NewRecorder()
		s.telTTS(w, r, auth.Session{UserID: "u1"})
		return w
	}
	w := post(url.Values{"text": {"Здравствуйте"}, "voice": {"ru_RU-dmitri-medium"}, "rate": {"1.2"}})
	b := w.Body.Bytes()
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/wav" || len(b) < 44 || binary.LittleEndian.Uint32(b[24:28]) != 8000 ||
		got["voice"] != "ru_RU-dmitri-medium" {
		t.Fatalf("%d %q %v", w.Code, w.Header(), got)
	}
	for text, want := range map[string]string{"": "1000", strings.Repeat("я", 1001): "1000", "сбой": "недоступен"} {
		if w := post(url.Values{"text": {text}}); w.Code != 422 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%q: %d %s", text[:min(len(text), 10)], w.Code, w.Body)
		}
	}

	api := func(method, body string) (*httptest.ResponseRecorder, error) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v1/telephony/tts", strings.NewReader(body))
		if method == "GET" {
			return w, s.apiTelVoices(w, r, apikeys.Principal{})
		}
		return w, s.apiTelSpeak(w, r, apikeys.Principal{})
	}
	if w, err := api("GET", ""); err != nil || !strings.Contains(w.Body.String(), "ru_RU-irina-medium") {
		t.Fatalf("voices: %v %s", err, w.Body)
	}
	if w, err := api("POST", `{"text":"Hello","voice":"en_US-amy-medium"}`); err != nil || w.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("speak: %v", err)
	}
	var ie *i18n.Error
	if _, err := api("POST", `{"text":""}`); !errors.As(err, &ie) || ie.Key != "tel.tts.err_text" {
		t.Fatalf("no text: %v", err)
	}

	s.TTS = nil
	if w := post(url.Values{"text": {"Здравствуйте"}}); w.Code != 422 || !strings.Contains(w.Body.String(), "GO24_TTS_URL") {
		t.Fatalf("no synthesis: %d %s", w.Code, w.Body)
	}
}
