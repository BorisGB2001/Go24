package web

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/notify"
)

// API: the person's inbox — notifications (tasks, comments, mentions) and the messenger — for someone who works
// through the API (an assistant, a bot). Messenger calls use the person's own messenger account.

type apiNotification struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"` // as the bell shows it
	Actor     string    `json:"actor"`
	TaskKey   string    `json:"task_key,omitempty"`
	TaskTitle string    `json:"task_title,omitempty"`
	PostID    string    `json:"post_id,omitempty"`
	Body      string    `json:"body,omitempty"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

// apiNotifications: GET /api/v1/notifications?unread=1&limit=50
func (s *Server) apiNotifications(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	list, err := s.Notify.List(r.Context(), p.UserID, 0, limit)
	if err != nil {
		return err
	}
	lang := s.I18n.Match("")
	tr := func(key string, args ...any) string { return s.I18n.T(lang, key, args...) }
	out := []apiNotification{}
	for _, n := range list {
		if r.URL.Query().Get("unread") == "1" && n.Read {
			continue
		}
		out = append(out, apiNotification{ID: n.ID, Kind: n.Kind, Actor: n.ActorName, TaskKey: n.TaskKey, TaskTitle: n.TaskTitle,
			PostID: n.PostID, Body: n.Body, Read: n.Read, CreatedAt: n.CreatedAt,
			Text: notify.Text(n, tr, func(t time.Time) string { return t.Local().Format(tr("format.datetime")) })})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiNotificationsRead: POST {"id": 5} (0 or none: all)
func (s *Server) apiNotificationsRead(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		ID int64 `json:"id"`
	}
	if r.ContentLength > 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	if err := s.Notify.MarkRead(r.Context(), p.UserID, in.ID); err != nil {
		return err
	}
	n, _ := s.Notify.Unread(r.Context(), p.UserID)
	writeJSON(w, http.StatusOK, map[string]int{"unread": n})
	return nil
}

type apiMessage struct {
	ID         string     `json:"id"`
	Sender     string     `json:"sender"`
	SenderID   string     `json:"sender_id,omitempty"`
	Body       string     `json:"body"`
	At         time.Time  `json:"at"`
	Mine       bool       `json:"mine"`
	ThreadRoot string     `json:"thread_root,omitempty"`
	Replies    int        `json:"replies,omitempty"`
	File       *apiIMFile `json:"file,omitempty"`
	Event      string     `json:"event,omitempty"`   // a line about the chat itself: added | removed | left | renamed | photo | owner | admin | unadmin
	Target     string     `json:"target,omitempty"`  // whom the line is about
	ReadBy     []string   `json:"read_by,omitempty"` // one's own message: who else has read it
	Poll       *apiPoll   `json:"poll,omitempty"`
	Reactions  []apiReact `json:"reactions,omitempty"`
}

// apiReact is a reaction on a message: the emoji, how many put it, who (their names) and whether the key owner did.
type apiReact struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
	Names string `json:"names,omitempty"`
	Mine  bool   `json:"mine,omitempty"`
}

// apiPoll is a poll with its votes (the counts — once the key owner has voted or the poll is over, and of a poll
// with hidden results only when it is over).
type apiPoll struct {
	Question string          `json:"question"`
	Answers  []apiPollAnswer `json:"answers"`
	Multi    bool            `json:"multi"`
	Hidden   bool            `json:"hidden"`
	Ended    bool            `json:"ended"`
	Votes    int             `json:"votes"`
	Voted    bool            `json:"voted"`
}

type apiPollAnswer struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Count int    `json:"count,omitempty"`
	Names string `json:"names,omitempty"`
	Mine  bool   `json:"mine,omitempty"`
}

// apiIMFile is a message's file: GET /api/v1/im/rooms/{room}/messages/{id}/file gives it.
type apiIMFile struct {
	Kind     string `json:"kind"` // file | image | audio | video
	Name     string `json:"name"`
	Mime     string `json:"mime,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Duration int    `json:"duration_ms,omitempty"`
}

func toAPIMessage(m matrixsync.Message) apiMessage {
	out := apiMessage{m.ID, m.SenderName, m.SenderID, m.Body, m.At, m.Mine, m.ThreadRoot, m.Replies, nil, m.Event, m.Target, m.ReadBy, nil, nil}
	for _, r := range m.Reactions {
		out.Reactions = append(out.Reactions, apiReact{r.Key, r.Count, r.Names, r.Mine})
	}
	if f := m.File; f != nil {
		out.File = &apiIMFile{f.Kind, f.Name, f.Mime, f.Size, f.Duration}
	}
	if pl := m.Poll; pl != nil {
		out.Poll = &apiPoll{Question: pl.Question, Multi: pl.Multi, Hidden: pl.Hidden, Ended: pl.Ended, Votes: pl.Votes, Voted: pl.Voted}
		for _, a := range pl.Answers {
			pa := apiPollAnswer{ID: a.ID, Text: a.Text, Mine: a.Mine}
			if pl.Shown() {
				pa.Count, pa.Names = a.Count, a.Names
			}
			out.Poll.Answers = append(out.Poll.Answers, pa)
		}
	}
	return out
}

