package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go24/internal/auth"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// «Обзвон» of «Маркетинг»: call lists by a CRM segment (marketing.CallList).

func (s *Server) mktCallsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	list, err := s.Marketing.CallLists(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	s.render(w, r, "marketing", page{TitleKey: "mkt.calls", Nav: "marketing", Flash: r.URL.Query().Get("msg"),
		Data: map[string]any{"Tab": "calls", "Calls": list}})
}

// mktCallPage is a call list: the editor of a draft, the progress and the cards of a started one.
func (s *Server) mktCallPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	ctx, q, id := r.Context(), r.URL.Query(), r.PathValue("id")
	tomorrow := time.Now().AddDate(0, 0, 1)
	tomorrow = time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 18, 0, 0, 0, time.Local)
	c := marketing.CallList{Status: "draft", Assign: "owner", Deadline: &tomorrow}
	if id != "new" {
		var err error
		if c, err = s.Marketing.CallList(ctx, id); err != nil {
			s.taskErr(w, r, err, "/marketing/calls")
			return
		}
	}
	data := map[string]any{"Tab": "calls", "C": c, "Mine": c.ID == "" || c.CreatedBy == sess.UserID || tasks.IsAdmin(ctx), "Staff": setOf(c.Staff)}
	if c.Editable() {
		segs, err := s.Marketing.Segments(ctx)
		if err != nil {
			s.taskErr(w, r, err, "/marketing/calls")
			return
		}
		var cards []marketing.Segment // a list of addresses has no phones
		for _, g := range segs {
			if g.Entity != "list" {
				cards = append(cards, g)
			}
		}
		data["Segments"] = cards
		if data["Users"], err = s.Tasks.ListUsers(ctx); err != nil {
			s.taskErr(w, r, err, "/marketing/calls")
			return
		}
		if c.SegmentID != "" {
			if list, err := s.Marketing.Callees(ctx, sess.UserID, c); err == nil {
				data["Count"] = len(list)
			} else {
				data["CountErr"] = s.errText(r, err)
			}
		}
	} else {
		const per = 100
		pg, _ := strconv.Atoi(q.Get("page"))
		pg = max(pg, 1)
		list, n, err := s.Marketing.CallItems(ctx, c.ID, q.Get("done"), per, (pg-1)*per)
		if err != nil {
			s.taskErr(w, r, err, "/marketing/calls")
			return
		}
		var pages []int
		for i := 1; i <= (n+per-1)/per; i++ {
			pages = append(pages, i)
		}
		data["Items"], data["Done"], data["Page"], data["Pages"] = list, q.Get("done"), pg, pages
	}
	title := c.Title
	if c.ID == "" {
		title = s.tr(r, "mkt.new_calls")
	}
	s.render(w, r, "mktcalls", page{Title: title, Nav: "marketing", Flash: q.Get("msg"), Data: data})
}

// mktCallSave keeps the draft; «Запустить» (go=start) then plans its calls.
func (s *Server) mktCallSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	_ = r.ParseForm()
	c := marketing.CallList{ID: r.PathValue("id"), Title: r.FormValue("title"), SegmentID: r.FormValue("segment"), Script: r.FormValue("script"),
		Assign: r.FormValue("assign"), Staff: r.Form["staff"]}
	if c.ID == "new" {
		c.ID = ""
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("deadline"), time.Local); err == nil {
		c.Deadline = &t
	}
	ctx, admin := r.Context(), tasks.IsAdmin(r.Context())
	id, err := s.Marketing.SaveCallList(ctx, sess.UserID, admin, c)
	if err != nil {
		s.taskErr(w, r, err, "/marketing/calls/"+r.PathValue("id"))
		return
	}
	back, msg := "/marketing/calls/"+id, s.tr(r, "mkt.calls_saved")
	if r.FormValue("go") == "start" {
		n, err := s.Marketing.StartCallList(ctx, sess.UserID, admin, id)
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		s.Log.Info("call list started", "user_id", sess.UserID, "calls", id, "cards", n)
		msg = s.tr(r, "mkt.calls_started", n)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) mktCallDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	if err := s.Marketing.DeleteCallList(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/marketing/calls/"+r.PathValue("id"))
		return
	}
	http.Redirect(w, r, "/marketing/calls", http.StatusSeeOther)
}
