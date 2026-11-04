package web

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/openlines"
)

// olTranscribe: the text of the sound file of a dialogue's message — a client's voice message from Telegram, WhatsApp,
// the widget… — made by Whisper on the first request and kept for every operator; only someone who sees the
// dialogue gets it (the access is checked every time, the kept text too).
func (s *Server) olTranscribe(ctx context.Context, userID string, admin bool, msg int64, lang, silent string) (string, error) {
	f, err := s.OpenLines.FileOf(ctx, msg)
	if err == nil {
		var who openlines.Who
		if who, err = s.olWho(ctx, userID, admin); err == nil {
			err = s.OpenLines.See(ctx, who, f.SessionID)
		}
	}
	if err != nil {
		return "", err
	}
	if !openlines.Audio(f.Name, f.Type) {
		return "", i18n.E("err.not_found")
	}
	text, err := s.OpenLines.Transcript(ctx, msg)
	if err != nil || text != "" {
		return text, err
	}
	if s.Speech == nil {
		return "", i18n.E("err.speech_off")
	}
	obj, err := s.Storages.Open(ctx, nil, f.Key)
	if err != nil {
		return "", err
	}
	defer obj.Close()
	if text, err = s.Speech.Transcribe(ctx, f.Name, io.LimitReader(obj, maxVoice), lang); err != nil {
		s.Log.Error("whisper", "err", err)
		return "", i18n.E("err.speech_failed")
	}
	if text = strings.TrimSpace(text); text == "" {
		text = silent
	}
	return text, s.OpenLines.SetTranscript(ctx, msg, text)
}

// openlinesTranscribe: «Текст» under a sound file of the dialogue.
func (s *Server) openlinesTranscribe(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	msg, _ := strconv.ParseInt(r.PathValue("msg"), 10, 64)
	lang, _, _ := strings.Cut(s.lang(r), "-")
	text, err := s.olTranscribe(r.Context(), sess.UserID, sess.Admin, msg, lang, s.tr(r, "im.voice_silent"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// apiOLTranscribe: POST /api/v1/openlines/messages/{msg}/transcribe — {"text"} of the message's sound file.
func (s *Server) apiOLTranscribe(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	msg, _ := strconv.ParseInt(r.PathValue("msg"), 10, 64)
	lang, _, _ := strings.Cut(s.portalLang(), "-")
	text, err := s.olTranscribe(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), msg, lang, s.apiTr(r)("im.voice_silent"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
	return nil
}
