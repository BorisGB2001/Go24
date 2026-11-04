package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/notify"
)

// REST compatible with the portal, the messenger, part 4 (GO24DEV-922): im.notify.get, im.notify.history.search;
// im.dialog.users.list, im.dialog.unread; im.department.get, im.department.employees.get,
// im.department.managers.get, im.department.colleagues.list, im.search.department.list; im.revision.get. The company
// structure is not shown to guests extranet.

func init() {
	for name, m := range map[string]portalMethod{
		"im.notify.get":                 {Scope: "im:read", Run: (*Server).portalNotifyGet},
		"im.notify.history.search":      {Scope: "im:read", Run: (*Server).portalNotifyHistory},
		"im.dialog.users.list":          {Scope: "im:read", Run: (*Server).portalDialogUsers},
		"im.dialog.unread":              {Scope: "im:write", Run: (*Server).portalDialogUnread},
		"im.department.get":             {Scope: "im:read", Run: (*Server).portalIMDeptGet},
		"im.department.employees.get":   {Scope: "im:read", Run: (*Server).portalIMDeptEmployees},
		"im.department.managers.get":    {Scope: "im:read", Run: (*Server).portalIMDeptManagers},
		"im.department.colleagues.list": {Scope: "im:read", Run: (*Server).portalIMColleagues},
		"im.search.department.list":     {Scope: "im:read", Run: (*Server).portalSearchDepts},
		"im.revision.get":               {Scope: "im:read", Run: (*Server).portalIMRevision},
	} {
		portalMethods[strings.ToLower(name)] = m // looked up in lower case
	}
}

// portalNotifyModules: SEARCH_TYPE of im.notify.history.search, a module → the section of go24's
// notification settings; another value is taken as a go24 section itself.
var portalNotifyModules = map[string]string{"im": "messenger", "socialnetwork": "projects", "blog": "feed",
	"voximplant": "telephony", "rest": "apps", "intranet": "company", "timeman": "company", "landing": "booking",
	"mail": "mail", "tasks": "tasks", "calendar": "calendar", "crm": "crm", "disk": "disk", "sign": "sign"}

