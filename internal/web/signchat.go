package web

import (
	"context"

	"go24/internal/i18n"
	"go24/internal/sign"
)

// ---------- «Подпись»: the client's link and code in the chat of the open lines ----------

// As Sign sending by messengers: the client of a CRM card's document gets the link to sign by, the code
// and the signed document in their latest dialogue of the open lines (Telegram, WhatsApp, the live chat…) — when the
// sender chose «В чат» or the client has neither an e-mail nor a phone.

// signChatOf is the dialogue the client c gets the link in: the card's client's latest one when c asks for the chat
// (c.Chat "chat" — none is an error) or has no other way; "" — by e-mail or SMS.
func (s *Server) signChatOf(ctx context.Context, d sign.Doc, c sign.Party) (string, error) {
	want := c.Chat != ""
	if !want && (c.Email != "" || c.Phone != "") {
		return "", nil
	}
	var chat string
	if s.OpenLines != nil && d.Entity != "" && d.EntityID != "" {
		var err error
		if chat, err = s.OpenLines.ChatOf(ctx, d.Entity, d.EntityID); err != nil {
			return "", err
		}
	}
	if want && chat == "" {
		return "", i18n.E("err.doc_send_chat")
	}
	return chat, nil
}

// signChat writes into the client's dialogue from the document's sender; false — it did not go.
func (s *Server) signChat(ctx context.Context, d sign.Doc, chat, text string) bool {
	if s.OpenLines == nil || chat == "" {
		return false
	}
	if err := s.OpenLines.Reply(ctx, d.AuthorID, chat, text); err != nil {
		s.Log.Warn("sign chat", "doc", d.ID, "err", err)
		return false
	}
	return true
}

// SignChatReady: the card has a dialogue with the client — «Подпись» may send the link into it (crm.Store.HasChat).
func (s *Server) SignChatReady(ctx context.Context, entity, entityID string) bool {
	chat, err := s.signChatOf(ctx, sign.Doc{Entity: entity, EntityID: entityID}, sign.Party{Chat: "chat"})
	return err == nil && chat != ""
}

// CRMClientChat writes the text from the user into the card's client's latest dialogue of the open lines and says the
// dialogue (crm.Store.ClientChat: «Сообщение в открытую линию» robots).
func (s *Server) CRMClientChat(ctx context.Context, userID, entity, entityID, text string) (string, error) {
	if s.OpenLines == nil {
		return "", i18n.E("err.robot_chat")
	}
	chat, err := s.OpenLines.ChatOf(ctx, entity, entityID)
	if err == nil && chat == "" {
		err = i18n.E("err.robot_chat")
	}
	if err != nil {
		return "", err
	}
	return chat, s.OpenLines.Reply(ctx, userID, chat, text)
}
