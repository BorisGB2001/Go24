package web

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// ---------- group chats of the messenger (made, named, with a picture; people added, removed, leaving), pinned
// messages and the search of messages ----------

func imRoomURL(room string) string { return "/im?room=" + url.QueryEscape(room) }

// imGroup makes a group chat of the person and the people chosen.
func (s *Server) imGroup(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_ = r.ParseForm()
	room, err := s.Matrix.CreateGroup(r.Context(), sess.UserID, r.FormValue("name"), r.Form["users"], r.FormValue("channel") == "1")
	if err != nil {
		s.taskErr(w, r, err, "/im")
		return
	}
	http.Redirect(w, r, imRoomURL(room), http.StatusSeeOther)
}

// imGroupAction: a change of a group chat — people added or removed, leaving, a new name or picture; a channel or a
// group chat, its owner, admins and rights.
func (s *Server) imGroupAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	room := r.FormValue("room")
	back := imRoomURL(room)
	var err error
	switch r.PathValue("action") {
	case "add":
		_ = r.ParseForm()
		err = s.Matrix.AddToGroup(r.Context(), sess.UserID, room, r.Form["users"])
	case "remove":
		err = s.Matrix.RemoveFromGroup(r.Context(), sess.UserID, room, r.FormValue("user"))
	case "leave":
		if err = s.Matrix.LeaveGroup(r.Context(), sess.UserID, room); err == nil {
			back = "/im"
		}
	case "rename":
		err = s.Matrix.RenameGroup(r.Context(), sess.UserID, room, r.FormValue("name"))
	case "topic":
		err = s.Matrix.SetGroupTopic(r.Context(), sess.UserID, room, r.FormValue("topic"))
	case "owner":
		err = s.Matrix.SetOwner(r.Context(), sess.UserID, room, r.FormValue("user"))
	case "admin":
		err = s.Matrix.SetAdmin(r.Context(), sess.UserID, room, r.FormValue("user"), r.FormValue("on") == "1")
	case "rights":
		err = s.Matrix.SetRights(r.Context(), sess.UserID, room, matrixsync.Rights{Add: r.FormValue("add"), Edit: r.FormValue("edit"), Write: r.FormValue("write")})
	case "channel":
		err = s.Matrix.SetChannel(r.Context(), sess.UserID, room, r.FormValue("on") == "1")
	case "photo":
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
		f, hdr, ferr := r.FormFile("file")
		if ferr != nil {
			s.taskErr(w, r, i18n.E("err.file_required"), back)
			return
		}
		defer f.Close()
		err = s.Matrix.SetGroupPhoto(r.Context(), sess.UserID, r.FormValue("room"), path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/")),
			hdr.Header.Get("Content-Type"), f, hdr.Size)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// imPhoto gives a chat's picture to someone in the chat.
func (s *Server) imPhoto(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	res, err := s.Matrix.RoomPhoto(r.Context(), sess.UserID, r.URL.Query().Get("room"))
	if err != nil {
		s.taskErr(w, r, err, "/im")
		return
	}
	defer res.Body.Close()
	mime := res.Header.Get("Content-Type")
	if !inlineMedia(mime) || !strings.HasPrefix(mime, "image/") {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.Copy(w, res.Body)
}

// imPin pins a message of the chat (on=1) or takes the pin back; the answer is the bar of pins as it is now.
func (s *Server) imPin(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	room := r.FormValue("room")
	err := s.Matrix.Pin(r.Context(), sess.UserID, room, r.FormValue("event"), r.FormValue("on") == "1")
	var html string
	if err == nil {
		var pins []matrixsync.Message
		if pins, err = s.Matrix.Pins(r.Context(), sess.UserID, room); err == nil {
			html, err = s.imFragment(r, "impins", pins)
		}
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// imSearch finds messages in the chat (room) or in all the person's chats; the results carry their chat's name.
func (s *Server) imSearch(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	hits, err := s.Matrix.Search(r.Context(), sess.UserID, q.Get("q"), q.Get("room"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	names := map[string]string{}
	if q.Get("room") == "" && len(hits) > 0 {
		if rooms, err := s.Matrix.Rooms(r.Context(), sess.UserID); err == nil {
			for _, rm := range rooms {
				names[rm.ID] = rm.Name
			}
		}
	}
	html, err := s.imFragment(r, "imsearch", map[string]any{"Hits": hits, "Names": names, "Q": strings.TrimSpace(q.Get("q"))})
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": html, "count": len(hits)})
}

// imTyping: the person is typing in the chat (on=1) or has stopped; the others see «печатает…».
func (s *Server) imTyping(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.Typing(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("on") == "1"); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// imRoomMark: the person's own mark of a chat in the list — pinned at the top, muted, marked unread, hidden (on=1) or not.
func (s *Server) imRoomMark(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.RoomMark(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("mark"), r.FormValue("on") == "1"); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// imAutoDelete turns the chat's auto-delete on (ttl — seconds) or off (0).
func (s *Server) imAutoDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	room := r.FormValue("room")
	ttl, _ := strconv.Atoi(r.FormValue("ttl"))
	if err := s.Matrix.SetAutoDelete(r.Context(), sess.UserID, room, ttl); err != nil {
		s.taskErr(w, r, err, imRoomURL(room))
		return
	}
	http.Redirect(w, r, imRoomURL(room), http.StatusSeeOther)
}

// imDraft keeps the unsent text of the chat or of its thread (sent as the person types and when they leave the page).
func (s *Server) imDraft(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.SetDraft(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("thread"), r.FormValue("text")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// imSave puts the message into the person's «Избранное» (on=1) or takes it out.
func (s *Server) imSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.Save(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), r.FormValue("on") == "1"); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// remindAt is when «Напомнить» means: "1h" — in an hour, "tomorrow" — tomorrow at 9:00, a local date and time
// (2006-01-02T15:04) as chosen, "" — no reminder.
func remindAt(v string, now time.Time) (time.Time, error) {
	switch v {
	case "":
		return time.Time{}, nil
	case "1h":
		return now.Add(time.Hour), nil
	case "tomorrow":
		y, m, d := now.AddDate(0, 0, 1).Date()
		return time.Date(y, m, d, 9, 0, 0, 0, now.Location()), nil
	}
	at, err := time.ParseInLocation("2006-01-02T15:04", v, now.Location())
	if err != nil || !at.After(now) {
		return time.Time{}, i18n.E("im.remind_bad")
	}
	return at, nil
}

// imRemind sets when go24 reminds the person of the message; the answer says when.
func (s *Server) imRemind(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	at, err := remindAt(r.FormValue("at"), time.Now())
	if err == nil {
		err = s.Matrix.Remind(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), at)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	when := ""
	if !at.IsZero() {
		when = s.tr(r, "im.remind_set", at.Local().Format(s.tr(r, "format.datetime")))
	}
	writeJSON(w, http.StatusOK, map[string]string{"when": when})
}

// imSaved is the person's «Избранное» (the list of the search pane).
func (s *Server) imSaved(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	hits, err := s.Matrix.Saved(r.Context(), sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	names := map[string]string{}
	if len(hits) > 0 {
		if rooms, err := s.Matrix.Rooms(r.Context(), sess.UserID); err == nil {
			for _, rm := range rooms {
				names[rm.ID] = rm.Name
			}
		}
	}
	html, err := s.imFragment(r, "imsearch", map[string]any{"Hits": hits, "Names": names, "Empty": s.tr(r, "im.saved_empty")})
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": html, "count": len(hits)})
}

// chatMedia is the chat's panel of what was sent to it: «Медиа» (pictures, videos), «Файлы»,
// «Аудио» (voice messages too) and «Ссылки».
type chatMedia struct {
	Room                string
	Media, Files, Audio []matrixsync.Message
	Links               []matrixsync.Link
}

func (s *Server) chatMedia(r *http.Request, userID, room string) (chatMedia, error) {
	files, links, err := s.Matrix.Media(r.Context(), userID, room)
	out := chatMedia{Room: room, Links: links}
	for _, m := range files {
		switch m.File.Kind {
		case "image", "video":
			out.Media = append(out.Media, m)
		case "audio":
			out.Audio = append(out.Audio, m)
		default:
			out.Files = append(out.Files, m)
		}
	}
	return out, err
}

// imMedia gives the chat's panel «Медиа, файлы, ссылки».
func (s *Server) imMedia(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.chatMedia(r, sess.UserID, r.URL.Query().Get("room"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	html, err := s.imFragment(r, "immedia", m)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": html})
}
