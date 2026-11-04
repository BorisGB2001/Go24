package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// ---------- «Сотрудники»: the grid's columns, its order and the actions on the people chosen ----------

// employeeGrid is what the table of the list shows and lets do.
type employeeGrid struct {
	Columns []string        // the viewer's columns, in the grid's order
	Shown   map[string]bool // the same, to ask
	All     []string        // every column one may choose
	Sort    string          // the column of the order ("" — by name)
	Desc    bool
	Chat    bool // «Создать чат» with the people chosen
	Move    bool // «Перевести в отдел», «Добавить в отдел»: an administrator, or the structure's «Права доступа» let it somewhere
	Admin   bool // «Уволить», «Вернуть»
	Guests  bool // the list of guests: their collabs in the column of the department
}

// Label is the locale key of the column's header.
func (g employeeGrid) Label(col string) string {
	if g.Guests && col == "department" {
		return "company.col.collabs"
	}
	return "company.col." + col
}

// Sortable: the list may be ordered by the column.
func (g employeeGrid) Sortable(col string) bool { return slices.Contains(company.ListSorts, col) }

// SortLink is the query ordering the list by the column: by it, then backwards, then by name again.
func (g employeeGrid) SortLink(keep listQuery, col string) string {
	q := url.Values{}
	for k, v := range keep.Values {
		q[k] = v
	}
	q.Del("sort")
	q.Del("desc")
	switch {
	case g.Sort != col && !(g.Sort == "" && col == "name"):
		q.Set("sort", col)
	case !g.Desc:
		q.Set("sort", col)
		q.Set("desc", "1")
	}
	return q.Encode()
}

// SortMark is the arrow beside the header the list is ordered by.
func (g employeeGrid) SortMark(col string) string {
	if g.Sort != col && !(g.Sort == "" && col == "name") {
		return ""
	}
	if g.Desc {
		return "↓"
	}
	return "↑"
}

func (s *Server) employeeGrid(r *http.Request, sess auth.Session, f company.Filter, sort string, desc bool) employeeGrid {
	cols, err := s.Company.Columns(r.Context(), sess.UserID)
	if err != nil {
		s.Log.Warn("employee columns", "user_id", sess.UserID, "err", err)
		cols = company.DefaultColumns
	}
	g := employeeGrid{Columns: cols, Shown: map[string]bool{}, All: company.ListColumns, Sort: sort, Desc: desc,
		Chat: s.groupChats() != nil && !f.Fired, Admin: sess.Admin, Guests: f.Guests}
	for _, c := range cols {
		g.Shown[c] = true
	}
	if !f.Guests && !f.Fired {
		a, err := s.Company.StructAccessOf(r.Context(), sess.UserID, sess.Admin)
		g.Move = err == nil && a.Any("staff")
	}
	return g
}

// employeesColumns: POST /company/columns — «Настроить колонки», kept for the viewer.
func (s *Server) employeesColumns(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := listBack(r.FormValue("back"))
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	cols := r.Form["col"]
	if r.FormValue("reset") == "1" {
		cols = nil
	}
	if err := s.Company.SetColumns(r.Context(), sess.UserID, cols); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// employeesBulk: POST /company/bulk — an action on the people ticked in the list (id): chat — a group chat of the viewer
// and them; move (into the department, out of the others) and add (to the department) — as userDepartments lets one by
// one; fire and return — administrators. The people before the one that fails keep what was done.
func (s *Server) employeesBulk(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := listBack(r.FormValue("back"))
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	var ids []string
	for _, id := range r.Form["id"] {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.taskErr(w, r, i18n.E("err.emp_bulk_none"), back)
		return
	}
	ctx := r.Context()
	switch action := r.FormValue("action"); action {
	case "chat":
		chats := s.groupChats()
		if chats == nil {
			s.taskErr(w, r, i18n.E("err.not_found"), back)
			return
		}
		ids = slices.DeleteFunc(ids, func(id string) bool { return id == sess.UserID })
		var names []string
		for _, id := range ids {
			p, err := s.Company.Profile(ctx, id)
			if err != nil {
				s.taskErr(w, r, err, back)
				return
			}
			if !p.Active {
				s.taskErr(w, r, i18n.E("err.emp_bulk_fired"), back)
				return
			}
			names = append(names, p.Name)
		}
		if len(ids) == 0 {
			s.taskErr(w, r, i18n.E("err.emp_bulk_none"), back)
			return
		}
		room, err := chats.CreateGroup(ctx, sess.UserID, chatName(sess.Name, names), ids, false)
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		http.Redirect(w, r, imRoomURL(room), http.StatusSeeOther)
		return
	case "move", "add":
		dept := r.FormValue("dept")
		if dept == "" {
			s.taskErr(w, r, i18n.E("err.emp_bulk_dept"), back)
			return
		}
		for _, id := range ids {
			err := s.setDepartments(r, sess, id, func(was []string) []string {
				if action == "move" {
					return []string{dept}
				}
				if slices.Contains(was, dept) {
					return was
				}
				return append(slices.Clone(was), dept)
			})
			if err != nil {
				s.syncDeptChats(ctx)
				s.taskErr(w, r, err, back)
				return
			}
		}
		s.syncDeptChats(ctx)
	case "fire", "return":
		if !sess.Admin {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
		for _, id := range ids {
			if err := s.setActive(r, sess, id, action == "return"); err != nil {
				s.afterActive(ctx)
				s.taskErr(w, r, err, back)
				return
			}
		}
		s.afterActive(ctx)
	default:
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// chatName names the chat of the people chosen: the first three of them with the viewer, and how many more.
func chatName(me string, names []string) string {
	all := append([]string{me}, names...)
	if len(all) <= 4 {
		return strings.Join(all, ", ")
	}
	return strings.Join(all[:4], ", ") + " +" + strconv.Itoa(len(all)-4)
}

// listBack is the list to come back to: one of «Сотрудники» only.
func listBack(back string) string {
	if back == "/company" || strings.HasPrefix(back, "/company?") {
		return back
	}
	return "/company"
}
