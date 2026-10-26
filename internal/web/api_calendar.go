package web

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST API of the calendar, as calendar.event.* and calendar.accessibility.get: the key's user sees what
// their calendar shows; meetings are changed by their organizers.

func (s *Server) apiCalendarRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/calendar/events", s.api("calendar:read", s.apiCalEvents))
	mux.HandleFunc("POST /api/v1/calendar/events", s.api("calendar:write", s.apiCalSave))
	mux.HandleFunc("GET /api/v1/calendar/events/{id}", s.api("calendar:read", s.apiCalEvent))
	mux.HandleFunc("PATCH /api/v1/calendar/events/{id}", s.api("calendar:write", s.apiCalSave))
	mux.HandleFunc("DELETE /api/v1/calendar/events/{id}", s.api("calendar:write", s.apiCalDelete))
	mux.HandleFunc("POST /api/v1/calendar/events/{id}/respond", s.api("calendar:write", s.apiCalRespond))
	mux.HandleFunc("POST /api/v1/calendar/events/{id}/join", s.api("calendar:write", s.apiCalJoin))
	mux.HandleFunc("POST /api/v1/calendar/events/{id}/chat", s.api("calendar:write", s.apiCalChat))
	mux.HandleFunc("GET /api/v1/calendar/open", s.api("calendar:read", s.apiCalOpen))
	mux.HandleFunc("GET /api/v1/calendar/categories", s.api("calendar:read", s.apiCalCategories))
	mux.HandleFunc("POST /api/v1/calendar/categories", s.api("calendar:write", s.apiCalCategorySave))
	mux.HandleFunc("PATCH /api/v1/calendar/categories/{id}", s.api("calendar:write", s.apiCalCategorySave))
	mux.HandleFunc("DELETE /api/v1/calendar/categories/{id}", s.api("calendar:write", s.apiCalCategoryDelete))
	mux.HandleFunc("GET /api/v1/calendar/events/{id}/ics", s.api("calendar:read", s.apiCalEventICS))
	mux.HandleFunc("POST /api/v1/calendar/import", s.api("calendar:write", s.apiCalImport))
	mux.HandleFunc("GET /api/v1/calendar/search", s.api("calendar:read", s.apiCalSearch))
	mux.HandleFunc("GET /api/v1/calendar/busy", s.api("calendar:read", s.apiCalBusy))
	mux.HandleFunc("GET /api/v1/calendar/rooms", s.api("calendar:read", s.apiCalRooms))
	mux.HandleFunc("GET /api/v1/calendar/sections", s.api("calendar:read", s.apiCalSections))
	mux.HandleFunc("POST /api/v1/calendar/sections", s.api("calendar:write", s.apiCalSectionSave))
	mux.HandleFunc("PATCH /api/v1/calendar/sections/{id}", s.api("calendar:write", s.apiCalSectionSave))
	mux.HandleFunc("DELETE /api/v1/calendar/sections/{id}", s.api("calendar:write", s.apiCalSectionDelete))
	mux.HandleFunc("POST /api/v1/calendar/links", s.api("calendar:write", s.apiCalLinkAdd))
	mux.HandleFunc("POST /api/v1/calendar/links/{id}/sync", s.api("calendar:write", s.apiCalLinkSync))
	mux.HandleFunc("POST /api/v1/calendar/dav", s.api("calendar:write", s.apiCalDavAdd))
	mux.HandleFunc("POST /api/v1/calendar/dav/{id}/sync", s.api("calendar:write", s.apiCalDavSync))
	mux.HandleFunc("GET /api/v1/calendar/people", s.api("calendar:read", s.apiCalPeople))
	mux.HandleFunc("POST /api/v1/calendar/people", s.api("calendar:write", s.apiCalPersonAdd))
	mux.HandleFunc("DELETE /api/v1/calendar/people/{id}", s.api("calendar:write", s.apiCalPersonDelete))
	mux.HandleFunc("GET /api/v1/calendar/access", s.api("calendar:read", s.apiCalAccess))
	mux.HandleFunc("PUT /api/v1/calendar/access", s.api("calendar:write", s.apiCalAccess))
	mux.HandleFunc("GET /api/v1/calendar/share", s.api("calendar:read", s.apiCalShare))
	mux.HandleFunc("PUT /api/v1/calendar/share", s.api("calendar:write", s.apiCalShare))
	mux.HandleFunc("GET /api/v1/calendar/settings", s.api("calendar:read", s.apiCalSettings))
	mux.HandleFunc("PUT /api/v1/calendar/settings", s.api("calendar:write", s.apiCalSettings))
	mux.HandleFunc("GET /api/v1/calendar/slots", s.api("calendar:read", s.apiCalSlots))
	mux.HandleFunc("POST /api/v1/calendar/slots", s.api("calendar:write", s.apiCalSlotSave))
	mux.HandleFunc("PATCH /api/v1/calendar/slots/{id}", s.api("calendar:write", s.apiCalSlotSave))
	mux.HandleFunc("DELETE /api/v1/calendar/slots/{id}", s.api("calendar:write", s.apiCalSlotDelete))
}

