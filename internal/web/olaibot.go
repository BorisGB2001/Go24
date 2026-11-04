package web

import (
	"context"

	"go24/internal/i18n"
)

// OLAIBotDue: the AI bot on the knowledge base answers the clients of open lines who wait for it (go24 worker, every few
// seconds); a dialogue the AI cannot answer (no connection, an error) goes to the operators.
func (s *Server) OLAIBotDue(ctx context.Context) {
	if s.OpenLines == nil {
		return
	}
	list, err := s.OpenLines.AIBotDue(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.Log.Error("open lines ai bot", "err", err)
		}
		return
	}
	for _, x := range list {
		answer, err := s.olAIBot(ctx, x.SessionID, x.Space, x.UserID, x.Question)
		if err != nil {
			s.Log.Warn("open lines ai bot answer", "err", err, "dialogue", x.SessionID)
			err = s.OpenLines.AIBotFail(ctx, x.SessionID)
		} else {
			err = s.OpenLines.AIBotAnswer(ctx, x.SessionID, answer)
		}
		if err != nil && ctx.Err() == nil {
			s.Log.Error("open lines ai bot", "err", err, "dialogue", x.SessionID)
		}
	}
}

// olAIBot has the AI answer the client from the knowledge base space's pages and the conversation so far.
func (s *Server) olAIBot(ctx context.Context, id, space, userID, question string) (string, error) {
	if s.AI == nil {
		return "", i18n.E("err.ai_call", "off")
	}
	x, err := s.OpenLines.Session(ctx, id)
	if err != nil {
		return "", err
	}
	text, err := s.olConversation(ctx, x)
	if err != nil {
		return "", err
	}
	pages, err := s.OpenLines.Knowledge(ctx, space, question)
	if err != nil {
		return "", err
	}
	lang := s.portalLang()
	if pages == "" {
		pages = "—"
	}
	return s.AI.Write(ctx, userID, "openlines", s.I18n.T(lang, "ol.aibot_prompt", x.Widget),
		s.I18n.T(lang, "ol.aibot_pages")+"\n\n"+pages+"\n\n"+text)
}
