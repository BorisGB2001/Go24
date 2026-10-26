package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/tasks"
)

// «Открытые события» of the calendar: /calendar/open — the coming open events (?cat= — of a category), the
// categories, joining an event without an invitation.

const openDays = 60 // the open events shown: those of the next two months

func (s *Server) calendarOpen(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	cats, err := s.Calendar.Categories(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "open events", err)
		return
	}
	q := r.URL.Query()
	data := map[string]any{"Categories": cats, "Me": sess.UserID, "Msg": q.Get("msg")}
	cat := q.Get("cat")
	for _, c := range cats {
		if c.ID == cat {
			data["Cat"] = c
			if q.Get("edit") == "1" && c.Mine {
				data["Edit"] = c
			}
		}
	}
	if data["Cat"] == nil {
		cat = ""
	}
	if q.Get("edit") == "new" {
		data["Edit"], data["New"] = calendar.Category{}, true
	}
	now := time.Now()
	if data["Events"], err = s.Calendar.OpenEvents(r.Context(), sess.UserID, cat, now, now.AddDate(0, 0, openDays)); err != nil {
		s.fail(w, "open events", err)
		return
	}
	s.render(w, r, "calopen", page{TitleKey: "calendar.open.title", Nav: "calendar", Data: data})
}

// openBack is where a form of the open events returns: the page it was sent from (?cat=), else the list.
func openBack(r *http.Request) string {
	if back := r.FormValue("back"); strings.HasPrefix(back, "/calendar/open") {
		return back
	}
	return "/calendar/open"
}

func (s *Server) categorySave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Calendar.SaveCategory(r.Context(), sess.UserID, calendar.Category{ID: r.PathValue("id"), Name: r.FormValue("name"),
		Description: r.FormValue("description"), Closed: r.FormValue("closed") == "on"})
	if err != nil {
		s.taskErr(w, r, err, "/calendar/open")
		return
	}
	http.Redirect(w, r, "/calendar/open?cat="+id+"&msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

func (s *Server) categoryDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Calendar.DeleteCategory(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/calendar/open")
		return
	}
	http.Redirect(w, r, "/calendar/open", http.StatusSeeOther)
}

func (s *Server) categoryMute(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Calendar.MuteCategory(r.Context(), sess.UserID, id, r.FormValue("on") == "1"); err != nil {
		s.taskErr(w, r, err, "/calendar/open")
		return
	}
	http.Redirect(w, r, "/calendar/open?cat="+id, http.StatusSeeOther)
}

// eventJoin: «Пойду» of an open event (join=1), «Не пойду» (join=0).
func (s *Server) eventJoin(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Calendar.Join(r.Context(), sess.UserID, id, r.FormValue("join") == "1"); err != nil {
		s.taskErr(w, r, err, "/calendar/"+id)
		return
	}
	s.meetingChatSync(r.Context(), sess.UserID, id)
	if r.FormValue("back") == "" {
		http.Redirect(w, r, "/calendar/"+id, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, openBack(r), http.StatusSeeOther)
}
