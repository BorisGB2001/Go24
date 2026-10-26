package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Коллабы»: /collabs (the person's collabs, a new one); a collab's page is that of its project ----------

func (s *Server) collabsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	archived := r.URL.Query().Get("archived") == "1" // «Архив»
	q := strings.TrimSpace(r.URL.Query().Get("q"))   // the search line
	list, err := s.Tasks.ListCollabs(r.Context(), sess.UserID, archived, q)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	may, err := s.Tasks.MayCreateCollab(collabCtx(r.Context(), sess), sess.UserID) // a guest never; else the company's rule
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	rooms := map[string]string{} // collab → its chat
	for _, c := range list {
		if room, err := s.Matrix.ProjectRoom(r.Context(), c.ID); err == nil && room != "" {
			rooms[c.ID] = room
		}
	}
	s.render(w, r, "collabs", page{TitleKey: "collab.title", Nav: "collabs", Data: map[string]any{
		"List": list, "Rooms": rooms, "CanCreate": may && !archived, "Archived": archived, "Q": q, "Messenger": s.Matrix != nil}})
}

// collabCreate makes a collab (name, description, avatar, members) and opens it.
func (s *Server) collabCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoSize+1<<20)
	if err := r.ParseMultipartForm(maxPhotoSize + 1<<20); err != nil && err != http.ErrNotMultipart {
		s.taskErr(w, r, err, "/collabs")
		return
	}
	key, err := s.Tasks.CreateCollab(collabCtx(r.Context(), sess), sess.UserID, r.FormValue("name"), r.FormValue("description"), r.Form["members"])
	if err != nil {
		s.taskErr(w, r, err, "/collabs")
		return
	}
	s.Log.Info("collab created", "user_id", sess.UserID, "collab", key)
	if f, hdr, ferr := r.FormFile("photo"); ferr == nil {
		defer f.Close()
		if p, err := s.Tasks.CollabByKey(r.Context(), sess.UserID, key); err == nil && hdr.Size <= maxPhotoSize {
			if err := s.storeProjectPhoto(r.Context(), sess.UserID, p, f, hdr.Size); err != nil {
				s.Log.Warn("collab photo", "err", err)
			}
		}
	}
	s.Matrix.Wake() // the collab's chat
	http.Redirect(w, r, "/p/"+key, http.StatusSeeOther)
}

// projectLeave: a member leaves a collab (or a project).
func (s *Server) projectLeave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.LeaveProject(r.Context(), p, sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/p/"+r.PathValue("key"))
		return
	}
	s.Matrix.Wake() // out of the chat too
	back := "/projects"
	if p.Collab() {
		back = "/collabs"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// collabSettings saves the collab's settings (the owner, moderators, an administrator).
func (s *Server) collabSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.CollabByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.SetCollabSettings(r.Context(), p, sess.UserID, tasks.CollabSettings{
			InvitePerm: r.FormValue("invite_perm"), GuestInvitePerm: r.FormValue("guest_invite_perm"),
			ChatHistory: r.FormValue("chat_history") == "on", GuestTasks: r.FormValue("guests_tasks") == "on",
			GuestFiles: r.FormValue("guests_files") == "on", EditPerm: r.FormValue("edit_perm"), ChatPerm: r.FormValue("chat_perm"),
		})
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("collab settings", "user_id", sess.UserID, "collab", p.Key)
	s.Matrix.Wake() // the chat's history for new members, who writes in it
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "collab.settings_saved"))+"#settings", http.StatusSeeOther)
}

// collabOwner hands the collab over to another employee of it (the owner or an administrator).
func (s *Server) collabOwner(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.CollabByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.TransferCollab(r.Context(), p, sess.UserID, r.FormValue("user_id"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("collab owner", "user_id", sess.UserID, "collab", p.Key, "owner", r.FormValue("user_id"))
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "collab.owner_changed"))+"#members", http.StatusSeeOther)
}

