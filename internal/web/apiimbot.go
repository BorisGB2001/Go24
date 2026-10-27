package web

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// ---------- Chat bots over REST, as imbot.* ----------
//
// Only an application calls them, with its token and the «imbot» right: imbot.register (POST /api/v1/imbot/bots),
// imbot.bot.list (GET), imbot.update (PATCH …/{bot}), imbot.unregister (DELETE …/{bot}); imbot.message.add,
// update and delete (…/{bot}/messages). {bot} is the bot's ID or code. Field names may be written writes
// them (CODE, EVENT_MESSAGE_ADD, PROPERTIES.NAME …): JSON keys are matched without regard to case. Commands:
// imbot.command.register, update, unregister (…/{bot}/commands[/{command}]) and imbot.command.answer
// (…/commands/{command}/answer); a message may carry KEYBOARD — its buttons.

func (s *Server) apiIMBotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/imbot/bots", s.api("imbot", s.apiBots))
	mux.HandleFunc("POST /api/v1/imbot/bots", s.api("imbot", s.apiBotRegister))
	mux.HandleFunc("PATCH /api/v1/imbot/bots/{bot}", s.api("imbot", s.apiBotUpdate))
	mux.HandleFunc("DELETE /api/v1/imbot/bots/{bot}", s.api("imbot", s.apiBotUnregister))
	mux.HandleFunc("POST /api/v1/imbot/bots/{bot}/messages", s.api("imbot", s.apiBotSend))
	mux.HandleFunc("PATCH /api/v1/imbot/bots/{bot}/messages/{id}", s.api("imbot", s.apiBotEdit))
	mux.HandleFunc("DELETE /api/v1/imbot/bots/{bot}/messages/{id}", s.api("imbot", s.apiBotDelete))
	mux.HandleFunc("GET /api/v1/imbot/bots/{bot}/commands", s.api("imbot", s.apiBotCommands))
	mux.HandleFunc("POST /api/v1/imbot/bots/{bot}/commands", s.api("imbot", s.apiCommandRegister))
	mux.HandleFunc("PATCH /api/v1/imbot/bots/{bot}/commands/{cmd}", s.api("imbot", s.apiCommandUpdate))
	mux.HandleFunc("DELETE /api/v1/imbot/bots/{bot}/commands/{cmd}", s.api("imbot", s.apiCommandUnregister))
	mux.HandleFunc("POST /api/v1/imbot/bots/{bot}/commands/{cmd}/answer", s.api("imbot", s.apiCommandAnswer))
}

// botReq is the body of imbot.register and imbot.update; event_handler sets all three handlers at once.
type botReq struct {
	Code         string `json:"code"`
	Type         string `json:"type"`
	EventHandler string `json:"event_handler"`
	MessageAdd   string `json:"event_message_add"`
	Welcome      string `json:"event_welcome_message"`
	BotDelete    string `json:"event_bot_delete"`
	Properties   struct {
		Name         string `json:"name"`
		LastName     string `json:"last_name"`
		WorkPosition string `json:"work_position"`
		Color        string `json:"color"`
	} `json:"properties"`
}

func (q botReq) bot() matrixsync.Bot {
	b := matrixsync.Bot{Code: q.Code, Type: q.Type, Name: q.Properties.Name, LastName: q.Properties.LastName,
		WorkPosition: q.Properties.WorkPosition, Color: q.Properties.Color,
		MessageHandler: q.MessageAdd, WelcomeHandler: q.Welcome, DeleteHandler: q.BotDelete}
	for _, h := range []*string{&b.MessageHandler, &b.WelcomeHandler, &b.DeleteHandler} {
		if *h = strings.TrimSpace(*h); *h == "" {
			*h = strings.TrimSpace(q.EventHandler)
		}
	}
	return b
}

// apiBotApp is the application of the token, with the messenger on.
func (s *Server) apiBotApp(r *http.Request, p apikeys.Principal) (apps.App, error) {
	if err := s.needMatrix(); err != nil {
		return apps.App{}, err
	}
	return s.apiApp(r, p)
}

