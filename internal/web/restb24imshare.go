package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// REST compatible with the portal, the messenger part 6 (GO24DEV-937): im.message.share — a task, a feed post, a
// meeting or a new chat made of a chat message, as its menu makes them; im.notify.answer — the quick answer to a
// notification: a comment of its task, post or meeting.

func init() {
	for name, m := range map[string]portalMethod{
		"im.message.share": {Scope: "im:write", Run: (*Server).portalMessageShare},
		"im.notify.answer": {Scope: "im:write", Run: (*Server).portalNotifyAnswer},
	} {
		portalMethods[strings.ToLower(name)] = m
	}
}

// im.message.share: MESSAGE_ID, DIALOG_ID, TYPE — TASK, POST, CALEND, CHAT → true. The new thing has the message's
// title and text with a link to it, the chat's project when the chat has one, and the message is answered with it in
// its chat, as «Создать задачу» (пост, встречу) of the message's menu does.
func (s *Server) portalMessageShare(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	sess := auth.Session{UserID: c.p.UserID}
	m, chat, projectID, err := s.Matrix.One(ctx, c.p.UserID, room, event)
	if err != nil {
		return portalOut{}, err
	}
	title, body := s.messageTask(c.r, m, chat, s.imMessageURL(room, event))
	switch strings.ToUpper(strings.TrimSpace(portalStr(portalParam(c.in, "TYPE")))) {
	case "TASK":
		group := ""
		if projectID != "" {
			list, err := s.Tasks.ListProjects(ctx, c.p.UserID)
			if err != nil {
				return portalOut{}, err
			}
			for _, p := range list {
				if p.ID == projectID {
					group = p.Key
				}
			}
		}
		if group == "" {
			if group, err = s.portalDefaultGroup(c); err != nil {
				return portalOut{}, err
			}
		}
		pr, err := s.Tasks.ProjectByKey(ctx, c.p.UserID, group)
		if err != nil {
			return portalOut{}, err
		}
		key, err := s.Tasks.CreateTask(ctx, c.p.UserID, pr, tasks.NewTask{Title: title, Description: body, ResponsibleID: c.p.UserID, Priority: 1})
		if err != nil {
			return portalOut{}, err
		}
		s.taskMessageDone(c.r, sess, room, event, key)
	case "POST":
		id, err := s.Feed.CreatePost(ctx, c.p.UserID, projectID, "", body, false)
		if err != nil {
			return portalOut{}, err
		}
		if err := s.Matrix.PublishPost(ctx, id); err != nil {
			s.Log.Warn("post of a message to matrix", "post", id, "err", err)
		}
		s.postMessageDone(c.r, sess, room, event, id)
	case "CALEND":
		start := time.Now().Truncate(time.Hour).Add(time.Hour) // the next whole hour, as the new meeting's form has it
		e := calendar.Event{Title: title, Description: body, Start: start, End: start.Add(time.Hour), ProjectID: projectID}
		var invite []string
		if people, err := s.Matrix.ChatMembers(ctx, c.p.UserID, room); err == nil {
			for _, p := range people {
				if !p.Me && p.ID != "" {
					invite = append(invite, p.ID)
				}
			}
		}
		id, err := s.Calendar.Save(ctx, c.p.UserID, e, invite)
		if err != nil {
			return portalOut{}, err
		}
		s.eventMessageDone(c.r, sess, room, event, id)
	case "CHAT":
		people, err := s.Matrix.ChatMembers(ctx, c.p.UserID, room)
		if err != nil {
			return portalOut{}, err
		}
		var ids []string
		for _, p := range people {
			if !p.Me && p.ID != "" {
				ids = append(ids, p.ID)
			}
		}
		nr, err := s.Matrix.CreateOpenGroup(ctx, c.p.UserID, title, ids, false, false)
		if err != nil {
			return portalOut{}, err
		}
		if _, err := s.Matrix.Send(ctx, c.p.UserID, nr, body, ""); err != nil {
			return portalOut{}, err
		}
		link := strings.TrimRight(s.BaseURL, "/") + "/im?room=" + url.QueryEscape(nr)
		if _, err := s.Matrix.Reply(ctx, c.p.UserID, room, s.tr(c.r, "im.chat_made", title, link), m.ThreadRoot, event); err != nil {
			s.Log.Warn("chat of a message: the answer in the chat", "room", nr, "err", err)
		}
	default:
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "TYPE_ERROR", "err.portal_field", "TYPE")
	}
	return portalOut{Result: true}, nil
}

// im.notify.answer: NOTIFY_ID, ANSWER_TEXT → {"result_message": [the answer's line]}: the text becomes a comment of
// the notification's task, feed post or meeting, as its author wrote it.
func (s *Server) portalNotifyAnswer(c portalCall) (portalOut, error) {
	id := strings.TrimSpace(portalStr(portalParam(c.in, "NOTIFY_ID")))
	if id == "" || id == "0" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "NOTIFY_ID_ERROR", "err.portal_notify")
	}
	text := strings.TrimSpace(portalStr(portalParam(c.in, "ANSWER_TEXT")))
	if text == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ANSWER_TEXT_ERROR", "err.portal_field", "ANSWER_TEXT")
	}
	ctx := c.r.Context()
	it, err := s.Notify.Get(ctx, c.p.UserID, id)
	if errors.Is(err, notify.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return portalOut{}, i18n.E("err.not_found")
	} else if err != nil {
		return portalOut{}, err
	}
	switch {
	case it.TaskKey != "":
		tid, err := s.Tasks.TaskIDByKey(ctx, it.TaskKey)
		if err != nil {
			return portalOut{}, err
		}
		if s.Matrix != nil {
			_, err = s.Matrix.PostTaskReply(ctx, c.p.UserID, tid, "", text)
		} else {
			_, err = s.Tasks.PostReply(ctx, c.p.UserID, tid, "", text)
		}
		if err != nil {
			return portalOut{}, err
		}
	case it.PostID != "":
		if _, err := s.publishComment(c.r, auth.Session{UserID: c.p.UserID}, it.PostID, text, nil, nil); err != nil {
			return portalOut{}, err
		}
	case it.EventID != "":
		if _, err := s.Calendar.AddComment(ctx, c.p.UserID, it.EventID, text, nil); err != nil {
			return portalOut{}, err
		}
	default:
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "NOTIFY_ANSWER_ERROR", "err.portal_notify_answer")
	}
	if err := s.Notify.MarkRead(ctx, c.p.UserID, it.ID); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"result_message": []string{s.tr(c.r, "im.notify_answered")}}}, nil
}
