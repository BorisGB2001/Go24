package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/matrixsync"
	"go24/internal/msgfmt"
	"go24/internal/tasks"
)

// ---------- the messenger inside go24 (Matrix underneath; each person sees their own chats) ----------

func (s *Server) imPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Matrix == nil {
		http.Redirect(w, r, "/my", http.StatusFound)
		return
	}
	rooms, err := s.Matrix.Rooms(r.Context(), sess.UserID)
	data := map[string]any{"Rooms": rooms, "Room": r.URL.Query().Get("room"), "Thread": r.URL.Query().Get("thread"), "At": r.URL.Query().Get("at"),
		"NoAccount": errors.Is(err, matrixsync.ErrNoAccount), "Element": s.Matrix.ElementURL, "Calls": s.Calls != nil,
		"Secure": s.CallURL} // the microphone needs https: voice messages are recorded there
	if err != nil && !errors.Is(err, matrixsync.ErrNoAccount) {
		s.Log.Error("im rooms", "err", err)
		data["Failed"] = true
	}
	data["Tab"] = "all" // the list's tab: «Коллабы», «Каналы», «Звонки» when asked for; «Коллабы» when a collab's chat is open
	switch tab := r.URL.Query().Get("tab"); {
	case tab == "collab" || tab == "channel" || (tab == "calls" && s.Rings != nil):
		data["Tab"] = tab
	}
	set, err := s.imSettingsOf(r.Context(), sess.UserID)
	if err != nil {
		s.Log.Warn("im settings", "err", err)
	}
	data["Settings"], data["Backgrounds"] = set, imBackgrounds
	if s.Rings != nil { // «Звонки» with the missed calls not looked at yet
		data["CallLog"] = true
		if n, err := s.Rings.Unseen(r.Context(), sess.UserID); err == nil {
			data["CallsUnseen"] = n
		} else {
			s.Log.Warn("im calls unseen", "err", err)
		}
	}
	if !tasks.IsGuestCtx(r.Context()) && data["NoAccount"] == false { // open chats one is not in: the «Каналы» tab and the search show them
		s.imOpenChats(r, sess, data)
	}
	if id := r.URL.Query().Get("room"); id != "" {
		for _, rm := range rooms {
			if rm.ID == id {
				data["Current"] = rm
				if pins, err := s.Matrix.Pins(r.Context(), sess.UserID, rm.ID); err == nil {
					data["Pins"] = pins
				}
				if rm.Kind == "group" || rm.Kind == "channel" { // its people in the header; nil — someone else's room of Element, not a group chat
					if g, err := s.Matrix.GroupInfo(r.Context(), sess.UserID, rm.ID); err == nil {
						data["Group"] = g
						data["ReadOnly"] = g != nil && !g.Writer // a channel one only reads
					} else {
						s.Log.Warn("im group", "err", err)
					}
				}
				if rm.Kind == "dm" || rm.Kind == "group" || rm.Kind == "channel" || rm.Kind == "collab" { // a collab's — its settings
					if ttl, can, err := s.Matrix.AutoDelete(r.Context(), sess.UserID, rm.ID); err == nil {
						data["AutoDelete"], data["AutoDeleteCan"], data["AutoDeleteTTLs"] = ttl, can, matrixsync.AutoDeleteTTLs
					} else {
						s.Log.Warn("im auto-delete", "err", err)
					}
				}
				pid := rm.CollabID // a group chat of a collab — or its main chat
				if rm.Kind == "collab" {
					if why := s.Matrix.CollabChatClosed(r.Context(), sess.UserID, rm.ID); why != "" { // its «кто может писать в чат»
						data["ReadOnly"], data["ReadOnlyKey"] = true, matrixsync.ChatClosedKeys[why]
					}
					pid = s.Matrix.RoomCollab(r.Context(), rm.ID)
				} else if (rm.Kind == "group" || rm.Kind == "channel") && pid == "" && !tasks.IsGuestCtx(r.Context()) {
					if g, _ := data["Group"].(*matrixsync.Group); g != nil && g.Owner { // «Добавить чат в коллаб»: to one of mine
						if cs, err := s.Tasks.ListCollabs(r.Context(), sess.UserID, false, ""); err == nil {
							data["CollabTargets"] = cs
						} else {
							s.Log.Warn("im collabs", "err", err)
						}
					}
				}
				if rm.Kind == "collab" || pid != "" {
					data["Tab"] = "collab"
				}
				if pid != "" { // its tasks, drive, calendar, people and chats in the header
					s.imCollabHead(r, sess, data, rooms, rm, pid)
				}
				if c, ok := data["Collab"].(tasks.Collab); ok && rm.Kind == "collab" && !c.Project.Archived && !tasks.IsGuestCtx(r.Context()) {
					if _, off := s.offSection(r.Context(), "/sign"); !off {
						data["CollabSign"] = true // «Документ на подпись»: its guest signs as the client
					}
				}
			}
		}
	}
	s.render(w, r, "im", page{TitleKey: "nav.messenger", Nav: "im", Data: data})
}