type apiAttendee struct {
	apiUser
	Status string `json:"status"` // invited | accepted | tentative | declined
}

type apiEvent struct {
	ID            string        `json:"id,omitempty"`
	Kind          string        `json:"kind"` // event | task (a deadline) | absence | birthday
	Title         string        `json:"title"`
	Description   string        `json:"description,omitempty"`
	Where         string        `json:"where,omitempty"`
	Start         time.Time     `json:"start"`
	End           time.Time     `json:"end"`
	AllDay        bool          `json:"all_day"`
	Owner         *apiUser      `json:"owner,omitempty"`
	Project       string        `json:"project,omitempty"` // a project's (a collab's) calendar
	Company       bool          `json:"company,omitempty"`
	Attendees     []apiAttendee `json:"attendees,omitempty"`
	MyStatus      string        `json:"my_status,omitempty"`
	Repeat        string        `json:"repeat,omitempty"` // daily | weekly | monthly | yearly
	Every         int           `json:"every,omitempty"`
	Until         *time.Time    `json:"until,omitempty"`
	RepeatDays    []int         `json:"repeat_days,omitempty"`   // weekly: the days, 1 — Monday … 7 — Sunday
	RepeatCount   int           `json:"repeat_count,omitempty"`  // ends after so many meetings (until — the last one's day)
	SeriesStart   *time.Time    `json:"series_start,omitempty"`  // a repeated meeting: its first occurrence
	RemindBefore  *int          `json:"remind_before,omitempty"` // minutes, the earliest of Reminds
	Reminds       []int         `json:"reminds,omitempty"`       // minutes before the start of each reminder
	Accessibility string        `json:"accessibility,omitempty"` // busy | tentative | absent | free
	Private       bool          `json:"private,omitempty"`
	Hidden        bool          `json:"hidden,omitempty"` // someone else's private meeting: only its time is shown
	Task          string        `json:"task,omitempty"`   // kind task: its key
	Overdue       bool          `json:"overdue,omitempty"`
	Person        string        `json:"person,omitempty"` // kind absence, birthday: whose
	AbsenceKind   string        `json:"absence_kind,omitempty"`
	Room          *apiRoom      `json:"room,omitempty"`
	Video         bool          `json:"video,omitempty"` // a video meeting of go24
	Join          string        `json:"join,omitempty"`  // its call: the link to join
	Color         string        `json:"color,omitempty"` // #rrggbb, "" — the colour of its calendar
	Important     bool          `json:"important,omitempty"`
	Calendar      string        `json:"calendar,omitempty"`      // one of the organizer's own calendars (sections), "" — the main one
	Of            string        `json:"of,omitempty"`            // shown from a colleague's calendar: whose
	CRM           []apiCRMRef   `json:"crm,omitempty"`           // the CRM cards of the meeting (a single meeting only)
	TimeZone      string        `json:"timezone,omitempty"`      // its IANA zone, "" — the portal's
	Category      string        `json:"category,omitempty"`      // an open event: the id of its category
	ActivityKind  string        `json:"activity_kind,omitempty"` // kind crm (a CRM activity): todo | call | meeting, its card in crm
	Chat          string        `json:"chat,omitempty"`          // «Чат встречи»: its messenger room
}