// bindBot subscribes the bot's handlers to its events for the employee p (the handlers are on the application's host).
func (s *Server) bindBot(r *http.Request, a apps.App, p apikeys.Principal, b matrixsync.Bot) error {
	for _, h := range []struct{ event, url string }{{matrixsync.BotMessageAdd, b.MessageHandler},
		{matrixsync.BotJoinChat, b.WelcomeHandler}, {matrixsync.BotDelete, b.DeleteHandler}} {
		if h.url == "" {
			continue
		}
		if _, err := s.Apps.BindEvent(r.Context(), a, apps.Event{Event: h.event, Handler: h.url, UserID: p.UserID}); err != nil {
			return err
		}
	}
	return nil
}

// apiBots is imbot.bot.list: the application's bots.
func (s *Server) apiBots(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	list, err := s.Matrix.Bots(r.Context(), a.ID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []matrixsync.Bot{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

// apiBotRegister is imbot.register: a new bot of the application; people see it in the messenger at once.
func (s *Server) apiBotRegister(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	var q botReq
	if err := readJSON(r, &q); err != nil {
		return err
	}
	b := q.bot()
	if b.Code == "" {
		return i18n.E("err.imbot_code_empty")
	}
	if err := s.bindBot(r, a, p, b); err != nil {
		return err
	}
	if b, err = s.Matrix.RegisterBot(r.Context(), a.ID, p.UserID, b); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, b)
	return nil
}

// apiBotUpdate is imbot.update: what is given changes, the rest stays.
func (s *Server) apiBotUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	var q botReq
	if err := readJSON(r, &q); err != nil {
		return err
	}
	ch := q.bot()
	if err := s.bindBot(r, a, p, ch); err != nil {
		return err
	}
	b, err := s.Matrix.UpdateBot(r.Context(), a.ID, r.PathValue("bot"), ch)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, b)
	return nil
}

// apiBotUnregister is imbot.unregister: the bot leaves the messenger, its messages stay.
func (s *Server) apiBotUnregister(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	if err := s.Matrix.DeleteBot(r.Context(), a.ID, r.PathValue("bot")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// botMsg is the body of imbot.message.add and update: dialog_id — an employee's ID (the private chat with them) or
// a chat's ID (DIALOG_ID of the bot's events); thread_id — a reply in that thread.
type botMsg struct {
	DialogID string          `json:"dialog_id"`
	Message  string          `json:"message"`
	ThreadID string          `json:"thread_id"`
	Keyboard json.RawMessage `json:"keyboard"`
	Menu     json.RawMessage `json:"menu"`
	Attach   json.RawMessage `json:"attach"`
}

// keyboard is KEYBOARD of the message: nil — none given, empty — "N" (take the buttons away).
func (m botMsg) keyboard() ([]matrixsync.Button, error) {
	raw := bytes.TrimSpace(m.Keyboard)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] != '[' {
		return []matrixsync.Button{}, nil
	}
	var kb []matrixsync.Button
	if err := json.Unmarshal(raw, &kb); err != nil {
		return nil, i18n.E("err.imbot_keyboard")
	}
	return matrixsync.NormKeyboard(kb)
}

// parts are KEYBOARD, MENU and ATTACH of the message; one not given is nil.
func (m botMsg) parts() (matrixsync.BotParts, error) {
	var p matrixsync.BotParts
	var err error
	if p.Keyboard, err = m.keyboard(); err != nil {
		return p, err
	}
	if raw := bytes.TrimSpace(m.Menu); len(raw) > 0 && string(raw) != "null" {
		p.Menu = []matrixsync.Button{}
		if raw[0] == '[' {
			var menu []matrixsync.Button
			if err := json.Unmarshal(raw, &menu); err != nil {
				return p, i18n.E("err.imbot_keyboard")
			}
			if p.Menu, err = matrixsync.NormMenu(menu); err != nil {
				return p, err
			}
		}
	}
	p.Attach, err = matrixsync.ParseAttach(m.Attach)
	return p, err
}

var brRe = regexp.MustCompile(`(?i)\[br\]`)

// botText is the message's text: [BR] is a new line, the other BB codes msgfmt shows as they are.
func botText(m string) string { return brRe.ReplaceAllString(m, "\n") }

// apiBot is the bot of the path, of the token's application.
func (s *Server) apiBot(r *http.Request, p apikeys.Principal) (matrixsync.Bot, error) {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return matrixsync.Bot{}, err
	}
	return s.Matrix.AppBot(r.Context(), a.ID, r.PathValue("bot"))
}