// collabDelete deletes the collab for good (the owner or an administrator, the name typed again).
func (s *Server) collabDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.CollabByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.DeleteCollab(r.Context(), p, sess.UserID, r.FormValue("confirm"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("collab deleted", "user_id", sess.UserID, "collab", p.Key, "admin", sess.AdminActive())
	s.Matrix.Wake() // its chat is emptied
	http.Redirect(w, r, "/collabs?msg="+url.QueryEscape(s.tr(r, "collab.deleted", p.Name)), http.StatusSeeOther)
}

// projectNav is the menu item of a project's pages: «Проекты», or «Коллабы» for a collab.
func projectNav(p tasks.Project) string {
	if p.Collab() {
		return "collabs"
	}
	return "projects"
}

// collabData is what a collab's page adds to that of a project: its chat in the messenger and, for those who may
// invite, the invitations of guests.
func (s *Server) collabData(r *http.Request, p tasks.Project, data map[string]any) {
	if !p.Collab() {
		return
	}
	if room, err := s.Matrix.ProjectRoom(r.Context(), p.ID); err == nil {
		data["ChatRoom"] = room
	}
	data["Messenger"] = s.Matrix != nil
	me := tasks.Viewer(r.Context())
	data["CanAddMembers"] = s.Tasks.CanAddMembers(r.Context(), p)
	data["CanDelete"] = p.MyRole == "owner" || p.MyRole == "admin"
	if set, err := s.Tasks.CollabSettings(r.Context(), p); err == nil {
		data["Settings"] = set
	}
	if p.CanManage() {
		if log, err := s.Tasks.CollabLog(r.Context(), p, 100); err == nil {
			data["Log"] = log
		} else {
			s.Log.Warn("collab log", "err", err)
		}
	}
	if p.Archived || !s.Tasks.CanInviteGuests(r.Context(), p, me) {
		return
	}
	list, err := s.Tasks.GuestInvites(r.Context(), p, me)
	if err != nil {
		s.Log.Warn("guest invites", "err", err)
		return
	}
	links := map[string]string{}
	for _, i := range list {
		links[i.ID] = s.joinURL(i)
	}
	mailbox := false
	if s.Mail != nil {
		boxes, _ := s.Mail.Mailboxes(r.Context(), me)
		mailbox = len(boxes) > 0
	}
	data["CanInviteGuests"], data["GuestInvites"], data["JoinLinks"], data["Now"], data["Mailbox"] = true, list, links, time.Now(), mailbox
}

// ---------- the archive of a collab is read-only ----------

// frozenOpen are the changes an archived collab still takes: restoring and deleting it, leaving it, taking a person
// out (access is revoked in the archive too), one's own marks on its tasks.
var frozenOpen = map[string]bool{
	"POST /p/{key}/archive": true, "POST /p/{key}/delete": true, "POST /p/{key}/leave": true,
	"POST /p/{key}/members/{uid}/delete": true, "POST /t/{key}/mark": true,
	"POST /api/v1/collabs/{key}/archive": true, "DELETE /api/v1/collabs/{key}": true,
	"DELETE /api/v1/collabs/{key}/members/{uid}": true,
}

// frozenPrefixes are the sections whose {key} is a project's or a task's.
var frozenPrefixes = []string{"/p/{key}", "/t/{key}", "/api/v1/tasks/{key}", "/api/v1/projects/{key}", "/api/v1/collabs/{key}"}

// frozen: the request changes something of a collab in the archive (its page, tasks, drive, members, settings) —
// refused for everyone, an administrator too, until the collab is restored.
func (s *Server) frozen(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || frozenOpen[r.Pattern] || s.Tasks == nil {
		return false
	}
	key := r.PathValue("key")
	if key == "" {
		return false
	}
	_, pat, _ := strings.Cut(r.Pattern, " ")
	for _, p := range frozenPrefixes {
		if strings.HasPrefix(pat, p) || strings.HasPrefix(pat, "/disk"+p) {
			return s.Tasks.CollabArchived(r.Context(), tasks.Viewer(r.Context()), key)
		}
	}
	return false
}

var errCollabArchived = i18n.E("err.collab_archived")

// frozenBack is the page an archived collab's form goes back to, with the message.
func (s *Server) frozenBack(w http.ResponseWriter, r *http.Request) {
	back := "/p/" + r.PathValue("key")
	if strings.HasPrefix(r.Pattern, "POST /t/") {
		back = "/t/" + r.PathValue("key")
	}
	if fromForm(r) {
		s.jsonErr(w, r, errCollabArchived)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "err.collab_archived")), http.StatusSeeOther)
}
