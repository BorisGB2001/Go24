package web

import (
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/tasks"
)

// REST API of «Коллабы»: the key's user sees their own collabs only; someone else's collab is not found.

func (s *Server) apiCollabRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/collabs", s.api("collabs:read", s.apiCollabs))
	mux.HandleFunc("POST /api/v1/collabs", s.api("collabs:write", s.apiCollabCreate))
	mux.HandleFunc("GET /api/v1/collabs/{key}", s.api("collabs:read", s.apiCollab))
	mux.HandleFunc("POST /api/v1/collabs/{key}/members", s.api("collabs:write", s.apiCollabMember))
	mux.HandleFunc("DELETE /api/v1/collabs/{key}/members/{uid}", s.api("collabs:write", s.apiCollabMemberRemove))
	mux.HandleFunc("PATCH /api/v1/collabs/{key}/settings", s.api("collabs:write", s.apiCollabSettings))
	mux.HandleFunc("POST /api/v1/collabs/{key}/archive", s.api("collabs:write", s.apiCollabArchive))
	mux.HandleFunc("POST /api/v1/collabs/{key}/owner", s.api("collabs:write", s.apiCollabOwner))
	mux.HandleFunc("DELETE /api/v1/collabs/{key}", s.api("collabs:write", s.apiCollabDelete))
	mux.HandleFunc("GET /api/v1/collabs/{key}/log", s.api("collabs:read", s.apiCollabLog))
	mux.HandleFunc("POST /api/v1/users/{id}/employee", s.api("collabs:write", s.apiMakeEmployee))
}

type apiCollab struct {
	apiProject
	Members  int                `json:"members"`
	Guests   int                `json:"guests"`
	Chat     string             `json:"chat,omitempty"` // the Matrix room of the collab
	Settings *apiCollabSettings `json:"settings,omitempty"`
	Counters *apiCollabCounters `json:"counters,omitempty"` // in the list: the caller's tasks needing attention
}

// apiCollabCounters: the caller's open tasks past the deadline and the tasks with comments they have not seen.
type apiCollabCounters struct {
	Overdue  int `json:"overdue"`
	Comments int `json:"comments"`
}

// apiCollabSettings: «кто может приглашать» (members, guests): owner | managers | members.
type apiCollabSettings struct {
	InvitePerm      string `json:"invite_perm"`
	GuestInvitePerm string `json:"guest_invite_perm"`
	ChatHistory     bool   `json:"chat_history"` // new members see the chat's earlier messages
	GuestTasks      bool   `json:"guests_tasks"` // guests make tasks
	GuestFiles      bool   `json:"guests_files"` // guests upload files
	EditPerm        string `json:"edit_perm"`    // who changes the settings, the name, the avatar: owner | managers
	ChatPerm        string `json:"chat_perm"`    // who writes in the chat: members | employees | managers
}

type apiCollabLogEntry struct {
	At          time.Time `json:"at"`
	Action      string    `json:"action"` // created | member_added | joined | role_changed | member_removed | left | settings | invite_created | invite_revoked | info | archived | restored | owner_changed
	Actor       string    `json:"actor,omitempty"`
	Target      string    `json:"target,omitempty"`
	TargetGuest bool      `json:"target_guest,omitempty"`
	Detail      string    `json:"detail,omitempty"`
}

type apiCollabMember struct {
	apiUser
	Role  string `json:"role"` // owner | manager (moderator) | member
	Guest bool   `json:"guest"`
}

