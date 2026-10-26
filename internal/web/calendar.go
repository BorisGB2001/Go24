package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Календарь» ----------

type calDay struct {
	Date                time.Time
	InMonth, Today, Off bool
	Items               []calendar.Event
}

// timed: a meeting placed on the hour grid of a day (week and day views).
type timed struct {
	E           calendar.Event
	Top, Height int // pixels from the first hour of the grid
}

type calCol struct {
	Date       time.Time
	Today, Off bool             // Off: a day off of one's settings
	AllDay     []calendar.Event // all-day meetings and task deadlines
	Timed      []timed
}

const calHourPx = 48

// calendarGrid is the week (?view=week) or the day (?view=day) on an hour grid (?date=2026-10-14). The grid is of the
// working hours of one's settings with two hours before and four after, widened to the meetings outside them.
func (s *Server) calendarGrid(w http.ResponseWriter, r *http.Request, sess auth.Session, view string) {
	cs, err := s.Calendar.Settings(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "calendar", err)
		return
	}
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if d, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), time.Local); err == nil {
		day = d
	}
	start, n := day, 1
	if view == "week" {
		start, n = cs.WeekOf(day), 7
	}
	end := start.AddDate(0, 0, n)
	items, sc, err := s.calRange(r, sess, start, end)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	onGrid := func(e calendar.Event) bool { return !e.AllDay && e.Kind != "task" && sameDay(e.Start, e.End) }
	hour0, hour1 := max(0, cs.WorkFrom-2), min(24, cs.WorkTo+4)
	for _, e := range items {
		if onGrid(e) && !e.Start.Before(start) && e.Start.Before(end) {
			hour0 = min(hour0, e.Start.Hour())
			hour1 = max(hour1, min(24, e.End.Hour()+min(1, e.End.Minute())))
		}
	}
	cols := make([]calCol, n)
	for i := range cols {
		d := start.AddDate(0, 0, i)
		cols[i] = calCol{Date: d, Today: sameDay(d, now), Off: cs.Off(d)}
		d1 := d.AddDate(0, 0, 1)
		for _, e := range items {
			if !e.Start.Before(d1) || e.End.Before(d) {
				continue
			}
			if !onGrid(e) {
				cols[i].AllDay = append(cols[i].AllDay, e)
				continue
			}
			mins := func(t time.Time) int {
				m := (t.Hour()-hour0)*60 + t.Minute()
				return max(0, min(m, (hour1-hour0)*60))
			}
			top, bottom := mins(e.Start), mins(e.End)
			cols[i].Timed = append(cols[i].Timed, timed{E: e, Top: top * calHourPx / 60, Height: max(18, (bottom-top)*calHourPx/60)})
		}
	}
	var hours []int
	for h := hour0; h < hour1; h++ {
		hours = append(hours, h)
	}
	step := n
	s.renderCal(w, r, sc, map[string]any{
		"View": view, "Cols": cols, "Hours": hours, "HourPx": calHourPx, "GridPx": (hour1 - hour0) * calHourPx, "Hour0": hour0, "Me": sess.UserID,
		"WorkTop": max(0, cs.WorkFrom-hour0) * calHourPx, "WorkBottom": min(hour1-hour0, cs.WorkTo-hour0) * calHourPx,
		"Month": start, "Day": day, "End": end.AddDate(0, 0, -1),
		"Prev": start.AddDate(0, 0, -step).Format("2006-01-02"), "Next": start.AddDate(0, 0, step).Format("2006-01-02"),
	})
}

// calListDays: the schedule (?view=list) shows this many days from the date.
const calListDays = 30

// calendarList is the schedule, as «Расписание»: the days from ?date= on that have something, each with
// its meetings, deadlines, absences and birthdays in order.
func (s *Server) calendarList(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if d, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), time.Local); err == nil {
		day = d
	}
	end := day.AddDate(0, 0, calListDays)
	items, sc, err := s.calRange(r, sess, day, end)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	var days []calDay
	for d := day; d.Before(end); d = d.AddDate(0, 0, 1) {
		cd := calDay{Date: d, InMonth: true, Today: sameDay(d, now)}
		for _, e := range items { // a meeting over several days is listed on each of them
			if e.Start.Before(d.AddDate(0, 0, 1)) && !e.End.Before(d) {
				cd.Items = append(cd.Items, e)
			}
		}
		if len(cd.Items) > 0 {
			days = append(days, cd)
		}
	}
	s.renderCal(w, r, sc, map[string]any{
		"View": "list", "Days": days, "Me": sess.UserID, "Day": day, "Month": day, "End": end.AddDate(0, 0, -1),
		"Prev": day.AddDate(0, 0, -calListDays).Format("2006-01-02"), "Next": end.Format("2006-01-02"),
	})
}

