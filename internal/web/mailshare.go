package web

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/matrixsync"
	"go24/internal/storages"
)

// «Поделиться» of a letter «Создать → Сообщение в Ленте / Чат» of a letter (and Outlook's «Поделиться
// в Teams»): a post of the feed (to all, a project or the people of the correspondence) or a message in a chat (a new
// group chat of the people of the correspondence or a chat of the user's) — the subject, who wrote it and when, the
// text, the letter's files and the user's word above it. The letter shows where it went.

// sharePerson is an employee of the letter's correspondence.
type sharePerson struct{ ID, Name string }

// mailPeople are go24's employees among the senders and the addressees of the conversation, the user aside.
func (s *Server) mailPeople(ctx context.Context, sess auth.Session, thread []mail.Message) ([]sharePerson, error) {
	var addrs []string
	for _, m := range thread {
		addrs = append(addrs, strings.ToLower(m.FromAddr))
		for _, a := range append(splitAddrs(m.To), splitAddrs(m.CC)...) {
			addrs = append(addrs, strings.ToLower(a.Address))
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text, name FROM users WHERE lower(email) = ANY($1) AND active AND NOT guest AND id::text <> $2
		ORDER BY name LIMIT 50`, addrs, sess.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sharePerson
	for rows.Next() {
		var p sharePerson
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// mailShareExtras: what the menu «Поделиться» offers and where the conversation went already.
func (s *Server) mailShareExtras(r *http.Request, sess auth.Session, ids []string, thread []mail.Message, data map[string]any) error {
	ctx := r.Context()
	shares, err := s.Mail.LetterShares(ctx, sess.UserID, ids)
	if err != nil {
		return err
	}
	data["LetterShares"] = shares
	if s.Feed == nil {
		return nil
	}
	if data["SharePeople"], err = s.mailPeople(ctx, sess, thread); err != nil {
		return err
	}
	projects, err := s.Tasks.ListProjects(ctx, sess.UserID)
	if err != nil {
		return err
	}
	var mine []sharePerson // the projects the user may post to, as the feed's form has them
	for _, p := range projects {
		if p.CanWork() && p.MyRole != "admin" {
			mine = append(mine, sharePerson{p.ID, p.Name})
		}
	}
	data["ShareProjects"] = mine
	data["Share"], data["ShareChat"] = true, s.Matrix != nil
	return nil
}

// mailShareText is the letter as a post or a message carries it: the user's word, the subject, who wrote it and when,
// its text — cut to n characters.
func (s *Server) mailShareText(r *http.Request, m mail.Message, note string, n int) string {
	head := s.tr(r, "task.letter_head", m.From()+" <"+m.FromAddr+">", m.SentAt.Local().Format(s.tr(r, "format.datetime")))
	subject := strings.TrimSpace(m.Subject)
	if subject == "" {
		subject = s.tr(r, "mail.no_subject")
	}
	text := "✉ " + subject + "\n" + head + "\n\n" + strings.TrimSpace(m.Body)
	if note = strings.TrimSpace(note); note != "" {
		text = note + "\n\n" + text
	}
	return clipText(text, n)
}

// mailShare sends the letter to the feed (do=post: to — "" all, a project's id, "people" with users) or a chat
// (do=chat: room — a chat of the user's, or "" for a new one of users named name).
func (s *Server) mailShare(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	m, err := s.Mail.Message(ctx, sess.UserID, r.PathValue("letter"))
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	back := mailURL(r.FormValue("folder"), m.ID)
	_ = r.ParseForm()
	var files []mail.Attachment
	if r.FormValue("files") == "1" {
		list, err := s.Mail.Files(ctx, sess.UserID, m.ID)
		if err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		for _, f := range list {
			if len(files) == maxPostFiles {
				break
			}
			a, err := s.Mail.ReadFile(ctx, sess.UserID, f.ID)
			if err != nil {
				s.mailErr(w, r, err, back)
				return
			}
			files = append(files, a)
		}
	}
	note := clipText(r.FormValue("note"), 2000)
	switch r.FormValue("do") {
	case "post":
		if s.Feed == nil {
			http.NotFound(w, r)
			return
		}
		id, err := s.mailToFeed(r, sess, m, note, files)
		if err == nil {
			err = s.Mail.LinkShare(ctx, sess.UserID, m.ID, "post", id, clipText(m.Subject, 300))
		}
		if err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		s.Log.Info("letter to the feed", "user_id", sess.UserID, "letter", m.ID, "post", id)
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, "mail.share.posted")), http.StatusSeeOther)
	case "chat":
		if s.Matrix == nil {
			s.mailErr(w, r, i18n.E("err.messenger"), back)
			return
		}
		room, title, err := s.mailToChat(r, sess, m, note, files)
		if err == nil {
			err = s.Mail.LinkShare(ctx, sess.UserID, m.ID, "chat", room, title)
		}
		if err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		s.Log.Info("letter to a chat", "user_id", sess.UserID, "letter", m.ID, "room", room)
		http.Redirect(w, r, imRoomURL(room), http.StatusSeeOther)
	default:
		s.mailErr(w, r, i18n.E("err.bad_form"), back)
	}
}

// mailToFeed publishes the letter as a post with its files; the post is gone again if any of it fails.
func (s *Server) mailToFeed(r *http.Request, sess auth.Session, m mail.Message, note string, files []mail.Attachment) (string, error) {
	ctx := r.Context()
	var to feed.Audience
	project := r.FormValue("to")
	if project == "people" {
		project, to.Users = "", r.Form["users"]
		if len(to.Users) == 0 {
			return "", i18n.E("err.post_to")
		}
	}
	title := clipText(strings.TrimSpace(reSubjectPrefix.ReplaceAllString(m.Subject, "")), 300)
	id, err := s.Feed.CreatePostTo(ctx, sess.UserID, project, to, title, s.mailShareText(r, m, note, 20000), false)
	if err != nil {
		return "", err
	}
	for _, a := range files {
		name := path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
		k := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
		if err = s.Storages.Put(ctx, sess.UserID, false, "", k, bytes.NewReader(a.Data), int64(len(a.Data)), a.ContentType); err != nil {
			break
		}
		if _, err = s.Storages.AddFile(ctx, storages.NewFile{Key: k, Name: name, ContentType: a.ContentType, Size: int64(len(a.Data)),
			PostID: id, UploadedBy: sess.UserID}); err != nil {
			break
		}
	}
	if err == nil && s.Matrix != nil { // kept in Matrix: the post exists only if Matrix took it
		if perr := s.Matrix.PublishPost(ctx, id); perr != nil {
			s.Log.Error("feed post to matrix", "err", perr)
			err = i18n.E("err.messenger")
		}
	}
	if err != nil {
		_ = s.Feed.DeletePost(ctx, sess.UserID, id)
		return "", err
	}
	return id, nil
}

// mailToChat sends the letter with its files to a chat of the user's, or to a new group chat; the chat and its name.
func (s *Server) mailToChat(r *http.Request, sess auth.Session, m mail.Message, note string, files []mail.Attachment) (room, title string, err error) {
	ctx := r.Context()
	if room = r.FormValue("room"); room != "" {
		rooms, err := s.Matrix.Rooms(ctx, sess.UserID)
		if err != nil {
			return "", "", err
		}
		i := slices.IndexFunc(rooms, func(x matrixsync.Room) bool { return x.ID == room })
		if i < 0 {
			return "", "", i18n.E("err.forbidden")
		}
		title = rooms[i].Name
	} else {
		title = strings.TrimSpace(r.FormValue("name"))
		if title == "" {
			title = clipText(strings.TrimSpace(reSubjectPrefix.ReplaceAllString(m.Subject, "")), 200)
		}
		if room, err = s.Matrix.CreateGroup(ctx, sess.UserID, title, r.Form["users"], false); err != nil {
			return "", "", err
		}
	}
	if _, err = s.Matrix.Reply(ctx, sess.UserID, room, s.mailShareText(r, m, note, 4000), "", ""); err != nil {
		return "", "", err
	}
	for _, a := range files {
		name := path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
		if _, err := s.Matrix.SendFile(ctx, sess.UserID, room, "", name, a.ContentType, bytes.NewReader(a.Data), int64(len(a.Data)), nil); err != nil {
			s.Log.Warn("letter file to a chat", "file", a.Name, "err", err)
		}
	}
	return room, clipText(title, 300), nil
}

// mailShareRooms are the user's chats for «Обсудить в чате» (the menu asks for them when opened).
func (s *Server) mailShareRooms(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	out := []map[string]string{}
	if s.Matrix == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	rooms, err := s.Matrix.Rooms(r.Context(), sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	for _, x := range rooms {
		if x.Kind != "feed" && len(out) < 100 {
			out = append(out, map[string]string{"id": x.ID, "name": x.Name})
		}
	}
	writeJSON(w, http.StatusOK, out)
}