func (s *Server) botMessage(r *http.Request) (botMsg, error) {
	var m botMsg
	if err := readJSON(r, &m); err != nil {
		return m, err
	}
	if m.DialogID == "" {
		m.DialogID = r.URL.Query().Get("dialog_id")
	}
	if strings.TrimSpace(m.DialogID) == "" {
		return m, i18n.E("err.imbot_dialog")
	}
	return m, nil
}

// apiBotSend is imbot.message.add: the bot writes into the dialog → {"id": the message, "dialog_id", "chat_id"}.
func (s *Server) apiBotSend(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	b, err := s.apiBot(r, p)
	if err != nil {
		return err
	}
	m, err := s.botMessage(r)
	if err != nil {
		return err
	}
	parts, err := m.parts()
	if err != nil {
		return err
	}
	room, err := s.Matrix.BotDialog(r.Context(), b.ID, m.DialogID)
	if err != nil {
		return err
	}
	msg, err := s.Matrix.BotSend(r.Context(), b.ID, room, botText(m.Message), m.ThreadID, parts)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": msg.ID, "dialog_id": m.DialogID, "chat_id": room})
	return nil
}

// apiBotEdit is imbot.message.update: the bot's own message gets a new text, buttons, menu or card.
func (s *Server) apiBotEdit(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	b, err := s.apiBot(r, p)
	if err != nil {
		return err
	}
	m, err := s.botMessage(r)
	if err != nil {
		return err
	}
	parts, err := m.parts()
	if err != nil {
		return err
	}
	room, err := s.Matrix.BotDialog(r.Context(), b.ID, m.DialogID)
	if err != nil {
		return err
	}
	if _, err := s.Matrix.BotEdit(r.Context(), b.ID, room, r.PathValue("id"), botText(m.Message), parts); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id")})
	return nil
}

