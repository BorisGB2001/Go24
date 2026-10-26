package speech

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTranscribe: the recording goes to /asr as audio_file with the language; the text comes back trimmed.
func TestTranscribe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("audio_file")
		if r.URL.Path != "/asr" || r.URL.Query().Get("language") != "ru" || r.URL.Query().Get("output") != "txt" || r.URL.Query().Get("vad_filter") != "true" || err != nil {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(f)
		_, _ = io.WriteString(w, "  привет, это "+string(b)+"\n")
	}))
	defer srv.Close()
	text, err := New(srv.URL).Transcribe(context.Background(), "voice.ogg", strings.NewReader("голос"), "ru")
	if err != nil || text != "привет, это голос" {
		t.Fatalf("%q %v", text, err)
	}
	if _, err := New(srv.URL).Transcribe(context.Background(), "voice.ogg", strings.NewReader("x"), "en"); err == nil {
		t.Fatal("an error answer passed")
	}
	if New("") != nil {
		t.Fatal("a client without an address")
	}
}
