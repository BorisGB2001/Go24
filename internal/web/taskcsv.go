package web

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Экспорт в Excel» and «Импорт задач» of the task list ----------

// taskExportHead are the columns of an exported list; their names are import columns too, so the file comes back in.
var taskExportHead = []string{"tasks.csv.key", "tasks.csv.title", "tasks.csv.status", "tasks.csv.project", "tasks.csv.creator",
	"tasks.csv.responsible", "tasks.csv.accomplices", "tasks.csv.auditors", "tasks.csv.deadline", "tasks.csv.created",
	"tasks.csv.closed", "tasks.csv.priority", "tasks.csv.tags", "tasks.csv.description"}

// taskExport gives the task list, as its filter has it, as a CSV file Excel opens (UTF-8 with BOM, «;»).
func (s *Server) taskExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.taskPerm(r, sess, "export") {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	f, _, err := s.listFilter(r, sess)
	var list []tasks.Task
	if err == nil {
		list, err = s.Tasks.ListTasks(r.Context(), f)
	}
	ids := make([]string, len(list))
	for i, t := range list {
		ids[i] = t.ID
	}
	var names map[string]map[string][]string
	if err == nil {
		names, err = s.Tasks.ParticipantNames(r.Context(), ids)
	}
	if err != nil {
		s.taskErr(w, r, err, "/tasks?"+filterQuery(r.URL.Query()).Encode())
		return
	}
	tr := func(k string) string { return s.tr(r, k) }
	when := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Local().Format("02.01.2006 15:04")
	}
	head := make([]string, len(taskExportHead))
	for i, k := range taskExportHead {
		head[i] = tr(k)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+tasks.ExportName(time.Now()))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	for _, t := range list {
		prio := ""
		if t.Priority == 2 {
			prio = tr("tasks.csv.yes")
		}
		created := t.CreatedAt
		_ = cw.Write([]string{t.Key(), t.Title, tr("status." + string(t.Status)), t.ProjectKey, t.CreatorName, t.ResponsibleName,
			strings.Join(names[t.ID]["accomplice"], ", "), strings.Join(names[t.ID]["auditor"], ", "), when(t.Deadline),
			when(&created), when(t.ClosedAt), prio, strings.Join(t.Tags, ", "), t.Description})
	}
	cw.Flush()
}

// taskImport makes tasks of an uploaded CSV in the chosen project (or each row's «Проект»).
func (s *Server) taskImport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.taskPerm(r, sess, "import") {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := "/tasks"
	var def tasks.Project
	var err error
	if key := r.FormValue("project"); key != "" {
		back += "?project=" + url.QueryEscape(key)
		def, err = s.Tasks.ProjectByKey(r.Context(), sess.UserID, key)
	}
	var table [][]string
	if err == nil {
		file, _, ferr := r.FormFile("file")
		if ferr != nil {
			err = i18n.E("err.crm_csv_empty")
		} else {
			defer file.Close()
			table, err = crm.ReadCSV(file)
		}
	}
	var res tasks.ImportResult
	if err == nil {
		res, err = s.Tasks.Import(r.Context(), sess.UserID, def, table)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("tasks import", "user_id", sess.UserID, "created", len(res.Created), "failed", len(res.Errors))
	s.Matrix.Wake()
	msg := s.tr(r, "tasks.import_done", len(res.Created), len(res.Errors))
	for i, e := range res.Errors {
		if i == 5 {
			msg += " …"
			break
		}
		msg += " · " + s.tr(r, "crm.import_row", e.Row) + ": " + s.I18n.Err(s.lang(r), e.Err)
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
