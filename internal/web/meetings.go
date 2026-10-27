package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Собрания и планёрки» («Время и отчёты»): the list of meetings, a meeting with its agenda, the minutes.

// meetingsPage: GET /worktime/meetings — the meetings the viewer sees (?role=owner|member, ?status=, ?q=).
func (s *Server) meetingsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	f := company.MeetingFilter{Role: q.Get("role"), Status: q.Get("status"), Query: q.Get("q")}
	list, err := s.Company.Meetings(r.Context(), sess.UserID, sess.Admin, f)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	s.render(w, r, "meetings", page{TitleKey: "meeting.title", Nav: "meetings", Data: map[string]any{
		"List": list, "Filter": f, "Statuses": company.MeetingStatuses, "Me": sess.UserID}})
}

// meetingFormPage: GET /worktime/meetings/new and /worktime/meetings/{id}/edit — the form of a meeting.
func (s *Server) meetingFormPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, time.Local)
	m := company.Meeting{Start: start, Minutes: 60, Owner: company.Person{ID: sess.UserID}}
	back := "/worktime/meetings"
	if v := r.PathValue("id"); v != "" {
		id, _ := strconv.ParseInt(v, 10, 64)
		var err error
		back = "/worktime/meetings/" + v
		if m, err = s.Company.Meeting(r.Context(), sess.UserID, sess.Admin, id); err == nil && !m.CanEdit(sess.UserID, sess.Admin) {
			err = company.ErrForbidden
		}
		if err != nil {
			s.taskErr(w, r, err, "/worktime/meetings")
			return
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.render(w, r, "meetingform", page{TitleKey: "meeting.title", Nav: "meetings", Data: map[string]any{
		"M": m, "Users": users, "Back": back, "Members": m.MemberIDs(), "TitleMax": company.MeetingTitleMax, "Durations": meetingDurations(m.Minutes)}})
}

// meetingDurations are the lengths offered in the form, in minutes (with the meeting's own, if another).
func meetingDurations(own int) []int {
	list := []int{15, 30, 45, 60, 90, 120, 180, 240, 480}
	if own > 0 && !slices.Contains(list, own) {
		list = append(list, own)
		slices.Sort(list)
	}
	return list
}

// meetingSave: POST /worktime/meetings (a new one) and /worktime/meetings/{id} — title, description, place, start
// (date and time), minutes, owner, keeper, members.
func (s *Server) meetingSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/worktime/meetings"
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	m := company.Meeting{Title: r.FormValue("title"), Description: r.FormValue("description"), Place: r.FormValue("place"),
		Owner: company.Person{ID: r.FormValue("owner")}, Keeper: company.Person{ID: r.FormValue("keeper")}}
	var was company.Meeting
	if v := r.PathValue("id"); v != "" {
		m.ID, _ = strconv.ParseInt(v, 10, 64)
		back += "/" + v
		var err error
		if was, err = s.Company.Meeting(r.Context(), sess.UserID, sess.Admin, m.ID); err != nil {
			s.taskErr(w, r, err, "/worktime/meetings")
			return
		}
	}
	start, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("start"), time.Local)
	if err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	m.Start = start
	m.Minutes, _ = strconv.Atoi(r.FormValue("minutes"))
	id, err := s.Company.SaveMeeting(r.Context(), sess.UserID, sess.Admin, m, r.Form["members"])
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("meeting saved", "user_id", sess.UserID, "id", id)
	s.syncMeetingEvent(r.Context(), id, was)
	http.Redirect(w, r, "/worktime/meetings/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// syncMeetingEvent keeps the meeting in the organizer's calendar with its people invited (was — the meeting before
// the change: another organizer takes the meeting into their own calendar). Failures are only logged: the meeting
// itself is saved.
func (s *Server) syncMeetingEvent(ctx context.Context, id int64, was company.Meeting) {
	if s.Calendar == nil {
		return
	}
	m, err := s.Company.Meeting(ctx, "", true, id)
	if err != nil {
		s.Log.Error("meeting event", "id", id, "err", err)
		return
	}
	actx := tasks.WithAdmin(ctx)
	if m.EventID != "" && was.ID != 0 && was.Owner.ID != m.Owner.ID {
		if err := s.Calendar.Delete(actx, was.Owner.ID, m.EventID); err != nil && !errors.Is(err, calendar.ErrNotFound) {
			s.Log.Error("meeting event", "id", id, "err", err)
		}
		m.EventID = ""
	}
	var invite []string
	for _, p := range m.People() {
		if p != m.Owner.ID {
			invite = append(invite, p)
		}
	}
	e := calendar.Event{ID: m.EventID, Title: m.Title, Description: m.Description, Where: m.Place, Start: m.Start, End: m.End(),
		Reminds: []int{15}}
	if e.ID != "" { // keep what the organizer set in the calendar
		if old, err := s.Calendar.Get(actx, m.Owner.ID, e.ID); err == nil {
			e.Reminds, e.Color, e.Important, e.Accessibility, e.Private, e.RoomID, e.Video, e.CalendarID =
				old.Reminds, old.Color, old.Important, old.Accessibility, old.Private, old.RoomID, old.Video, old.CalendarID
		} else {
			e.ID = ""
		}
	}
	eid, err := s.Calendar.Save(actx, m.Owner.ID, e, invite)
	if err != nil {
		s.Log.Error("meeting event", "id", id, "err", err)
		return
	}
	if eid != m.EventID {
		if err := s.Company.SetMeetingEvent(ctx, id, eid); err != nil {
			s.Log.Error("meeting event", "id", id, "err", err)
		}
	}
}

// dropMeetingEvent deletes the meeting of the calendar of a deleted meeting.
func (s *Server) dropMeetingEvent(ctx context.Context, m company.Meeting) {
	if m.EventID == "" || s.Calendar == nil {
		return
	}
	if err := s.Calendar.Delete(tasks.WithAdmin(ctx), m.Owner.ID, m.EventID); err != nil && !errors.Is(err, calendar.ErrNotFound) {
		s.Log.Error("meeting event", "id", m.ID, "err", err)
	}
}

// meetingID is the meeting of the path.
func meetingID(r *http.Request) (int64, string) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, "/worktime/meetings/" + r.PathValue("id")
}