// imOpenChats puts into the page the open chats the employee is not in, and the one asked for (?room=) as a preview
// with «Вступить».
func (s *Server) imOpenChats(r *http.Request, sess auth.Session, data map[string]any) {
	cs, err := s.Matrix.OpenChats(r.Context(), sess.UserID, "")
	if err != nil {
		s.Log.Warn("im open chats", "err", err)
		return
	}
	var out []matrixsync.OpenChat
	for _, c := range cs {
		if c.Joined {
			continue
		}
		out = append(out, c)
		if c.ID == r.URL.Query().Get("room") {
			data["OpenChat"], data["ReadOnly"] = c, true
		}
	}
	data["OpenChats"] = out
}

// imCollabHead puts into the header of a collab's chat (its main one or a group chat of it) the collab — its tasks,
// drive, calendar, people, «Пригласить» — and its chats: the main one, the group chats one is in, making one, adding
// one's own group chat; in a group chat of it — taking the chat out (its owner, the collab's owner and moderators).
func (s *Server) imCollabHead(r *http.Request, sess auth.Session, data map[string]any, rooms []matrixsync.Room, rm matrixsync.Room, pid string) {
	c, err := s.Tasks.CollabCard(r.Context(), sess.UserID, pid)
	if err != nil {
		if !errors.Is(err, tasks.ErrNotFound) {
			s.Log.Warn("im collab", "err", err)
		}
		return
	}
	data["Collab"] = c
	if s.Tasks.CanInviteGuests(r.Context(), c.Project, sess.UserID) { // «Пригласить»: guests, else colleagues
		data["CollabInvite"] = "guests"
	} else if ok, _ := s.Tasks.CanInvite(r.Context(), c.Project); ok {
		data["CollabInvite"] = "members"
	}
	main, err := s.Matrix.ProjectRoom(r.Context(), pid)
	if err != nil {
		s.Log.Warn("im collab room", "err", err)
	}
	var chats, mine []matrixsync.Room
	for _, o := range rooms {
		switch {
		case o.CollabID == pid:
			chats = append(chats, o)
		case o.CollabID == "" && (o.Kind == "group" || o.Kind == "channel"):
			mine = append(mine, o) // one may add it, if it is theirs (the server checks)
		}
	}
	data["CollabMain"], data["CollabChats"] = main, chats
	guest := tasks.IsGuestCtx(r.Context())
	if may, _ := s.Tasks.CanInvite(r.Context(), c.Project); !guest && !c.Project.Archived && (may || c.Project.MyRole == "owner") && len(mine) > 0 {
		data["CollabChatAdd"] = mine
	}
	if rm.CollabID != "" && !guest {
		if g, _ := data["Group"].(*matrixsync.Group); (g != nil && g.Owner) || c.Project.CanManage() {
			data["CollabChatRemove"] = true
		}
	}
}

func (s *Server) imFragment(r *http.Request, name string, data any) (string, error) {
	var buf bytes.Buffer
	err := s.pages[s.lang(r)]["im"].ExecuteTemplate(&buf, name, data)
	return buf.String(), err
}

