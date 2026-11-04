package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/speech"
	"go24/internal/telephony"
)

// «Озвучить текст» (GO24DEV-1002): a sound of telephony — the greeting of a line, of a voice menu, the music of a queue,
// the sound of «Информационный звонок» — spoken by the Piper voices from a text instead of an uploaded WAV. The page
// listens to it and puts it into the form's file field, so it is saved as an uploaded file.
//
// POST /telephony/tts {text, voice, rate}          — the WAV as Asterisk plays it (8 kHz), for any employee
// GET  /api/v1/telephony/tts                       — {"voices": […]}
// POST /api/v1/telephony/tts {text, voice, rate}   — the WAV

// ttsOn: the server has speech synthesis (ttsVoices of the pages).
var ttsOn atomic.Bool

// speak is the text read by the voice, made a sound of telephony.
func (s *Server) speak(ctx context.Context, text, voice string, rate float64) ([]byte, error) {
	if s.TTS == nil {
		return nil, i18n.E("tel.tts.off")
	}
	wav, err := s.TTS.Speak(ctx, text, voice, rate)
	if errors.Is(err, speech.ErrText) {
		return nil, i18n.E("tel.tts.err_text", speech.MaxText)
	}
	if err != nil {
		s.Log.Error("tts", "err", err)
		return nil, i18n.E("tel.tts.err_down")
	}
	return telephony.Greeting(wav)
}

func writeWAV(w http.ResponseWriter, wav []byte) {
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(wav)
}

func (s *Server) telTTS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	rate, _ := strconv.ParseFloat(r.FormValue("rate"), 64)
	wav, err := s.speak(r.Context(), r.FormValue("text"), r.FormValue("voice"), rate)
	if err != nil {
		http.Error(w, s.errText(r, err), http.StatusUnprocessableEntity)
		return
	}
	writeWAV(w, wav)
}

func (s *Server) apiTelVoices(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.TTS == nil {
		return i18n.E("tel.tts.off")
	}
	writeJSON(w, http.StatusOK, map[string]any{"voices": speech.Voices, "max_text": speech.MaxText})
	return nil
}

func (s *Server) apiTelSpeak(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Text  string  `json:"text"`
		Voice string  `json:"voice"`
		Rate  float64 `json:"rate"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	wav, err := s.speak(r.Context(), in.Text, in.Voice, in.Rate)
	if err != nil {
		return err
	}
	writeWAV(w, wav)
	return nil
}
