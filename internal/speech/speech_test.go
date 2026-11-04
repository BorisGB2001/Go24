package speech

import (
	"context"
	"encoding/json"
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

// TestSpeak: the text goes to /synthesize with the voice and the rate as length_scale; a WAV comes back. An unknown voice
// becomes the default one, an empty or too long text never leaves go24.
func TestSpeak(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		if r.URL.Path != "/synthesize" || json.NewDecoder(r.Body).Decode(&got) != nil {
			w.WriteHeader(400)
			return
		}
		if got["text"] == "bad" {
			w.WriteHeader(500)
			return
		}
		_, _ = io.WriteString(w, "RIFF"+strings.Repeat("\x00", 60))
	}))
	defer srv.Close()
	s := NewSynth(srv.URL + "/")
	wav, err := s.Speak(context.Background(), "  Здравствуйте ", "en_US-amy-medium", 2)
	if err != nil || string(wav[:4]) != "RIFF" || got["text"] != "Здравствуйте" || got["voice"] != "en_US-amy-medium" || got["length_scale"] != 0.5 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := s.Speak(context.Background(), "x", "../evil", 9); err != nil || got["voice"] != Voices[0] || got["length_scale"] != 1.0 {
		t.Fatalf("unknown voice or rate: %v %v", got, err)
	}
	got = nil
	if _, err := s.Speak(context.Background(), " ", "", 1); err != ErrText || got != nil {
		t.Fatal("an empty text was sent")
	}
	if _, err := s.Speak(context.Background(), strings.Repeat("я", MaxText+1), "", 1); err != ErrText || got != nil {
		t.Fatal("a too long text was sent")
	}
	if _, err := s.Speak(context.Background(), "bad", "", 1); err == nil {
		t.Fatal("an error answer passed")
	}
	if NewSynth("") != nil {
		t.Fatal("a synthesis without an address")
	}
}
