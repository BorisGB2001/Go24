package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/calendar"
	"go24/internal/feed"
	"go24/internal/i18n"
)

// REST compatible with the portal (GO24DEV-873): the calendar — calendar.event.get, .getbyid, .get.nearest, .add,
// .update, .delete, calendar.meeting.status.get/set, calendar.accessibility.get, calendar.section.get, .add, .update,
// .delete. Meetings and calendars go by their numbers (migration 735); the rights are those of the calendar pages: one
// sees what one's calendar shows, meetings are changed by their organizers, a colleague's calendar — by whom they gave
// «Управление» of it.
//
// A calendar is a type and an owner: «user» and the employee's number, «group» and the project's,
// «company_calendar». Its sections are go24's calendars: the person's main one and their own, the project's, the
// company's. the portal keeps a copy of a meeting in the calendar of each invited person; go24 keeps one meeting, which
// the invited see in their main calendar with their own MEETING_STATUS.

func init() {
	for name, m := range map[string]portalMethod{
		"calendar.event.get":          {Scope: "calendar:read", Run: (*Server).portalCalEvents},
		"calendar.event.getbyid":      {Scope: "calendar:read", Run: (*Server).portalCalEvent},
		"calendar.event.get.nearest":  {Scope: "calendar:read", Run: (*Server).portalCalNearest},
		"calendar.event.add":          {Scope: "calendar:write", Run: (*Server).portalCalAdd},
		"calendar.event.update":       {Scope: "calendar:write", Run: (*Server).portalCalUpdate},
		"calendar.event.delete":       {Scope: "calendar:write", Run: (*Server).portalCalDelete},
		"calendar.meeting.status.get": {Scope: "calendar:read", Run: (*Server).portalCalStatusGet},
		"calendar.meeting.status.set": {Scope: "calendar:write", Run: (*Server).portalCalStatusSet},
		"calendar.accessibility.get":  {Scope: "calendar:read", Run: (*Server).portalCalAccessibility},
		"calendar.section.get":        {Scope: "calendar:read", Run: (*Server).portalCalSections},
		"calendar.section.add":        {Scope: "calendar:write", Run: (*Server).portalCalSectionAdd},
		"calendar.section.update":     {Scope: "calendar:write", Run: (*Server).portalCalSectionUpdate},
		"calendar.section.delete":     {Scope: "calendar:write", Run: (*Server).portalCalSectionDelete},
	} {
		portalMethods[name] = m
	}
}

// portalCal is a calendar: Type user (Owner — the employee's id), group (Owner — the project's id, Key — its
// key) or company_calendar.
type portalCal struct {
	Type, Owner, Key string
}

// portalCalOf reads type and ownerId; a user's calendar without ownerId is the caller's own.
func (s *Server) portalCalOf(c portalCall) (portalCal, error) {
	ctx := c.r.Context()
	cal := portalCal{Type: strings.ToLower(portalStr(portalParam(c.in, "type")))}
	owner := portalParam(c.in, "ownerId")
	switch cal.Type {
	case "", "user":
		cal.Type = "user"
		id, err := s.portalNum(ctx, "users", owner)
		if err != nil {
			return cal, err
		}
		cal.Owner = id
		if id == "" {
			cal.Owner = c.p.UserID
		}
	case "group":
		n, err := strconv.ParseInt(portalStr(owner), 10, 64)
		if err != nil {
			return cal, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "ownerId")
		}
		if err := s.DB.QueryRow(ctx, `SELECT id::text, key FROM projects WHERE num = $1`, n).Scan(&cal.Owner, &cal.Key); err != nil {
			return cal, i18n.E("err.not_found")
		}
		if _, err := s.Tasks.ProjectByKey(ctx, c.p.UserID, cal.Key); err != nil {
			return cal, err
		}
	case "company_calendar", "company":
		cal.Type = "company_calendar"
	default:
		return cal, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "type")
	}
	return cal, nil
}

// ---------- sections ----------

// portalSection is a go24 calendar as a section: the main calendar of User, one's own Calendar, a Project's,
// or the Company's.
type portalSection struct {
	User, Calendar, Project string
	Company                 bool
}