// meetingPage: GET /worktime/meetings/{id} — the meeting, its agenda and minutes; ?print=1 — the minutes to print.
func (s *Server) meetingPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, _ := meetingID(r)
	m, err := s.Company.Meeting(r.Context(), sess.UserID, sess.Admin, id)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/meetings")
		return
	}
	can := m.CanEdit(sess.UserID, sess.Admin) && m.Status != company.MeetingDone
	data := map[string]any{"M": m, "Can": can, "Owner": m.CanEdit(sess.UserID, sess.Admin), "Me": sess.UserID}
	if r.URL.Query().Get("print") == "1" {
		var buf bytes.Buffer
		if err := s.pages[s.lang(r)]["meeting"].ExecuteTemplate(&buf, "mprint", data); err != nil {
			s.fail(w, "meeting print", err)
			return
		}
		s.invoiceHTML(w, buf.Bytes())
		return
	}
	if can {
		if data["Users"], err = s.Tasks.ListUsers(r.Context()); err != nil {
			s.taskErr(w, r, err, "/worktime/meetings")
			return
		}
		projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
		if err != nil {
			s.taskErr(w, r, err, "/worktime/meetings")
			return
		}
		var mine []tasks.Project
		for _, p := range projects {
			if p.CanCreate() && p.MyRole != "admin" {
				mine = append(mine, p)
			}
		}
		data["Projects"] = mine
		sched, err := s.Tasks.Schedule(r.Context())
		if err != nil {
			s.taskErr(w, r, err, "/worktime/meetings")
			return
		}
		data["Deadline"] = sched.DefaultDeadline(time.Now())
	}
	s.render(w, r, "meeting", page{Title: m.Title, Nav: "meetings", Data: data})
}

// meetingDelete: POST /worktime/meetings/{id}/delete — with its meeting in the calendar.
func (s *Server) meetingDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, back := meetingID(r)
	m, err := s.Company.DeleteMeeting(r.Context(), sess.UserID, sess.Admin, id)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.dropMeetingEvent(r.Context(), m)
	s.Log.Info("meeting deleted", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, "/worktime/meetings", http.StatusSeeOther)
}

