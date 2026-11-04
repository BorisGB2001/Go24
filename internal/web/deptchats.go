package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// «Чаты и каналы» of departments, as in the portal company structure: administrators and the department's head
// and deputies make a chat or a channel of the department or tie one of their group chats to it; go24 brings the
// department's people into it and takes out those who left the department (those the department brought).

// deptChatsPage: GET /company/departments/{id}/chats — the department's chats, and the forms to make and tie them.
func (s *Server) deptChatsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	name, err := s.Company.DepartmentName(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/company/structure")
		return
	}
	can, err := s.Company.CanManageDeptChats(r.Context(), sess.UserID, sess.Admin, id)
	if err != nil {
		s.taskErr(w, r, err, "/company/structure")
		return
	}
	list, err := s.Company.DeptChats(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/company/structure")
		return
	}
	data := map[string]any{"ID": id, "Name": name, "Chats": list, "Can": can}
	// the viewer's group chats and channels: to tie, and the names of the tied ones as the messenger shows them now
	if chats := s.groupChats(); chats != nil {
		rooms, err := chats.Rooms(r.Context(), sess.UserID)
		if err != nil {
			s.Log.Warn("department chats rooms", "user_id", sess.UserID, "err", err)
		}
		tied := map[string]int{}
		for i, c := range list {
			tied[c.Room] = i
		}
		var free []map[string]string
		for _, rm := range rooms {
			if rm.Kind != "group" && rm.Kind != "channel" {
				continue
			}
			if i, ok := tied[rm.ID]; ok {
				if rm.Name != "" && rm.Name != list[i].Name {
					list[i].Name = rm.Name
					_ = s.Company.SetDeptChatName(r.Context(), rm.ID, rm.Name)
				}
				continue
			}
			free = append(free, map[string]string{"ID": rm.ID, "Name": rm.Name, "Kind": rm.Kind})
		}
		data["Rooms"], data["Messenger"] = free, true
	}
	s.render(w, r, "deptchats", page{TitleKey: "deptchat.title", Nav: "structure", Data: data})
}

// deptChatsPost: POST /company/departments/{id}/chats — op=create (name, channel, sub) | link (room, sub) |
// unlink (room) | sub (room, sub).
func (s *Server) deptChatsPost(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/company/departments/" + id + "/chats"
	op := r.FormValue("op")
	in := deptChatIn{Room: r.FormValue("room"), Name: r.FormValue("name"), Channel: r.FormValue("channel") == "1", Sub: r.FormValue("sub") == "1"}
	room, err := s.deptChatOp(r.Context(), sess.UserID, sess.Admin, id, op, in)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("department chat "+op, "user_id", sess.UserID, "id", id, "room", room)
	if op == "create" {
		back = imRoomURL(room)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

type deptChatIn struct {
	Room    string `json:"room"`
	Name    string `json:"name"`
	Channel bool   `json:"channel"`
	Sub     bool   `json:"sub"`
}

// deptChatOp does what the page or the REST asks for the department's chats; it answers the room.
func (s *Server) deptChatOp(ctx context.Context, userID string, admin bool, deptID, op string, in deptChatIn) (string, error) {
	chats := s.groupChats()
	if chats == nil {
		return "", i18n.E("err.not_found")
	}
	dept, err := s.Company.DepartmentName(ctx, deptID)
	if err != nil {
		return "", err
	}
	if ok, err := s.Company.CanManageDeptChats(ctx, userID, admin, deptID); err != nil {
		return "", err
	} else if !ok {
		return "", company.ErrForbidden
	}
	c := company.DeptChat{DepartmentID: deptID, Room: in.Room, Name: strings.TrimSpace(in.Name), Channel: in.Channel, Sub: in.Sub, OwnerID: userID}
	switch op {
	case "create":
		if c.Name == "" {
			c.Name = dept
		}
		people, err := s.Company.DeptPeople(ctx, deptID, c.Sub)
		if err != nil {
			return "", err
		}
		people = slices.DeleteFunc(people, func(id string) bool { return id == userID })
		if len(people) == 0 {
			return "", i18n.E("err.dept_chat_people")
		}
		if c.Room, err = chats.CreateGroup(ctx, userID, c.Name, people, c.Channel); err != nil {
			return "", err
		}
		if err := s.Company.AddDeptChat(ctx, c); err != nil {
			return "", err
		}
		return c.Room, s.Company.SetDeptChatBrought(ctx, deptID, c.Room, people)
	case "link": // one of the person's group chats or channels, which they may invite into
		rooms, err := chats.Rooms(ctx, userID)
		if err != nil {
			return "", err
		}
		i := slices.IndexFunc(rooms, func(rm matrixsync.Room) bool { return rm.ID == c.Room && (rm.Kind == "group" || rm.Kind == "channel") })
		if i < 0 {
			return "", i18n.E("err.not_found")
		}
		c.Name, c.Channel = rooms[i].Name, rooms[i].Kind == "channel"
		if err := s.Company.AddDeptChat(ctx, c); err != nil {
			return "", err
		}
		if err := s.deptChatSync(ctx, c); err != nil {
			_ = s.Company.RemoveDeptChat(ctx, deptID, c.Room)
			return "", err
		}
		return c.Room, nil
	case "unlink":
		return c.Room, s.Company.RemoveDeptChat(ctx, deptID, c.Room)
	case "sub":
		if err := s.Company.SetDeptChatSub(ctx, deptID, c.Room, c.Sub); err != nil {
			return "", err
		}
		list, err := s.Company.DeptChats(ctx, deptID)
		if err != nil {
			return "", err
		}
		for _, t := range list {
			if t.Room == c.Room {
				return c.Room, s.deptChatSync(ctx, t)
			}
		}
		return c.Room, nil
	}
	return "", i18n.E("err.bad_form")
}

// deptChatSync brings the department's new people into its chat and takes out those it brought who left the
// department; one who left the chat by themselves is not brought back.
func (s *Server) deptChatSync(ctx context.Context, c company.DeptChat) error {
	chats := s.groupChats()
	if chats == nil {
		return nil
	}
	want, err := s.Company.DeptPeople(ctx, c.DepartmentID, c.Sub)
	if err != nil {
		return err
	}
	want = slices.DeleteFunc(want, func(id string) bool { return id == c.OwnerID })
	had, err := s.Company.DeptChatBrought(ctx, c.DepartmentID, c.Room)
	if err != nil {
		return err
	}
	var add []string
	for _, id := range want {
		if !slices.Contains(had, id) {
			add = append(add, id)
		}
	}
	if len(add) > 0 {
		if err := chats.AddToGroup(ctx, c.OwnerID, c.Room, add); err != nil {
			return err
		}
	}
	for _, id := range had {
		if !slices.Contains(want, id) {
			if err := chats.RemoveFromGroup(ctx, c.OwnerID, c.Room, id); err != nil { // left the chat already, or is its admin
				s.Log.Warn("department chat remove", "room", c.Room, "user_id", id, "err", err)
			}
		}
	}
	if len(add) == 0 && len(had) == len(want) {
		return nil
	}
	return s.Company.SetDeptChatBrought(ctx, c.DepartmentID, c.Room, want)
}

// syncDeptChats brings all the departments' chats, and the members of groups with departments, in line with the
// structure (after a change of it, and from the worker's loop).
func (s *Server) syncDeptChats(ctx context.Context) {
	s.syncGroupDepts(ctx, "")
	if s.groupChats() == nil {
		return
	}
	list, err := s.Company.DeptChats(ctx, "")
	if err != nil {
		s.Log.Warn("department chats", "err", err)
		return
	}
	for _, c := range list {
		if err := s.deptChatSync(ctx, c); err != nil {
			s.Log.Warn("department chat sync", "department", c.DepartmentID, "room", c.Room, "err", err)
		}
	}
}

// DeptChatsLoop keeps the departments' chats in line with the structure every few minutes (changes made by the
// REST, invitations accepted, dismissals).
func (s *Server) DeptChatsLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncDeptChats(ctx)
		}
	}
}