// portalSectionNum is the number of the section, given when it is asked for first.
func (s *Server) portalSectionNum(ctx context.Context, x portalSection) (int64, error) {
	null := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	var n int64
	err := s.DB.QueryRow(ctx, `WITH found AS (
			SELECT num FROM portal_calendar_sections WHERE user_id::text IS NOT DISTINCT FROM $1 AND calendar_id::text IS NOT DISTINCT FROM $2
				AND project_id::text IS NOT DISTINCT FROM $3 AND company = $4),
		added AS (
			INSERT INTO portal_calendar_sections (user_id, calendar_id, project_id, company)
			SELECT $1::uuid, $2::uuid, $3::uuid, $4 WHERE NOT EXISTS (SELECT 1 FROM found)
			ON CONFLICT DO NOTHING RETURNING num)
		SELECT num FROM found UNION ALL SELECT num FROM added`, null(x.User), null(x.Calendar), null(x.Project), x.Company).Scan(&n)
	if err != nil { // added by another request in between
		err = s.DB.QueryRow(ctx, `SELECT num FROM portal_calendar_sections WHERE user_id::text IS NOT DISTINCT FROM $1
			AND calendar_id::text IS NOT DISTINCT FROM $2 AND project_id::text IS NOT DISTINCT FROM $3 AND company = $4`,
			null(x.User), null(x.Calendar), null(x.Project), x.Company).Scan(&n)
	}
	return n, err
}

// portalSectionByNum is the section of the number.
func (s *Server) portalSectionByNum(c portalCall, v any) (portalSection, error) {
	var x portalSection
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(v)), 10, 64)
	if err != nil {
		return x, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "section")
	}
	err = s.DB.QueryRow(c.r.Context(), `SELECT coalesce(user_id::text, ''), coalesce(calendar_id::text, ''), coalesce(project_id::text, ''), company
		FROM portal_calendar_sections WHERE num = $1`, n).Scan(&x.User, &x.Calendar, &x.Project, &x.Company)
	if err != nil {
		return x, i18n.E("err.not_found")
	}
	return x, nil
}

// portalSectionPerm: what the caller may do with a calendar, as PERM.
func portalSectionPerm(view, edit, own bool) map[string]bool {
	return map[string]bool{"view_time": view, "view_title": view, "view_full": view, "add": edit, "edit": edit,
		"edit_section": own, "access": own}
}

// calendar.section.get: the sections of the calendar — of a person their main one first, then their own by name (one's
// own only to oneself), of a project or the company — one.
func (s *Server) portalCalSections(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	cal, err := s.portalCalOf(c)
	if err != nil {
		return portalOut{}, err
	}
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := []map[string]any{}
	add := func(x portalSection, name, color string, owner, by int64, edit, own bool) error {
		n, err := s.portalSectionNum(ctx, x)
		if err != nil {
			return err
		}
		out = append(out, map[string]any{"ID": strconv.FormatInt(n, 10), "NAME": name, "DESCRIPTION": "", "COLOR": color,
			"TEXT_COLOR": "", "CAL_TYPE": cal.Type, "OWNER_ID": strconv.FormatInt(owner, 10), "CREATED_BY": strconv.FormatInt(by, 10),
			"ACTIVE": "Y", "EXPORT": map[string]any{"ALLOW": false}, "PERM": portalSectionPerm(true, edit, own)})
		return nil
	}
	switch cal.Type {
	case "user":
		num, err := s.portalUserNum(c, cal.Owner)
		if err != nil {
			return portalOut{}, err
		}
		edit := cal.Owner == c.p.UserID
		if !edit {
			if edit, err = s.Calendar.Manages(ctx, c.p.UserID, cal.Owner); err != nil {
				return portalOut{}, err
			}
			if _, err := s.Calendar.PersonName(ctx, c.p.UserID, cal.Owner); err != nil { // a colleague one may see
				return portalOut{}, err
			}
		}
		color, err := s.Calendar.MainColor(ctx, cal.Owner)
		if err != nil {
			return portalOut{}, err
		}
		if err := add(portalSection{User: cal.Owner}, s.I18n.T(lang, "calendar.cals.personal"), color, num, num, edit, edit); err != nil {
			return portalOut{}, err
		}
		if cal.Owner == c.p.UserID {
			list, err := s.Calendar.Sections(ctx, c.p.UserID)
			if err != nil {
				return portalOut{}, err
			}
			for _, x := range list {
				if err := add(portalSection{Calendar: x.ID}, x.Name, x.Color, num, num, x.URL == "", true); err != nil {
					return portalOut{}, err
				}
			}
		}
	case "group":
		var name string
		var num, by int64
		if err := s.DB.QueryRow(ctx, `SELECT p.name, p.num, coalesce(u.num, 0) FROM projects p LEFT JOIN users u ON u.id = p.owner_id
			WHERE p.id::text = $1`, cal.Owner).Scan(&name, &num, &by); err != nil {
			return portalOut{}, err
		}
		if err := add(portalSection{Project: cal.Owner}, name, "", num, by, true, false); err != nil {
			return portalOut{}, err
		}
	default:
		if err := add(portalSection{Company: true}, s.I18n.T(lang, "calendar.cals.company"), "", 0, 0, true, false); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: out}, nil
}

