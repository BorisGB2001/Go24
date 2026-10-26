package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Права доступа» of «Онлайн-запись»: who works with it and who manages it besides administrators
// (booking.Rights). The others don't see the section in the menu, its pages and REST answer «Доступ запрещён».

// bookingMay: what the employee may in «Онлайн-запись» (use — work with it, manage — resources, types, links).
func (s *Server) bookingMay(ctx context.Context, userID string, admin bool) (use, manage bool) {
	if s.Booking == nil {
		return false, false
	}
	use, manage, err := s.Booking.May(ctx, userID, admin)
	if err != nil {
		s.Log.Warn("booking rights", "err", err)
	}
	return use, manage
}

// bookingUser: a page of «Онлайн-запись» for those who work with it.
func (s *Server) bookingUser(next func(http.ResponseWriter, *http.Request, auth.Session)) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		if use, _ := s.bookingMay(r.Context(), sess.UserID, sess.Admin); !use {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
		next(w, r, sess)
	})
}

// apiBkUse: REST of «Онлайн-запись» for the keys of those who work with it.
func (s *Server) apiBkUse(h apiHandler) apiHandler {
	return func(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
		if use, _ := s.bookingMay(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID)); !use {
			return i18n.E("err.forbidden")
		}
		return h(w, r, p)
	}
}

// bookingMenu drops «Онлайн-запись» from the menu's items of an employee who doesn't work with it.
func (s *Server) bookingMenu(ctx context.Context, userID string, admin bool, avail []string) []string {
	if use, _ := s.bookingMay(ctx, userID, admin); use || s.Booking == nil {
		return avail
	}
	return slices.DeleteFunc(avail, func(id string) bool { return id == "booking" })
}

// bookingRights: the page «Права доступа» (administrators).
func (s *Server) bookingRights(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	rights, err := s.Booking.GetRights(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	users, err := s.Tasks.ListUsers(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	pick := func(a booking.Audience) []tasks.User { // the chosen as the picker's choice (a department is «dept:ID»)
		names, err2 := s.Booking.Names(ctx, a)
		if err2 != nil {
			err = err2
		}
		var out []tasks.User
		for _, rc := range names {
			u := tasks.User{ID: rc.ID, Name: rc.Name}
			if rc.Dept {
				u.ID = "dept:" + rc.ID
			}
			out = append(out, u)
		}
		return out
	}
	data := map[string]any{"Users": users, "Use": pick(rights.Use), "Manage": pick(rights.Manage)}
	if err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	s.render(w, r, "bookingrights", page{TitleKey: "booking.rights", Nav: "booking", Data: data})
}

func (s *Server) bookingRightsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/booking/rights"
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	audience := func(name string) booking.Audience {
		var a booking.Audience
		if name == "use" && r.FormValue("use_who") != "chosen" {
			return a
		}
		for _, v := range r.Form[name] {
			if d, ok := strings.CutPrefix(v, "dept:"); ok {
				a.Depts = append(a.Depts, d)
			} else if v != "" {
				a.Users = append(a.Users, v)
			}
		}
		return a
	}
	if err := s.Booking.SaveRights(r.Context(), sess.UserID, booking.Rights{Use: audience("use"), Manage: audience("manage")}); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// bookingManager: the employee manages «Онлайн-запись» (resources, types, links).
func (s *Server) bookingManager(r *http.Request, sess auth.Session) bool {
	_, manage := s.bookingMay(r.Context(), sess.UserID, sess.Admin)
	return manage
}