type apiDeptChat struct {
	Room    string `json:"room"`
	Name    string `json:"name"`
	Channel bool   `json:"channel"`
	Sub     bool   `json:"sub"`
	OwnerID string `json:"owner_id"`
	URL     string `json:"url"`
}

// GET /api/v1/departments/{id}/chats — the department's chats and channels.
func (s *Server) apiDeptChats(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	if _, err := s.Company.DepartmentName(r.Context(), id); err != nil {
		return err
	}
	list, err := s.Company.DeptChats(r.Context(), id)
	if err != nil {
		return err
	}
	out := make([]apiDeptChat, len(list))
	for i, c := range list {
		out[i] = apiDeptChat{Room: c.Room, Name: c.Name, Channel: c.Channel, Sub: c.Sub, OwnerID: c.OwnerID,
			URL: strings.TrimRight(s.BaseURL, "/") + imRoomURL(c.Room)}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/departments/{id}/chats {"name","channel","sub"} — makes the department's chat; {"room","sub"} — ties
// one of the key owner's group chats → {"room"}.
func (s *Server) apiDeptChatAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in deptChatIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	op := "create"
	if in.Room != "" {
		op = "link"
	}
	room, err := s.deptChatOp(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), r.PathValue("id"), op, in)
	if err != nil {
		return err
	}
	s.Log.Info("api department chat "+op, "user_id", p.UserID, "id", r.PathValue("id"), "room", room)
	writeJSON(w, http.StatusCreated, map[string]string{"room": room})
	return nil
}

// PATCH /api/v1/departments/{id}/chats?room=… {"sub"} — whether the people of sub-departments come in.
func (s *Server) apiDeptChatUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in deptChatIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Room = r.URL.Query().Get("room")
	if _, err := s.deptChatOp(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), r.PathValue("id"), "sub", in); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// DELETE /api/v1/departments/{id}/chats?room=… — unties the chat (it stays in the messenger).
func (s *Server) apiDeptChatDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	in := deptChatIn{Room: r.URL.Query().Get("room")}
	if _, err := s.deptChatOp(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), r.PathValue("id"), "unlink", in); err != nil {
		return err
	}
	s.Log.Info("api department chat unlink", "user_id", p.UserID, "id", r.PathValue("id"), "room", in.Room)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