// calendar.section.add: one's own calendar (name, color) → its number; projects and the company have one calendar each.
func (s *Server) portalCalSectionAdd(c portalCall) (portalOut, error) {
	cal, err := s.portalCalOf(c)
	if err != nil {
		return portalOut{}, err
	}
	if cal.Type != "user" || cal.Owner != c.p.UserID {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "type")
	}
	id, err := s.Calendar.SaveSection(c.r.Context(), c.p.UserID, calendar.Section{Name: portalStr(portalParam(c.in, "name")),
		Color: portalStr(portalParam(c.in, "color"))})
	if err != nil {
		return portalOut{}, err
	}
	n, err := s.portalSectionNum(c.r.Context(), portalSection{Calendar: id})
	return portalOut{Result: n}, err
}

// calendar.section.update: id, name and color — of one's own calendar; of the main one — the colour only.
func (s *Server) portalCalSectionUpdate(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	x, err := s.portalSectionByNum(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	color, hasColor := portalParamOK(c.in, "color")
	switch {
	case x.User == c.p.UserID:
		if hasColor {
			err = s.Calendar.SetMainColor(ctx, c.p.UserID, portalStr(color))
		}
	case x.Calendar != "":
		list, err := s.Calendar.Sections(ctx, c.p.UserID)
		if err != nil {
			return portalOut{}, err
		}
		i := slices.IndexFunc(list, func(y calendar.Section) bool { return y.ID == x.Calendar })
		if i < 0 {
			return portalOut{}, i18n.E("err.not_found")
		}
		cur := list[i]
		if v, ok := portalParamOK(c.in, "name"); ok {
			cur.Name = portalStr(v)
		}
		if hasColor {
			cur.Color = portalStr(color)
		}
		_, err = s.Calendar.SaveSection(ctx, c.p.UserID, cur)
		if err != nil {
			return portalOut{}, err
		}
	default:
		return portalOut{}, calendar.ErrForbidden
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: portalStr(portalParam(c.in, "id"))}, nil
}

// calendar.section.delete: one's own calendar with its meetings; the main one is not deleted.
func (s *Server) portalCalSectionDelete(c portalCall) (portalOut, error) {
	x, err := s.portalSectionByNum(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if x.Calendar == "" {
		return portalOut{}, calendar.ErrForbidden
	}
	if err := s.Calendar.DeleteSection(c.r.Context(), c.p.UserID, x.Calendar); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// ---------- meetings out ----------

const (
	portalCalLayout = "02.01.2006 15:04:05"
	portalCalDay    = "02.01.2006"
)

// portalCalStatus: the answer of go24 → MEETING_STATUS (Y — will come, N — declined, Q — not answered or
// «maybe», H — the organizer).
func portalCalStatus(status string) string {
	switch status {
	case "accepted":
		return "Y"
	case "declined":
		return "N"
	case "host":
		return "H"
	}
	return "Q"
}

var portalCalDays = []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}

var portalCalAccess = map[string]string{"busy": "busy", "tentative": "quest", "absent": "absent", "free": "free"}

// portalCalNums: the numbers of the meetings and of the people of the list.
func (s *Server) portalCalNums(ctx context.Context, list []calendar.Event) (events, users map[string]int64, err error) {
	var ids, people []string
	for _, e := range list {
		ids = append(ids, e.ID)
		people = append(people, e.OwnerID)
		for _, a := range e.Attendees {
			people = append(people, a.ID)
		}
	}
	if events, err = s.portalNums(ctx, "events", ids); err != nil {
		return nil, nil, err
	}
	users, err = s.portalNums(ctx, "users", people)
	return events, users, err
}

// portalCalOut gives the meetings (the deadlines, absences and birthdays a calendar shows are not
// its events).
func (s *Server) portalCalOut(c portalCall, list []calendar.Event) ([]map[string]any, error) {
	ctx := c.r.Context()
	list = slices.DeleteFunc(slices.Clone(list), func(e calendar.Event) bool { return e.Kind != "event" })
	nums, users, err := s.portalCalNums(ctx, list)
	if err != nil {
		return nil, err
	}
	num := func(m map[string]int64, id string) string { return strconv.FormatInt(m[id], 10) }
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		sec, calType, owner := portalSection{User: e.OwnerID}, "user", num(users, e.OwnerID)
		switch {
		case e.ProjectID != "":
			sec, calType = portalSection{Project: e.ProjectID}, "group"
			var n int64
			if err := s.DB.QueryRow(ctx, `SELECT num FROM projects WHERE id::text = $1`, e.ProjectID).Scan(&n); err != nil {
				return nil, err
			}
			owner = strconv.FormatInt(n, 10)
		case e.Company:
			sec, calType, owner = portalSection{Company: true}, "company_calendar", "0"
		case e.OwnerID != c.p.UserID && e.MyStatus != "": // the invited see it in their main calendar
			sec, owner = portalSection{User: c.p.UserID}, num(users, c.p.UserID)
		case e.CalendarID != "":
			sec = portalSection{Calendar: e.CalendarID}
		}
		secNum, err := s.portalSectionNum(ctx, sec)
		if err != nil {
			return nil, err
		}
		loc := e.Loc()
		from, to, length := e.Start.In(loc).Format(portalCalLayout), e.End.In(loc).Format(portalCalLayout), int64(e.End.Sub(e.Start).Seconds())
		if e.AllDay {
			from, to = e.Start.Format(portalCalDay), e.End.Format(portalCalDay)
			y1, m1, d1 := e.Start.Date()
			y2, m2, d2 := e.End.Date()
			days := int64(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)).Hours()/24) + 1
			length = days * 86400
		}
		tz := e.TZ
		if tz == "" {
			tz = time.Local.String()
		}
		importance := "normal"
		if e.Important {
			importance = "high"
		}
		var list []map[string]any
		codes := []string{}
		if len(e.Attendees) > 0 {
			list = append(list, map[string]any{"id": num(users, e.OwnerID), "entryId": num(nums, e.ID), "status": "H"})
			codes = append(codes, "U"+num(users, e.OwnerID))
			for _, a := range e.Attendees {
				if a.ID == e.OwnerID {
					continue
				}
				list = append(list, map[string]any{"id": num(users, a.ID), "entryId": num(nums, e.ID), "status": portalCalStatus(a.Status)})
				codes = append(codes, "U"+num(users, a.ID))
			}
		}
		status := portalCalStatus(e.MyStatus)
		if e.OwnerID == c.p.UserID {
			status = "H"
		}
		remind := []map[string]any{}
		for _, m := range e.Reminds {
			remind = append(remind, map[string]any{"type": "min", "count": m})
		}
		x := map[string]any{"ID": num(nums, e.ID), "PARENT_ID": num(nums, e.ID), "DELETED": "N", "CAL_TYPE": calType,
			"OWNER_ID": owner, "NAME": e.Title, "DESCRIPTION": e.Description, "LOCATION": e.Where,
			"DATE_FROM": from, "DATE_TO": to, "TZ_FROM": tz, "TZ_TO": tz, "DT_SKIP_TIME": portalYN(e.AllDay), "DT_LENGTH": length,
			"DATE_FROM_TS_UTC": strconv.FormatInt(e.Start.Unix(), 10), "DATE_TO_TS_UTC": strconv.FormatInt(e.End.Unix(), 10),
			"SECT_ID": strconv.FormatInt(secNum, 10), "SECTION_ID": strconv.FormatInt(secNum, 10),
			"ACCESSIBILITY": portalCalAccess[e.Accessibility], "IMPORTANCE": importance, "PRIVATE_EVENT": portalYN(e.Private),
			"COLOR": e.Color, "IS_MEETING": len(e.Attendees) > 0, "MEETING_STATUS": status,
			"MEETING_HOST": num(users, e.OwnerID), "CREATED_BY": num(users, e.OwnerID), "ATTENDEE_LIST": list,
			"ATTENDEES_CODES": codes, "REMIND": remind, "RRULE": "", "EXDATE": ""}
		if e.Hidden { // someone else's private meeting: its time only
			x["NAME"], x["DESCRIPTION"], x["LOCATION"] = "", "", ""
		}
		if e.Repeat != "" {
			rule := map[string]any{"FREQ": strings.ToUpper(e.Repeat), "INTERVAL": max(e.Every, 1)}
			if len(e.Days) > 0 {
				days := map[string]string{}
				for _, d := range e.Days {
					days[portalCalDays[d-1]] = portalCalDays[d-1]
				}
				rule["BYDAY"] = days
			}
			if e.Count > 0 {
				rule["COUNT"] = e.Count
			}
			if e.Until != nil {
				rule["UNTIL"] = e.Until.Format(portalCalDay)
			}
			x["RRULE"] = rule
			x["DATE_FROM_ORIGINAL"] = e.SeriesStart.In(loc).Format(portalCalLayout)
		}
		out = append(out, x)
	}
	return out, nil
}