// calendarSearch is the search of one's calendar, as the filter: the meetings with the text (?q=) of a
// preset (?preset= invites | organizer | declined), by day.
func (s *Server) calendarSearch(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q, preset := r.URL.Query().Get("q"), r.URL.Query().Get("preset")
	if !slices.Contains(calendar.Presets, preset) {
		preset = ""
	}
	items, err := s.Calendar.Search(r.Context(), sess.UserID, q, preset)
	if err != nil {
		s.taskErr(w, r, err, "/calendar")
		return
	}
	now := time.Now()
	var days []calDay
	for _, e := range items {
		d := time.Date(e.Start.Year(), e.Start.Month(), e.Start.Day(), 0, 0, 0, 0, time.Local)
		if n := len(days); n == 0 || !days[n-1].Date.Equal(d) {
			days = append(days, calDay{Date: d, InMonth: true, Today: sameDay(d, now)})
		}
		days[len(days)-1].Items = append(days[len(days)-1].Items, e)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	s.renderCal(w, r, nil, map[string]any{
		"View": "search", "Days": days, "Found": len(items), "Me": sess.UserID, "Day": today, "Month": today,
		"Search": q, "Preset": preset, "Back": r.URL.RequestURI(),
	})
}

// calendarPage is the month: six weeks from the first day of the week (of one's settings) on or before the 1st
// (?month=2026-10).
func (s *Server) calendarPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if v := r.URL.Query().Get("view"); v == "week" || v == "day" {
		s.calendarGrid(w, r, sess, v)
		return
	} else if v == "list" {
		s.calendarList(w, r, sess)
		return
	}
	if q := r.URL.Query(); (q.Get("q") != "" || q.Get("preset") != "") && q.Get("project") == "" && q.Get("user") == "" {
		s.calendarSearch(w, r, sess)
		return
	}
	cs, err := s.Calendar.Settings(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "calendar", err)
		return
	}
	now := time.Now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if m, err := time.ParseInLocation("2006-01", r.URL.Query().Get("month"), time.Local); err == nil {
		month = m
	}
	start := cs.WeekOf(month)
	end := start.AddDate(0, 0, 42)
	items, sc, err := s.calRange(r, sess, start, end)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	days := make([]calDay, 42)
	for i := range days {
		d := start.AddDate(0, 0, i)
		days[i] = calDay{Date: d, InMonth: d.Month() == month.Month(), Today: sameDay(d, now), Off: cs.Off(d)}
	}
	for _, e := range items {
		for i := range days { // a meeting over several days is shown on each of them
			d0, d1 := days[i].Date, days[i].Date.AddDate(0, 0, 1)
			if e.Start.Before(d1) && !e.End.Before(d0) {
				days[i].Items = append(days[i].Items, e)
			}
		}
	}
	var weeks [][]calDay
	for i := 0; i < 42; i += 7 {
		weeks = append(weeks, days[i:i+7])
	}
	var head []int // the weekdays from the first one
	for i := range 7 {
		head = append(head, (cs.WeekStart+i-1)%7+1)
	}
	s.renderCal(w, r, sc, map[string]any{
		"Month": month, "WeekDays": head, "Prev": month.AddDate(0, -1, 0).Format("2006-01"), "Next": month.AddDate(0, 1, 0).Format("2006-01"),
		"Weeks": weeks, "Me": sess.UserID, "View": "month", "Day": time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local),
	})
}

