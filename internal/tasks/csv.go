package tasks

import (
	"context"
	"errors"
	"strings"
	"time"

	"go24/internal/i18n"
)

// Export and import of tasks, as «Экспорт в Excel» and «Импорт задач» of the portal task list: the list goes out as
// a table (the web layer writes it), tasks come in from a table whose first row names the columns (in Russian or
// English, the case and spaces don't matter). The importer is the creator of every task; people are found by name or
// e-mail; a row that fails tells its number and why.

// ImportColumns are the fields a column may fill, by the names people give the columns (the export's own headers
// among them, so an exported list comes back in).
var ImportColumns = map[string]string{
	"title": "title", "название": "title", "задача": "title", "name": "title",
	"description": "description", "описание": "description",
	"responsible": "responsible", "ответственный": "responsible", "исполнитель": "responsible", "assignee": "responsible",
	"accomplices": "accomplices", "соисполнители": "accomplices", "соисполнитель": "accomplices",
	"auditors": "auditors", "наблюдатели": "auditors", "наблюдатель": "auditors",
	"deadline": "deadline", "крайний срок": "deadline", "срок": "deadline",
	"priority": "priority", "важная": "priority", "важность": "priority", "приоритет": "priority", "important": "priority",
	"tags": "tags", "теги": "tags", "тег": "tags",
	"project": "project", "проект": "project", "группа": "project", "group": "project",
	"checklist": "checklist", "чек-лист": "checklist", "чеклист": "checklist",
}

// ImportResult tells what an import did.
type ImportResult struct {
	Created []string      // the keys of the new tasks
	Errors  []ImportError // the first 50
}

// ImportError is a row that was not imported (the row's number in the table, from 2) and why.
type ImportError struct {
	Row int
	Err *i18n.Error
}

// ImportMax caps the rows of one import.
const ImportMax = 5000

// Import makes tasks of the table's rows in the project def, or in the row's «Проект» (a key or a name of a project
// the user may work in).
func (s *Store) Import(ctx context.Context, userID string, def Project, table [][]string) (ImportResult, error) {
	var res ImportResult
	if len(table) < 2 {
		return res, i18n.E("err.crm_csv_empty")
	}
	if len(table) > ImportMax+1 {
		return res, i18n.E("err.tasks_import_big", ImportMax)
	}
	cols := map[int]string{}
	for i, h := range table[0] {
		if f := ImportColumns[strings.ToLower(strings.TrimSpace(h))]; f != "" {
			cols[i] = f
		}
	}
	hasTitle := false
	for _, f := range cols {
		hasTitle = hasTitle || f == "title"
	}
	if !hasTitle {
		return res, i18n.E("err.tasks_import_columns")
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		return res, err
	}
	people := map[string]string{} // a name or an e-mail, folded → id
	for _, u := range users {
		people[nameKey(u.Name)] = u.ID
		if u.Email != "" {
			people[nameKey(u.Email)] = u.ID
		}
	}
	projects, err := s.ListProjects(ctx, userID)
	if err != nil {
		return res, err
	}
	byName := map[string]Project{}
	for _, p := range projects {
		byName[nameKey(p.Key)], byName[nameKey(p.Name)] = p, p
	}
	sched, err := s.Schedule(ctx)
	if err != nil {
		return res, err
	}
	endH, endM := 18, 0
	if t, err := time.Parse("15:04", sched.End); err == nil {
		endH, endM = t.Hour(), t.Minute()
	}
	fail := func(row int, err error) error {
		var ie *i18n.Error
		if !errors.As(err, &ie) {
			return err
		}
		if len(res.Errors) < 50 {
			res.Errors = append(res.Errors, ImportError{row, ie})
		}
		return nil
	}
	for n, rec := range table[1:] {
		row := n + 2
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		v := map[string]string{}
		for i, f := range cols {
			if i < len(rec) {
				v[f] = strings.TrimSpace(rec[i])
			}
		}
		in := NewTask{Title: v["title"], Description: v["description"], Priority: 1}
		p := def
		var err error
		if name := v["project"]; name != "" {
			var ok bool
			if p, ok = byName[nameKey(name)]; !ok {
				err = i18n.E("err.tasks_import_project", name)
			}
		} else if p.ID == "" {
			err = i18n.E("err.tasks_import_no_project")
		}
		person := func(name string) string {
			id := people[nameKey(name)]
			if id == "" && err == nil {
				err = i18n.E("err.tasks_import_user", name)
			}
			return id
		}
		if name := v["responsible"]; name != "" {
			in.ResponsibleID = person(name)
		}
		for _, name := range splitList(v["accomplices"]) {
			in.Accomplices = append(in.Accomplices, person(name))
		}
		for _, name := range splitList(v["auditors"]) {
			in.Auditors = append(in.Auditors, person(name))
		}
		if d := v["deadline"]; d != "" && err == nil {
			if in.Deadline, err = ParseImportDate(d, endH, endM); err != nil {
				err = i18n.E("err.tasks_import_date", d)
			}
		}
		if highWords[nameKey(v["priority"])] {
			in.Priority = 2
		}
		if c := v["checklist"]; c != "" {
			in.Checklist = strings.Split(strings.ReplaceAll(c, ";", "\n"), "\n")
		}
		var key string
		if err == nil {
			key, err = s.CreateTask(ctx, userID, p, in)
		}
		if err == nil && v["tags"] != "" {
			var id string
			if id, err = s.TaskIDByKey(ctx, key); err == nil {
				for _, tag := range splitList(v["tags"]) {
					if err = s.SetTag(ctx, userID, id, tag, true); err != nil {
						break
					}
				}
			}
			if err != nil { // the task is made; a bad tag only tells
				if ferr := fail(row, err); ferr != nil {
					return res, ferr
				}
				err = nil
			}
		}
		if err != nil {
			if ferr := fail(row, err); ferr != nil {
				return res, ferr
			}
			continue
		}
		res.Created = append(res.Created, key)
	}
	return res, nil
}

