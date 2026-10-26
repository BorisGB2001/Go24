package web

import (
	"net/http"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/tasks"
)

// guestProfile is a person's card for a guest: the person and the tasks they share — nothing of the company (its
// structure, managers, thanks, the company's own fields of the card).
func (s *Server) guestProfile(w http.ResponseWriter, r *http.Request, sess auth.Session, p company.Profile) {
	open, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{UserID: p.ID, Doing: true, OnlyOpen: true, Viewer: sess.UserID})
	if err != nil {
		s.taskErr(w, r, err, guestLanding)
		return
	}
	p.Departments = nil
	mxid, element := s.matrixOf(p)
	tab := ""
	if r.URL.Query().Get("tab") == "tasks" {
		tab = "tasks"
	}
	s.render(w, r, "profile", page{Title: p.FullName(), Nav: "collabs", Data: map[string]any{
		"Guest": true, "Tab": tab, "MatrixID": mxid, "ElementURL": element, "P": p, "Tasks": open,
		"CanEdit": sess.UserID == p.ID, "Me": sess.UserID == p.ID,
	}})
}

// ---------- guests: people from outside see their collabs only ----------
//
// A guest (users.guest) passes only through the sections of their collabs: the collabs, their tasks, kanban, drive,
// calendar and chat, the messenger, notifications and their own card. Every other page and API method of the company
// (the structure, the feed, the employees, CRM, mail, processes, the knowledge base, settings…) answers «not found»,
// as if it did not exist. Inside the allowed sections the stores show a guest only what their collabs hold.

// guestRoutes are the routes (ServeMux patterns) open to a guest.
var guestRoutes = map[string]bool{
	"GET /search": true, "GET /my": true, "GET /call": true, "GET /call/user": true, "POST /call/ring/{id}/{action}": true, "POST /call/ring-more": true, "GET /people": true, "GET /presence": true, "POST /presence": true, "POST /prefs/columns": true, "GET /prefs/widths": true, "POST /prefs/widths": true,
	"GET /tasks/new": true, "POST /tasks/new": true,
	// collabs
	"GET /collabs": true, "GET /p/{key}": true, "GET /p/{key}/photo": true, "GET /p/{key}/board": true,
	"GET /p/{key}/gantt": true, "GET /join/{token}": true, "POST /join/{token}": true,
	"POST /p/{key}/tasks": true, "POST /p/{key}/leave": true, "POST /p/{key}/invite": true,
	// tasks
	"GET /t/{key}": true, "POST /t/{key}/action": true, "POST /t/{key}/edit": true, "POST /t/{key}/deadline": true,
	"POST /t/{key}/stage": true, "POST /t/{key}/participants": true, "POST /t/{key}/checklist": true,
	"POST /t/{key}/checklist/{id}": true, "POST /t/{key}/checklists": true, "POST /t/{key}/checklists/{id}": true, "POST /t/{key}/comments": true, "GET /t/{key}/messenger": true,
	"GET /t/{key}/live": true, "POST /t/{key}/comments/{id}/react": true, "POST /t/{key}/comments/{id}/result": true,
	"POST /t/{key}/timer": true, "POST /timer/stop": true, "POST /t/{key}/time": true, "POST /t/{key}/time/{id}": true,
	"POST /t/{key}/time/{id}/delete": true, "POST /t/{key}/mark": true, "POST /t/{key}/tags": true,
	"POST /t/{key}/rate": true, "POST /t/{key}/reminders": true, "POST /t/{key}/reminders/{id}/delete": true,
	"POST /t/{key}/links": true, "POST /t/{key}/links/{id}/delete": true, "POST /t/{key}/delete": true,
	// a collab's drive and the files of its tasks
	"GET /disk/p/{key}": true, "POST /disk/p/{key}/upload": true, "POST /disk/p/{key}/folders": true,
	"POST /disk/p/{key}/delete": true, "GET /disk/p/{key}/edit/{file}": true, "POST /disk/p/{key}/new-doc": true, "/office/": true, "POST /files": true, "GET /files/{id}": true,
	// calendar: the guest's meetings (of collabs) and the deadlines of their tasks
	"GET /calendar": true, "GET /calendar/new": true, "POST /calendar/new": true, "GET /calendar/{id}": true, "GET /calendar/busy": true, "GET /calendar/sync": true, "POST /calendar/sync": true,
	"POST /calendar/{id}": true, "POST /calendar/{id}/delete": true, "POST /calendar/{id}/respond": true, "POST /calendar/{id}/move": true, "POST /calendar/sections": true, "POST /calendar/sections/{id}/delete": true,
	// messenger: the chats of collabs and private chats with their people
	"GET /im": true, "GET /im/messages": true, "POST /im/send": true, "POST /im/react": true, "GET /im/direct": true, "POST /im/upload": true, "POST /im/edit": true, "POST /im/delete": true, "POST /im/forward": true, "GET /im/file": true, "GET /im/file/preview": true, "POST /im/transcribe": true,
	"GET /im/live": true, "POST /im/settings": true, "GET /im/notes": true, "GET /im/unread": true, "POST /im/read": true, "GET /im/photo": true, "POST /im/pin": true, "GET /im/search": true, "POST /im/typing": true, "POST /im/mark": true, "POST /im/draft": true, "POST /im/save": true, "POST /im/remind": true, "GET /im/saved": true, "GET /im/media": true, "GET /im/threads": true, "POST /im/poll": true, "GET /im/export": true, "POST /im/poll/{action}": true, "POST /im/group/{action}": true, // a guest added to a group chat: leaves it, adds only people they may write to
	// notifications
	"GET /notifications": true, "GET /notifications/pop": true, "GET /notifications/live": true,
	"POST /notifications/read": true, "GET /notifications/{id}": true,
	"GET /settings/notifications": true, "POST /settings/notifications": true,
	"POST /settings/notifications/{channel}/link": true, "POST /settings/notifications/{channel}/unlink": true,
	// people: one's own card, the cards of the people of one's collabs
	"GET /u/{id}": true, "POST /u/{id}": true, "GET /u/{id}/photo": true, "POST /u/{id}/photo": true, "GET /u/{id}/card": true,
}

