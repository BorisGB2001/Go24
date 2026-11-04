package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/i18n"
)

// REST compatible with the portal, the files of chat bots 2.0 (GO24DEV-963): imbot.v2.File.upload, download — as the
// bot; im.v2.File.upload, download — as the caller. A file is sent into the chat as a message of its own, so fileId is
// the number of that message (Message.params.FILE_ID of the events gives it too); downloadUrl is a signed address of
// go24 that gives the file for an hour while its holder is in the chat.

func init() {
	for name, run := range map[string]func(*Server, portalCall) (portalOut, error){
		"imbot.v2.File.upload":   (*Server).portalV2BotFileUpload,
		"imbot.v2.File.download": (*Server).portalV2BotFileDownload,
	} {
		run := run
		portalMethods[strings.ToLower(name)] = portalMethod{Scope: "imbot", Run: func(s *Server, c portalCall) (portalOut, error) {
			return run(s, portalV2(c))
		}}
	}
	portalMethods["im.v2.file.upload"] = portalMethod{Scope: "im:write", Run: func(s *Server, c portalCall) (portalOut, error) {
		return s.portalV2FileUpload(portalV2(c))
	}}
	portalMethods["im.v2.file.download"] = portalMethod{Scope: "im:read", Run: func(s *Server, c portalCall) (portalOut, error) {
		return s.portalV2FileDownload(portalV2(c))
	}}
}

const portalChatFileTTL = time.Hour // how long downloadUrl of *.File.download works

// portalV2FileSend sends the file of fields (name, content in base64, message — the text sent before it) into the room
// as the caller (an employee or a bot) → {file: {id, chatId, name, extension, size}, messageId, chatId, dialogId}.
func (s *Server) portalV2FileSend(c portalCall, room string) (portalOut, error) {
	f := portalV2Fields(c)
	if d := portalMap(portalParam(f, "FILE_DATA")); d != nil {
		f = d
	}
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(portalV2Str(f, "name"), "\\", "/")))
	b64 := portalV2Str(f, "content")
	if name == "" || name == "." || name == "/" || b64 == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "FILE_EMPTY", "err.file_required")
	}
	if _, rest, cut := strings.Cut(b64, ";base64,"); cut { // a data: address
		b64 = rest
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "FILE_INVALID_CONTENT", "err.portal_field", "content")
	}
	if len(data) > 100<<20 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "FILE_TOO_LARGE", "err.file_required")
	}
	ctx := c.r.Context()
	if m := strings.TrimSpace(botText(portalV2Str(f, "message"))); m != "" {
		if _, err := s.Matrix.Send(ctx, c.p.UserID, room, m, ""); err != nil {
			return portalOut{}, err
		}
	}
	msg, err := s.Matrix.SendFile(ctx, c.p.UserID, room, "", name, portalDiskContentType(name, data), bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		return portalOut{}, err
	}
	num, err := s.portalMessageNum(ctx, room, msg.ID)
	if err != nil {
		return portalOut{}, err
	}
	chat, err := s.portalChatNum(ctx, room)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"file": map[string]any{"id": num, "chatId": chat, "name": name,
		"extension": strings.TrimPrefix(path.Ext(name), "."), "size": len(data)},
		"messageId": num, "chatId": chat, "dialogId": portalV2Str(c.in, "DIALOG_ID")}}, nil
}

// portalV2FileLink checks the caller sees the file fileId (in the room of dialogId, when given) → {downloadUrl}.
func (s *Server) portalV2FileLink(c portalCall, dialog bool) (portalOut, error) {
	c.in["MESSAGE_ID"] = portalParam(c.in, "fileId")
	room, event, err := s.portalMessage(c)
	if err == nil && dialog {
		var in string
		if in, err = s.portalDialog(c, portalParam(c.in, "DIALOG_ID")); err == nil && in != room {
			err = i18n.E("err.not_found")
		}
	}
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusNotFound, "FILE_NOT_FOUND", "err.not_found")
	}
	_, body, err := s.Matrix.OpenFile(c.r.Context(), c.p.UserID, room, event)
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusForbidden, "FILE_ACCESS_ERROR", "err.forbidden")
	}
	body.Close()
	return portalOut{Result: map[string]any{"downloadUrl": s.portalChatFileLink(c.p.UserID, room, event)}}, nil
}

// imbot.v2.File.upload: botId, dialogId — a chat the bot is in —, fields (name, content, message) → the file sent
// by the bot.
func (s *Server) portalV2BotFileUpload(c portalCall) (portalOut, error) {
	_, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	room, err := s.portalV2Room(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalV2FileSend(c, room)
}

// imbot.v2.File.download: botId, fileId — a file of a chat the bot is in → {downloadUrl}.
func (s *Server) portalV2BotFileDownload(c portalCall) (portalOut, error) {
	_, c, err := s.portalV2As(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalV2FileLink(c, false)
}

// im.v2.File.upload: dialogId, fields (name, content, message) → the file sent by the caller.
func (s *Server) portalV2FileUpload(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, err := s.portalV2Room(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalV2FileSend(c, room)
}

// im.v2.File.download: dialogId, fileId — a file of that chat → {downloadUrl}.
func (s *Server) portalV2FileDownload(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	return s.portalV2FileLink(c, true)
}

// portalChatFileTicket is what downloadUrl gives: the file of the message event of the room, read as user.
type portalChatFileTicket struct {
	User  string `json:"u"`
	Room  string `json:"r"`
	Event string `json:"v"`
	Exp   int64  `json:"e"`
}

// portalChatFileLink is the signed address of the file of the message event for user (an employee or a bot).
func (s *Server) portalChatFileLink(user, room, event string) string {
	raw, _ := json.Marshal(portalChatFileTicket{User: user, Room: room, Event: event, Exp: time.Now().Add(portalChatFileTTL).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return s.BaseURL + "/rest/imfile?token=" + payload + "." + s.Sessions.Stamp("portal-im-file", payload)
}

// portalChatFile: GET /rest/imfile?token= — the bytes of downloadUrl, while its holder — an active employee or a bot of
// an application — is still in the chat.
func (s *Server) portalChatFile(w http.ResponseWriter, r *http.Request) {
	var t portalChatFileTicket
	payload, stamp, _ := strings.Cut(r.URL.Query().Get("token"), ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || !s.Sessions.Stamped(stamp, "portal-im-file", payload) || json.Unmarshal(raw, &t) != nil || time.Now().Unix() > t.Exp {
		s.portalFail(w, r, i18n.E("err.portal_disk_link"))
		return
	}
	var live bool
	if err := s.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active)
		OR EXISTS (SELECT 1 FROM im_bots WHERE id::text = $1 AND app_id IS NOT NULL)`, t.User).Scan(&live); err != nil || !live {
		s.portalFail(w, r, i18n.E("err.portal_disk_link"))
		return
	}
	if s.Matrix == nil {
		s.portalFail(w, r, i18n.E("err.not_found"))
		return
	}
	f, body, err := s.Matrix.OpenFile(r.Context(), t.User, t.Room, t.Event)
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	defer body.Close()
	mime, _ := imFileType(f.Name, f.Mime, true)
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.Name))
	_, _ = io.Copy(w, body)
}