// highWords say «важная задача» in the priority column.
var highWords = map[string]bool{"да": true, "yes": true, "1": true, "+": true, "true": true, "высокий": true, "высокая": true,
	"high": true, "важная": true, "важно": true, "срочно": true}

// ParseImportDate reads a deadline as Excel and people write it: 31.12.2026 18:00, 31.12.2026, 2026-12-31 18:00,
// 2026-12-31T18:00, 2026-12-31 (a date alone ends at the end of the working day endH:endM), in local time.
func ParseImportDate(v string, endH, endM int) (*time.Time, error) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{"02.01.2006 15:04", "02.01.2006 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2.1.2006 15:04"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return &t, nil
		}
	}
	for _, layout := range []string{"02.01.2006", "2006-01-02", "2.1.2006", "02.01.06"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			t = time.Date(t.Year(), t.Month(), t.Day(), endH, endM, 0, 0, time.Local)
			return &t, nil
		}
	}
	return nil, i18n.E("err.bad_deadline")
}

// splitList splits «Анна Ли, Пётр С» (by «,» or «;»).
func splitList(v string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// nameKey makes names compare the way people type them: case, ё/е and extra spaces don't matter.
func nameKey(s string) string { return strings.Join(strings.Fields(fold(s)), " ") }

// ParticipantNames gives the accomplices and auditors of the tasks by name: task id → role → names.
func (s *Store) ParticipantNames(ctx context.Context, ids []string) (map[string]map[string][]string, error) {
	out := map[string]map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT tp.task_id::text, tp.role, u.name FROM task_participants tp JOIN users u ON u.id = tp.user_id
		WHERE tp.task_id = ANY($1::uuid[]) AND tp.role IN ('accomplice', 'auditor') ORDER BY u.name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, role, name string
		if err := rows.Scan(&id, &role, &name); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string][]string{}
		}
		out[id][role] = append(out[id][role], name)
	}
	return out, rows.Err()
}

// ExportName is the file's name: tasks-2026-10-22.csv.
func ExportName(now time.Time) string {
	return "tasks-" + now.Format(time.DateOnly) + ".csv"
}