// imMessages: a page of a room (HTML), or a thread with its root.
func (s *Server) imMessages(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	room := r.URL.Query().Get("room")
	var html, more, draft string
	var err error
	if root := r.URL.Query().Get("thread"); root != "" {
		var rootMsg matrixsync.Message
		var replies []matrixsync.Message
		if rootMsg, replies, err = s.Matrix.Thread(r.Context(), sess.UserID, room, root); err == nil {
			s.transcripts(r.Context(), append(replies, rootMsg))
			html, err = s.imFragment(r, "imthread", map[string]any{"Root": rootMsg, "Replies": replies})
		}
		if err == nil { // the thread is open: its replies are read
			if err := s.Matrix.ThreadRead(r.Context(), sess.UserID, room, root); err != nil {
				s.Log.Warn("im thread read", "err", err)
			}
		}
		if err == nil { // the thread's own draft goes into its field
			if draft, err = s.Matrix.ThreadDraft(r.Context(), sess.UserID, room, root); err != nil {
				s.Log.Warn("im thread draft", "err", err)
				err = nil
			}
		}
	} else if at := r.URL.Query().Get("at"); at != "" { // the page around a message: a search result, a pin
		var msgs []matrixsync.Message
		if msgs, more, err = s.Matrix.Around(r.Context(), sess.UserID, room, at); err == nil {
			s.transcripts(r.Context(), msgs)
			html, err = s.imFragment(r, "imlist", msgs)
		}
	} else {
		var msgs []matrixsync.Message
		msgs, more, err = s.Matrix.Messages(r.Context(), sess.UserID, room, r.URL.Query().Get("from"))
		open := false
		if err != nil { // an open chat one is not in yet: its latest messages before joining
			if om, omore, oerr := s.Matrix.OpenMessages(r.Context(), sess.UserID, room, r.URL.Query().Get("from")); oerr == nil {
				msgs, more, err, open = om, omore, nil, true
			}
		}
		if err == nil {
			s.transcripts(r.Context(), msgs)
			html, err = s.imFragment(r, "imlist", msgs)
			if r.URL.Query().Get("from") == "" && !open { // the chat is open: its main timeline is read, the threads when opened
				if err := s.Matrix.MarkRoomRead(r.Context(), sess.UserID, room); err != nil {
					s.Log.Warn("im mark read", "err", err)
				}
			}
		}
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html, "more": more, "draft": draft})
}

func (s *Server) imSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.Matrix.Reply(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("body"), r.FormValue("thread"), r.FormValue("reply"))
	var html string
	if err == nil {
		html, err = s.imFragment(r, "immsg", m)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": m.ID, "html": html})
}

// imEdit changes the text of one's own message; the answer is the message as it is now.
func (s *Server) imEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.Matrix.Edit(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), r.FormValue("body"))
	var html string
	if err == nil {
		html, err = s.imFragment(r, "immsg", m)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": m.ID, "html": html})
}