func (s *Server) needMatrix() error {
	if s.Matrix == nil {
		return i18n.E("err.not_found")
	}
	return nil
}

// apiRooms: the person's chats with unread counts and the last message.
func (s *Server) apiRooms(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	rooms, err := s.Matrix.Rooms(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	type room struct {
		ID     string      `json:"id"`
		Name   string      `json:"name"`
		Kind   string      `json:"kind"`
		PeerID string      `json:"peer_id,omitempty"`
		Unread int         `json:"unread"`
		Thread int         `json:"thread_unread,omitempty"` // unread replies in its threads
		Last   *apiMessage `json:"last,omitempty"`
		Pinned bool        `json:"pinned,omitempty"` // at the top of the list
		Muted  bool        `json:"muted,omitempty"`
		Marked bool        `json:"marked_unread,omitempty"`
		Hidden bool        `json:"hidden,omitempty"` // out of the list until a new message
	}
	out := []room{}
	for _, x := range rooms {
		rm := room{ID: x.ID, Name: x.Name, Kind: x.Kind, PeerID: x.PeerID, Unread: x.Unread, Thread: x.Threads, Pinned: x.Favorite, Muted: x.Muted, Marked: x.Marked, Hidden: x.Hidden}
		if x.Last != nil {
			m := toAPIMessage(*x.Last)
			rm.Last = &m
		}
		out = append(out, rm)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiRoomMessages: the newest messages of a chat (?thread=<root> for a thread); ?read=1 marks the chat read.
func (s *Server) apiRoomMessages(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	room := r.PathValue("room")
	var list []matrixsync.Message
	if root := r.URL.Query().Get("thread"); root != "" {
		head, replies, err := s.Matrix.Thread(r.Context(), p.UserID, room, root)
		if err != nil {
			return err
		}
		list = append([]matrixsync.Message{head}, replies...)
	} else {
		msgs, _, err := s.Matrix.Messages(r.Context(), p.UserID, room, "")
		if err != nil {
			return err
		}
		list = msgs
	}
	out := []apiMessage{}
	for _, m := range list {
		out = append(out, toAPIMessage(m))
	}
	if r.URL.Query().Get("read") == "1" && len(list) > 0 { // the whole chat, threads included
		if err := s.Matrix.MarkRoomRead(r.Context(), p.UserID, room); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiRoomSend: POST {"body": "…", "thread": "<root event>"}
func (s *Server) apiRoomSend(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		Body   string `json:"body"`
		Thread string `json:"thread"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m, err := s.Matrix.Send(r.Context(), p.UserID, r.PathValue("room"), in.Body, in.Thread)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAPIMessage(m))
	return nil
}

// apiMessageEdit: PATCH {"body": "…"} — a new text of the key owner's own message.
func (s *Server) apiMessageEdit(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m, err := s.Matrix.Edit(r.Context(), p.UserID, r.PathValue("room"), r.PathValue("id"), in.Body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIMessage(m))
	return nil
}

// apiMessageDelete takes back the key owner's own message.
func (s *Server) apiMessageDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	if err := s.Matrix.Delete(r.Context(), p.UserID, r.PathValue("room"), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiReaction: PUT puts the key owner's reaction (the emoji, e.g. 👍) on a message, DELETE takes it back.
func (s *Server) apiReaction(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	err := s.Matrix.SetReaction(r.Context(), p.UserID, r.PathValue("room"), r.PathValue("id"), r.PathValue("key"), r.Method == http.MethodPut)
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiRoomFile: POST multipart (file, thread, duration in ms for a voice message) → the file sent to the chat.
func (s *Server) apiRoomFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return i18n.E("err.file_required")
	}
	defer f.Close()
	var extra map[string]any
	if d, err := strconv.Atoi(r.FormValue("duration")); err == nil && d > 0 {
		extra = map[string]any{"duration": d}
	}
	m, err := s.Matrix.SendFile(r.Context(), p.UserID, r.PathValue("room"), r.FormValue("thread"),
		path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/")), hdr.Header.Get("Content-Type"), f, hdr.Size, extra)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAPIMessage(m))
	return nil
}

// apiMessageFile gives the file of a message the key's owner sees.
func (s *Server) apiMessageFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	f, body, err := s.Matrix.OpenFile(r.Context(), p.UserID, r.PathValue("room"), r.PathValue("id"))
	if err != nil {
		return err
	}
	defer body.Close()
	mime := f.Mime
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, body)
	return nil
}

// apiTranscript: POST → {"text": …} of a voice message (Whisper, made once and kept).
func (s *Server) apiTranscript(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	text, err := s.transcribe(r, p.UserID, r.PathValue("room"), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
	return nil
}

// apiDirect: POST {"user_id": "…"} → the private chat with that employee.
func (s *Server) apiDirect(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	room, err := s.Matrix.DirectChat(r.Context(), p.UserID, in.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"room": room})
	return nil
}

// apiNotes: POST → the person's own chat «Заметки» (made on first use).
func (s *Server) apiNotes(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	room, err := s.Matrix.NotesChat(r.Context(), p.UserID, s.I18n.T(s.lang(r), "im.notes"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"room": room})
	return nil
}

// apiGroup makes a group chat: {"name": "…", "user_ids": […], "channel": true — a channel} → {"room": …}.
func (s *Server) apiGroup(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		Name    string   `json:"name"`
		UserIDs []string `json:"user_ids"`
		Channel bool     `json:"channel"`
		Open    bool     `json:"open"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	room, err := s.Matrix.CreateOpenGroup(r.Context(), p.UserID, in.Name, in.UserIDs, in.Channel, in.Open)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]string{"room": room})
	return nil
}

type apiMember struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Owner     bool   `json:"owner"`
	Admin     bool   `json:"admin,omitempty"`
	Guest     bool   `json:"guest,omitempty"`
	Removable bool   `json:"removable,omitempty"`
}

type apiRights struct {
	Add   string `json:"add"`
	Edit  string `json:"edit"`
	Write string `json:"write"`
}

// apiGroupMembers: the people of a group chat, its rights and what of them the key's owner may.
func (s *Server) apiGroupMembers(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	g, err := s.Matrix.GroupInfo(r.Context(), p.UserID, r.PathValue("room"))
	if err != nil {
		return err
	}
	if g == nil {
		return i18n.E("err.not_found")
	}
	out := []apiMember{}
	for _, m := range g.Members {
		out = append(out, apiMember{m.ID, m.Name, m.Owner, m.Admin, m.Guest, m.Removable})
	}
	writeJSON(w, http.StatusOK, map[string]any{"owner": g.Owner, "channel": g.Channel, "open": g.Open, "writer": g.Writer, "topic": g.Topic,
		"rights": apiRights(g.Rights), "can_add": g.CanAdd, "can_edit": g.CanEdit, "members": out})
	return nil
}

// apiGroupAdd adds people to a group chat: {"user_ids": […]}.
func (s *Server) apiGroupAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Matrix.AddToGroup(r.Context(), p.UserID, r.PathValue("room"), in.UserIDs); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiGroupRemove takes someone out of a group chat (the owner), or the key's owner leaves it (their own ID).
func (s *Server) apiGroupRemove(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var err error
	if user := r.PathValue("user"); user == p.UserID {
		err = s.Matrix.LeaveGroup(r.Context(), p.UserID, r.PathValue("room"))
	} else {
		err = s.Matrix.RemoveFromGroup(r.Context(), p.UserID, r.PathValue("room"), user)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiGroupRename changes a group chat (the owner): {"name": "…"} — a new name, {"channel": true|false} — a channel
// or a group chat, {"owner": "user id"} — the chat handed to them, {"admin": "user id", "on": true|false} — an
// admin, {"topic": "…"} — its description,
// {"rights": {"add", "edit", "write": "all"|"admins"|"owner"}} — who adds people, changes the chat and writes,
// {"open": true|false} — any employee finds it and joins, or by invitation only.
func (s *Server) apiGroupRename(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		Name    string     `json:"name"`
		Channel *bool      `json:"channel"`
		Owner   string     `json:"owner"`
		Admin   string     `json:"admin"`
		On      bool       `json:"on"`
		Topic   *string    `json:"topic"`
		Rights  *apiRights `json:"rights"`
		Open    *bool      `json:"open"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	room := r.PathValue("room")
	var err error
	switch {
	case in.Rights != nil:
		err = s.Matrix.SetRights(r.Context(), p.UserID, room, matrixsync.Rights(*in.Rights))
	case in.Channel != nil:
		err = s.Matrix.SetChannel(r.Context(), p.UserID, room, *in.Channel)
	case in.Open != nil:
		err = s.Matrix.SetOpen(r.Context(), p.UserID, room, *in.Open)
	case in.Owner != "":
		err = s.Matrix.SetOwner(r.Context(), p.UserID, room, in.Owner)
	case in.Admin != "":
		err = s.Matrix.SetAdmin(r.Context(), p.UserID, room, in.Admin, in.On)
	case in.Topic != nil:
		err = s.Matrix.SetGroupTopic(r.Context(), p.UserID, room, *in.Topic)
	default:
		err = s.Matrix.RenameGroup(r.Context(), p.UserID, room, in.Name)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiOpenChats lists the open group chats and channels (?q= — by their name and description): any
// employee joins them (POST …/rooms/{room}/join); none for guests.
func (s *Server) apiOpenChats(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	cs, err := s.Matrix.OpenChats(r.Context(), p.UserID, r.URL.Query().Get("q"))
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, c := range cs {
		out = append(out, map[string]any{"room": c.ID, "name": c.Name, "topic": c.Topic, "channel": c.Channel, "members": c.Members, "joined": c.Joined})
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": out})
	return nil
}

// apiJoinOpen: the key's owner joins an open chat.
func (s *Server) apiJoinOpen(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	if err := s.Matrix.JoinOpen(r.Context(), p.UserID, r.PathValue("room")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiSearch finds messages by their words: ?q=…&room=… (no room — in all the key owner's chats), newest first.
func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	hits, err := s.Matrix.Search(r.Context(), p.UserID, r.URL.Query().Get("q"), r.URL.Query().Get("room"))
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, m := range hits {
		out = append(out, map[string]any{"room": m.RoomID, "message": toAPIMessage(m)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiPins: the chat's pinned messages, the latest pin first.
func (s *Server) apiPins(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	pins, err := s.Matrix.Pins(r.Context(), p.UserID, r.PathValue("room"))
	if err != nil {
		return err
	}
	out := []apiMessage{}
	for _, m := range pins {
		out = append(out, toAPIMessage(m))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiPin pins a message (PUT) or takes the pin back (DELETE).
func (s *Server) apiPin(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	if err := s.Matrix.Pin(r.Context(), p.UserID, r.PathValue("room"), r.PathValue("id"), r.Method == http.MethodPut); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiRoomMark changes the key owner's own mark of a chat: {"mark": "favorite" | "mute" | "unread" | "hidden", "on": true}.
func (s *Server) apiRoomMark(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		Mark string `json:"mark"`
		On   bool   `json:"on"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Matrix.RoomMark(r.Context(), p.UserID, r.PathValue("room"), in.Mark, in.On); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiPoll starts a poll in the chat: {"question", "answers": [...], "multi", "hidden", "thread"} → the poll.
func (s *Server) apiPoll(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		Question string   `json:"question"`
		Answers  []string `json:"answers"`
		Multi    bool     `json:"multi"`
		Hidden   bool     `json:"hidden"`
		Thread   string   `json:"thread"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m, err := s.Matrix.CreatePoll(r.Context(), p.UserID, r.PathValue("room"), in.Thread, in.Question, in.Answers, in.Multi, in.Hidden)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAPIMessage(m))
	return nil
}

// apiPollAction votes in a poll (vote: {"answers": [ids]}, none — the vote is taken back) or ends one's own (end);
// the answer is the poll as it is now.
func (s *Server) apiPollAction(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var m matrixsync.Message
	var err error
	switch r.PathValue("action") {
	case "vote":
		var in struct {
			Answers []string `json:"answers"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		m, err = s.Matrix.Vote(r.Context(), p.UserID, r.PathValue("room"), r.PathValue("id"), in.Answers)
	case "end":
		m, err = s.Matrix.EndPoll(r.Context(), p.UserID, r.PathValue("room"), r.PathValue("id"), s.tr(r, "im.poll_ended"))
	default:
		return i18n.E("err.not_found")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIMessage(m))
	return nil
}

// apiChatMedia is what was sent to the chat, newest first: the messages with a file (media — pictures and videos,
// files, audio) and the links written in messages.
func (s *Server) apiChatMedia(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	m, err := s.chatMedia(r, p.UserID, r.PathValue("room"))
	if err != nil {
		return err
	}
	list := func(ms []matrixsync.Message) []apiMessage {
		out := []apiMessage{}
		for _, m := range ms {
			out = append(out, toAPIMessage(m))
		}
		return out
	}
	type link struct {
		URL        string    `json:"url"`
		MessageID  string    `json:"message_id"`
		ThreadRoot string    `json:"thread_root,omitempty"`
		Sender     string    `json:"sender"`
		At         time.Time `json:"at"`
	}
	links := []link{}
	for _, l := range m.Links {
		links = append(links, link{l.URL, l.MessageID, l.ThreadRoot, l.SenderName, l.At})
	}
	writeJSON(w, http.StatusOK, map[string]any{"media": list(m.Media), "files": list(m.Files), "audio": list(m.Audio), "links": links})
	return nil
}
