package web

import (
	"net/http"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Дела» of the CRM: planned on a card, «Мои дела», done or deleted ----------

// crmActivityAdd plans a deed on a card (who may change the card).
func (s *Server) crmActivityAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	err := s.crmCheck(r, sess, kind, id, "write")
	if err == nil {
		var deadline time.Time
		if deadline, err = time.ParseInLocation("2006-01-02T15:04", r.FormValue("deadline"), time.Local); err != nil {
			err = i18n.E("err.crm_activity_deadline")
		} else {
			_, err = s.CRM.AddActivity(r.Context(), sess.UserID, crm.Activity{Entity: crmEntities[kind], EntityID: id, Kind: r.FormValue("kind"),
				Subject: r.FormValue("subject"), Description: r.FormValue("description"), Deadline: deadline,
				Responsible: crm.Ref{ID: r.FormValue("responsible")}})
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#activities", http.StatusSeeOther)
}

// crmActivityAction: "done" or "delete" a deed — its responsible, or who may change its card.
func (s *Server) crmActivityAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	a, err := s.CRM.Activity(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/crm/activities")
		return
	}
	back := a.Path() + "#activities"
	if b := r.FormValue("back"); strings.HasPrefix(b, "/crm/") {
		back = b
	}
	if a.Responsible.ID != sess.UserID {
		for kind, entity := range crmEntities {
			if entity == a.Entity {
				err = s.crmCheck(r, sess, kind, a.EntityID, "write")
			}
		}
	}
	if err == nil {
		switch r.FormValue("op") {
		case "done":
			_, err = s.CRM.CompleteActivity(r.Context(), sess.UserID, a.ID)
		case "delete":
			err = s.CRM.DeleteActivity(r.Context(), a.ID)
		default:
			err = i18n.E("err.bad_form")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, a.Path())
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// crmActivities is «Мои дела»: the person's open deeds — overdue, today's, planned or all.
func (s *Server) crmActivities(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	filter := r.URL.Query().Get("f")
	if filter != "overdue" && filter != "planned" && filter != "all" {
		filter = "today"
	}
	list, err := s.CRM.MyActivities(r.Context(), sess.UserID, filter, time.Now())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	data := map[string]any{"Kind": "activities", "Items": list, "Filter": filter}
	if err = s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmactivities", page{TitleKey: "crm.activities", Nav: "crm_activities", Data: data})
}
