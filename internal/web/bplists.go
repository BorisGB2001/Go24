package web

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/tasks"
)

// ---------- «Списки» (bp.List) ----------

// bpListsHome: the lists the employee sees; «Создать список» of administrators.
func (s *Server) bpListsHome(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	lists, err := s.BP.Lists(bpAdmin(r, sess), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	s.render(w, r, "bplists", page{TitleKey: "lists.title", Nav: "bp", Data: map[string]any{"Lists": lists}})
}

// bpListPage: the table of a list's elements — words to find (q), the order (sort, desc=1), pages of bp.ListPage.
func (s *Server) bpListPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := bpAdmin(r, sess)
	l, err := s.BP.List(ctx, sess.UserID, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	q := r.URL.Query()
	pg, _ := strconv.Atoi(q.Get("page"))
	pg = max(pg, 0)
	f := bp.ElementFilter{Query: q.Get("q"), Sort: q.Get("sort"), Desc: q.Get("desc") == "1", Limit: bp.ListPage + 1, Offset: pg * bp.ListPage}
	list, err := s.BP.Elements(ctx, l, f)
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	data := map[string]any{"L": l, "Elements": list, "Q": f.Query, "Sort": f.Sort, "Desc": f.Desc, "Page": pg,
		"CanWrite": s.BP.CanWriteList(ctx, sess.UserID, l.ID)}
	if len(list) > bp.ListPage { // one more than a page: there is a next page
		data["Elements"], data["Next"] = list[:bp.ListPage], pg+1
	}
	if data["Users"], err = s.Tasks.ListUsers(r.Context()); err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	s.render(w, r, "bplist", page{Title: l.Name, Nav: "bp", Data: data})
}

// bpListExport: «Выгрузить CSV» — the elements found (q) in the table's order, every field, for Excel.
func (s *Server) bpListExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := bpAdmin(r, sess)
	l, err := s.BP.List(ctx, sess.UserID, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	q := r.URL.Query()
	list, err := s.BP.Elements(ctx, l, bp.ElementFilter{Query: q.Get("q"), Sort: q.Get("sort"), Desc: q.Get("desc") == "1", Limit: 100000})
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	names := map[string]string{}
	for _, u := range users {
		names[u.ID] = u.Name
	}
	head := []string{s.tr(r, "lists.el_name")}
	for _, f := range l.Fields {
		head = append(head, f.Label)
	}
	head = append(head, s.tr(r, "lists.created_by"), s.tr(r, "lists.created_at"), s.tr(r, "lists.changed_at"))
	rows := make([][]string, len(list))
	for i, e := range list {
		row := []string{e.Name}
		for _, f := range l.Fields {
			row = append(row, s.listShown(r, f, e.Values[f.Key], names))
		}
		rows[i] = append(row, e.CreatedBy.Name, e.CreatedAt.Local().Format(s.tr(r, "format.datetime")), e.ChangedAt.Local().Format(s.tr(r, "format.datetime")))
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(biFileName(l.Name)+".csv"))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(rows)
}

// listShown is a value as people read it: «Да»/«Нет», an employee's name, a date and time without «T».
func (s *Server) listShown(r *http.Request, f bp.Field, v string, names map[string]string) string {
	switch {
	case v == "":
	case f.Kind == "bool":
		return s.tr(r, "bp.bool."+v)
	case f.Kind == "user":
		return names[v]
	}
	return f.Shown(v)
}

// bpListSettings: a list's name, description, fields and columns, its rights and the templates of its elements
// (administrators; «new» — a new list).
func (s *Server) bpListSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := bpAdmin(r, sess)
	var l bp.List
	if id := r.PathValue("id"); id != "new" {
		var err error
		if l, err = s.BP.List(ctx, sess.UserID, id); err != nil {
			s.taskErr(w, r, err, "/bp/lists")
			return
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	data := map[string]any{"L": l, "FieldRows": bpFieldRows(l.Fields, users), "FieldKinds": bp.FieldKinds}
	if l.ID != "" {
		for _, op := range []string{"read", "write"} {
			refs, err := s.BP.ListRights(r.Context(), l.ID, op)
			if err != nil {
				s.taskErr(w, r, err, "/bp/lists")
				return
			}
			var chips []tasks.User
			for _, ref := range refs {
				chips = append(chips, tasks.User{ID: ref.ID, Name: ref.Name})
			}
			data[map[string]string{"read": "Readers", "write": "Writers"}[op]] = chips
		}
		all, err := s.BP.Templates(r.Context(), bp.EntityList, false)
		if err != nil {
			s.taskErr(w, r, err, "/bp/lists")
			return
		}
		data["Templates"] = slices.DeleteFunc(all, func(t bp.Template) bool { return t.ListID != l.ID })
	}
	title := l.Name
	if title == "" {
		title = s.tr(r, "lists.new")
	}
	s.render(w, r, "bplistedit", page{Title: title, Nav: "bp", Data: data})
}

// bpListSave: op=meta (name, description, fields, columns «col»), op=read|write (the rights: to — people and «dept:ID»).
func (s *Server) bpListSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := bpAdmin(r, sess)
	id := r.PathValue("id")
	back := "/bp/lists/" + id + "/settings"
	if op := r.FormValue("op"); op == "read" || op == "write" {
		msg := s.tr(r, "common.saved")
		if err := s.BP.SetListRights(ctx, id, op, r.Form["to"]); err != nil {
			msg = s.errText(r, err)
		}
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	var l bp.List
	if id != "new" {
		var err error
		if l, err = s.BP.List(ctx, sess.UserID, id); err != nil {
			s.taskErr(w, r, err, "/bp/lists")
			return
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	old := l.Fields
	l.Name, l.Description, l.Fields = r.FormValue("name"), r.FormValue("description"), parseFields(r, "f", users)
	l.Columns = r.Form["col"]
	for _, f := range l.Fields { // a field added shows in the table while it has few columns
		if !slices.ContainsFunc(old, func(o bp.Field) bool { return o.Key == f.Key }) && len(l.Columns) < 6 {
			l.Columns = append(l.Columns, f.Key)
		}
	}
	saved, err := s.BP.SaveList(ctx, sess.UserID, l)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/bp/lists/"+saved+"/settings?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// bpListDelete: «Удалить список» with its elements (administrators).
func (s *Server) bpListDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.BP.DeleteList(bpAdmin(r, sess), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	http.Redirect(w, r, "/bp/lists", http.StatusSeeOther)
}

// bpListLine is a line of an element's history as the page shows it: a change's field with its label.
type bpListLine struct {
	bp.ListLine
	Fields []bp.Field // of each change
}

// bpElementPage: an element's card — its fields (to change, for one who may), history and business processes;
// /bp/lists/{id}/add — a new element of the list.
func (s *Server) bpElementPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := bpAdmin(r, sess)
	var e bp.Element
	var l bp.List
	var err error
	if lid := r.PathValue("list"); lid != "" {
		l, err = s.BP.List(ctx, sess.UserID, lid)
		e.ListID = l.ID
	} else {
		e, l, err = s.BP.Element(ctx, sess.UserID, r.PathValue("id"))
	}
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	fields := slices.Clone(l.Fields) // the inputs show the element's values
	if e.ID != "" {
		for i := range fields {
			fields[i].Default = e.Values[fields[i].Key]
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	data := map[string]any{"L": l, "E": e, "Fields": fields, "Users": users, "CanWrite": s.BP.CanWriteList(ctx, sess.UserID, l.ID)}
	if e.ID != "" {
		lines, err := s.BP.ElementLog(r.Context(), e.ID)
		if err != nil {
			s.taskErr(w, r, err, "/bp/lists")
			return
		}
		name := bp.Field{Key: "name", Label: s.tr(r, "lists.el_name"), Kind: "string"}
		var log []bpListLine
		for _, ln := range lines {
			v := bpListLine{ListLine: ln}
			for _, c := range ln.Changes {
				f, ok := name, c.Key == "name"
				for _, lf := range l.Fields {
					if lf.Key == c.Key {
						f, ok = lf, true
					}
				}
				if !ok { // a field taken away from the list
					f = bp.Field{Key: c.Key, Label: c.Key, Kind: "string"}
				}
				v.Fields = append(v.Fields, f)
			}
			log = append(log, v)
		}
		data["Log"] = log
		// «Бизнес-процессы»: those run on the element, the templates of the list the viewer starts
		tpls, err := s.BP.ListTemplates(r.Context(), l.ID)
		if err == nil {
			tpls, err = s.BP.Startable(ctx, sess.UserID, tpls)
		}
		if err == nil {
			data["BPInstances"], err = s.BP.Instances(r.Context(), sess.UserID, bp.EntityList, e.ID)
		}
		if err != nil {
			s.taskErr(w, r, err, "/bp/lists")
			return
		}
		data["BPTemplates"] = tpls
	}
	title := e.Name
	if title == "" {
		title = s.tr(r, "lists.el_new")
	}
	s.render(w, r, "bplistel", page{Title: title, Nav: "bp", Data: data})
}

// bpElementSave adds an element (/bp/lists/{list}/add) or changes one: the name and the fields f_<key>.
func (s *Server) bpElementSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := bpAdmin(r, sess)
	e := bp.Element{ID: r.PathValue("id"), ListID: r.PathValue("list"), Name: r.FormValue("name"), Values: map[string]string{}}
	back := "/bp/lists/" + e.ListID + "/add"
	if e.ID != "" {
		back = "/bp/el/" + e.ID
		old, l, err := s.BP.Element(ctx, sess.UserID, e.ID)
		if err != nil {
			s.taskErr(w, r, err, "/bp/lists")
			return
		}
		e.ListID = old.ListID
		for _, f := range l.Fields {
			if _, ok := r.Form["f_"+f.Key]; ok {
				e.Values[f.Key] = r.FormValue("f_" + f.Key)
			}
		}
	} else {
		l, err := s.BP.List(ctx, sess.UserID, e.ListID)
		if err != nil {
			s.taskErr(w, r, err, "/bp/lists")
			return
		}
		for _, f := range l.Fields {
			e.Values[f.Key] = r.FormValue("f_" + f.Key)
		}
	}
	id, err := s.BP.SaveElement(ctx, sess.UserID, e)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if r.FormValue("more") != "" { // «Сохранить и добавить ещё»
		http.Redirect(w, r, "/bp/lists/"+e.ListID+"/add?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	if e.ID != "" {
		http.Redirect(w, r, "/bp/el/"+id+"?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/bp/lists/"+e.ListID, http.StatusSeeOther)
}

// bpElementDelete deletes an element; back to its list.
func (s *Server) bpElementDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := bpAdmin(r, sess)
	e, _, err := s.BP.Element(ctx, sess.UserID, r.PathValue("id"))
	if err == nil {
		err = s.BP.DeleteElement(ctx, sess.UserID, e.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/bp/lists")
		return
	}
	http.Redirect(w, r, "/bp/lists/"+e.ListID, http.StatusSeeOther)
}
