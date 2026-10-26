package web

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/matrixsync"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- a task made of a chat message («Создать задачу» in the message's menu) ----------

// imSource is the message a new task is made of.
type imSource struct {
	Room, Event, Sender string
}

// imMessageURL is the go24 address of a chat message (the chat opened at it).
func (s *Server) imMessageURL(room, event string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/im?room=" + url.QueryEscape(room) + "&at=" + url.QueryEscape(event)
}

// taskFromMessage fills the new task's form: the title of the message's first line, the message and a link to it in
// the description, the chat's project (a collab too) when the chat has one.
func (s *Server) taskFromMessage(r *http.Request, sess auth.Session, room, event string, f *taskForm) {
	m, chat, projectID, err := s.Matrix.One(r.Context(), sess.UserID, room, event)
	if err != nil {
		return
	}
	f.IM = &imSource{Room: room, Event: event, Sender: m.SenderName}
	f.Title, f.Description = s.messageTask(r, m, chat, s.imMessageURL(room, event))
	if projectID != "" {
		for _, p := range f.Projects {
			if p.ID == projectID {
				f.Project = p.Key
			}
		}
	}
}

// messageTask is the title (the message's first line) and the description (who wrote it, when, where; the message;
// the link to it) of a task made of a chat message.
func (s *Server) messageTask(r *http.Request, m matrixsync.Message, chat, link string) (string, string) {
	body, title := m.Body, ""
	if m.File != nil {
		body = m.File.Name
	}
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			title = clipText(line, 200)
			break
		}
	}
	if title == "" {
		title = s.tr(r, "task.message_from", m.SenderName)
	}
	when := m.At.Local().Format(s.tr(r, "format.datetime"))
	head := s.tr(r, "task.message_head", m.SenderName, when)
	if chat != "" {
		head = s.tr(r, "task.message_head_chat", m.SenderName, when, chat)
	}
	return title, clipText(head+"\n\n"+body+"\n\n"+link, 20000)
}

// taskMessageDone answers the message in its chat with the new task (as the person who made it) and copies the
// message's file to the task.
func (s *Server) taskMessageDone(r *http.Request, sess auth.Session, room, event, key string) {
	ctx := r.Context()
	m, _, _, err := s.Matrix.One(ctx, sess.UserID, room, event)
	if err != nil {
		s.Log.Warn("task of a message", "task", key, "err", err)
		return
	}
	id, err := s.Tasks.TaskIDByKey(ctx, key)
	var t tasks.Task
	if err == nil {
		t, err = s.Tasks.TaskByID(ctx, id) // just made by this person
	}
	if err != nil {
		s.Log.Warn("task of a message", "task", key, "err", err)
		return
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/t/" + key
	if _, err := s.Matrix.Reply(ctx, sess.UserID, room, s.tr(r, "im.task_made", key, t.Title, link), m.ThreadRoot, event); err != nil {
		s.Log.Warn("task of a message: the answer in the chat", "task", key, "err", err)
	}
	if m.File == nil || s.Tasks.CanAttach(ctx, sess.UserID, t.ID) != nil {
		return
	}
	name, err := s.messageFileTo(r, sess, room, event, storages.NewFile{TaskID: t.ID})
	if err != nil {
		s.Log.Warn("message file to a task", "task", key, "err", err)
		return
	}
	if err := s.Tasks.FileAdded(ctx, sess.UserID, t.ID, name); err != nil {
		s.Log.Warn("file history", "err", err)
	}
}

// messageFileTo copies the file of a chat message to go24's storage, as a file of what nf says (a task, a post);
// the answer is its name.
func (s *Server) messageFileTo(r *http.Request, sess auth.Session, room, event string, nf storages.NewFile) (string, error) {
	ctx := r.Context()
	file, body, err := s.Matrix.OpenFile(ctx, sess.UserID, room, event)
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxUploadSize+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxUploadSize {
		return "", i18n.E("err.file_too_big")
	}
	name := path.Base(strings.ReplaceAll(file.Name, "\\", "/"))
	k := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
	if err := s.Storages.Put(ctx, sess.UserID, false, "", k, bytes.NewReader(data), int64(len(data)), file.Mime); err != nil {
		return "", err
	}
	nf.Key, nf.Name, nf.ContentType, nf.Size, nf.UploadedBy = k, name, file.Mime, int64(len(data)), sess.UserID
	_, err = s.Storages.AddFile(ctx, nf)
	return name, err
}

// imFileDrive is «Сохранить на Диск» of a message's file: the file is copied to «Мой диск», into
// its «Сохранённые файлы» (made when missing).
func (s *Server) imFileDrive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	d, err := s.Drive.Open(ctx, sess.UserID, "my")
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	folder, err := s.driveFolderNamed(r, sess, d, s.tr(r, "im.drive_folder"))
	if err == nil {
		var f *matrixsync.File
		var body io.ReadCloser
		if f, body, err = s.Matrix.OpenFile(ctx, sess.UserID, r.FormValue("room"), r.FormValue("event")); err == nil {
			body.Close()
			err = s.Drive.Fits(ctx, d, f.Size) // the drive's limit
		}
	}
	if err == nil {
		_, err = s.messageFileTo(r, sess, r.FormValue("room"), r.FormValue("event"),
			storages.NewFile{Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder)})
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": s.tr(r, "im.drive_saved"), "url": d.Path() + "?folder=" + url.QueryEscape(folder)})
}

// driveFolderNamed is the folder of that name at the top of the drive, made when missing.
func (s *Server) driveFolderNamed(r *http.Request, sess auth.Session, d drive.Drive, name string) (string, error) {
	folders, _, _, err := s.Drive.List(r.Context(), d, "")
	if err != nil {
		return "", err
	}
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) {
			return f.ID, nil
		}
	}
	return s.Drive.CreateFolder(r.Context(), sess.UserID, d, "", name)
}

// imMail is «Переслать по почте» of a message: a new letter of the person's first mailbox with the message (who and
// when, its text, the link to it) and its file attached; the answer is where the letter opens in the editor.
func (s *Server) imMail(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	room, event := r.FormValue("room"), r.FormValue("event")
	boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
	if err == nil && len(boxes) == 0 {
		err = i18n.E("err.mail_no_box")
	}
	var m matrixsync.Message
	var chat string
	if err == nil {
		m, chat, _, err = s.Matrix.One(ctx, sess.UserID, room, event)
	}
	var id string
	if err == nil {
		title, text := s.messageTask(r, m, chat, s.imMessageURL(room, event))
		id, err = s.Mail.SaveDraft(ctx, sess.UserID, mail.Draft{MailboxID: boxes[0].ID, Subject: title, HTML: "<p><br></p>" + mail.TextHTML(text)})
	}
	if err == nil && m.File != nil { // the file goes along, as an attachment
		var f *matrixsync.File
		var body io.ReadCloser
		if f, body, err = s.Matrix.OpenFile(ctx, sess.UserID, room, event); err == nil {
			defer body.Close()
			var data []byte
			if data, err = io.ReadAll(io.LimitReader(body, maxUploadSize+1)); err == nil && int64(len(data)) > maxUploadSize {
				err = i18n.E("err.file_too_big")
			}
			if err == nil {
				_, err = s.Mail.AttachDraft(ctx, sess.UserID, id, mail.Attachment{Name: path.Base(strings.ReplaceAll(f.Name, "\\", "/")), ContentType: f.Mime, Data: data}, false)
			}
		}
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": "/mail?letter=" + url.QueryEscape(id)})
}