// portalCalFind is the meeting of the number (the id parameter) as the caller sees it.
func (s *Server) portalCalFind(c portalCall, name string) (calendar.Event, error) {
	v := portalParam(c.in, name)
	if portalStr(v) == "" {
		return calendar.Event{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", name)
	}
	id, err := s.portalNum(c.r.Context(), "events", v)
	if err != nil {
		return calendar.Event{}, err
	}
	return s.Calendar.Get(c.r.Context(), c.p.UserID, id)
}

// calendar.event.get: the meetings of the calendar (type, ownerId) between from and to (by default a month back and
// three ahead), of the sections section[] only.
func (s *Server) portalCalEvents(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	cal, err := s.portalCalOf(c)
	if err != nil {
		return portalOut{}, err
	}
	now := time.Now()
	from, to := now.AddDate(0, -1, 0), now.AddDate(0, 3, 0)
	for name, t := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := portalStr(portalParam(c.in, name)); v != "" {
			x, err := portalCalTime(v, time.Local)
			if err != nil {
				return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", name)
			}
			*t = x
		}
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return portalOut{}, i18n.E("err.event_time")
	}
	var list []calendar.Event
	switch cal.Type {
	case "group":
		list, err = s.Calendar.ProjectRange(ctx, c.p.UserID, cal.Owner, from, to)
	case "company_calendar":
		if list, err = s.Calendar.Range(ctx, c.p.UserID, from, to); err == nil {
			list = slices.DeleteFunc(list, func(e calendar.Event) bool { return !e.Company })
		}
	default:
		if cal.Owner != c.p.UserID {
			list, err = s.Calendar.PersonRange(ctx, c.p.UserID, cal.Owner, from, to)
		} else if list, err = s.Calendar.Range(ctx, c.p.UserID, from, to); err == nil { // one's own calendars
			list = slices.DeleteFunc(list, func(e calendar.Event) bool {
				return e.Of != "" || e.ProjectID != "" || e.Company && e.OwnerID != c.p.UserID && e.MyStatus == ""
			})
		}
	}
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalCalOut(c, list)
	if err != nil {
		return portalOut{}, err
	}
	if sections := portalList(portalParam(c.in, "section")); len(sections) > 0 {
		out = slices.DeleteFunc(out, func(x map[string]any) bool { return !slices.Contains(sections, x["SECT_ID"].(string)) })
	}
	return portalOut{Result: out}, nil
}

