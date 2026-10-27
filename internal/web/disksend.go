package web

import (
	"bytes"
	"io"
	"net/http"
	"net/url"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Отправить в чат» of a file of the drive: the file goes into a chat as a message ----------

// diskChat is a chat of the picker of «Отправить в чат».
type diskChat struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// diskChats lists the chats the person may send a file to, newest activity first (the feed's rooms are not chats).
func (s *Server) diskChats(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.needMatrix(); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	rooms, err := s.Matrix.Rooms(r.Context(), sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := []diskChat{}
	for _, x := range rooms {
		if x.Kind != "feed" {
			out = append(out, diskChat{x.ID, x.Name, x.Kind})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// sendToChat posts a file the person can read into the chat under their name, as a file sent from the chat itself.
func (s *Server) sendToChat(r *http.Request, sess auth.Session, id, room string) (matrixsync.Message, error) {
	if room == "" {
		return matrixsync.Message{}, i18n.E("err.disk_send_room")
	}
	if !reFileID.MatchString(id) {
		return matrixsync.Message{}, tasks.ErrNotFound
	}
	m, err := s.fileAccess(r, sess, id)
	if err != nil {
		return matrixsync.Message{}, err
	}
	if m.Drive == "" {
		return matrixsync.Message{}, tasks.ErrNotFound // an attachment, not a file of a drive
	}
	if err := s.needMatrix(); err != nil {
		return matrixsync.Message{}, err
	}
	return s.fileToChat(r, sess, m, room)
}

// fileToChat sends the stored file's bytes into the chat.
func (s *Server) fileToChat(r *http.Request, sess auth.Session, m storages.FileMeta, room string) (matrixsync.Message, error) {
	var src io.Reader = bytes.NewReader(nil)
	if m.Size > 0 {
		obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
		if err != nil {
			return matrixsync.Message{}, err
		}
		defer obj.Close()
		src = obj
	}
	return s.Matrix.SendFile(r.Context(), sess.UserID, room, "", m.Name, m.ContentType, src, m.Size, nil)
}

// diskSend is «Отправить в чат» of the page: POST room=…; the answer names the chat to open.
func (s *Server) diskSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if tasks.IsGuestCtx(r.Context()) {
		s.jsonErr(w, r, i18n.E("err.forbidden"))
		return
	}
	room := r.FormValue("room")
	if _, err := s.sendToChat(r, sess, r.PathValue("id"), room); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": s.tr(r, "disk.sent"), "url": "/im?room=" + url.QueryEscape(room)})
}

// apiDiskSend is POST /api/v1/disk/files/{id}/send {"room"}: the file goes into the chat as the key's employee does
// it (the key needs «im:write» too).
func (s *Server) apiDiskSend(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !p.Can("im:write") {
		return i18n.E("err.api_no_scope", "im:write")
	}
	var in struct{ Room string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m, err := s.sendToChat(r, auth.Session{UserID: p.UserID}, r.PathValue("id"), in.Room)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": m.ID, "room": in.Room})
	return nil
}
