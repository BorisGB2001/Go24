package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// The context of a bot's chat passes it (GO24DEV-965): a link of go24 with ?IM_DIALOG={dialogId}&
// BOT_CONTEXT={json} (/online/ or /im) opens the chat — with a bot of imbot.v2 it gets ONIMBOTV2CONTEXTGET with the
// context —, and im.v2.Chat.Bot.sendContext does the same over REST. The link leaves no context in the address: it
// redirects to the chat.

func init() {
	portalMethods["im.v2.chat.bot.sendcontext"] = portalMethod{Scope: "im:write", Run: (*Server).portalBotSendContext}
}

// botContextMax is the size of BOT_CONTEXT taken.
const botContextMax = 8 << 10

// botContextOf is the JSON of BOT_CONTEXT (nil — none, or not JSON).
func botContextOf(raw string) any {
	if raw == "" || len(raw) > botContextMax {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil
	}
	return v
}

// imDialogRoom is the room of IM_DIALOG for the employee: «chatN», a colleague's or a bot's number (their private
// chat, started if need be); for a bot it is given too.
func (s *Server) imDialogRoom(r *http.Request, userID, dialog string) (string, *matrixsync.Bot, error) {
	ctx := r.Context()
	dialog = strings.TrimSpace(dialog)
	if num, ok := strings.CutPrefix(strings.ToLower(dialog), "chat"); ok {
		room, err := s.portalChatRoom(ctx, num)
		return room, nil, err
	}
	n, err := strconv.ParseInt(dialog, 10, 64)
	if err != nil {
		return "", nil, i18n.E("err.not_found")
	}
	if b, err := s.Matrix.LiveBot(ctx, n); err == nil {
		room, err := s.Matrix.DirectChat(ctx, userID, b.ID)
		return room, &b, err
	}
	var other string
	if err := s.DB.QueryRow(ctx, `SELECT id::text FROM users WHERE num = $1 AND active`, n).Scan(&other); err != nil {
		return "", nil, i18n.E("err.not_found")
	}
	room, err := s.Matrix.DirectChat(ctx, userID, other)
	return room, nil, err
}

// imDialogLink: GET /online/?IM_DIALOG=…&BOT_CONTEXT=… (and /im with them) — the chat opened, its bot given the
// context; then the chat's page without them.
func (s *Server) imDialogLink(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	if s.Matrix == nil || q.Get("IM_DIALOG") == "" {
		http.Redirect(w, r, "/im", http.StatusFound)
		return
	}
	room, bot, err := s.imDialogRoom(r, sess.UserID, q.Get("IM_DIALOG"))
	if err != nil {
		s.taskErr(w, r, err, "/im")
		return
	}
	if c := botContextOf(q.Get("BOT_CONTEXT")); bot != nil && c != nil {
		if err := s.Matrix.BotContext(r.Context(), *bot, sess.UserID, room, c); err != nil {
			s.Log.Warn("im bot context", "err", err)
		}
	}
	http.Redirect(w, r, "/im?room="+url.QueryEscape(room), http.StatusSeeOther)
}

// im.v2.Chat.Bot.sendContext: dialogId — a bot's number —, context (an object) → {result: true}: the caller's chat
// with the bot opened, the bot gets ONIMBOTV2CONTEXTGET.
func (s *Server) portalBotSendContext(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(portalParam(c.in, "dialogId"))), 10, 64)
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "DIALOG_ID_EMPTY", "err.portal_dialog")
	}
	ctx := c.r.Context()
	b, err := s.Matrix.LiveBot(ctx, n)
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusNotFound, "BOT_NOT_FOUND", "err.not_found")
	}
	data := portalParam(c.in, "context")
	if raw, ok := data.(string); ok { // a JSON string, as the link gives it
		data = botContextOf(raw)
	}
	if data == nil {
		data = map[string]any{}
	}
	room, err := s.Matrix.DirectChat(ctx, c.p.UserID, b.ID)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Matrix.BotContext(ctx, b, c.p.UserID, room, data); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"result": true}}, nil
}
