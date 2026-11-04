package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"go24/internal/ai"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/openlines"
)

// CoPilot in open lines: «Подвести итоги» — the AI sums the dialogue up (also by itself when a dialogue
// of a line with «CoPilot: итоги диалога» is closed), «Подсказать ответ» — it drafts the operator's next answer.

// olConversation is the dialogue as the AI reads it: the line, the client and the conversation (its last part).
func (s *Server) olConversation(ctx context.Context, x openlines.Session) (string, error) {
	msgs, err := s.OpenLines.Messages(ctx, x.ID, 0)
	if err != nil {
		return "", err
	}
	text := openlines.Conversation(x, msgs)
	if strings.TrimSpace(text) == "" {
		return "", i18n.E("err.ol_copilot_empty")
	}
	if r := []rune(text); len(r) > 30000 {
		text = string(r[len(r)-30000:])
	}
	lang := s.portalLang()
	about := s.I18n.T(lang, "ol.copilot_about", x.Widget, x.Title())
	switch x.Channel {
	case "telegram":
		about += " (Telegram)"
	case "max":
		about += " (MAX)"
	case "vk":
		about += " (VK)"
	case "viber":
		about += " (Viber)"
	case "ok":
		about += " (OK)"
	case "avito":
		about += " (Avito)"
	case "whatsapp":
		about += " (WhatsApp)"
	case "app":
		about += " (" + x.Connector + ")"
	}
	return about + "\n\n" + text, nil
}

// olSummarize has the AI write the summary of the dialogue and keeps it; the client's CRM card gets it in its
// timeline. The AI is that of userID, "" — of the dialogue's operator.
func (s *Server) olSummarize(ctx context.Context, id, userID string) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	x, err := s.OpenLines.Session(ctx, id)
	if err != nil {
		return err
	}
	if userID = cmp.Or(userID, x.OperatorID); userID == "" {
		return ai.ErrNone
	}
	text, err := s.olConversation(ctx, x)
	if err != nil {
		return err
	}
	lang := s.portalLang()
	summary, err := s.AI.Write(ctx, userID, "openlines", s.I18n.T(lang, "ol.copilot_summary_prompt"), text)
	if err != nil {
		return err
	}
	if summary == "" {
		return i18n.E("err.ai_call", "empty")
	}
	if err := s.OpenLines.SetSummary(ctx, x.ID, summary); err != nil {
		return err
	}
	entity, card := "lead", x.LeadID
	if card == "" {
		entity, card = "contact", x.ContactID
	}
	if card != "" && s.CRM != nil && x.Summary == "" {
		body := s.I18n.T(lang, "ol.copilot_crm", x.Widget, x.CreatedAt.Local().Format(s.I18n.T(lang, "format.datetime"))) + "\n" + summary
		if err := s.CRM.AddEvent(ctx, entity, card, userID, "comment", body); err != nil {
			s.Log.Warn("open lines summary crm", "err", err, "dialogue", x.ID)
		}
	}
	return nil
}

// olSuggest has the AI draft the operator's next answer to the client from the conversation and the operator's quick
// answers; the operator edits and sends it.
func (s *Server) olSuggest(ctx context.Context, id, userID string) (string, error) {
	if s.AI == nil {
		return "", ai.ErrNone
	}
	x, err := s.OpenLines.Session(ctx, id)
	if err != nil {
		return "", err
	}
	text, err := s.olConversation(ctx, x)
	if err != nil {
		return "", err
	}
	lang := s.portalLang()
	quick, err := s.OpenLines.Quick(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(quick) > 0 {
		var b strings.Builder
		for i, q := range quick {
			if i == 30 {
				break
			}
			b.WriteString("— " + q.Title + ": " + q.Body + "\n")
		}
		text = s.I18n.T(lang, "ol.copilot_quick") + "\n" + b.String() + "\n" + text
	}
	answer, err := s.AI.Write(ctx, userID, "openlines", s.I18n.T(lang, "ol.copilot_suggest_prompt"), text)
	if err == nil && answer == "" {
		err = i18n.E("err.ai_call", "empty")
	}
	return answer, err
}

// OLSummarizeDue: CoPilot sums up the dialogues just closed in the lines with «CoPilot: итоги диалога» (go24's clock).
func (s *Server) OLSummarizeDue(ctx context.Context, now time.Time) {
	if s.AI == nil || s.OpenLines == nil {
		return
	}
	ids, err := s.OpenLines.ToSummarize(ctx, now)
	if err != nil {
		s.Log.Error("open lines summaries", "err", err)
		return
	}
	for _, id := range ids {
		if err := s.olSummarize(ctx, id, ""); err != nil && !errors.Is(err, ai.ErrNone) && ctx.Err() == nil {
			s.Log.Error("open lines summary", "err", err, "dialogue", id)
		}
	}
}

// openlinesSuggest: «Подсказать ответ» — the draft as JSON {text} (or {error}) for the reply's field.
func (s *Server) openlinesSuggest(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	who, err := s.olWho(r.Context(), sess.UserID, sess.Admin)
	if err == nil {
		err = s.OpenLines.See(r.Context(), who, id)
	}
	var text string
	if err == nil {
		text, err = s.olSuggest(r.Context(), id, sess.UserID)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}