// calRange is what the calendar shows: the person's own calendar with the calendars of colleagues added to its panel
// over it, or (?project=KEY) the calendar of one project — a collab's calendar, its meetings and deadlines only — or
// (?user=ID) the calendar of a colleague, as «Календари сотрудников».
func (s *Server) calRange(r *http.Request, sess auth.Session, from, to time.Time) ([]calendar.Event, *calScope, error) {
	if key := r.URL.Query().Get("project"); key != "" {
		p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, key)
		if err != nil {
			return nil, nil, err
		}
		list, err := s.Calendar.ProjectRange(r.Context(), sess.UserID, p.ID, from, to)
		return list, &calScope{Project: &p}, err
	}
	if id := r.URL.Query().Get("user"); id != "" && id != sess.UserID {
		name, err := s.Calendar.PersonName(r.Context(), sess.UserID, id)
		if err != nil {
			return nil, nil, err
		}
		list, err := s.Calendar.PersonRange(r.Context(), sess.UserID, id, from, to)
		return list, &calScope{Person: &calendar.Colleague{ID: id, Name: name}}, err
	}
	list, err := s.Calendar.Range(r.Context(), sess.UserID, from, to)
	if err != nil {
		return nil, nil, err
	}
	if !tasks.IsGuestCtx(r.Context()) {
		if list, err = s.Calendar.WithPeople(r.Context(), sess.UserID, list, from, to); err != nil {
			return nil, nil, err
		}
	}
	cs, err := s.Calendar.Settings(r.Context(), sess.UserID) // declined meetings and task deadlines — if one shows them
	if err != nil {
		return nil, nil, err
	}
	return slices.DeleteFunc(list, func(e calendar.Event) bool {
		return !cs.ShowDeclined && e.MyStatus == "declined" || !cs.ShowTasks && e.Kind == "task"
	}), nil, nil
}

// calScope: the calendar shown is not one's own but that of a project or of a colleague.
type calScope struct {
	Project *tasks.Project
	Person  *calendar.Colleague
}

