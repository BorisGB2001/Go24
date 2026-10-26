package web

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST API of «Коллабы»: the key's user sees their own collabs only; someone else's collab is not found.

func (s *Server) apiCollabRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/collabs", s.api("collabs:read", s.apiCollabs))
	mux.HandleFunc("POST /api/v1/collabs", s.api("collabs:write", s.apiCollabCreate))
	mux.HandleFunc("GET /api/v1/collabs/{key}", s.api("collabs:read", s.apiCollab))
	mux.HandleFunc("PATCH /api/v1/collabs/{key}", s.api("collabs:write", s.apiCollabUpdate))
	mux.HandleFunc("PUT /api/v1/collabs/{key}/photo", s.api("collabs:write", s.apiCollabPhoto))
	mux.HandleFunc("DELETE /api/v1/collabs/{key}/photo", s.api("collabs:write", s.apiCollabPhoto))
	mux.HandleFunc("GET /api/v1/collabs/{key}/invites", s.api("collabs:read", s.apiCollabInvites))
	mux.HandleFunc("POST /api/v1/collabs/{key}/invites", s.api("collabs:write", s.apiCollabInvite))
	mux.HandleFunc("DELETE /api/v1/collabs/{key}/invites/{id}", s.api("collabs:write", s.apiCollabInviteRevoke))
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
	Archived bool               `json:"archived"`
	Photo    string             `json:"photo_url,omitempty"`
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
			Archived: c.Archived, Photo: s.collabPhotoURL(c.Project),
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
	out := apiCollab{apiProject: toAPIProject(c), Members: len(members), Archived: c.Archived, Photo: s.collabPhotoURL(c)}
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

// collabPhotoURL is the full address of the collab's avatar ("" — none).
func (s *Server) collabPhotoURL(c tasks.Project) string {
	if u := c.PhotoURL(); u != "" {
		return s.BaseURL + u
	}
	return ""
}

// apiCollabUpdate changes the name and the description (socialnetwork.collab.update); fields left out stay.
func (s *Server) apiCollabUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	in := struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}{c.Name, c.Description}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if c.Frozen() {
		return errCollabArchived
	}
	if err := s.Tasks.SaveProjectInfo(r.Context(), c, in.Name, in.Description, c.DateStart, c.DateFinish); err != nil {
		return err
	}
	s.Matrix.Wake() // the chat is named after the collab
	return s.writeCollab(w, r, p, c.Key, http.StatusOK)
}

// apiCollabPhoto: PUT — the collab's avatar, the body is the image itself (JPEG, PNG, WebP or GIF, up to 5 MB);
// DELETE removes it. Those who change the collab's settings.
func (s *Server) apiCollabPhoto(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if c.Frozen() {
		return errCollabArchived
	}
	if !c.CanEdit() {
		return tasks.ErrForbidden
	}
	if r.Method == http.MethodDelete {
		_, err = s.Tasks.SetProjectPhoto(r.Context(), c, "", "")
	} else {
		data, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPhotoSize+1))
		if rerr != nil || len(data) == 0 || len(data) > maxPhotoSize {
			return i18n.E("err.photo")
		}
		err = s.storeProjectPhoto(r.Context(), p.UserID, c, bytes.NewReader(data), int64(len(data)))
	}
	if err != nil {
		return err
	}
	return s.writeCollab(w, r, p, c.Key, http.StatusOK)
}

// apiGuestInvite is an invitation into the collab: kind link (for anyone, until it expires or is revoked), email or
// phone (for one person, once).
type apiGuestInvite struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	URL       string    `json:"url"`
	Email     string    `json:"email,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	Name      string    `json:"name,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	Expires   time.Time `json:"expires_at"`
	State     string    `json:"state"` // active | revoked | used | expired
	Uses      int       `json:"uses"`
	UsedBy    string    `json:"used_by,omitempty"`
	Sent      bool      `json:"sent,omitempty"` // the e-mail went from the inviter's mailbox
}

func (s *Server) toAPIGuestInvite(i tasks.GuestInvite) apiGuestInvite {
	state := "active"
	switch {
	case i.Revoked:
		state = "revoked"
	case i.Kind != "link" && i.Uses > 0:
		state = "used"
	case !i.Active(time.Now()):
		state = "expired"
	}
	return apiGuestInvite{i.ID, i.Kind, s.joinURL(i), i.Email, i.Phone, i.Name, i.CreatedBy, i.CreatedAt, i.Expires, state,
		i.Uses, i.UsedBy, false}
}

// apiCollabInvites: the collab's invitations of the last month, active first (those who may invite guests).
func (s *Server) apiCollabInvites(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	list, err := s.Tasks.GuestInvites(r.Context(), c, p.UserID)
	if err != nil {
		return err
	}
	out := make([]apiGuestInvite, 0, len(list))
	for _, i := range list {
		out = append(out, s.toAPIGuestInvite(i))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCollabInvite makes an invitation {kind: link | email | phone, contact, name, days (1–90, a week by default),
// send: true — the e-mail from the inviter's first mailbox in go24}; the answer has the link to pass on.
func (s *Server) apiCollabInvite(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Kind    string `json:"kind"`
		Contact string `json:"contact"`
		Name    string `json:"name"`
		Days    int    `json:"days"`
		Send    bool   `json:"send"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if c.Frozen() {
		return errCollabArchived
	}
	if in.Kind == "" {
		in.Kind = "link"
	}
	inv, err := s.Tasks.CreateGuestInvite(r.Context(), c, p.UserID, in.Kind, in.Contact, in.Name, time.Duration(in.Days)*24*time.Hour)
	if err != nil {
		return err
	}
	s.Log.Info("guest invite", "user_id", p.UserID, "collab", c.Key, "kind", in.Kind, "api", true)
	out := s.toAPIGuestInvite(inv)
	if in.Kind == "email" && in.Send {
		var name string
		if err := s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id = $1`, p.UserID).Scan(&name); err != nil {
			return err
		}
		out.Sent = s.sendGuestInvite(r, p.UserID, name, c, inv)
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

// apiCollabInviteRevoke: the link stops working at once (people who joined by it stay).
func (s *Server) apiCollabInviteRevoke(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.Tasks.CollabByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.RevokeGuestInvite(r.Context(), c, p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
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