// calendar.event.getbyid: the meeting of the number id.
func (s *Server) portalCalEvent(c portalCall) (portalOut, error) {
	e, err := s.portalCalFind(c, "id")
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalCalOut(c, []calendar.Event{e})
	if err != nil || len(out) == 0 {
		return portalOut{Result: nil}, err
	}
	return portalOut{Result: out[0]}, nil
}

// calendar.event.get.nearest: the caller's coming meetings for days (60) ahead, at most maxEventsCount.
func (s *Server) portalCalNearest(c portalCall) (portalOut, error) {
	days, limit := 60, 0
	if n, err := strconv.Atoi(portalStr(portalParam(c.in, "days"))); err == nil && n > 0 {
		days = min(n, 366)
	}
	if n, err := strconv.Atoi(portalStr(portalParam(c.in, "maxEventsCount"))); err == nil && n > 0 {
		limit = n
	}
	list, err := s.Calendar.Upcoming(c.r.Context(), c.p.UserID, time.Now(), days, -1)
	if err != nil {
		return portalOut{}, err
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	out, err := s.portalCalOut(c, list)
	return portalOut{Result: out}, err
}

// ---------- meetings in ----------

// portalCalTime reads a moment of the calendar in the zone: ISO 8601 or 28.10.2026 10:00[:00]; a day alone — its start.
func portalCalTime(v string, loc *time.Location) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04",
		portalCalLayout, "02.01.2006 15:04", "2006-01-02", portalCalDay} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, i18n.E("err.bad_date")
}