// renderCal draws the calendar page; a project's or a colleague's calendar keeps ?project= or ?user= in its links
// (Q), a project's is under its section.
func (s *Server) renderCal(w http.ResponseWriter, r *http.Request, sc *calScope, data map[string]any) {
	pg := page{TitleKey: "calendar.title", Nav: "calendar", Data: data}
	data["Q"] = ""
	if sc == nil { // the panel «Календари»: one's own calendars, the colour of the main one, colleagues' calendars
		me, _ := data["Me"].(string)
		var err error
		if data["Sections"], err = s.Calendar.Sections(r.Context(), me); err == nil {
			data["MainColor"], err = s.Calendar.MainColor(r.Context(), me)
		}
		if err == nil && !tasks.IsGuestCtx(r.Context()) {
			if data["People"], err = s.Calendar.People(r.Context(), me); err == nil {
				data["Share"], err = s.Calendar.Share(r.Context(), me)
			}
			if err == nil {
				data["Users"], err = s.Tasks.ListUsers(r.Context())
			}
			data["Colleagues"] = true
		}
		if err != nil {
			s.fail(w, "calendar", err)
			return
		}
		data["Colors"] = calendar.Colors
		data["Presets"] = calendar.Presets
		if data["Invites"], err = s.Calendar.Invitations(r.Context(), me); err != nil {
			s.fail(w, "calendar", err)
			return
		}
	} else if p := sc.Project; p != nil {
		data["Project"], data["Q"] = *p, "&project="+url.QueryEscape(p.Key)
		pg.TitleKey, pg.Title = "", p.Name
		if p.Collab() {
			pg.Nav = "collabs"
		}
	} else if sc.Person != nil {
		data["Person"], data["Q"] = *sc.Person, "&user="+url.QueryEscape(sc.Person.ID)
		pg.TitleKey, pg.Title = "", sc.Person.Name
	}
	s.render(w, r, "calendar", pg)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// eventPage: a new meeting (?date=2026-10-15) or an existing one — the form for its organizer, the answer for the invited.
// remindPreset is a choice of the meeting form's reminders: minutes before the start and the locale key of its text.
type remindPreset struct {
	M   int
	Key string
}

func (s *Server) eventPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var e calendar.Event
	copied := "" // the meeting copied (?copy=)
	at := ""     // an occurrence of a repeated meeting (?at=its start): changed or deleted alone, with the following or all
	if id := r.PathValue("id"); id != "" {
		var err error
		if e, err = s.Calendar.Get(r.Context(), sess.UserID, id); err != nil {
			s.taskErr(w, r, err, "/calendar")
			return
		}
		if t, perr := time.ParseInLocation("2006-01-02T15:04", r.URL.Query().Get("at"), time.Local); perr == nil && e.Repeat != "" {
			if o := e.Occurrence(t); o != nil {
				e, at = *o, t.Format("2006-01-02T15:04")
			}
		}
	} else {
		day := time.Now()
		if d, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), time.Local); err == nil {
			day = d
		}
		h := time.Now().Hour() + 1
		if !sameDay(day, time.Now()) || h > 20 {
			h = 10
		}
		e.Start = time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, time.Local)
		e.End = e.Start.Add(time.Hour)
		if st, err := time.ParseInLocation("2006-01-02T15:04", r.URL.Query().Get("start"), time.Local); err == nil { // a span chosen on the grid
			e.Start, e.End = st, st.Add(time.Hour)
			if en, err := time.ParseInLocation("2006-01-02T15:04", r.URL.Query().Get("end"), time.Local); err == nil && en.After(st) {
				e.End = en
			}
		}
		e.OwnerID = sess.UserID
		cs, err := s.Calendar.Settings(r.Context(), sess.UserID) // the reminder of one's settings, by default 15 minutes before
		if err != nil {
			s.taskErr(w, r, err, "/calendar")
			return
		}
		if e.Reminds = []int{}; cs.Remind >= 0 {
			e.Reminds = []int{cs.Remind}
		}
		if title, text, ok := s.feedSource(r, sess); ok { // «Событие» of the feed: of a post or the feed's form
			e.Title, e.Description = title, text
		}
		if id := r.URL.Query().Get("user"); id != "" && id != sess.UserID { // from a colleague's calendar: they are invited
			if name, err := s.Calendar.PersonName(r.Context(), sess.UserID, id); err == nil {
				e.Attendees = []calendar.Attendee{{ID: id, Name: name}}
			}
		}
		if key := r.URL.Query().Get("project"); key != "" { // from a project's (a collab's) calendar: a meeting there
			if p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, key); err == nil {
				e.ProjectID = p.ID
				s.scrumPrefill(r, sess, p, r.URL.Query().Get("scrum"), &e) // «Мероприятия» of a scrum team
			}
		}
		if id := r.URL.Query().Get("copy"); id != "" { // «Копировать»: a new meeting of the viewer, as that one
			src, err := s.Calendar.Get(r.Context(), sess.UserID, id)
			if err == nil && src.Hidden {
				err = calendar.ErrNotFound
			}
			if err != nil {
				s.taskErr(w, r, err, "/calendar")
				return
			}
			e = copyOf(src, sess.UserID)
			copied = src.ID
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/calendar")
		return
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/calendar")
		return
	}
	var invited []tasks.User
	for _, a := range e.Attendees {
		invited = append(invited, tasks.User{ID: a.ID, Name: a.Name})
	}
	title := "calendar.new"
	if e.ID != "" {
		title = "calendar.event"
	}
	minutes := slices.Clone(calendar.RemindPresets)
	for _, m := range e.Reminds { // a reminder set elsewhere (REST) stays in the list
		if !slices.Contains(minutes, m) {
			minutes = append(minutes, m)
		}
	}
	slices.Sort(minutes)
	var presets []remindPreset
	for _, m := range minutes {
		p := remindPreset{M: m, Key: "calendar.remind.n"}
		if slices.Contains(calendar.RemindPresets, m) {
			p.Key = "calendar.remind." + strconv.Itoa(m)
		}
		presets = append(presets, p)
	}
	data := map[string]any{
		"E": e, "Presets": presets, "Users": users, "Invited": invited, "Projects": projects, "Me": sess.UserID,
		"CanEdit": e.ID == "" || e.OwnerID == sess.UserID || tasks.IsAdmin(r.Context()), "At": at,
		"Colors": calendar.Colors,
	}
	if data["Sections"], err = s.Calendar.Sections(r.Context(), sess.UserID); err != nil { // one's own calendars
		s.taskErr(w, r, err, "/calendar")
		return
	}
	if !tasks.IsGuestCtx(r.Context()) { // the rooms of the company
		if data["Rooms"], err = s.Calendar.Rooms(r.Context()); err != nil {
			s.taskErr(w, r, err, "/calendar")
			return
		}
	}
	s.eventMailData(r, sess, data)                // the people from outside, invited by mail; a meeting made of a letter
	if copied != "" && data["MailBoxes"] != nil { // the people from outside of the copied meeting are invited too
		if ext, err := s.Calendar.Externals(r.Context(), copied); err == nil {
			var addrs []string
			for _, x := range ext {
				addrs = append(addrs, x.Email)
			}
			data["ExternalText"] = strings.Join(addrs, ", ")
		}
	}
	if e.ID != "" && !e.Hidden { // «Файлы» and «Комментарии»
		if data["Disc"], err = s.Calendar.Discussion(r.Context(), e.ID); err != nil {
			s.taskErr(w, r, err, "/calendar")
			return
		}
		data["Admin"] = tasks.IsAdmin(r.Context())
	}
	if room, ev := r.URL.Query().Get("im_room"), r.URL.Query().Get("im_event"); e.ID == "" && room != "" && ev != "" && s.Matrix != nil {
		s.eventFromMessage(r, sess, room, ev, data) // «Создать встречу» of a chat message
	}
	s.render(w, r, "event", page{TitleKey: title, Nav: "calendar", Data: data})
}