// guestAPI are the REST API methods open to a guest's key (the same sections as on the pages).
var guestAPI = map[string]bool{
	"GET /api/v1/me": true, "GET /api/v1/projects": true, "GET /api/v1/projects/{key}": true,
	"GET /api/v1/projects/{key}/tasks": true, "POST /api/v1/projects/{key}/tasks": true,
	"GET /api/v1/tasks": true, "GET /api/v1/tasks/{key}": true, "GET /api/v1/tasks/{key}/views": true, "PATCH /api/v1/tasks/{key}": true,
	"DELETE /api/v1/tasks/{key}": true, "POST /api/v1/tasks/{key}/actions/{action}": true,
	"POST /api/v1/tasks/{key}/comments": true, "POST /api/v1/tasks/{key}/checklist": true,
	"POST /api/v1/tasks/{key}/checklist/{id}/toggle": true, "DELETE /api/v1/tasks/{key}/checklist/{id}": true,
	"POST /api/v1/tasks/{key}/tags": true, "POST /api/v1/tasks/{key}/links": true,
	"DELETE /api/v1/tasks/{key}/links/{id}": true, "POST /api/v1/tasks/{key}/files": true,
	"POST /api/v1/tasks/{key}/participants": true, "DELETE /api/v1/tasks/{key}/participants/{role}/{uid}": true,
	"POST /api/v1/tasks/{key}/time": true, "POST /api/v1/tasks/{key}/timer/start": true, "POST /api/v1/timer/stop": true,
	"GET /api/v1/notifications": true, "POST /api/v1/notifications/read": true,
	"GET /api/v1/im/rooms": true, "GET /api/v1/im/rooms/{room}/messages": true,
	"POST /api/v1/im/rooms/{room}/messages": true, "PATCH /api/v1/im/rooms/{room}/messages/{id}": true, "DELETE /api/v1/im/rooms/{room}/messages/{id}": true, "PUT /api/v1/im/rooms/{room}/messages/{id}/reactions/{key}": true, "DELETE /api/v1/im/rooms/{room}/messages/{id}/reactions/{key}": true, "POST /api/v1/im/rooms/{room}/polls": true, "GET /api/v1/im/rooms/{room}/history": true, "GET /api/v1/im/rooms/{room}/files": true, "POST /api/v1/im/rooms/{room}/polls/{id}/{action}": true, "POST /api/v1/im/rooms/{room}/files": true, "GET /api/v1/im/rooms/{room}/messages/{id}/file": true, "POST /api/v1/im/rooms/{room}/messages/{id}/transcript": true, "POST /api/v1/im/direct": true, "POST /api/v1/im/notes": true, "GET /api/v1/im/calls": true, "GET /api/v1/im/settings": true, "PUT /api/v1/im/settings": true, "GET /api/v1/im/rooms/{room}/members": true, "POST /api/v1/im/rooms/{room}/members": true, "DELETE /api/v1/im/rooms/{room}/members/{user}": true, "GET /api/v1/im/search": true, "POST /api/v1/im/rooms/{room}/marks": true, "GET /api/v1/im/rooms/{room}/pins": true, "PUT /api/v1/im/rooms/{room}/pins/{id}": true, "DELETE /api/v1/im/rooms/{room}/pins/{id}": true,
	"GET /api/v1/collabs": true, "GET /api/v1/collabs/{key}": true, "DELETE /api/v1/collabs/{key}/members/{uid}": true,
	"GET /api/v1/calendar/events": true, "POST /api/v1/calendar/events": true, "GET /api/v1/calendar/events/{id}": true,
	"PATCH /api/v1/calendar/events/{id}": true, "DELETE /api/v1/calendar/events/{id}": true,
	"POST /api/v1/calendar/events/{id}/respond": true, "GET /api/v1/calendar/busy": true,
	"GET /api/v1/calendar/sections": true, "POST /api/v1/calendar/sections": true, "PATCH /api/v1/calendar/sections/{id}": true,
	"DELETE /api/v1/calendar/sections/{id}": true,
}

// guestLanding is where a guest comes after signing in, and instead of the company's start page.
const guestLanding = "/collabs"

// guestPass: a guest may open this route (r.Pattern); everyone else may open everything.
func guestPass(r *http.Request, routes map[string]bool) bool {
	return !tasks.IsGuestCtx(r.Context()) || routes[r.Pattern]
}

// knowsPerson: the signed-in person may see this person's card, photo, private chat (a guest — only the people of
// their collabs, an employee — a guest only of a collab of theirs).
func (s *Server) knowsPerson(r *http.Request, viewerID, personID string) bool {
	if tasks.IsAdmin(r.Context()) {
		return true
	}
	ok, err := s.Tasks.KnowsPerson(r.Context(), viewerID, personID)
	if err != nil {
		s.Log.Warn("knows person", "err", err)
	}
	return ok
}

// landing is the first page of a signed-in person: «Коллабы» for a guest, the first item of the menu for an employee.
func (s *Server) landing(r *http.Request, userID string) string {
	if s.Tasks != nil {
		if guest, err := s.Tasks.IsGuest(r.Context(), userID); err == nil && guest {
			return guestLanding
		}
	}
	return s.menuStart(r.Context(), userID)
}