// apiCRMRef is a CRM card of a meeting: entity deal | lead | contact | company.
type apiCRMRef struct {
	Entity string `json:"entity"`
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
}

// apiEventCRM is the meeting with its CRM cards.
func (s *Server) apiEventCRM(r *http.Request, e calendar.Event) (apiEvent, error) {
	a := toAPIEvent(e)
	if s.CRM == nil || e.Hidden {
		return a, nil
	}
	refs, err := s.Calendar.CRM(r.Context(), e.ID)
	for _, c := range refs {
		a.CRM = append(a.CRM, apiCRMRef{c.Entity, c.ID, c.Title})
	}
	return a, err
}

type apiRoom struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
	Capacity int    `json:"capacity,omitempty"`
}

func toAPIEvent(e calendar.Event) apiEvent {
	a := apiEvent{ID: e.ID, Kind: e.Kind, Title: e.Title, Description: e.Description, Where: e.Where, Start: e.Start,
		End: e.End, AllDay: e.AllDay, Project: e.ProjectKey, Company: e.Company, MyStatus: e.MyStatus, Repeat: e.Repeat,
		Until: e.Until, Reminds: e.Reminds, Accessibility: e.Accessibility, Private: e.Private, Hidden: e.Hidden,
		Task: e.TaskKey, Overdue: e.Overdue, Person: e.PersonID, AbsenceKind: e.AbsenceKind, Color: e.Color, Important: e.Important,
		Calendar: e.CalendarID, Of: e.Of, TimeZone: e.TZ, Category: e.CategoryID, Chat: e.ChatRoom}
	if len(e.Reminds) > 0 {
		a.RemindBefore = &e.Reminds[len(e.Reminds)-1]
	}
	if e.Card != nil {
		a.ActivityKind, a.CRM = e.ActKind, []apiCRMRef{{e.Card.Entity, e.Card.ID, e.Card.Title}}
	}
	if e.Kind == "event" {
		a.Owner = &apiUser{ID: e.OwnerID, Name: e.OwnerName}
		if e.Repeat != "" {
			a.Every, a.SeriesStart, a.RepeatDays, a.RepeatCount = max(e.Every, 1), &e.SeriesStart, e.Days, e.Count
		}
	}
	if e.Video && !e.Hidden {
		a.Video, a.Join = true, "/call?event="+e.ID
	}
	if e.RoomID != "" {
		a.Room = &apiRoom{ID: e.RoomID, Name: e.RoomName}
	}
	for _, at := range e.Attendees {
		a.Attendees = append(a.Attendees, apiAttendee{apiUser{at.ID, at.Name}, at.Status})
	}
	return a
}

// apiRange: ?from= and ?to= (RFC 3339 or 2006-01-02; by default the current month), at most a year.
func apiRange(r *http.Request) (time.Time, time.Time, error) {
	parse := func(name string, def time.Time) (time.Time, error) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return def, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			return t, nil
		}
		return time.Time{}, i18n.E("err.event_time")
	}
	now := time.Now()
	from, err := parse("from", time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local))
	if err != nil {
		return from, from, err
	}
	to, err := parse("to", from.AddDate(0, 1, 0))
	if err != nil || !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return from, to, i18n.E("err.event_time")
	}
	return from, to, nil
}

