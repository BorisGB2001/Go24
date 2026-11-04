package web

import (
	"net/http"
	"strconv"
	"strings"

	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// The widgets of applications in the messenger IM_TEXTAREA, IM_SIDEBAR, IM_CONTEXT_MENU and
// IM_NAVIGATION (GO24DEV-977): placement.bind with OPTIONS — iconName, context (USER;CHAT;LINES;CRM;ALL), role
// (ADMIN — administrators only), color, width and height of the input field's frame, extranet (Y — guests see it
// too). The frame gets the employee's tokens and PLACEMENT_OPTIONS {dialogId} of the chat (with messageId of the
// message for IM_CONTEXT_MENU; IM_NAVIGATION — none) gives them, and roomId, eventId of go24.

// imApps are the widgets of the messenger the employee sees: by place, those of the chat of the kind (imChatKind;
// "" — the messenger without a chat: IM_NAVIGATION only).
func (s *Server) imApps(r *http.Request, sess auth.Session, kind string) map[string][]apps.Placement {
	if s.Apps == nil {
		return nil
	}
	admin, guest := s.apiAdmin(r.Context(), sess.UserID), tasks.IsGuestCtx(r.Context())
	out := map[string][]apps.Placement{}
	for _, place := range apps.IMPlaces {
		if kind == "" && place != "IM_NAVIGATION" {
			continue
		}
		for _, p := range s.appTabs(r, sess.UserID, place) {
			if p.For(admin, guest) && (place == "IM_NAVIGATION" || p.ForChat(kind)) {
				out[place] = append(out[place], p)
			}
		}
	}
	return out
}

// imChatKind is the chat as OPTIONS.context names it: USER — a private chat with a person (and «Заметки»), CRM — the
// chat of a CRM card, CHAT — any other group chat; a private chat with a bot is none of them (only ALL takes it).
func (s *Server) imChatKind(r *http.Request, rm matrixsync.Room) string {
	switch rm.Kind {
	case "dm":
		if rm.PeerBot {
			return "BOT"
		}
		return "USER"
	case "notes":
		return "USER"
	}
	var crm bool
	if err := s.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM crm_chats WHERE room_id = $1)`, rm.ID).Scan(&crm); err != nil {
		s.Log.Warn("im apps crm chat", "err", err)
	}
	if crm {
		return "CRM"
	}
	return "CHAT"
}

// imAppOptions are PLACEMENT_OPTIONS of a widget of the messenger opened in the chat room (with the message event
// for IM_CONTEXT_MENU): dialogId — «chatN», or the number of the colleague (or bot) of a private chat —, messageId;
// the employee is in the chat, the widget is for them (OPTIONS.role, extranet). IM_NAVIGATION has no chat.
func (s *Server) imAppOptions(r *http.Request, sess auth.Session, p apps.Placement, room, event string) (map[string]any, error) {
	out := map[string]any{}
	if !p.For(s.apiAdmin(r.Context(), sess.UserID), tasks.IsGuestCtx(r.Context())) {
		return nil, i18n.E("err.not_found")
	}
	if p.Placement == "IM_NAVIGATION" || s.Matrix == nil {
		return out, nil
	}
	ctx := r.Context()
	rooms, err := s.Matrix.Rooms(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	var rm *matrixsync.Room
	for i := range rooms {
		if rooms[i].ID == room {
			rm = &rooms[i]
		}
	}
	if rm == nil || !p.ForChat(s.imChatKind(r, *rm)) {
		return nil, i18n.E("err.not_found")
	}
	if rm.Kind == "dm" && rm.PeerID != "" {
		var n int64
		if err := s.DB.QueryRow(ctx, `SELECT num FROM users WHERE id::text = $1 UNION ALL SELECT num FROM im_bots WHERE id::text = $1 LIMIT 1`,
			rm.PeerID).Scan(&n); err != nil {
			return nil, err
		}
		out["dialogId"] = strconv.FormatInt(n, 10)
	} else {
		n, err := s.portalChatNum(ctx, rm.ID)
		if err != nil {
			return nil, err
		}
		out["dialogId"] = "chat" + strconv.FormatInt(n, 10)
	}
	out["roomId"] = rm.ID
	if p.Placement == "IM_CONTEXT_MENU" && strings.HasPrefix(event, "$") {
		n, err := s.portalMessageNum(ctx, rm.ID, event)
		if err != nil {
			return nil, err
		}
		out["messageId"], out["eventId"] = n, event
	}
	return out, nil
}

// imAppIcon is the icon of go24 for OPTIONS.iconName of Font Awesome («fa-star» — star, «fa-cog» — settings), when
// go24 has one like it.
func imAppIcon(name string) string {
	name = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "fa "), "fa-")
	if a, ok := faIcons[name]; ok {
		name = a
	}
	if iconNames[name] {
		return name
	}
	return "layout-grid"
}

// faIcons are go24's icons of the names of Font Awesome that differ.
var faIcons = map[string]string{"bars": "menu-2", "cog": "settings", "gear": "settings", "comment": "message-circle", "comments": "messages",
	"envelope": "mail", "user": "users", "users": "users-group", "file": "file-text", "calendar": "calendar-event", "cloud": "cloud-upload",
	"bug": "tool", "wrench": "tool", "image": "photo", "bullhorn": "speakerphone", "magic": "sparkles", "chart-bar": "chart-bar",
	"bookmark": "bookmark", "book": "books", "tasks": "list-check", "shopping-cart": "shopping-cart", "map-marker": "map-pin"}

// imAppColors are the colors of OPTIONS.color names them.
var imAppColors = map[string]string{"RED": "#e5484d", "GREEN": "#30a46c", "MINT": "#3dd6b5", "LIGHT_BLUE": "#5eb1ef",
	"DARK_BLUE": "#2f5fd0", "PURPLE": "#8e4ec6", "AQUA": "#12a5b8", "PINK": "#e93d82", "LIME": "#8bc34a", "BROWN": "#a1724e",
	"AZURE": "#3e8ee0", "KHAKI": "#a69a5e", "SAND": "#d1b26f", "ORANGE": "#f76b15", "MARENGO": "#4b5563", "GRAY": "#8b8d98",
	"GRAPHITE": "#3b3f46"}