func (s *Server) apiCollabs(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Tasks.ListCollabs(r.Context(), p.UserID, r.URL.Query().Get("archived") == "1", r.URL.Query().Get("q"))
	if err != nil {
		return err
	}
	out := make([]apiCollab, 0, len(list))
	for _, c := range list {
		room, _ := s.Matrix.ProjectRoom(r.Context(), c.ID)
		out = append(out, apiCollab{apiProject: toAPIProject(c.Project), Members: c.Members, Guests: c.Guests, Chat: room,
			Counters: &apiCollabCounters{Overdue: c.Overdue, Comments: c.Comments}})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiCollabCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Members     []string `json:"members"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	if s.apiAdmin(ctx, p.UserID) { // «only administrators make collabs» lets them
		ctx = tasks.AsAdministrator(ctx)
	}
	key, err := s.Tasks.CreateCollab(ctx, p.UserID, in.Name, in.Description, in.Members)
	if err != nil {
		return err
	}
	s.Matrix.Wake() // the collab's chat
	return s.writeCollab(w, r, p, key, http.StatusCreated)
}

func (s *Server) apiCollab(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.writeCollab(w, r, p, r.PathValue("key"), http.StatusOK)
}

// writeCollab answers with the collab and its members.
func (s *Server) writeCollab(w http.ResponseWriter, r *http.Request, p apikeys.Principal, key string, status int) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, key)
	if err != nil {
		return err
	}
	members, err := s.Tasks.Members(r.Context(), c.ID)
	if err != nil {
		return err
	}
	out := apiCollab{apiProject: toAPIProject(c), Members: len(members)}
	out.Chat, _ = s.Matrix.ProjectRoom(r.Context(), c.ID)
	if set, err := s.Tasks.CollabSettings(r.Context(), c); err == nil {
		out.Settings = &apiCollabSettings{set.InvitePerm, set.GuestInvitePerm, set.ChatHistory, set.GuestTasks, set.GuestFiles,
			set.EditPerm, set.ChatPerm}
	}
	ms := make([]apiCollabMember, 0, len(members))
	for _, m := range members {
		ms = append(ms, apiCollabMember{apiUser{m.ID, m.Name}, m.Role, m.Guest})
		if m.Guest {
			out.Guests++
		}
	}
	writeJSON(w, status, map[string]any{"collab": out, "members": ms})
	return nil
}

// apiCollabMember adds a person to the collab or changes their role (owner and moderators).
func (s *Server) apiCollabMember(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if in.Role == "" {
		in.Role = "member"
	}
	if err := s.Tasks.SetMember(r.Context(), c, in.UserID, in.Role); err != nil {
		return err
	}
	s.Matrix.Wake()
	return s.writeCollab(w, r, p, c.Key, http.StatusOK)
}

// apiCollabMemberRemove takes a person out of the collab (owner and moderators; anyone may take themselves out).
func (s *Server) apiCollabMemberRemove(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	uid := r.PathValue("uid")
	if uid == p.UserID {
		err = s.Tasks.LeaveProject(r.Context(), c, uid)
	} else {
		err = s.Tasks.RemoveMember(r.Context(), c, uid)
	}
	if err != nil {
		return err
	}
	s.Matrix.Wake()
	if uid == p.UserID {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return s.writeCollab(w, r, p, c.Key, http.StatusOK)
}

// apiCollabSettings changes the settings; fields left out stay as they are.
func (s *Server) apiCollabSettings(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	set, err := s.Tasks.CollabSettings(r.Context(), c)
	if err != nil {
		return err
	}
	in := apiCollabSettings(set)
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Tasks.SetCollabSettings(r.Context(), c, p.UserID, tasks.CollabSettings(in)); err != nil {
		return err
	}
	s.Matrix.Wake()
	return s.writeCollab(w, r, p, c.Key, http.StatusOK)
}

// apiCollabArchive moves the collab to the archive ({"archived": true}) or back.
func (s *Server) apiCollabArchive(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Archived bool `json:"archived"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.SetArchived(r.Context(), c, in.Archived); err != nil {
		return err
	}
	s.Matrix.Wake()
	return s.writeCollab(w, r, p, c.Key, http.StatusOK)
}

// apiCollabOwner hands the collab over ({"user_id": …}, an employee of it): the owner or an administrator.
func (s *Server) apiCollabOwner(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		UserID string `json:"user_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.TransferCollab(r.Context(), c, p.UserID, in.UserID); err != nil {
		return err
	}
	return s.writeCollab(w, r, p, c.Key, http.StatusOK)
}

// apiCollabDelete deletes the collab (the owner); ?confirm= is its name, as in the dialog.
func (s *Server) apiCollabDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.DeleteCollab(r.Context(), c, p.UserID, r.URL.Query().Get("confirm")); err != nil {
		return err
	}
	s.Log.Info("collab deleted", "user_id", p.UserID, "collab", c.Key, "api", true)
	s.Matrix.Wake()
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiCollabLog is the collab's log (the owner and moderators), newest first.
func (s *Server) apiCollabLog(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	list, err := s.Tasks.CollabLog(r.Context(), c, 500)
	if err != nil {
		return err
	}
	out := make([]apiCollabLogEntry, 0, len(list))
	for _, e := range list {
		out = append(out, apiCollabLogEntry{e.At, e.Action, e.Actor, e.Target, e.TargetGuest, e.Detail})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiMakeEmployee makes a guest of collabs an employee (an administrator's key).
func (s *Server) apiMakeEmployee(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return tasks.ErrForbidden
	}
	id := r.PathValue("id")
	if err := s.makeEmployee(r.Context(), p.UserID, id); err != nil {
		return err
	}
	s.Log.Info("guest made employee", "user_id", p.UserID, "target", id, "key_id", p.KeyID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