// apiCalEvents: what the user's calendar shows between ?from and ?to (occurrences of repeated meetings, deadlines of
// tasks, absences, birthdays), or (?project=KEY) the calendar of a project, or (?user=ID) a colleague's calendar.
func (s *Server) apiCalEvents(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to, err := apiRange(r)
	if err != nil {
		return err
	}
	var list []calendar.Event
	if key := r.URL.Query().Get("project"); key != "" {
		proj, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, key)
		if err != nil {
			return err
		}
		list, err = s.Calendar.ProjectRange(r.Context(), p.UserID, proj.ID, from, to)
		if err != nil {
			return err
		}
	} else if id := r.URL.Query().Get("user"); id != "" && id != p.UserID {
		if list, err = s.Calendar.PersonRange(r.Context(), p.UserID, id, from, to); err != nil {
			return err
		}
	} else if list, err = s.Calendar.Range(r.Context(), p.UserID, from, to); err != nil {
		return err
	}
	out := make([]apiEvent, 0, len(list))
	for _, e := range list {
		out = append(out, toAPIEvent(e))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCalSearch is the calendar's search: meetings with the text ?q= in the title, description or place, of the preset
// ?preset= invites (not answered yet) | organizer | declined.
func (s *Server) apiCalSearch(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Calendar.Search(r.Context(), p.UserID, r.URL.Query().Get("q"), r.URL.Query().Get("preset"))
	if err != nil {
		return err
	}
	out := make([]apiEvent, 0, len(list))
	for _, e := range list {
		out = append(out, toAPIEvent(e))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiCalEvent(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	p, r, err := s.apiCalActor(r, p)
	if err != nil {
		return err
	}
	e, err := s.Calendar.Get(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	a, err := s.apiEventCRM(r, e)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

// apiOccurrence: ?at= (RFC 3339) — the original start of an occurrence of a repeated meeting, ?scope= one (default)
// | following | all — what a change or deletion is for.
func apiOccurrence(r *http.Request) (*time.Time, string, error) {
	v := r.URL.Query().Get("at")
	if v == "" {
		return nil, "", nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, "", i18n.E("err.event_time")
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = calendar.ScopeOne
	}
	return &t, scope, nil
}

// apiCalSave creates (POST) or changes (PATCH: only the fields given) a meeting; attendees are user IDs and replace
// the invited people.
func (s *Server) apiCalSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	p, r, err := s.apiCalActor(r, p)
	if err != nil {
		return err
	}
	var in struct {
		Title         *string      `json:"title"`
		Description   *string      `json:"description"`
		Where         *string      `json:"where"`
		Start         *time.Time   `json:"start"`
		End           *time.Time   `json:"end"`
		AllDay        *bool        `json:"all_day"`
		Project       *string      `json:"project"` // a project's key ("" — personal)
		Company       *bool        `json:"company"`
		Attendees     *[]string    `json:"attendees"`
		Repeat        *string      `json:"repeat"`
		Every         *int         `json:"every"`
		Until         *string      `json:"until"`         // 2006-01-02, "" — no end
		RepeatDays    *[]int       `json:"repeat_days"`   // weekly: 1 — Monday … 7 — Sunday; [] — the day of the start
		RepeatCount   *int         `json:"repeat_count"`  // ends after so many meetings; 0 — by until or never
		RemindBefore  *int         `json:"remind_before"` // minutes, one reminder; -1 — no reminder
		Reminds       *[]int       `json:"reminds"`       // minutes, all reminders; [] — none
		Accessibility *string      `json:"accessibility"`
		Private       *bool        `json:"private"`
		Room          *string      `json:"room"` // a meeting room's id ("" — none)
		Video         *bool        `json:"video"`
		Color         *string      `json:"color"` // #rrggbb, "" — the colour of its calendar
		Important     *bool        `json:"important"`
		Calendar      *string      `json:"calendar"` // one of one's own calendars ("" — the main one)
		CRM           *[]apiCRMRef `json:"crm"`      // the CRM cards, all of them; [] — none
		TimeZone      *string      `json:"timezone"` // an IANA zone, "" — the portal's
		Category      *string      `json:"category"` // an open event's category ("" — an ordinary meeting)
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var e calendar.Event
	var invite []string
	id := r.PathValue("id")
	at, scope, err := apiOccurrence(r)
	if err != nil {
		return err
	}
	if id != "" {
		if e, err = s.Calendar.Get(r.Context(), p.UserID, id); err != nil {
			return err
		}
		if at != nil { // the fields not given are those of the occurrence
			o := e.Occurrence(*at)
			if o == nil {
				return i18n.E("err.not_found")
			}
			e = *o
		}
		for _, a := range e.Attendees {
			invite = append(invite, a.ID)
		}
	} else {
		if in.Start == nil {
			return i18n.E("err.event_time")
		}
		if in.End == nil {
			end := in.Start.Add(time.Hour)
			in.End = &end
		}
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&e.Title, in.Title)
	set(&e.Description, in.Description)
	set(&e.Where, in.Where)
	set(&e.Repeat, in.Repeat)
	set(&e.Accessibility, in.Accessibility)
	set(&e.RoomID, in.Room)
	set(&e.Color, in.Color)
	set(&e.CalendarID, in.Calendar)
	set(&e.TZ, in.TimeZone)
	set(&e.CategoryID, in.Category)
	if in.Start != nil {
		if in.End == nil { // moved: the same length
			e.End = in.Start.In(time.Local).Add(e.End.Sub(e.Start))
		}
		e.Start = in.Start.In(time.Local)
	}
	if in.End != nil {
		e.End = in.End.In(time.Local)
	}
	if in.AllDay != nil {
		e.AllDay = *in.AllDay
	}
	if in.Company != nil {
		e.Company = *in.Company
	}
	if in.Private != nil {
		e.Private = *in.Private
	}
	if in.Video != nil {
		e.Video = *in.Video
	}
	if in.Important != nil {
		e.Important = *in.Important
	}
	if in.Every != nil {
		e.Every = *in.Every
	}
	if in.RepeatDays != nil {
		e.Days = *in.RepeatDays
	}
	if in.RepeatCount != nil {
		e.Count = *in.RepeatCount
	}
	if in.Until != nil {
		e.Until, e.Count = nil, 0
		if *in.Until != "" {
			u, err := time.ParseInLocation("2006-01-02", *in.Until, time.Local)
			if err != nil {
				return i18n.E("err.event_time")
			}
			e.Until = &u
		}
	}
	if in.RemindBefore != nil {
		e.Reminds = []int{*in.RemindBefore}
	}
	if in.Reminds != nil {
		e.Reminds = *in.Reminds
	}
	if in.Project != nil {
		e.ProjectID = ""
		if *in.Project != "" {
			proj, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, *in.Project)
			if err != nil {
				return err
			}
			e.ProjectID = proj.ID
		}
	}
	if in.Attendees != nil {
		invite = slices.Clone(*in.Attendees)
	}
	var crmRefs []calendar.CRMRef
	if in.CRM != nil && s.CRM != nil { // a card linked anew is one the person may see
		var had []calendar.CRMRef
		if id != "" {
			if had, err = s.Calendar.CRM(r.Context(), id); err != nil {
				return err
			}
		}
		for _, c := range *in.CRM {
			ref := calendar.CRMRef{Entity: c.Entity, ID: c.ID}
			if !ref.Valid() {
				return i18n.E("err.bad_form")
			}
			if !slices.ContainsFunc(had, func(h calendar.CRMRef) bool { return h.Entity == ref.Entity && h.ID == ref.ID }) {
				if _, err := s.crmRefCard(r, auth.Session{UserID: p.UserID}, ref); err != nil {
					return err
				}
			}
			crmRefs = append(crmRefs, ref)
		}
	}
	if at != nil && id != "" {
		id, err = s.Calendar.SaveOccurrence(r.Context(), p.UserID, id, *at, scope, e, invite)
	} else {
		id, err = s.Calendar.Save(r.Context(), p.UserID, e, invite)
	}
	if err != nil {
		return err
	}
	if at == nil || scope == calendar.ScopeAll {
		s.feedMeeting(r.Context(), p.UserID, id)
	}
	if in.CRM != nil && s.CRM != nil {
		if err := s.Calendar.SetCRM(r.Context(), p.UserID, id, crmRefs); err != nil {
			return err
		}
	}
	s.meetingChatSync(r.Context(), p.UserID, id)
	if e, err = s.Calendar.Get(r.Context(), p.UserID, id); err != nil {
		return err
	}
	a, err := s.apiEventCRM(r, e)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, a)
	return nil
}

func (s *Server) apiCalDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	p, r, err := s.apiCalActor(r, p)
	if err != nil {
		return err
	}
	at, scope, err := apiOccurrence(r)
	if err != nil {
		return err
	}
	if at != nil {
		err = s.Calendar.DeleteOccurrence(r.Context(), p.UserID, r.PathValue("id"), *at, scope)
	} else {
		err = s.Calendar.Delete(r.Context(), p.UserID, r.PathValue("id"))
		if err == nil {
			s.withdraw(r.Context(), feed.SourceMeeting, r.PathValue("id"))
		}
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiCalRespond: {"status": "accepted" | "tentative" | "declined"} — the invited person's answer.
func (s *Server) apiCalRespond(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	p, r, err := s.apiCalActor(r, p)
	if err != nil {
		return err
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := s.Calendar.Respond(r.Context(), p.UserID, id, in.Status); err != nil {
		return err
	}
	e, err := s.Calendar.Get(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIEvent(e))
	return nil
}

// apiCalBusy is the planner (calendar.accessibility.get): the busy time of ?user=… between ?from and ?to; with
// ?length=minutes — also the nearest start (not before ?from) at which all of them are free in working hours.
func (s *Server) apiCalBusy(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to, err := apiRange(r)
	if err != nil {
		return err
	}
	people, err := s.Calendar.Busy(r.Context(), p.UserID, r.URL.Query()["user"], from, to, r.URL.Query().Get("except"))
	if err != nil {
		return err
	}
	type slot struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
		Kind  string    `json:"kind"` // busy | tentative | absent | booking
		Title string    `json:"title,omitempty"`
		Event string    `json:"event,omitempty"`
	}
	type person struct {
		apiUser
		Busy []slot `json:"busy"`
	}
	out := struct {
		People []person   `json:"people"`
		Free   *time.Time `json:"free,omitempty"`
	}{People: []person{}}
	for _, pp := range people {
		o := person{apiUser: apiUser{pp.ID, pp.Name}, Busy: []slot{}}
		for _, sl := range pp.Slots {
			o.Busy = append(o.Busy, slot{sl.Start, sl.End, sl.Kind, sl.Title, sl.EventID})
		}
		out.People = append(out.People, o)
	}
	if m, err := strconv.Atoi(r.URL.Query().Get("length")); err == nil && m > 0 {
		sched, err := s.Tasks.Schedule(r.Context())
		if err != nil {
			return err
		}
		start := from
		if now := time.Now(); now.After(start) {
			start = now
		}
		if t, ok := calendar.FirstFree(people, start, time.Duration(m)*time.Minute, sched, int(to.Sub(start).Hours()/24)+1); ok && t.Before(to) {
			out.Free = &t
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCalRooms: the meeting rooms of the company (calendar.resource.list), with ?from&to — their bookings.
func (s *Server) apiCalRooms(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if tasks.IsGuestCtx(r.Context()) {
		return tasks.ErrNotFound
	}
	from, to, err := apiRange(r)
	if err != nil {
		return err
	}
	rooms, err := s.Calendar.Rooms(r.Context())
	if err != nil {
		return err
	}
	type booking struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
		Title string    `json:"title,omitempty"`
		Event string    `json:"event,omitempty"`
	}
	type room struct {
		apiRoom
		Busy []booking `json:"busy"`
	}
	out := make([]room, 0, len(rooms))
	for _, rm := range rooms {
		_, slots, err := s.Calendar.RoomBusy(r.Context(), p.UserID, rm.ID, from, to)
		if err != nil {
			return err
		}
		o := room{apiRoom{rm.ID, rm.Name, rm.Location, rm.Capacity}, []booking{}}
		for _, sl := range slots {
			o.Busy = append(o.Busy, booking{sl.Start, sl.End, sl.Title, sl.EventID})
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiSection is one of the user's own calendars (calendar.section.*); the main one has id "main", its
// colour only.
type apiSection struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color"`
	Main  bool   `json:"main,omitempty"`
	// a calendar connected by its iCal link: the link, when it was loaded last, why it was not
	URL       string     `json:"url,omitempty"`
	SyncedAt  *time.Time `json:"synced_at,omitempty"`
	SyncError string     `json:"sync_error,omitempty"`
	// a calendar of a CalDAV server connected two ways: its collection and the login
	DAV   string `json:"caldav,omitempty"`
	Login string `json:"login,omitempty"`
}

func apiSectionOf(x calendar.Section) apiSection {
	return apiSection{ID: x.ID, Name: x.Name, Color: x.Color, URL: x.URL, SyncedAt: x.SyncedAt, SyncError: x.SyncError, DAV: x.DAV, Login: x.Login}
}

// apiCalSections: the main calendar first, then one's own by name.
func (s *Server) apiCalSections(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	main, err := s.Calendar.MainColor(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	list, err := s.Calendar.Sections(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []apiSection{{ID: "main", Color: main, Main: true}}
	for _, x := range list {
		out = append(out, apiSectionOf(x))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCalSectionSave: {"name", "color"} — adds (POST) or changes (PATCH: the fields given) one's own calendar; PATCH
// main — the colour of the main calendar.
func (s *Server) apiCalSectionSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if id == "main" {
		if in.Color != nil {
			if err := s.Calendar.SetMainColor(r.Context(), p.UserID, *in.Color); err != nil {
				return err
			}
		}
		return s.apiCalSections(w, r, p)
	}
	var x calendar.Section
	if id != "" {
		list, err := s.Calendar.Sections(r.Context(), p.UserID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(x calendar.Section) bool { return x.ID == id })
		if i < 0 {
			return calendar.ErrNotFound
		}
		x = list[i]
	}
	if in.Name != nil {
		x.Name = *in.Name
	}
	if in.Color != nil {
		x.Color = *in.Color
	}
	id, err := s.Calendar.SaveSection(r.Context(), p.UserID, x)
	if err != nil {
		return err
	}
	list, err := s.Calendar.Sections(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	for _, x := range list {
		if x.ID == id {
			writeJSON(w, status, apiSection{ID: x.ID, Name: x.Name, Color: x.Color})
		}
	}
	return nil
}

// apiCalSectionDelete removes one's own calendar with its meetings.
func (s *Server) apiCalSectionDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Calendar.DeleteSection(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---------- calendars of colleagues ----------

type apiPerson struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// apiCalPeople: the colleagues whose calendars are in one's panel, by name.
func (s *Server) apiCalPeople(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Calendar.People(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]apiPerson, 0, len(list))
	for _, x := range list {
		out = append(out, apiPerson(x))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCalPersonAdd: {"user", "color"} — adds a colleague's calendar to one's panel (again — changes its colour).
func (s *Server) apiCalPersonAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ User, Color string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Calendar.AddPerson(r.Context(), p.UserID, in.User, in.Color); err != nil {
		return err
	}
	list, err := s.Calendar.People(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.ID == in.User {
			writeJSON(w, http.StatusCreated, apiPerson(x))
		}
	}
	return nil
}

// apiCalPersonDelete takes a colleague's calendar off one's panel.
func (s *Server) apiCalPersonDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Calendar.RemovePerson(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiCalShare: {"share": "busy" | "full"} — what colleagues see in one's calendar (PUT sets it).
func (s *Server) apiCalShare(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPut {
		var in struct{ Share string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Calendar.SetShare(r.Context(), p.UserID, in.Share); err != nil {
			return err
		}
	}
	v, err := s.Calendar.Share(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"share": v})
	return nil
}

type apiGrant struct {
	User  apiUser `json:"user"`
	Level string  `json:"level"` // view | edit
}

// apiCalAccess: access to one's calendar given to colleagues by name ("given") and the calendars of colleagues given
// to one ("granted"); PUT {"user", "level": "view" | "edit" | ""} gives it, changes it or ("") takes it away. With
// «edit» one works with the colleague's meetings by ?as=ID of the events' methods, as that colleague.
func (s *Server) apiCalAccess(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPut {
		var in struct{ User, Level string }
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Calendar.SetGrant(r.Context(), p.UserID, in.User, in.Level); err != nil {
			return err
		}
	}
	given, err := s.Calendar.Grants(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	granted, err := s.Calendar.Granted(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	conv := func(list []calendar.Grant) []apiGrant {
		out := make([]apiGrant, 0, len(list))
		for _, g := range list {
			out = append(out, apiGrant{apiUser{ID: g.UserID, Name: g.Name}, g.Level})
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string][]apiGrant{"given": conv(given), "granted": conv(granted)})
	return nil
}

// apiCalActor: with ?as=ID the key's user works with the meetings of a colleague who gave them «Управление» of their
// calendar — as that colleague, whose private meetings stay hidden from them.
func (s *Server) apiCalActor(r *http.Request, p apikeys.Principal) (apikeys.Principal, *http.Request, error) {
	actor, r, err := s.actorOf(r, p.UserID, r.URL.Query().Get("as"))
	p.UserID = actor
	return p, r, err
}

// apiCalSettings: the settings of one's calendar (PUT changes those given, the rest stay) — {"week_start": 1 Monday …
// 7 Sunday, "work_from", "work_to" (hours), "days_off": [6, 7], "show_declined", "show_tasks", "remind": minutes, -1 none}.
func (s *Server) apiCalSettings(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	cs, err := s.Calendar.Settings(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	if r.Method == http.MethodPut {
		if err := readJSON(r, &cs); err != nil {
			return err
		}
		if err := s.Calendar.SaveSettings(r.Context(), p.UserID, cs); err != nil {
			return err
		}
		if cs, err = s.Calendar.Settings(r.Context(), p.UserID); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, cs)
	return nil
}

// apiSlotLink is a link of «Поделиться слотами»: the personal one has no members.
type apiSlotLink struct {
	ID        string              `json:"id"`
	URL       string              `json:"url"`
	Members   []string            `json:"members"`
	Length    int                 `json:"length"` // minutes, one of calendar.SlotLengths
	Rules     []calendar.SlotRule `json:"rules"`  // weekdays 1 Monday … 7 Sunday, from and to minutes since midnight
	DaysAhead int                 `json:"days_ahead"`
	Active    bool                `json:"active"`
	Meetings  int                 `json:"meetings"`
}

func (s *Server) apiSlotLinkOf(l calendar.SlotLink) apiSlotLink {
	return apiSlotLink{ID: l.ID, URL: s.BaseURL + "/cs/" + l.Token, Members: l.Members, Length: l.Length, Rules: l.Rules,
		DaysAhead: l.DaysAhead, Active: l.Active, Meetings: l.Meetings}
}

// apiCalSlots: one's links, the personal one first.
func (s *Server) apiCalSlots(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Calendar.SlotLinks(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []apiSlotLink{}
	for _, l := range list {
		out = append(out, s.apiSlotLinkOf(l))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCalSlotSave: {"members", "length", "rules", "days_ahead", "active"} — adds a joint link (POST, members needed)
// or changes one (PATCH: the fields given).
func (s *Server) apiCalSlotSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Members   *[]string            `json:"members"`
		Length    *int                 `json:"length"`
		Rules     *[]calendar.SlotRule `json:"rules"`
		DaysAhead *int                 `json:"days_ahead"`
		Active    *bool                `json:"active"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	l := calendar.SlotLink{Length: 60, DaysAhead: 30, Active: true}
	if id := r.PathValue("id"); id != "" {
		var err error
		if l, err = s.Calendar.SlotLink(r.Context(), p.UserID, id); err != nil {
			return err
		}
	} else {
		x, err := s.Calendar.Settings(r.Context(), p.UserID)
		if err != nil {
			return err
		}
		l.Rules = calendar.DefaultSlotRules(x)
	}
	if in.Members != nil {
		l.Members = *in.Members
	}
	if in.Length != nil {
		l.Length = *in.Length
	}
	if in.Rules != nil {
		l.Rules = *in.Rules
	}
	if in.DaysAhead != nil {
		l.DaysAhead = *in.DaysAhead
	}
	if in.Active != nil {
		l.Active = *in.Active
	}
	id, err := s.Calendar.SaveSlotLink(r.Context(), p.UserID, l)
	if err != nil {
		return err
	}
	if l, err = s.Calendar.SlotLink(r.Context(), p.UserID, id); err != nil {
		return err
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, s.apiSlotLinkOf(l))
	return nil
}

// apiCalSlotDelete removes a joint link; the personal one is only switched off.
func (s *Server) apiCalSlotDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Calendar.DeleteSlotLink(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
