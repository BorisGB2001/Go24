package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
)

// crmCardChat is «Чат» of a card: its group chat, made by the first one who opens it (the card's
// responsible and observers in it, the first message names the card with its link); whoever opens it later — and the
// card's people who are not in it yet — join it. Anyone who may see the card may open its chat.
func (s *Server) crmCardChat(r *http.Request, userID, kind, id string) (string, error) {
	if err := s.needMatrix(); err != nil {
		return "", err
	}
	ctx, entity := r.Context(), crmEntities[kind]
	room, err := s.CRM.ChatOf(ctx, entity, id)
	if err != nil {
		return "", err
	}
	people, err := s.CRM.ChatPeople(ctx, entity, id)
	if err != nil {
		return "", err
	}
	if room != "" {
		return room, s.crmChatJoin(ctx, room, append(people, userID))
	}
	label, title, err := s.crmChatName(r, entity, id)
	if err != nil {
		return "", err
	}
	if room, err = s.Matrix.CreateCardGroup(ctx, userID, label+": "+title, people); err != nil {
		return "", err
	}
	made, err := s.CRM.SetChat(ctx, userID, entity, id, room)
	if err != nil {
		return "", err
	}
	if !made { // someone made it a moment before: theirs is the card's
		_ = s.Matrix.LeaveGroup(ctx, userID, room)
		if room, err = s.CRM.ChatOf(ctx, entity, id); err != nil {
			return "", err
		}
		return room, s.crmChatJoin(ctx, room, append(people, userID))
	}
	link := strings.TrimRight(s.BaseURL, "/") + "/crm/" + kind + "/" + id
	if _, err := s.Matrix.Send(ctx, userID, room, label+" «"+title+"»\n"+link, ""); err != nil {
		s.Log.Warn("crm chat: first message", "err", err)
	}
	return room, nil
}

// crmChatJoin brings the people into the card's chat: one who is in it (its maker first, then the card's people)
// invites the rest.
func (s *Server) crmChatJoin(ctx context.Context, room string, people []string) error {
	var err error
	for _, by := range people {
		if err = s.Matrix.AddToGroup(ctx, by, room, people); err == nil {
			return nil
		}
	}
	return err
}

// crmChatName: what the card is («Сделка», a smart process's name…) and its title.
func (s *Server) crmChatName(r *http.Request, entity, id string) (string, string, error) {
	label := s.tr(r, "crm."+entity)
	if entity == "deal" {
		d, err := s.CRM.Deal(r.Context(), id)
		if err != nil {
			return "", "", err
		}
		if d.TypeID != "" {
			t, err := s.CRM.Type(r.Context(), d.TypeID)
			if err != nil {
				return "", "", err
			}
			label = t.Name
		}
		return label, d.Title, nil
	}
	title, err := s.CRM.CardTitle(r.Context(), entity, id)
	return label, title, err
}

// crmChat: POST /crm/{kind}/{id}/chat — «Чат» opens the card's chat in the messenger.
func (s *Server) crmChat(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if crmEntities[kind] == "" {
		http.NotFound(w, r)
		return
	}
	back := "/crm/" + kind + "/" + id
	err := s.crmCheck(r, sess, kind, id, "read")
	var room string
	if err == nil {
		room, err = s.crmCardChat(r, sess.UserID, kind, id)
	}
	if err != nil {
		s.crmErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/im?room="+url.QueryEscape(room), http.StatusSeeOther)
}

// POST /api/v1/crm/{kind}/{id}/chat — the card's chat, made or joined as by «Чат»: {"room": …, "url": …}.
func (s *Server) apiCRMChat(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	room, err := s.crmCardChat(r, p.UserID, r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"room": room, "url": "/im?room=" + url.QueryEscape(room)})
	return nil
}

// CRMChatJoin brings the card's responsible and observers into its chat, when it has one — in the background: the
// card is saved already, the messenger may be slow.
func (s *Server) CRMChatJoin(ctx context.Context, entity, id string) {
	room, err := s.CRM.ChatOf(ctx, entity, id)
	if err != nil || room == "" || s.Matrix == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		people, err := s.CRM.ChatPeople(ctx, entity, id)
		if err == nil {
			err = s.crmChatJoin(ctx, room, people)
		}
		if err != nil {
			s.Log.Warn("crm chat: join", "entity", entity, "err", err)
		}
	}()
}
