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

// TestSegments: a long recording asks Whisper for JSON; the pieces come back with their time, empty ones dropped.
func TestSegments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("audio_file")
		if r.URL.Query().Get("output") != "json" || err != nil {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(f)
		_, _ = io.WriteString(w, `{"language":"ru","text":"x","segments":[{"id":1,"start":0.5,"end":3.2,"text":" Добрый день, `+string(b)+`"},`+
			`{"start":3.2,"end":4,"text":"  "},{"start":65,"end":70.1,"text":" Договорились."}]}`)
	}))
	defer srv.Close()
	segs, err := New(srv.URL).Segments(context.Background(), "call.ogg", strings.NewReader("коллеги"), "ru")
	if err != nil || len(segs) != 2 || segs[0].Text != "Добрый день, коллеги" || segs[0].Start != 0.5 || segs[1].Start != 65 || segs[1].Text != "Договорились." {
		t.Fatalf("%+v %v", segs, err)
	}
}