// copyOf is the new meeting «Копировать» makes of src for the viewer: its organizer, with its people (the organizer
// of src among them), at its time; the room, the calendar of someone else and the answers do not go over.
func copyOf(src calendar.Event, viewer string) calendar.Event {
	e := calendar.Event{OwnerID: viewer, ProjectID: src.ProjectID, Company: src.Company, Title: src.Title,
		Description: src.Description, Where: src.Where, Start: src.Start, End: src.End, AllDay: src.AllDay,
		Repeat: src.Repeat, Every: src.Every, Until: src.Until, Reminds: slices.Clone(src.Reminds),
		Accessibility: src.Accessibility, Private: src.Private, Video: src.Video,
		Color: src.Color, Important: src.Important}
	if src.OwnerID == viewer {
		e.CalendarID = src.CalendarID
	} else {
		e.Attendees = append(e.Attendees, calendar.Attendee{ID: src.OwnerID, Name: src.OwnerName})
	}
	for _, a := range src.Attendees {
		if a.ID != viewer && a.ID != src.OwnerID {
			e.Attendees = append(e.Attendees, calendar.Attendee{ID: a.ID, Name: a.Name})
		}
	}
	return e
}

func (s *Server) eventSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/calendar")
		return
	}
	allDay := r.FormValue("all_day") == "on"
	parse := func(name string) (time.Time, error) {
		v := strings.TrimSpace(r.FormValue(name))
		if t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local); err == nil {
			return t, nil
		}
		if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			return t, nil
		}
		return time.Time{}, i18n.E("err.event_time")
	}
	start, err := parse("start")
	var end time.Time
	if err == nil {
		end, err = parse("end")
	}
	var id string
	msg := s.tr(r, "common.saved")
	if err == nil {
		e := calendar.Event{ID: r.PathValue("id"), Title: r.FormValue("title"), Description: r.FormValue("description"),
			Where: r.FormValue("where"), Start: start, End: end, AllDay: allDay, Repeat: r.FormValue("repeat"),
			Accessibility: r.FormValue("accessibility"), Private: r.FormValue("private") == "on", RoomID: r.FormValue("room"),
			Video: r.FormValue("video") == "on", Color: r.FormValue("color"), Important: r.FormValue("important") == "on"}
		e.Every, _ = strconv.Atoi(r.FormValue("every"))
		if u, perr := time.ParseInLocation("2006-01-02", r.FormValue("until"), time.Local); perr == nil {
			e.Until = &u
		}
		for _, v := range r.Form["remind"] {
			if m, perr := strconv.Atoi(v); perr == nil {
				e.Reminds = append(e.Reminds, m)
			}
		}
		switch cal := r.FormValue("cal"); cal {
		case "", "personal":
		case "company":
			e.Company = true
		default:
			if id, ok := strings.CutPrefix(cal, "c:"); ok { // one of one's own calendars
				e.CalendarID = id
				break
			}
			e.ProjectID = cal
		}
		var before *calendar.Event // as it was: a changed meeting goes by mail again
		if e.ID != "" {
			if b, gerr := s.Calendar.Get(r.Context(), sess.UserID, e.ID); gerr == nil {
				before = &b
			}
		}
		if at, perr := time.ParseInLocation("2006-01-02T15:04", r.FormValue("at"), time.Local); perr == nil && e.ID != "" {
			// an occurrence of a repeated meeting; one taken out of the series is not sent by mail (it has no
			// RECURRENCE-ID yet), the whole series is
			scope := r.FormValue("scope")
			if id, err = s.Calendar.SaveOccurrence(r.Context(), sess.UserID, e.ID, at, scope, e, r.Form["invite"]); err == nil && scope == calendar.ScopeAll {
				if m := s.eventMailSend(r, sess, id, before); m != "" {
					msg = m
				}
			}
		} else if id, err = s.Calendar.Save(r.Context(), sess.UserID, e, r.Form["invite"]); err == nil {
			if m := s.eventMailSend(r, sess, id, before); m != "" {
				msg = m
			}
			if room, ev := r.FormValue("im_room"), r.FormValue("im_event"); e.ID == "" && room != "" && ev != "" && s.Matrix != nil {
				s.eventMessageDone(r, sess, room, ev, id) // of a chat message: the answer there
			}
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/calendar")
		return
	}
	back := "/calendar?month=" + start.Format("2006-01")
	if e, err := s.Calendar.Get(r.Context(), sess.UserID, id); err == nil && e.ProjectKey != "" { // back to the project's calendar
		back += "&project=" + url.QueryEscape(e.ProjectKey)
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(msg)+"#"+id, http.StatusSeeOther)
}