// meetingStatus: POST /worktime/meetings/{id}/status (status: prepare | action | done).
func (s *Server) meetingStatus(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, back := meetingID(r)
	if err := s.Company.SetMeetingStatus(r.Context(), sess.UserID, sess.Admin, id, r.FormValue("status")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// meetingProtocol: POST /worktime/meetings/{id}/protocol (text) — the common result.
func (s *Server) meetingProtocol(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, back := meetingID(r)
	if err := s.Company.SetMeetingProtocol(r.Context(), sess.UserID, sess.Admin, id, r.FormValue("text")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#protocol", http.StatusSeeOther)
}

// meetingNext: POST /worktime/meetings/{id}/next (start — optional, a week later by default) — «Следующее собрание».
func (s *Server) meetingNext(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, back := meetingID(r)
	at, _ := time.ParseInLocation("2006-01-02T15:04", r.FormValue("start"), time.Local)
	nid, err := s.Company.NextMeeting(r.Context(), sess.UserID, sess.Admin, id, at)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("meeting next", "user_id", sess.UserID, "id", id, "next", nid)
	s.syncMeetingEvent(r.Context(), nid, company.Meeting{})
	http.Redirect(w, r, "/worktime/meetings/"+strconv.FormatInt(nid, 10), http.StatusSeeOther)
}

// meetingItemForm reads an item's fields.
func meetingItemForm(r *http.Request) company.MeetingItem {
	i := company.MeetingItem{Title: r.FormValue("title"), Reporter: company.Person{ID: r.FormValue("reporter")}, Result: r.FormValue("result")}
	i.ParentID, _ = strconv.ParseInt(r.FormValue("parent"), 10, 64)
	i.Minutes, _ = strconv.Atoi(r.FormValue("minutes"))
	return i
}

// meetingItemAdd: POST /worktime/meetings/{id}/items (title, reporter, minutes, parent — a sub-item).
func (s *Server) meetingItemAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, back := meetingID(r)
	iid, err := s.Company.AddMeetingItem(r.Context(), sess.UserID, sess.Admin, id, meetingItemForm(r))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#item"+strconv.FormatInt(iid, 10), http.StatusSeeOther)
}

// meetingItemID is the item of the path and the address of its meeting.
func meetingItemID(r *http.Request) (int64, string) {
	id, _ := strconv.ParseInt(r.PathValue("item"), 10, 64)
	return id, "/worktime/meetings/" + r.PathValue("id") + "#item" + r.PathValue("item")
}

// meetingItemUpdate: POST /worktime/meetings/{id}/items/{item} (title, reporter, minutes, result) or, with only
// result, its result.
func (s *Server) meetingItemUpdate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	iid, back := meetingItemID(r)
	var err error
	if _ = r.ParseForm(); r.Form.Has("title") {
		i := meetingItemForm(r)
		i.ID = iid
		err = s.Company.UpdateMeetingItem(r.Context(), sess.UserID, sess.Admin, i)
	} else {
		err = s.Company.SetMeetingItemResult(r.Context(), sess.UserID, sess.Admin, iid, r.FormValue("result"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// meetingItemDelete: POST /worktime/meetings/{id}/items/{item}/delete.
func (s *Server) meetingItemDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	iid, _ := meetingItemID(r)
	_, back := meetingID(r)
	if err := s.Company.DeleteMeetingItem(r.Context(), sess.UserID, sess.Admin, iid); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#agenda", http.StatusSeeOther)
}

// meetingItemMove: POST /worktime/meetings/{id}/items/{item}/move (dir: up | down).
func (s *Server) meetingItemMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	iid, back := meetingItemID(r)
	if err := s.Company.MoveMeetingItem(r.Context(), sess.UserID, sess.Admin, iid, r.FormValue("dir") == "up"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// meetingItemTask: POST /worktime/meetings/{id}/items/{item}/task (project, title, responsible, deadline) — «Поставить
// задачу» on the item: the task tells where it comes from, the item shows it with its status.
func (s *Server) meetingItemTask(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	iid, back := meetingItemID(r)
	deadline, err := parseDeadline(r.FormValue("deadline"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	key, err := s.meetingTask(r, sess.UserID, sess.Admin, iid, meetingTaskIn{Project: r.FormValue("project"), Title: r.FormValue("title"),
		Description: r.FormValue("description"), Responsible: r.FormValue("responsible"), Deadline: deadline})
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("meeting task", "user_id", sess.UserID, "item", iid, "task", key)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// meetingTaskIn is a task set on an agenda item.
type meetingTaskIn struct {
	Project, Title, Description, Responsible string
	Deadline                                 *time.Time
}

// meetingTask sets a task on the item (of a meeting the person changes) and returns its key: the task tells which
// meeting it comes from.
func (s *Server) meetingTask(r *http.Request, userID string, admin bool, itemID int64, in meetingTaskIn) (string, error) {
	m, err := s.Company.MeetingOfItem(r.Context(), userID, admin, itemID)
	if err != nil {
		return "", err
	}
	p, err := s.Tasks.ProjectByKey(r.Context(), userID, in.Project)
	if err != nil {
		return "", err
	}
	desc := s.tr(r, "meeting.task_from", m.Title, m.Start.In(time.Local).Format("02.01.2006 15:04")) + "\n" +
		s.BaseURL + "/worktime/meetings/" + strconv.FormatInt(m.ID, 10)
	if v := strings.TrimSpace(in.Description); v != "" {
		desc = v + "\n\n" + desc
	}
	key, err := s.Tasks.CreateTask(r.Context(), userID, p, tasks.NewTask{Title: in.Title, Description: desc,
		ResponsibleID: in.Responsible, Priority: 1, Deadline: in.Deadline})
	if err != nil {
		return "", err
	}
	tid, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err != nil {
		return "", err
	}
	return key, s.Company.LinkMeetingTask(r.Context(), itemID, tid)
}
