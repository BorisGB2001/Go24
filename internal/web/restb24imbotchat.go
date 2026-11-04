package web

import (
	"net/http"
	"strings"
)

// REST compatible with the portal, the chats of bots (GO24DEV-937): imbot.chat.add, imbot.chat.get,
// imbot.chat.updateTitle, updateAvatar, updateColor, imbot.chat.setOwner, setManager, imbot.chat.user.add, delete and
// list, imbot.dialog.get, imbot.message.like. Each is its im.* method done by the bot BOT_ID (the application's only
// bot without it): the bot makes the chat, owns it, and the chat's rights are the bot's there, as in the messenger.

func init() {
	for name, run := range map[string]func(*Server, portalCall) (portalOut, error){
		"imbot.chat.add":          (*Server).portalChatAdd,
		"imbot.chat.get":          (*Server).portalDialogGet,
		"imbot.chat.updateTitle":  (*Server).portalChatTitle,
		"imbot.chat.updateAvatar": (*Server).portalChatAvatar,
		"imbot.chat.updateColor":  (*Server).portalChatColor,
		"imbot.chat.setOwner":     (*Server).portalChatOwner,
		"imbot.chat.setManager":   (*Server).portalChatManager,
		"imbot.chat.user.add":     (*Server).portalChatUserAdd,
		"imbot.chat.user.delete":  (*Server).portalChatUserDelete,
		"imbot.chat.user.list":    (*Server).portalChatUsers,
		"imbot.dialog.get":        (*Server).portalBotDialogGet,
		"imbot.message.like":      (*Server).portalMessageLike,
	} {
		run := run
		portalMethods[strings.ToLower(name)] = portalMethod{Scope: "imbot", Run: func(s *Server, c portalCall) (portalOut, error) {
			bc, err := s.portalAsBot(c)
			if err != nil {
				return portalOut{}, err
			}
			return run(s, bc)
		}}
	}
}

// portalAsBot is the call made by the bot BOT_ID of the application.
func (s *Server) portalAsBot(c portalCall) (portalCall, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return c, err
	}
	c.p.UserID = b.ID
	return c, nil
}

// imbot.chat.setManager: CHAT_ID, USER_ID, IS_MANAGER (Y — the chat's admin, N — no longer) → true.
func (s *Server) portalChatManager(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	user, err := s.portalChatPerson(c, portalParam(c.in, "USER_ID"))
	if err != nil {
		return portalOut{}, err
	}
	if user == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "USER_ID_EMPTY", "err.portal_user")
	}
	on := true
	if v := portalFlagOf(c.in, "IS_MANAGER"); v != nil {
		on = *v
	}
	err = s.Matrix.SetAdmin(c.r.Context(), c.p.UserID, room, user, on)
	return portalOut{Result: err == nil}, err
}

// imbot.dialog.get: DIALOG_ID — «chatN», a chat the bot is in, or an employee's number, the bot's private chat with
// them — as im.dialog.get gives it.
func (s *Server) portalBotDialogGet(c portalCall) (portalOut, error) {
	_, b, err := s.portalBot(c)
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.portalBotDialog(c, b)
	if err != nil {
		return portalOut{}, err
	}
	chat, err := s.portalChatOf(c, room)
	return portalOut{Result: chat}, err
}