func (s *Server) eventDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if at, perr := time.ParseInLocation("2006-01-02T15:04", r.FormValue("at"), time.Local); perr == nil && r.FormValue("scope") != calendar.ScopeAll {
		if err := s.Calendar.DeleteOccurrence(r.Context(), sess.UserID, id, at, r.FormValue("scope")); err != nil {
			s.taskErr(w, r, err, "/calendar")
			return
		}
		http.Redirect(w, r, "/calendar?month="+at.Format("2006-01"), http.StatusSeeOther)
		return
	}
	s.eventMailCancel(r, sess, id) // the people from outside are told by mail
	if err := s.Calendar.Delete(r.Context(), sess.UserID, id); err != nil {
		s.taskErr(w, r, err, "/calendar")
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

func (s *Server) eventRespond(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Calendar.Respond(r.Context(), sess.UserID, id, r.FormValue("status")); err != nil {
		s.taskErr(w, r, err, "/calendar/"+id)
		return
	}
	back := "/calendar/" + id
	if r.FormValue("back") != "" { // answered from the search («Приглашения»): back to it
		back = calBack(r)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// eventBusy is the planner of the meeting form (?user=…&start=…&end=…&except=ID): the busy time of the people on the
// day of the meeting, who of them is busy at its time, and the nearest time all are free in working hours (two weeks).
func (s *Server) eventBusy(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	start, err := time.ParseInLocation("2006-01-02T15:04", q.Get("start"), time.Local)
	end, err2 := time.ParseInLocation("2006-01-02T15:04", q.Get("end"), time.Local)
	if err != nil || err2 != nil || end.Before(start) {
		s.jsonErr(w, r, i18n.E("err.event_time"))
		return
	}
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	from := day
	if now := time.Now(); now.After(from) && sameDay(now, day) {
		from = now
	}
	people, err := s.Calendar.Busy(r.Context(), sess.UserID, q["user"], day, day.AddDate(0, 0, 14), q.Get("except"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	cs, err := s.Calendar.Settings(r.Context(), sess.UserID) // the planner's hours are those of the day view
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	type slot struct {
		Start, End  string
		Kind, Title string
		ID          string `json:",omitempty"`
	}
	type person struct {
		ID, Name string
		Busy     bool // busy at the meeting's time
		Slots    []slot
	}
	out := struct {
		Day        string
		Hour0      int
		Hour1      int
		People     []person
		Free       string // "" — no free time in two weeks
		FreeIsSame bool   // the meeting's time is free for everyone
	}{Day: day.Format("2006-01-02"), Hour0: max(0, cs.WorkFrom-2), Hour1: min(24, cs.WorkTo+4), People: []person{}}
	next := day.AddDate(0, 0, 1)
	for _, p := range people {
		o := person{ID: p.ID, Name: p.Name, Slots: []slot{}}
		for _, sl := range p.Slots {
			if sl.Start.Before(end) && sl.End.After(start) && end.After(start) {
				o.Busy = true
			}
			if sl.Start.Before(next) && sl.End.After(day) {
				o.Slots = append(o.Slots, slot{sl.Start.Format("2006-01-02T15:04"), sl.End.Format("2006-01-02T15:04"), sl.Kind, sl.Title, sl.EventID})
			}
		}
		out.People = append(out.People, o)
	}
	if room := q.Get("room"); room != "" && !tasks.IsGuestCtx(r.Context()) { // the meeting room as one more row
		rm, slots, err := s.Calendar.RoomBusy(r.Context(), sess.UserID, room, day, day.AddDate(0, 0, 14))
		if err == nil {
			p := calendar.Person{ID: "room:" + rm.ID, Name: "🚪 " + rm.Name}
			for _, sl := range slots {
				if sl.EventID != q.Get("except") || sl.EventID == "" {
					p.Slots = append(p.Slots, sl)
				}
			}
			people = append(people, p)
			o := person{ID: p.ID, Name: p.Name, Slots: []slot{}}
			for _, sl := range p.Slots {
				o.Busy = o.Busy || sl.Start.Before(end) && sl.End.After(start) && end.After(start)
				if sl.Start.Before(next) && sl.End.After(day) {
					o.Slots = append(o.Slots, slot{sl.Start.Format("2006-01-02T15:04"), sl.End.Format("2006-01-02T15:04"), sl.Kind, sl.Title, sl.EventID})
				}
			}
			out.People = append(out.People, o)
		}
	}
	if !start.Before(from) {
		from = start
	}
	if t, ok := calendar.FirstFree(people, from, end.Sub(start), sched, 14); ok {
		out.Free, out.FreeIsSame = t.Format("2006-01-02T15:04"), t.Equal(start)
	}
	writeJSON(w, http.StatusOK, out)
}

// eventMove: a meeting dragged on the calendar (start, end; at — an occurrence of a repeated meeting, which is then
// taken out of the series alone). Everything else stays; the invited are asked again, people from outside get an
// update by mail.
func (s *Server) eventMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	start, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("start"), time.Local)
	end, err2 := time.ParseInLocation("2006-01-02T15:04", r.FormValue("end"), time.Local)
	if err != nil || err2 != nil {
		s.jsonErr(w, r, i18n.E("err.event_time"))
		return
	}
	e, err := s.Calendar.Get(r.Context(), sess.UserID, id)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	before := e
	var invite []string
	for _, a := range e.Attendees {
		invite = append(invite, a.ID)
	}
	if at, perr := time.ParseInLocation("2006-01-02T15:04", r.FormValue("at"), time.Local); perr == nil && e.Repeat != "" {
		e.Start, e.End = start, end
		if id, err = s.Calendar.SaveOccurrence(r.Context(), sess.UserID, id, at, calendar.ScopeOne, e, invite); err != nil {
			s.jsonErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id})
		return
	}
	e.Start, e.End = start, end
	if _, err := s.Calendar.Save(r.Context(), sess.UserID, e, invite); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	msg := ""
	if xs, err := s.Calendar.Externals(r.Context(), id); err == nil && len(xs) > 0 { // the same people from outside
		var addrs []string
		for _, x := range xs {
			addrs = append(addrs, x.Email)
		}
		r.Form.Set("external", strings.Join(addrs, ", "))
		msg = s.eventMailSend(r, sess, id, &before)
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "msg": msg})
}

// roomsPage: the meeting rooms of the company (Settings, administrators), each with its bookings of the week.
func (s *Server) roomsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	rooms, err := s.Calendar.Rooms(r.Context())
	if err != nil {
		s.fail(w, "rooms", err)
		return
	}
	type booked struct {
		calendar.Room
		Slots []calendar.Slot
	}
	today := time.Now().Truncate(24 * time.Hour)
	var list []booked
	for _, rm := range rooms {
		_, slots, err := s.Calendar.RoomBusy(r.Context(), sess.UserID, rm.ID, time.Now(), today.AddDate(0, 0, 8))
		if err != nil {
			s.fail(w, "rooms", err)
			return
		}
		list = append(list, booked{rm, slots})
	}
	s.render(w, r, "rooms", page{TitleKey: "rooms.title", Nav: "rooms", Data: map[string]any{"Rooms": list}})
}

