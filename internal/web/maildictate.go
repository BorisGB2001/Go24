package web

import (
	"io"
	"net/http"
	"slices"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// maxDictation is the longest piece of «Диктовка» go24 takes: the script sends a phrase at a time (a pause or
// half a minute), so a few megabytes are plenty.
const maxDictation = 8 << 20

// mailDictate is «Диктовка» of the letter being written (static/js/mailvoice.js): the form's «audio» — a phrase
// recorded in the browser — becomes text by Whisper, in the language «lang» (mail.Languages), else of the interface.
func (s *Server) mailDictate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDictation+1<<20)
	f, h, err := r.FormFile("audio")
	if err != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	defer f.Close()
	text, err := s.dictate(r, h.Filename, f, r.FormValue("lang"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// apiMailDictate: POST /api/v1/mail/dictate?lang=ru, the body — a recording (webm, ogg, wav, mp3…) → {"text": …}.
func (s *Server) apiMailDictate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	text, err := s.dictate(r, "dictation.webm", http.MaxBytesReader(w, r.Body, maxDictation), r.URL.Query().Get("lang"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
	return nil
}

// dictate gives the text of a recording; silence gives "".
func (s *Server) dictate(r *http.Request, name string, audio io.Reader, lang string) (string, error) {
	if s.Speech == nil {
		return "", i18n.E("err.speech_off")
	}
	if lang == "" {
		lang, _, _ = strings.Cut(s.lang(r), "-")
	}
	if !slices.Contains(mail.Languages, lang) {
		lang = "" // Whisper finds the language itself
	}
	text, err := s.Speech.Transcribe(r.Context(), name, io.LimitReader(audio, maxDictation), lang)
	if err != nil {
		s.Log.Error("whisper", "err", err)
		return "", i18n.E("err.speech_failed")
	}
	return strings.TrimSpace(text), nil
}