// portalCalInput turns the fields into a meeting of go24 (apiEventIn): only those given.
func (s *Server) portalCalInput(c portalCall, cal portalCal) (apiEventIn, error) {
	ctx := c.r.Context()
	var in apiEventIn
	get := func(name string) (any, bool) { return portalParamOK(c.in, name) }
	str := func(name string) *string {
		if v, ok := get(name); ok {
			x := portalStr(v)
			return &x
		}
		return nil
	}
	yes := func(name string) *bool {
		if v, ok := get(name); ok {
			x := strings.EqualFold(portalStr(v), "Y") || portalStr(v) == "true" || portalStr(v) == "1"
			return &x
		}
		return nil
	}
	bad := func(name string) error {
		return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", name)
	}
	in.Title, in.Description, in.Where, in.Color = str("name"), str("description"), str("location"), str("color")
	in.AllDay, in.Private = yes("skip_time"), yes("private_event")
	tz := str("timezone_from")
	if tz != nil && !calendar.ValidTZ(*tz) {
		return in, bad("timezone_from")
	}
	in.TimeZone = tz
	loc := time.Local
	if tz != nil && *tz != "" {
		loc, _ = time.LoadLocation(*tz)
	}
	for name, dst := range map[string]**time.Time{"from": &in.Start, "to": &in.End} {
		if v := str(name); v != nil && *v != "" {
			t, err := portalCalTime(*v, loc)
			if err != nil {
				return in, bad(name)
			}
			if name == "to" && in.AllDay != nil && *in.AllDay && t.Hour() == 0 && t.Minute() == 0 {
				t = t.Add(23*time.Hour + 59*time.Minute) // the last day of a whole-day meeting
			}
			*dst = &t
		}
	}
	if v := str("accessibility"); v != nil {
		a := map[string]string{"busy": "busy", "quest": "tentative", "absent": "absent", "free": "free"}[strings.ToLower(*v)]
		if a == "" {
			return in, bad("accessibility")
		}
		in.Accessibility = &a
	}
	if v := str("importance"); v != nil {
		x := strings.EqualFold(*v, "high")
		in.Important = &x
	}
	if v, ok := get("remind"); ok {
		mins := []int{}
		items, _ := v.([]any)
		for _, x := range items {
			it := portalMap(x)
			n, err := strconv.Atoi(portalStr(portalParam(it, "count")))
			if err != nil || n < 0 {
				return in, bad("remind")
			}
			switch strings.ToLower(portalStr(portalParam(it, "type"))) {
			case "hour":
				n *= 60
			case "day":
				n *= 1440
			}
			mins = append(mins, n)
		}
		in.Reminds = &mins
	}
	if v, ok := get("rrule"); ok {
		rule := portalMap(v)
		repeat, every, count, until, days := "", 1, 0, "", []int{}
		if rule != nil {
			repeat = strings.ToLower(portalStr(portalParam(rule, "FREQ")))
			if !slices.Contains([]string{"daily", "weekly", "monthly", "yearly"}, repeat) {
				return in, bad("rrule")
			}
			if n, err := strconv.Atoi(portalStr(portalParam(rule, "INTERVAL"))); err == nil && n > 0 {
				every = n
			}
			if n, err := strconv.Atoi(portalStr(portalParam(rule, "COUNT"))); err == nil && n > 0 {
				count = n
			}
			if u := portalStr(portalParam(rule, "UNTIL")); u != "" {
				t, err := portalCalTime(u, time.Local)
				if err != nil {
					return in, bad("rrule")
				}
				until = t.Format("2006-01-02")
			}
			byday := portalList(portalParam(rule, "BYDAY"))
			if len(byday) == 1 {
				byday = strings.Split(byday[0], ",")
			}
			if m := portalMap(portalParam(rule, "BYDAY")); m != nil {
				byday = byday[:0]
				for _, d := range m {
					byday = append(byday, portalStr(d))
				}
			}
			for _, d := range byday {
				if i := slices.Index(portalCalDays, strings.ToUpper(strings.TrimSpace(d))); i >= 0 {
					days = append(days, i+1)
				}
			}
			slices.Sort(days)
		}
		in.Repeat, in.Every, in.RepeatCount, in.Until, in.RepeatDays = &repeat, &every, &count, &until, &days
	}
	if v, ok := get("attendees"); ok {
		people := []string{}
		for _, x := range portalList(v) {
			id, err := s.portalNum(ctx, "users", strings.TrimPrefix(strings.ToUpper(x), "U"))
			if err != nil || id == "" {
				return in, bad("attendees")
			}
			people = append(people, id)
		}
		in.Attendees = &people
	}
	if v, ok := get("section"); ok && portalStr(v) != "" {
		x, err := s.portalSectionByNum(c, v)
		if err != nil {
			return in, err
		}
		switch {
		case x.Calendar != "" && cal.Type == "user":
			in.Calendar = &x.Calendar
		case x.User != "" && x.User == cal.Owner && cal.Type == "user", x.Project != "" && x.Project == cal.Owner, x.Company && cal.Type == "company_calendar":
			if cal.Type == "user" {
				main := ""
				in.Calendar = &main
			}
		default:
			return in, bad("section")
		}
	}
	return in, nil
}