// portalNotifications: the caller's notifications by Filter, LIMIT (up to 50) older than LAST_ID, in the fields of
// im.notify.get — notify_type 1 a confirming one (its buttons in notify_buttons), 2 from a person, 4 a system one; with
// their authors in «users» and the counts.
func (s *Server) portalNotifications(c portalCall, f notify.Filter) (portalOut, error) {
	ctx := c.r.Context()
	limit, _ := strconv.Atoi(portalStr(portalParam(c.in, "LIMIT")))
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	last, _ := strconv.ParseInt(portalStr(portalParam(c.in, "LAST_ID")), 10, 64)
	items, err := s.Notify.Search(ctx, c.p.UserID, f, last, limit)
	if err != nil {
		return portalOut{}, err
	}
	var total, unread int
	if err := s.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE read_at IS NULL) FROM notifications
		WHERE user_id = $1`, c.p.UserID).Scan(&total, &unread); err != nil {
		return portalOut{}, err
	}
	var actors []string
	for _, it := range items {
		if it.ActorID != "" {
			actors = append(actors, it.ActorID)
		}
	}
	users, err := s.portalIMUsers(c, actors)
	if err != nil {
		return portalOut{}, err
	}
	nums, err := s.portalNums(ctx, "users", actors)
	if err != nil {
		return portalOut{}, err
	}
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	yn := map[bool]string{true: "Y", false: "N"}
	list := []map[string]any{}
	for _, it := range items {
		n := s.apiNotification(it)
		typ, buttons := 4, []map[string]string{}
		if it.ActorID != "" {
			typ = 2
		}
		if ans := it.Answers(); ans != nil {
			typ = 1
			for _, a := range ans {
				key := "calendar.answer." + a // as the bell titles its buttons
				if a == "accept" || a == "decline" {
					key = "notify.ans." + a
				}
				buttons = append(buttons, map[string]string{"VALUE": a, "TITLE": s.I18n.T(lang, key)})
			}
		}
		list = append(list, map[string]any{"id": it.ID, "chat_id": 0, "author_id": nums[it.ActorID],
			"date": it.CreatedAt.Format(time.RFC3339), "notify_type": typ, "notify_module": "go24",
			"notify_event": it.Kind, "notify_tag": "", "notify_sub_tag": "", "notify_title": "", "setting_name": it.Kind,
			"notify_read": yn[it.Read], "text": n.Text, "notify_buttons": buttons,
			"params": map[string]string{"URL": strings.TrimRight(s.BaseURL, "/") + it.Link()}})
	}
	people := []map[string]any{}
	for _, u := range users {
		people = append(people, u)
	}
	return portalOut{Result: map[string]any{"chat_id": 0, "notifications": list, "users": people,
		"total_count": total, "total_unread_count": unread}}, nil
}

// im.notify.get: the caller's notifications, newest first, LIMIT (up to 50) older than LAST_ID.
func (s *Server) portalNotifyGet(c portalCall) (portalOut, error) {
	return s.portalNotifications(c, notify.Filter{})
}

// im.notify.history.search: the caller's notifications with SEARCH_TEXT, of SEARCH_TYPE (a module — tasks,
// calendar, crm, im…) and of the day SEARCH_DATE, as im.notify.get gives them.
func (s *Server) portalNotifyHistory(c portalCall) (portalOut, error) {
	f := notify.Filter{Q: strings.TrimSpace(portalStr(portalParam(c.in, "SEARCH_TEXT")))}
	if t := strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "SEARCH_TYPE")))); t != "" {
		f.Group = t
		if g, ok := portalNotifyModules[t]; ok {
			f.Group = g
		}
	}
	if d := strings.TrimSpace(portalStr(portalParam(c.in, "SEARCH_DATE"))); d != "" {
		day, err := portalDay(d)
		if err != nil {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "SEARCH_DATE_ERROR", "err.portal_field", "SEARCH_DATE")
		}
		f.From, f.To = day, day
	}
	return s.portalNotifications(c, f)
}

// portalDay is the day of a date: 2026-10-28, 2026-10-28T10:00:00+02:00 or 28.10.2026.
func portalDay(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local), nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		t, err = time.ParseInLocation("02.01.2006", v, time.Local)
	}
	return t, err
}

// im.dialog.users.list: the people of DIALOG_ID in the messenger's fields (see portalIMUsers); SKIP_EXTERNAL=Y leaves
// the guests out.
func (s *Server) portalDialogUsers(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, err := s.portalDialog(c, portalParam(c.in, "DIALOG_ID"))
	if err != nil {
		return portalOut{}, err
	}
	mem, err := s.Matrix.ChatMembers(c.r.Context(), c.p.UserID, room)
	if err != nil {
		return portalOut{}, err
	}
	var ids []string
	for _, m := range mem {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	users, err := s.portalIMUsers(c, ids)
	if err != nil {
		return portalOut{}, err
	}
	nums, err := s.portalNums(c.r.Context(), "users", ids)
	if err != nil {
		return portalOut{}, err
	}
	skip := strings.ToUpper(portalStr(portalParam(c.in, "SKIP_EXTERNAL"))) == "Y"
	out := []map[string]any{}
	for _, id := range ids {
		if u, ok := users[nums[id]]; ok && !(skip && u["extranet"] == true) {
			out = append(out, u)
		}
	}
	return portalOut{Result: out}, nil
}

// im.dialog.unread: DIALOG_ID marked unread for the caller (MESSAGE_ID, the message to read from, is not needed: the
// chat is marked as a whole, as «Не прочитано» in the messenger).
func (s *Server) portalDialogUnread(c portalCall) (portalOut, error) {
	return s.portalMarkDialog(c, "unread", true)
}

// portalDeptNumsIn: the department numbers of ID.
func portalDeptNumsIn(c portalCall) []int64 {
	var out []int64
	for _, v := range portalList(portalParam(c.in, "ID")) {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// portalIMDepts: the departments matching cond ($1 — its argument) as the messenger gives them: id, name, full_name
// (the department and those above it, the company last), manager_user_id (the head's number, 0 — none); by number.
func (s *Server) portalIMDepts(c portalCall, cond string, arg any) ([]map[string]any, error) {
	rows, err := s.DB.Query(c.r.Context(), `WITH RECURSIVE up AS (
			SELECT d.id AS dept, d.parent_id, d.name, 0 AS depth FROM departments d
			UNION ALL SELECT up.dept, p.parent_id, p.name, up.depth + 1 FROM up JOIN departments p ON p.id = up.parent_id)
		SELECT d.num, d.name, (SELECT string_agg(up.name, ' / ' ORDER BY up.depth) FROM up WHERE up.dept = d.id),
			coalesce(h.num, 0)
		FROM departments d LEFT JOIN users h ON h.id = d.head_id WHERE `+cond+` ORDER BY d.num`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var num, head int64
		var name, full string
		if err := rows.Scan(&num, &name, &full, &head); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": num, "name": name, "full_name": full, "manager_user_id": head})
	}
	return out, rows.Err()
}

// portalIMPeople: the employees ids as numbers, or, with USER_DATA=Y, in the messenger's fields; in the order given.
func (s *Server) portalIMPeople(c portalCall, ids []string) (any, error) {
	nums, err := s.portalNums(c.r.Context(), "users", ids)
	if err != nil {
		return nil, err
	}
	if strings.ToUpper(portalStr(portalParam(c.in, "USER_DATA"))) != "Y" {
		out := []int64{}
		for _, id := range ids {
			if n, ok := nums[id]; ok {
				out = append(out, n)
			}
		}
		return out, nil
	}
	users, err := s.portalIMUsers(c, ids)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, id := range ids {
		if u, ok := users[nums[id]]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

// im.department.get: the departments ID (numbers) — see portalIMDepts; USER_DATA=Y adds the head in the messenger's
// fields as manager_user_data.
func (s *Server) portalIMDeptGet(c portalCall) (portalOut, error) {
	depts, err := s.portalIMDepts(c, `d.num = ANY($1)`, portalDeptNumsIn(c))
	if err != nil {
		return portalOut{}, err
	}
	if strings.ToUpper(portalStr(portalParam(c.in, "USER_DATA"))) == "Y" {
		for _, d := range depts {
			d["manager_user_data"] = nil
			if d["manager_user_id"] == int64(0) {
				continue
			}
			ids, err := s.portalUserIDs(c, strconv.FormatInt(d["manager_user_id"].(int64), 10))
			if err != nil {
				return portalOut{}, err
			}
			users, err := s.portalIMUsers(c, ids)
			if err != nil {
				return portalOut{}, err
			}
			for _, u := range users {
				d["manager_user_data"] = u
			}
		}
	}
	return portalOut{Result: depts}, nil
}

// portalDeptPeople: {department number: its people} of the departments ID; sql lists the user ids of the department $1.
func (s *Server) portalDeptPeople(c portalCall, sql string) (portalOut, error) {
	out := map[string]any{}
	for _, n := range portalDeptNumsIn(c) {
		rows, err := s.DB.Query(c.r.Context(), sql, n)
		if err != nil {
			return portalOut{}, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return portalOut{}, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return portalOut{}, err
		}
		people, err := s.portalIMPeople(c, ids)
		if err != nil {
			return portalOut{}, err
		}
		out[strconv.FormatInt(n, 10)] = people
	}
	return portalOut{Result: out}, nil
}

// im.department.employees.get: {department: its active employees, the head among them} of the departments ID — numbers,
// or with USER_DATA=Y in the messenger's fields.
func (s *Server) portalIMDeptEmployees(c portalCall) (portalOut, error) {
	return s.portalDeptPeople(c, `SELECT u.id::text FROM users u JOIN departments d ON d.num = $1
		WHERE u.active AND (u.id = d.head_id OR EXISTS (SELECT 1 FROM user_departments ud
			WHERE ud.user_id = u.id AND ud.department_id = d.id))
		ORDER BY u.id = d.head_id DESC, u.name, u.num`)
}

// im.department.managers.get: {department: its head} of the departments ID.
func (s *Server) portalIMDeptManagers(c portalCall) (portalOut, error) {
	return s.portalDeptPeople(c, `SELECT u.id::text FROM users u JOIN departments d ON d.num = $1 AND d.head_id = u.id`)
}

// im.department.colleagues.list: the caller's colleagues — the people of their departments, the heads of those, and,
// for a head, the heads of the departments under theirs; by name, LIMIT (up to 50) from OFFSET; numbers or, with
// USER_DATA=Y, in the messenger's fields.
func (s *Server) portalIMColleagues(c portalCall) (portalOut, error) {
	limit, _ := strconv.Atoi(portalStr(portalParam(c.in, "LIMIT")))
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	offset, _ := strconv.Atoi(portalStr(portalParam(c.in, "OFFSET")))
	offset = max(offset, 0)
	const colleagues = `WITH mine AS (SELECT department_id AS id FROM user_departments WHERE user_id = $1
			UNION SELECT id FROM departments WHERE head_id = $1)
		SELECT u.id::text, u.name, u.num FROM users u WHERE u.active AND NOT u.guest AND u.id <> $1 AND (
			EXISTS (SELECT 1 FROM user_departments ud WHERE ud.user_id = u.id AND ud.department_id IN (SELECT id FROM mine))
			OR EXISTS (SELECT 1 FROM departments d WHERE d.head_id = u.id
				AND (d.id IN (SELECT id FROM mine) OR d.parent_id IN (SELECT id FROM departments WHERE head_id = $1))))`
	ctx := c.r.Context()
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM (`+colleagues+`) x`, c.p.UserID).Scan(&total); err != nil {
		return portalOut{}, err
	}
	rows, err := s.DB.Query(ctx, colleagues+` ORDER BY u.name, u.num OFFSET $2 LIMIT $3`, c.p.UserID, offset, limit)
	if err != nil {
		return portalOut{}, err
	}
	var ids []string
	for rows.Next() {
		var id, name string
		var num int64 // ordered by
		if err := rows.Scan(&id, &name, &num); err != nil {
			rows.Close()
			return portalOut{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return portalOut{}, err
	}
	people, err := s.portalIMPeople(c, ids)
	out := portalOut{Result: people, Total: total, List: true}
	if offset+len(ids) < total {
		out.Next = offset + len(ids)
	}
	return out, err
}

// im.search.department.list: the departments whose name has FIND (three characters at least), as im.department.get
// gives them.
func (s *Server) portalSearchDepts(c portalCall) (portalOut, error) {
	find, err := s.portalFind(c)
	if err != nil {
		return portalOut{}, err
	}
	q := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(find) + "%"
	depts, err := s.portalIMDepts(c, `d.name ILIKE $1`, q)
	return portalOut{Result: depts}, err
}

// portalIMRevision is the revision of go24's im.* methods; it grows when they change, so an application can tell.
const portalIMRevision = 4

// im.revision.get: the revisions of the messenger's API — rest, web and mobile.
func (s *Server) portalIMRevision(c portalCall) (portalOut, error) {
	return portalOut{Result: map[string]int{"rest": portalIMRevision, "web": portalIMRevision, "mobile": portalIMRevision}}, nil
}