// imDelete takes back one's own message.
func (s *Server) imDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.Delete(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// imForward copies a message into another chat of the person (to).
func (s *Server) imForward(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.Matrix.Forward(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), r.FormValue("to"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": m.ID, "room": m.RoomID})
}

// imUpload sends a file into the chat (the paper clip, a dropped or pasted file, a voice message).
func (s *Server) imUpload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.jsonErr(w, r, i18n.E("err.file_required"))
		return
	}
	defer f.Close()
	var extra map[string]any
	if d, err := strconv.Atoi(r.FormValue("duration")); err == nil && d > 0 { // a voice message, ms
		extra = map[string]any{"duration": d}
	}
	name := path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/"))
	m, err := s.Matrix.SendFile(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("thread"), name,
		hdr.Header.Get("Content-Type"), f, hdr.Size, extra)
	var html string
	if err == nil {
		html, err = s.imFragment(r, "immsg", m)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": m.ID, "html": html})
}

// transcripts puts the kept text under the voice messages of the page.
func (s *Server) transcripts(ctx context.Context, msgs []matrixsync.Message) {
	var ids []string
	for _, m := range msgs {
		if m.File != nil && m.File.Kind == "audio" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 || s.DB == nil {
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT event_id, body FROM voice_transcripts WHERE event_id = ANY($1)`, ids)
	if err != nil {
		s.Log.Warn("voice transcripts", "err", err)
		return
	}
	text := map[string]string{}
	for rows.Next() {
		var id, body string
		if rows.Scan(&id, &body) == nil {
			text[id] = body
		}
	}
	rows.Close()
	for i := range msgs {
		if f := msgs[i].File; f != nil && text[msgs[i].ID] != "" {
			f.Transcript = text[msgs[i].ID]
		}
	}
}

// maxVoice is the longest recording go24 sends to Whisper (about half an hour of Opus).
const maxVoice = 25 << 20

// imTranscribe gives the text of a voice message (or of any sound file) of the chat.
func (s *Server) imTranscribe(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	text, err := s.transcribe(r, sess.UserID, r.FormValue("room"), r.FormValue("event"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// transcribe: the text of the sound file of a message, made by Whisper on the first request and kept; only someone
// who sees the message gets it (the access is checked every time, the kept text too).
func (s *Server) transcribe(r *http.Request, userID, room, event string) (string, error) {
	f, body, err := s.Matrix.OpenFile(r.Context(), userID, room, event)
	if err != nil {
		return "", err
	}
	defer body.Close()
	if f.Kind != "audio" {
		return "", i18n.E("err.not_found")
	}
	var text string
	if s.DB.QueryRow(r.Context(), `SELECT body FROM voice_transcripts WHERE event_id = $1`, event).Scan(&text) == nil {
		return text, nil
	}
	if s.Speech == nil {
		return "", i18n.E("err.speech_off")
	}
	lang, _, _ := strings.Cut(s.lang(r), "-")
	if text, err = s.Speech.Transcribe(r.Context(), f.Name, io.LimitReader(body, maxVoice), lang); err != nil {
		s.Log.Error("whisper", "err", err)
		return "", i18n.E("err.speech_failed")
	}
	if text == "" {
		text = s.tr(r, "im.voice_silent")
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO voice_transcripts (event_id, room_id, body) VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, event, room, text); err != nil {
		s.Log.Warn("keep transcript", "err", err)
	}
	return text, nil
}

// inlineMedia are the files the browser shows in the page (pictures, sound, video); anything else — and SVG,
// which can carry scripts — is only downloaded.
func inlineMedia(mime string) bool {
	return (strings.HasPrefix(mime, "image/") && !strings.Contains(mime, "svg")) || strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/")
}

// imFileType is how a chat file is given: a picture (not SVG), sound, video or PDF — inline, to be shown in the
// browser (the viewer shows a PDF as «Диск» does), unless asked as a download; anything else — as bytes to save.
func imFileType(name, mime string, download bool) (string, string) {
	if fileKind(name, mime) == mail.PreviewPDF {
		mime = "application/pdf"
	}
	if !inlineMedia(mime) && mime != "application/pdf" {
		return "application/octet-stream", "attachment"
	}
	if download {
		return mime, "attachment"
	}
	return mime, "inline"
}

// imFile gives the file of a chat message to someone who sees the message (?dl=1 — as a download).
func (s *Server) imFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	f, body, err := s.Matrix.OpenFile(r.Context(), sess.UserID, q.Get("room"), q.Get("event"))
	if err != nil {
		s.taskErr(w, r, err, "/im")
		return
	}
	defer body.Close()
	mime, disp := imFileType(f.Name, f.Mime, q.Get("dl") == "1")
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(f.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, body)
}

// imFilePreview is the viewer's view of a chat file, as filePreview of «Диск»: its kind and the HTML of a document.
func (s *Server) imFilePreview(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	f, body, err := s.Matrix.OpenFile(r.Context(), sess.UserID, q.Get("room"), q.Get("event"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	defer body.Close()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, imPreview(f.Name, f.Mime, body))
}

// imPreview is the kind of a file for the viewer and, for a document up to 20 MB, its HTML (mail.Preview).
func imPreview(name, mime string, body io.Reader) map[string]any {
	out := map[string]any{"name": name, "kind": fileKind(name, mime)}
	if out["kind"] == mail.PreviewHTML {
		out["kind"] = mail.PreviewNone
		if data, err := io.ReadAll(io.LimitReader(body, 20<<20+1)); err == nil && len(data) <= 20<<20 {
			if h, ok := mail.Preview(name, mime, data); ok {
				out["kind"], out["html"] = mail.PreviewHTML, h
			}
		}
	}
	return out
}

// imRead: the open chat (or its open thread) got a message while the person was looking at it (or came back to the
// tab).
func (s *Server) imRead(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error
	if root := r.FormValue("thread"); root != "" {
		err = s.Matrix.ThreadRead(r.Context(), sess.UserID, r.FormValue("room"), root)
	} else {
		err = s.Matrix.MarkRoomRead(r.Context(), sess.UserID, r.FormValue("room"))
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// imThreads is «Треды» of the chat: its threads, those with unread replies first (HTML for the side pane).
func (s *Server) imThreads(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Matrix.Threads(r.Context(), sess.UserID, r.URL.Query().Get("room"))
	var html string
	if err == nil {
		html, err = s.imFragment(r, "imthreads", list)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// imReadAll reads all the person's chats («Прочитать всё» of the chat list).
func (s *Server) imReadAll(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.MarkAllRead(r.Context(), sess.UserID); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// imDirect opens (or starts) the private chat with a colleague.
func (s *Server) imDirect(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Matrix == nil {
		http.Redirect(w, r, "/u/"+r.URL.Query().Get("user"), http.StatusFound)
		return
	}
	room, err := s.Matrix.DirectChat(r.Context(), sess.UserID, r.URL.Query().Get("user"))
	if err != nil {
		s.taskErr(w, r, err, "/im")
		return
	}
	http.Redirect(w, r, "/im?room="+room, http.StatusSeeOther)
}

// imNotes opens the person's «Заметки» — the chat with themselves, made on first use.
func (s *Server) imNotes(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Matrix == nil {
		http.Redirect(w, r, "/my", http.StatusFound)
		return
	}
	room, err := s.Matrix.NotesChat(r.Context(), sess.UserID, s.I18n.T(s.lang(r), "im.notes"))
	if err != nil {
		s.taskErr(w, r, err, "/im")
		return
	}
	http.Redirect(w, r, "/im?room="+url.QueryEscape(room), http.StatusSeeOther)
}

// imLive streams the person's new messages (Server-Sent Events), rendered for the page.
func (s *Server) imLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	flusher, ok := w.(http.Flusher)
	if !ok || s.Matrix == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	msgs := make(chan matrixsync.Message, 32)
	counts := make(chan [3]map[string]int, 4)
	go func() {
		if err := s.Matrix.Live(r.Context(), sess.UserID, func(m matrixsync.Message) { msgs <- m },
			func(c, at, th map[string]int) { counts <- [3]map[string]int{c, at, th} }); err != nil && r.Context().Err() == nil {
			s.Log.Error("im live", "err", err)
		}
	}()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case c := <-counts:
			writeUnread(w, c[0], c[1], c[2], false)
			flusher.Flush()
		case m := <-msgs:
			if m.Change != "" { // an edit or a deletion of a message on the screen, a read receipt, someone typing
				ch := map[string]any{"id": m.ID, "room": m.RoomID, "change": m.Change, "body": m.Body, "at": m.At.UnixMilli()}
				if m.Change == "edit" { // the new text as the chat shows it
					ch["text"] = markMentions(msgfmt.HTML(m.Body))
				}
				if m.Call != nil { // a call's line: it is drawn anew (the call is over)
					if html, err := s.imFragment(r, "immsg", m); err == nil {
						ch["html"] = html
					}
				}
				data, _ := json.Marshal(ch)
				fmt.Fprintf(w, "event: change\ndata: %s\n\n", data)
				flusher.Flush()
				continue
			}
			html, err := s.imFragment(r, "immsg", m)
			if err != nil {
				continue
			}
			preview, name := msgfmt.Plain(m.Body), m.SenderName
			if m.Event != "" { // a line about the chat: its text, without a sender
				if t, err := s.imFragment(r, "imevent", m); err == nil {
					preview, name = t, ""
				}
			}
			data, _ := json.Marshal(map[string]any{"id": m.ID, "room": m.RoomID, "thread": m.ThreadRoot, "mine": m.Mine,
				"tome": m.ToMe, "preview": preview, "name": name, "html": html})
			fmt.Fprintf(w, "event: msg\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// imReact puts or takes back a reaction; the answer is the chat (or thread) page again, so the counts are fresh.
func (s *Server) imReact(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.React(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), r.FormValue("emoji")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeUnread sends the chats' unread counts and the unread mentions of the person:
// {"rooms": {room: n}, "mentions": {room: n}, "threads": {room: unread replies in its threads} (only the chats with
// some), "total": n (the threads too), "sound": true — the person wants a sound on a new message («Настройки» of the
// messenger)}.
func writeUnread(w http.ResponseWriter, rooms, mentions, threads map[string]int, sound bool) {
	total := 0
	for _, n := range rooms {
		total += n
	}
	th := map[string]int{}
	for id, n := range threads {
		if n > 0 {
			th[id] = n
			total += n
		}
	}
	at := map[string]int{}
	for id, n := range mentions {
		if n > 0 {
			at[id] = n
		}
	}
	data, _ := json.Marshal(map[string]any{"rooms": rooms, "mentions": at, "threads": th, "total": total, "sound": sound})
	fmt.Fprintf(w, "event: unread\ndata: %s\n\n", data)
}

// imUnread streams only the unread counts (the «Мессенджер» counter of the menu on every page; one tab of a
// browser holds it, see menu.js).
func (s *Server) imUnread(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	flusher, ok := w.(http.Flusher)
	if !ok || s.Matrix == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 10000\n\n")
	flusher.Flush()
	counts := make(chan [3]map[string]int, 4)
	go func() {
		err := s.Matrix.Live(r.Context(), sess.UserID, nil, func(c, at, th map[string]int) { counts <- [3]map[string]int{c, at, th} })
		if err != nil && r.Context().Err() == nil && !errors.Is(err, matrixsync.ErrNoAccount) {
			s.Log.Error("im unread", "err", err)
		}
	}()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			s.touchSeen(r.Context(), sess.UserID) // an open page keeps the person «в сети»
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case c := <-counts:
			set, err := s.imSettingsOf(r.Context(), sess.UserID) // read each time: a change counts at once
			if err != nil {
				s.Log.Warn("im settings", "err", err)
			}
			writeUnread(w, c[0], c[1], c[2], set.Sound)
			flusher.Flush()
		}
	}
}