// portalCalActor: the user whose calendar is changed — the caller, or a colleague who gave them «Управление» of theirs.
func (s *Server) portalCalActor(c portalCall, cal portalCal) (portalCall, error) {
	if cal.Type != "user" {
		return c, nil
	}
	actor, r, err := s.actorOf(c.r, c.p.UserID, cal.Owner)
	if err != nil {
		return c, err
	}
	c.r, c.p.UserID = r, actor
	return c, nil
}

// calendar.event.add: a meeting in the calendar (type, ownerId, section) → its number.
func (s *Server) portalCalAdd(c portalCall) (portalOut, error) {
	cal, err := s.portalCalOf(c)
	if err != nil {
		return portalOut{}, err
	}
	if c, err = s.portalCalActor(c, cal); err != nil {
		return portalOut{}, err
	}
	in, err := s.portalCalInput(c, cal)
	if err != nil {
		return portalOut{}, err
	}
	if in.Start == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "from")
	}
	if in.End == nil {
		end := *in.Start
		if in.AllDay == nil || !*in.AllDay {
			end = end.Add(time.Hour)
		}
		in.End = &end
	}
	switch cal.Type {
	case "group":
		in.Project = &cal.Key
	case "company_calendar":
		yes := true
		in.Company = &yes
	}
	a, err := s.calSave(c.r, c.p, "", nil, "", in)
	if err != nil {
		return portalOut{}, err
	}
	nums, err := s.portalNums(c.r.Context(), "events", []string{a.ID})
	return portalOut{Result: nums[a.ID]}, err
}

// calendar.event.update: the fields given of the meeting id; with recursion_mode this | next and current_date_from —
// of an occurrence of a repeated one (this one, this and the following) → the meeting's number.
func (s *Server) portalCalUpdate(c portalCall) (portalOut, error) {
	cal, err := s.portalCalOf(c)
	if err != nil {
		return portalOut{}, err
	}
	if c, err = s.portalCalActor(c, cal); err != nil {
		return portalOut{}, err
	}
	e, err := s.portalCalFind(c, "id")
	if err != nil {
		return portalOut{}, err
	}
	in, err := s.portalCalInput(c, cal)
	if err != nil {
		return portalOut{}, err
	}
	at, scope, err := s.portalCalOccurrence(c, e)
	if err != nil {
		return portalOut{}, err
	}
	a, err := s.calSave(c.r, c.p, e.ID, at, scope, in)
	if err != nil {
		return portalOut{}, err
	}
	nums, err := s.portalNums(c.r.Context(), "events", []string{a.ID})
	return portalOut{Result: nums[a.ID]}, err
}

// portalCalOccurrence: recursion_mode this | next | all and current_date_from — which occurrences of a repeated meeting.
func (s *Server) portalCalOccurrence(c portalCall, e calendar.Event) (*time.Time, string, error) {
	mode := strings.ToLower(portalStr(portalParam(c.in, "recursion_mode")))
	if e.Repeat == "" || mode == "" || mode == "all" {
		return nil, "", nil
	}
	scope := map[string]string{"this": calendar.ScopeOne, "next": calendar.ScopeFollowing}[mode]
	v := portalStr(portalParam(c.in, "current_date_from"))
	if scope == "" || v == "" {
		return nil, "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "recursion_mode")
	}
	day, err := portalCalTime(v, e.Loc())
	if err != nil {
		return nil, "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "current_date_from")
	}
	// the occurrence of that day: the series' time of day on it
	s0 := e.SeriesStart.In(e.Loc())
	at := time.Date(day.Year(), day.Month(), day.Day(), s0.Hour(), s0.Minute(), s0.Second(), 0, e.Loc())
	return &at, scope, nil
}