// apiBotDelete is imbot.message.delete: the bot takes its message back (?dialog_id=).
func (s *Server) apiBotDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	b, err := s.apiBot(r, p)
	if err != nil {
		return err
	}
	dialog := r.URL.Query().Get("dialog_id")
	if strings.TrimSpace(dialog) == "" {
		return i18n.E("err.imbot_dialog")
	}
	room, err := s.Matrix.BotDialog(r.Context(), b.ID, dialog)
	if err != nil {
		return err
	}
	if err := s.Matrix.Delete(r.Context(), b.ID, room, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// cmdReq is the body of imbot.command.register and update (its FIELDS may hold what changes); LANG — the command's
// title and params by language gives them.
type cmdReq struct {
	Command  string         `json:"command"`
	Common   *matrixsync.YN `json:"common"`
	Hidden   *matrixsync.YN `json:"hidden"`
	Extranet *matrixsync.YN `json:"extranet_support"`
	Handler  string         `json:"event_command_add"`
	Lang     []cmdLang      `json:"lang"`
	Fields   *cmdReq        `json:"fields"`
}

type cmdLang struct {
	LanguageID string `json:"language_id"`
	Title      string `json:"title"`
	Params     string `json:"params"`
}

func (q cmdReq) lang() map[string]matrixsync.CommandText {
	if q.Lang == nil {
		return nil
	}
	out := map[string]matrixsync.CommandText{}
	for _, l := range q.Lang {
		if id := strings.ToLower(strings.TrimSpace(l.LanguageID)); id != "" {
			out[id] = matrixsync.CommandText{Title: strings.TrimSpace(l.Title), Params: strings.TrimSpace(l.Params)}
		}
	}
	return out
}

func yn(v *matrixsync.YN) *bool {
	if v == nil {
		return nil
	}
	b := bool(*v)
	return &b
}

// bindCommand subscribes the command's handler to ONIMCOMMANDADD for the employee p.
func (s *Server) bindCommand(r *http.Request, a apps.App, p apikeys.Principal, handler string) error {
	if handler = strings.TrimSpace(handler); handler == "" {
		return nil
	}
	_, err := s.Apps.BindEvent(r.Context(), a, apps.Event{Event: matrixsync.BotCommand, Handler: handler, UserID: p.UserID})
	return err
}

// apiBotCommands: the bot's commands.
func (s *Server) apiBotCommands(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	list, err := s.Matrix.Commands(r.Context(), a.ID, r.PathValue("bot"))
	if err != nil {
		return err
	}
	if list == nil {
		list = []matrixsync.Command{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

// apiCommandRegister is imbot.command.register: a «/command» of the bot.
func (s *Server) apiCommandRegister(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	var q cmdReq
	if err := readJSON(r, &q); err != nil {
		return err
	}
	if strings.TrimSpace(q.Handler) == "" {
		return i18n.E("err.imbot_command_handler")
	}
	if err := s.bindCommand(r, a, p, q.Handler); err != nil {
		return err
	}
	c := matrixsync.Command{Command: q.Command, Lang: q.lang(), Handler: q.Handler}
	for _, f := range []struct {
		to *bool
		v  *matrixsync.YN
	}{{&c.Common, q.Common}, {&c.Hidden, q.Hidden}, {&c.Extranet, q.Extranet}} {
		if f.v != nil {
			*f.to = bool(*f.v)
		}
	}
	if c, err = s.Matrix.RegisterCommand(r.Context(), a.ID, r.PathValue("bot"), c); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, c)
	return nil
}

// apiCommandUpdate is imbot.command.update: what is given changes.
func (s *Server) apiCommandUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	var q cmdReq
	if err := readJSON(r, &q); err != nil {
		return err
	}
	if q.Fields != nil {
		q = *q.Fields
	}
	if err := s.bindCommand(r, a, p, q.Handler); err != nil {
		return err
	}
	c, err := s.Matrix.UpdateCommand(r.Context(), a.ID, r.PathValue("bot"), r.PathValue("cmd"), matrixsync.CommandChange{
		Common: yn(q.Common), Hidden: yn(q.Hidden), Extranet: yn(q.Extranet), Lang: q.lang(), Handler: q.Handler})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

// apiCommandUnregister is imbot.command.unregister.
func (s *Server) apiCommandUnregister(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	if err := s.Matrix.DeleteCommand(r.Context(), a.ID, r.PathValue("bot"), r.PathValue("cmd")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiCommandAnswer is imbot.command.answer: the bot answers the command of MESSAGE_ID in its chat (dialog_id, when
// given, names another dialog) → {"id", "chat_id"}.
func (s *Server) apiCommandAnswer(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiBotApp(r, p)
	if err != nil {
		return err
	}
	var q struct {
		botMsg
		MessageID string `json:"message_id"`
	}
	if err := readJSON(r, &q); err != nil {
		return err
	}
	parts, err := q.parts()
	if err != nil {
		return err
	}
	c, err := s.Matrix.BotCommand(r.Context(), a.ID, r.PathValue("bot"), r.PathValue("cmd"))
	if err != nil {
		return err
	}
	room, thread := "", q.ThreadID
	if strings.TrimSpace(q.DialogID) != "" {
		room, err = s.Matrix.BotDialog(r.Context(), c.BotID, q.DialogID)
	} else {
		var t string
		room, t, err = s.Matrix.CommandRoom(r.Context(), a.ID, c, strings.TrimSpace(q.MessageID))
		thread = cmp.Or(thread, t)
	}
	if err != nil {
		return err
	}
	msg, err := s.Matrix.BotSend(r.Context(), c.BotID, room, botText(q.Message), thread, parts)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": msg.ID, "chat_id": room})
	return nil
}