// roomSave adds a room (no id) or changes one; roomDelete removes it.
func (s *Server) roomSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	seats, _ := strconv.Atoi(r.FormValue("capacity"))
	if _, err := s.Calendar.SaveRoom(r.Context(), calendar.Room{ID: r.PathValue("id"), Name: r.FormValue("name"),
		Location: r.FormValue("location"), Capacity: seats}); err != nil {
		s.taskErr(w, r, err, "/settings/rooms")
		return
	}
	http.Redirect(w, r, "/settings/rooms?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

func (s *Server) roomDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Calendar.DeleteRoom(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/settings/rooms")
		return
	}
	http.Redirect(w, r, "/settings/rooms", http.StatusSeeOther)
}

// sectionSave adds one of one's own calendars (no id), changes one, or (id main) sets the colour of the main calendar;
// back to the calendar as it was.
func (s *Server) sectionSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := calBack(r)
	var err error
	if id := r.FormValue("id"); id == "main" {
		err = s.Calendar.SetMainColor(r.Context(), sess.UserID, r.FormValue("color"))
	} else {
		_, err = s.Calendar.SaveSection(r.Context(), sess.UserID, calendar.Section{ID: id, Name: r.FormValue("name"), Color: r.FormValue("color")})
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// sectionDelete removes one of one's own calendars with its meetings.
func (s *Server) sectionDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Calendar.DeleteSection(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/calendar")
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// calBack is where a form of the calendar's panel returns: the calendar page it was sent from.
func calBack(r *http.Request) string {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/calendar") || strings.HasPrefix(back, "//") {
		back = "/calendar"
	}
	return back
}

// personSave adds a colleague's calendar to one's panel or changes its colour (user, color).
func (s *Server) personSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Calendar.AddPerson(r.Context(), sess.UserID, r.FormValue("user"), r.FormValue("color")); err != nil {
		s.taskErr(w, r, err, calBack(r))
		return
	}
	http.Redirect(w, r, calBack(r), http.StatusSeeOther)
}