// calendar.event.delete: the organizer deletes the meeting; an invited person declines it, as deleting it from their
// calendar does.
func (s *Server) portalCalDelete(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	e, err := s.portalCalFind(c, "id")
	if err != nil {
		return portalOut{}, err
	}
	if e.OwnerID != c.p.UserID && e.MyStatus != "" {
		err = s.Calendar.Respond(ctx, c.p.UserID, e.ID, "declined")
	} else if err = s.Calendar.Delete(ctx, c.p.UserID, e.ID); err == nil {
		s.withdraw(ctx, feed.SourceMeeting, e.ID)
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// calendar.meeting.status.get: the caller's answer to the meeting eventId — Y, N, Q or H (its organizer).
func (s *Server) portalCalStatusGet(c portalCall) (portalOut, error) {
	e, err := s.portalCalFind(c, "eventId")
	if err != nil {
		return portalOut{}, err
	}
	if e.OwnerID == c.p.UserID {
		return portalOut{Result: "H"}, nil
	}
	return portalOut{Result: portalCalStatus(e.MyStatus)}, nil
}

// calendar.meeting.status.set: the invited person answers the meeting eventId — status Y (will come), N (declined) or
// Q («maybe»).
func (s *Server) portalCalStatusSet(c portalCall) (portalOut, error) {
	e, err := s.portalCalFind(c, "eventId")
	if err != nil {
		return portalOut{}, err
	}
	status := map[string]string{"Y": "accepted", "N": "declined", "Q": "tentative"}[strings.ToUpper(portalStr(portalParam(c.in, "status")))]
	if status == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "status")
	}
	if err := s.Calendar.Respond(c.r.Context(), c.p.UserID, e.ID, status); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// calendar.accessibility.get: the busy time of the employees users[] between from and to — {number: [{ID, NAME,
// DATE_FROM, DATE_TO, ACCESSIBILITY, IMPORTANCE, EVENT_TYPE}]}; the meetings one may not see have no name.
func (s *Server) portalCalAccessibility(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	var people []string
	for _, x := range portalList(portalParam(c.in, "users")) {
		id, err := s.portalNum(ctx, "users", x)
		if err != nil || id == "" {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "users")
		}
		people = append(people, id)
	}
	var from, to time.Time
	for name, t := range map[string]*time.Time{"from": &from, "to": &to} {
		x, err := portalCalTime(portalStr(portalParam(c.in, name)), time.Local)
		if err != nil {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", name)
		}
		*t = x
	}
	if to.Hour() == 0 && to.Minute() == 0 { // a day: up to its end takes it
		to = to.AddDate(0, 0, 1)
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return portalOut{}, i18n.E("err.event_time")
	}
	list, err := s.Calendar.Busy(ctx, c.p.UserID, people, from, to, "")
	if err != nil {
		return portalOut{}, err
	}
	var ids []string
	for _, p := range list {
		ids = append(ids, p.ID)
		for _, sl := range p.Slots {
			if sl.EventID != "" {
				ids = append(ids, sl.EventID)
			}
		}
	}
	users, err := s.portalNums(ctx, "users", ids)
	if err != nil {
		return portalOut{}, err
	}
	events, err := s.portalNums(ctx, "events", ids)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	for _, p := range list {
		slots := []map[string]any{}
		for _, sl := range p.Slots {
			access, kind := map[string]string{"tentative": "quest", "absent": "absent"}[sl.Kind], "user"
			if access == "" {
				access = "busy"
			}
			if sl.Kind == "absent" {
				kind = "absence"
			}
			x := map[string]any{"ID": "", "NAME": sl.Title, "DATE_FROM": sl.Start.In(time.Local).Format(portalCalLayout),
				"DATE_TO": sl.End.In(time.Local).Format(portalCalLayout), "DATE_FROM_TS_UTC": strconv.FormatInt(sl.Start.Unix(), 10),
				"DATE_TO_TS_UTC": strconv.FormatInt(sl.End.Unix(), 10), "DT_SKIP_TIME": "N", "ACCESSIBILITY": access,
				"IMPORTANCE": "normal", "EVENT_TYPE": kind}
			if n, ok := events[sl.EventID]; ok {
				x["ID"] = strconv.FormatInt(n, 10)
			}
			slots = append(slots, x)
		}
		out[strconv.FormatInt(users[p.ID], 10)] = slots
	}
	return portalOut{Result: out}, nil
}
