package web

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/bi"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «BI-аналитика → Свои наборы данных»: a table of a CSV file as a data set ----------

// biFiles: the sets of one's own and «Загрузить CSV»; ?id= — a set: its columns with their types, the first rows,
// the file loaded again.
func (s *Server) biFiles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Admin": sess.Admin, "UserID": sess.UserID, "MaxMB": bi.MaxFileSize >> 20}
	id := r.URL.Query().Get("id")
	if id == "" {
		files, err := s.BI.Files(r.Context())
		if err != nil {
			s.fail(w, "bi files", err)
			return
		}
		data["Files"] = files
		s.render(w, r, "bifiles", page{TitleKey: "bi.files.title", Nav: "bi", Data: data})
		return
	}
	f, err := s.biFileOf(r, id)
	if err != nil {
		s.taskErr(w, r, err, "/bi/files")
		return
	}
	unread, err := s.BI.Unread(r.Context(), f)
	if err != nil {
		s.fail(w, "bi files", err)
		return
	}
	tr := func(k string) string { return s.tr(r, k) }
	res, err := s.BI.Rows(r.Context(), sess.UserID, bi.Export{Dataset: f.Code(), Limit: 20}, tr)
	if err != nil {
		s.fail(w, "bi files", err)
		return
	}
	rows := make([][]string, len(res.Rows))
	for n, row := range res.Rows {
		for _, v := range row[1:] { // the row's number first
			rows[n] = append(rows[n], biValue(v, tr))
		}
	}
	data["F"], data["Unread"], data["Preview"], data["Types"] = f, unread, rows, bi.Types
	data["Editable"] = f.Editable(biViewer(sess))
	s.render(w, r, "bifiles", page{TitleKey: "bi.files.title", Nav: "bi", Data: data})
}

// biValue is a value of an exported row as the page shows it.
func biValue(v any, tr func(string) string) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSuffix(v, " 00:00:00") // a date without its time
	case bool:
		return tr(map[bool]string{true: "bi.yes", false: "bi.no"}[v])
	case float64:
		return biNum(v, tr("bi.decimal"))
	}
	return ""
}

func (s *Server) biFileOf(r *http.Request, id string) (bi.File, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return bi.File{}, bi.ErrNotFound
	}
	return s.BI.File(r.Context(), n)
}

// biUpload reads the CSV file of a form (the field file) and its name.
func (s *Server) biUpload(w http.ResponseWriter, r *http.Request) (bi.Table, string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, bi.MaxFileSize+1<<16)
	if err := r.ParseMultipartForm(bi.MaxFileSize); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return bi.Table{}, "", i18n.E("bi.files.err.size", bi.MaxFileSize>>20)
		}
		return bi.Table{}, "", i18n.E("bi.files.err.csv")
	}
	file, head, err := r.FormFile("file")
	if err != nil {
		return bi.Table{}, "", i18n.E("bi.files.err.nofile")
	}
	defer file.Close()
	b, err := io.ReadAll(file)
	if err != nil {
		return bi.Table{}, "", err
	}
	t, err := bi.ParseCSV(b)
	return t, filepath.Base(head.Filename), err
}

// biFileCreate: «Загрузить CSV» — a new set of the file, its types guessed; the page of the set to check them.
func (s *Server) biFileCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	t, name, err := s.biUpload(w, r)
	var id int64
	if err == nil {
		id, err = s.BI.CreateFile(r.Context(), sess.UserID, r.FormValue("name"), name, t)
	}
	if err != nil {
		s.taskErr(w, r, err, "/bi/files")
		return
	}
	http.Redirect(w, r, "/bi/files?id="+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// biFileAct: the set's name and columns saved (save), the file loaded again — its rows replacing those there or
// added to them (load), the set deleted (delete); by its author or an administrator.
func (s *Server) biFileAct(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("fid")
	f, err := s.biFileOf(r, id)
	if err == nil && !f.Editable(biViewer(sess)) {
		err = tasks.ErrForbidden
	}
	back := "/bi/files?id=" + id
	if err != nil {
		s.taskErr(w, r, err, "/bi/files")
		return
	}
	switch r.PathValue("act") {
	case "save":
		cols := make([]bi.FileColumn, len(f.Columns))
		for i, c := range f.Columns {
			cols[i] = bi.FileColumn{Code: c.Code, Name: r.FormValue("name_" + c.Code), Type: r.FormValue("type_" + c.Code)}
		}
		err = s.BI.SaveFile(r.Context(), f.ID, r.FormValue("title"), r.FormValue("description"), cols)
	case "load":
		var t bi.Table
		var name string
		if t, name, err = s.biUpload(w, r); err == nil {
			err = s.BI.LoadFile(r.Context(), f.ID, name, t, r.FormValue("mode") == "add")
		}
	case "delete":
		if err = s.BI.DeleteFile(r.Context(), f.ID); err == nil {
			back = "/bi/files"
		}
	default:
		err = bi.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