// personDelete takes a colleague's calendar off one's panel.
func (s *Server) personDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Calendar.RemovePerson(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, calBack(r))
		return
	}
	http.Redirect(w, r, calBack(r), http.StatusSeeOther)
}

// shareSave: what colleagues see in one's calendar — the busy time or the meetings.
func (s *Server) shareSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Calendar.SetShare(r.Context(), sess.UserID, r.FormValue("share")); err != nil {
		s.taskErr(w, r, err, calBack(r))
		return
	}
	http.Redirect(w, r, calBack(r), http.StatusSeeOther)
}

// calendarSync: the subscription to one's calendar from Outlook, Google Calendar or a phone (the link to the feed).
func (s *Server) calendarSync(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	f, err := s.Calendar.FeedOf(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "calendar sync", err)
		return
	}
	link := ""
	if f.Token != "" {
		link = strings.TrimRight(s.BaseURL, "/") + "/calendar/feed/" + f.Token + ".ics"
	}
	sections, err := s.Calendar.Sections(r.Context(), sess.UserID) // the calendar a file is imported into
	if err != nil {
		s.fail(w, "calendar sync", err)
		return
	}
	s.render(w, r, "calsync", page{TitleKey: "calsync.title", Nav: "calendar", Data: map[string]any{
		"Feed": f, "Link": link, "Sections": sections, "CalDAV": s.caldavURL(r), "Webcal": strings.Replace(strings.Replace(link, "https://", "webcal://", 1), "http://", "webcal://", 1)}})
}

// calendarSyncSave issues the link (again, with renew — the old one stops working) and keeps the choice of tasks.
func (s *Server) calendarSyncSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, err := s.Calendar.SetFeed(r.Context(), sess.UserID, r.FormValue("renew") == "1", r.FormValue("tasks") == "on"); err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	http.Redirect(w, r, "/calendar/sync?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// calendarFeed is the iCalendar feed of a subscription (no sign-in: the secret link is the key).
func (s *Server) calendarFeed(w http.ResponseWriter, r *http.Request) {
	ics, err := s.Calendar.FeedICS(r.Context(), strings.TrimSuffix(r.PathValue("file"), ".ics"), time.Now())
	if err != nil {
		if !errors.Is(err, calendar.ErrNotFound) {
			s.Log.Error("calendar feed", "err", err)
		}
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write([]byte(ics))
}

// calendarSettings is «Настройки календаря»: the first day of the week, the working hours and days, what is shown,
// the reminder of a new meeting.
func (s *Server) calendarSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cs, err := s.Calendar.Settings(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "calendar settings", err)
		return
	}
	var hours []int
	for h := range 25 {
		hours = append(hours, h)
	}
	s.render(w, r, "calsettings", page{TitleKey: "calset.title", Nav: "calendar", Data: map[string]any{
		"S": cs, "Days": []int{1, 2, 3, 4, 5, 6, 7}, "Hours": hours, "Reminds": calendar.RemindPresets}})
}

// calendarSettingsSave keeps the settings of the form.
func (s *Server) calendarSettingsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/calendar/settings")
		return
	}
	num := func(k string) int {
		n, err := strconv.Atoi(r.FormValue(k))
		if err != nil {
			return -2 // not a choice of the form: refused by SaveSettings
		}
		return n
	}
	cs := calendar.Settings{WeekStart: num("week_start"), WorkFrom: num("work_from"), WorkTo: num("work_to"), DaysOff: []int{},
		ShowDeclined: r.FormValue("show_declined") == "on", ShowTasks: r.FormValue("show_tasks") == "on", Remind: num("remind")}
	for _, v := range r.Form["days_off"] {
		n, err := strconv.Atoi(v)
		if err != nil {
			n = 0
		}
		cs.DaysOff = append(cs.DaysOff, n)
	}
	if err := s.Calendar.SaveSettings(r.Context(), sess.UserID, cs); err != nil {
		s.taskErr(w, r, err, "/calendar/settings")
		return
	}
	http.Redirect(w, r, "/calendar/settings?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}
