package web

import (
	"net/http"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/company"
	"go24/internal/i18n"
)

// REST API of «Собрания и планёрки» (meeting.*): the meetings the key owner sees, their agenda and the
// tasks set on it; the organizer, the secretary and administrators change a meeting.
func (s *Server) apiMeetingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/pulse", s.api("company:read", s.apiPulse)) // «Пульс компании»
	mux.HandleFunc("GET /api/v1/meetings", s.api("company:read", s.apiMeetings))
	mux.HandleFunc("POST /api/v1/meetings", s.api("company:write", s.apiMeetingCreate))
	mux.HandleFunc("GET /api/v1/meetings/{id}", s.api("company:read", s.apiMeeting))
	mux.HandleFunc("PATCH /api/v1/meetings/{id}", s.api("company:write", s.apiMeetingUpdate))
	mux.HandleFunc("DELETE /api/v1/meetings/{id}", s.api("company:write", s.apiMeetingDelete))
	mux.HandleFunc("POST /api/v1/meetings/{id}/next", s.api("company:write", s.apiMeetingNext))
	mux.HandleFunc("POST /api/v1/meetings/{id}/items", s.api("company:write", s.apiMeetingItemAdd))
	mux.HandleFunc("PATCH /api/v1/meetings/{id}/items/{item}", s.api("company:write", s.apiMeetingItemUpdate))
	mux.HandleFunc("DELETE /api/v1/meetings/{id}/items/{item}", s.api("company:write", s.apiMeetingItemDelete))
	mux.HandleFunc("POST /api/v1/meetings/{id}/items/{item}/tasks", s.api("company:write", s.apiMeetingItemTask))
}

type apiMeetingTask struct {
	Key         string     `json:"key"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	Responsible apiDeptRef `json:"responsible"`
	Deadline    *time.Time `json:"deadline,omitempty"`
	ItemID      int64      `json:"item_id"`
}

type apiMeetingItem struct {
	ID       int64            `json:"id"`
	ParentID int64            `json:"parent_id,omitempty"`
	Num      string           `json:"num"`
	Title    string           `json:"title"`
	Reporter *apiDeptRef      `json:"reporter,omitempty"`
	Minutes  int              `json:"minutes,omitempty"`
	Result   string           `json:"result"`
	Tasks    []apiMeetingTask `json:"tasks"`
}

type apiMeetingOut struct {
	ID          int64            `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Place       string           `json:"place"`
	Start       time.Time        `json:"start"`
	Minutes     int              `json:"minutes"`
	Status      string           `json:"status"`
	Owner       apiDeptRef       `json:"owner"`
	Keeper      *apiDeptRef      `json:"keeper,omitempty"`
	Members     []apiDeptRef     `json:"members"`
	PrevID      int64            `json:"prev_id,omitempty"`
	NextID      int64            `json:"next_id,omitempty"`
	EventID     string           `json:"event_id,omitempty"`
	Protocol    string           `json:"protocol"`
	Started     *time.Time       `json:"started,omitempty"`
	Finished    *time.Time       `json:"finished,omitempty"`
	Items       []apiMeetingItem `json:"items,omitempty"`
	Carried     []apiMeetingTask `json:"carried,omitempty"`
}

func apiMeetingTasks(list []company.MeetingTask) []apiMeetingTask {
	out := make([]apiMeetingTask, 0, len(list))
	for _, t := range list {
		out = append(out, apiMeetingTask{Key: t.Key, Title: t.Title, Status: t.Status, Responsible: apiDeptRef{t.Responsible.ID, t.Responsible.Name},
			Deadline: t.Deadline, ItemID: t.ItemID})
	}
	return out
}

func apiMeetingOf(m company.Meeting) apiMeetingOut {
	o := apiMeetingOut{ID: m.ID, Title: m.Title, Description: m.Description, Place: m.Place, Start: m.Start, Minutes: m.Minutes,
		Status: m.Status, Owner: apiDeptRef{m.Owner.ID, m.Owner.Name}, Members: []apiDeptRef{}, PrevID: m.PrevID, NextID: m.NextID,
		EventID: m.EventID, Protocol: m.Protocol, Started: m.Started, Finished: m.Finished}
	if m.Keeper.ID != "" {
		o.Keeper = &apiDeptRef{m.Keeper.ID, m.Keeper.Name}
	}
	for _, p := range m.Members {
		o.Members = append(o.Members, apiDeptRef{p.ID, p.Name})
	}
	for _, i := range m.Items {
		ai := apiMeetingItem{ID: i.ID, ParentID: i.ParentID, Num: i.Num, Title: i.Title, Minutes: i.Minutes, Result: i.Result,
			Tasks: apiMeetingTasks(i.Tasks)}
		if i.Reporter.ID != "" {
			ai.Reporter = &apiDeptRef{i.Reporter.ID, i.Reporter.Name}
		}
		o.Items = append(o.Items, ai)
	}
	if len(m.Carried) > 0 {
		o.Carried = apiMeetingTasks(m.Carried)
	}
	return o
}

// apiMeetingTime reads a time: RFC 3339 or «2006-01-02T15:04» by the portal's clock.
func apiMeetingTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
	if err != nil {
		return time.Time{}, i18n.E("err.bad_form")
	}
	return t, nil
}

// GET /api/v1/meetings?role=owner|member&status=prepare|action|done&q= — the meetings the key owner sees.
func (s *Server) apiMeetings(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	list, err := s.Company.Meetings(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID),
		company.MeetingFilter{Role: q.Get("role"), Status: q.Get("status"), Query: q.Get("q")})
	if err != nil {
		return err
	}
	out := make([]apiMeetingOut, 0, len(list))
	for _, m := range list {
		out = append(out, apiMeetingOf(m))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMeetingOut(w http.ResponseWriter, r *http.Request, p apikeys.Principal, id int64, status int) error {
	m, err := s.Company.Meeting(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), id)
	if err != nil {
		return err
	}
	writeJSON(w, status, apiMeetingOf(m))
	return nil
}

// GET /api/v1/meetings/{id} — the meeting with its agenda and the open tasks of the previous one.
func (s *Server) apiMeeting(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return s.apiMeetingOut(w, r, p, id, http.StatusOK)
}

type apiMeetingIn struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Place       *string   `json:"place"`
	Start       *string   `json:"start"`
	Minutes     *int      `json:"minutes"`
	OwnerID     *string   `json:"owner_id"`
	KeeperID    *string   `json:"keeper_id"`
	Members     *[]string `json:"members"`
	PrevID      int64     `json:"prev_id"`  // create only
	Status      *string   `json:"status"`   // prepare | action | done (after the protocol: a finished meeting is read-only)
	Protocol    *string   `json:"protocol"` // the common result
}

// apiMeetingSave applies the fields given onto the meeting (a new one: m.ID 0).
func (s *Server) apiMeetingSave(r *http.Request, p apikeys.Principal, m company.Meeting, in apiMeetingIn) (int64, error) {
	admin := s.apiAdmin(r.Context(), p.UserID)
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&m.Title, in.Title)
	set(&m.Description, in.Description)
	set(&m.Place, in.Place)
	set(&m.Owner.ID, in.OwnerID)
	set(&m.Keeper.ID, in.KeeperID)
	if in.Start != nil {
		t, err := apiMeetingTime(*in.Start)
		if err != nil {
			return 0, err
		}
		m.Start = t
	}
	if in.Minutes != nil {
		m.Minutes = *in.Minutes
	}
	members := m.MemberIDs()
	if in.Members != nil {
		members = *in.Members
	}
	was := m
	id, err := s.Company.SaveMeeting(r.Context(), p.UserID, admin, m, members)
	if err != nil {
		return 0, err
	}
	if in.Protocol != nil {
		if err := s.Company.SetMeetingProtocol(r.Context(), p.UserID, admin, id, *in.Protocol); err != nil {
			return 0, err
		}
	}
	if in.Status != nil {
		if err := s.Company.SetMeetingStatus(r.Context(), p.UserID, admin, id, *in.Status); err != nil {
			return 0, err
		}
	}
	s.syncMeetingEvent(r.Context(), id, was)
	return id, nil
}

// POST /api/v1/meetings {"title", "start", "minutes" (60), "place", "description", "owner_id" (the key owner),
// "keeper_id", "members": [IDs], "prev_id"} — a new meeting, in the organizer's calendar with the people invited.
func (s *Server) apiMeetingCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in apiMeetingIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.apiMeetingSave(r, p, company.Meeting{Minutes: 60, PrevID: in.PrevID}, in)
	if err != nil {
		return err
	}
	s.Log.Info("api meeting created", "user_id", p.UserID, "id", id)
	return s.apiMeetingOut(w, r, p, id, http.StatusCreated)
}

// PATCH /api/v1/meetings/{id} — the fields given (as POST), "status" and "protocol" too.
func (s *Server) apiMeetingUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in apiMeetingIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	m, err := s.Company.Meeting(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), id)
	if err != nil {
		return err
	}
	if _, err := s.apiMeetingSave(r, p, m, in); err != nil {
		return err
	}
	return s.apiMeetingOut(w, r, p, id, http.StatusOK)
}

// DELETE /api/v1/meetings/{id} — with its agenda and its meeting in the calendar (the tasks stay).
func (s *Server) apiMeetingDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	m, err := s.Company.DeleteMeeting(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), id)
	if err != nil {
		return err
	}
	s.dropMeetingEvent(r.Context(), m)
	s.Log.Info("api meeting deleted", "user_id", p.UserID, "id", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// POST /api/v1/meetings/{id}/next {"start"} — the next meeting of the series (a week later by default).
func (s *Server) apiMeetingNext(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Start string `json:"start"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	var at time.Time
	if in.Start != "" {
		var err error
		if at, err = apiMeetingTime(in.Start); err != nil {
			return err
		}
	}
	nid, err := s.Company.NextMeeting(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), id, at)
	if err != nil {
		return err
	}
	s.syncMeetingEvent(r.Context(), nid, company.Meeting{})
	return s.apiMeetingOut(w, r, p, nid, http.StatusCreated)
}

type apiMeetingItemIn struct {
	ParentID   int64   `json:"parent_id"` // create only
	Title      *string `json:"title"`
	ReporterID *string `json:"reporter_id"`
	Minutes    *int    `json:"minutes"`
	Result     *string `json:"result"`
}

func (in apiMeetingItemIn) apply(i *company.MeetingItem) {
	if in.Title != nil {
		i.Title = *in.Title
	}
	if in.ReporterID != nil {
		i.Reporter.ID = *in.ReporterID
	}
	if in.Minutes != nil {
		i.Minutes = *in.Minutes
	}
	if in.Result != nil {
		i.Result = *in.Result
	}
}

// POST /api/v1/meetings/{id}/items {"title", "reporter_id", "minutes", "result", "parent_id" — a sub-item} — an
// agenda item at the end; answers the meeting.
func (s *Server) apiMeetingItemAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in apiMeetingItemIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	i := company.MeetingItem{ParentID: in.ParentID}
	in.apply(&i)
	if _, err := s.Company.AddMeetingItem(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), id, i); err != nil {
		return err
	}
	return s.apiMeetingOut(w, r, p, id, http.StatusCreated)
}

// apiMeetingItem finds the item of the path in its meeting.
func (s *Server) apiMeetingItem(r *http.Request, p apikeys.Principal) (company.MeetingItem, int64, error) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	iid, _ := strconv.ParseInt(r.PathValue("item"), 10, 64)
	m, err := s.Company.Meeting(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), id)
	if err != nil {
		return company.MeetingItem{}, id, err
	}
	for _, i := range m.Items {
		if i.ID == iid {
			return i, id, nil
		}
	}
	return company.MeetingItem{}, id, company.ErrNotFound
}

// PATCH /api/v1/meetings/{id}/items/{item} — the fields given; answers the meeting.
func (s *Server) apiMeetingItemUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	i, id, err := s.apiMeetingItem(r, p)
	if err != nil {
		return err
	}
	var in apiMeetingItemIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.apply(&i)
	if err := s.Company.UpdateMeetingItem(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), i); err != nil {
		return err
	}
	return s.apiMeetingOut(w, r, p, id, http.StatusOK)
}

// DELETE /api/v1/meetings/{id}/items/{item} — with its sub-items.
func (s *Server) apiMeetingItemDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	i, _, err := s.apiMeetingItem(r, p)
	if err != nil {
		return err
	}
	if err := s.Company.DeleteMeetingItem(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), i.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// POST /api/v1/meetings/{id}/items/{item}/tasks {"project": "KEY", "title", "description", "responsible_id",
// "deadline"} — a task set on the item; answers {"key"}.
func (s *Server) apiMeetingItemTask(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	i, _, err := s.apiMeetingItem(r, p)
	if err != nil {
		return err
	}
	var in struct {
		Project       string `json:"project"`
		Title         string `json:"title"`
		Description   string `json:"description"`
		ResponsibleID string `json:"responsible_id"`
		Deadline      string `json:"deadline"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	t := meetingTaskIn{Project: in.Project, Title: in.Title, Description: in.Description, Responsible: in.ResponsibleID}
	if t.Title == "" {
		t.Title = i.Title
	}
	if in.Deadline != "" {
		d, err := apiMeetingTime(in.Deadline)
		if err != nil {
			return err
		}
		t.Deadline = &d
	}
	key, err := s.meetingTask(r, p.UserID, s.apiAdmin(r.Context(), p.UserID), i.ID, t)
	if err != nil {
		return err
	}
	s.Log.Info("api meeting task", "user_id", p.UserID, "item", i.ID, "task", key)
	writeJSON(w, http.StatusCreated, map[string]string{"key": key})
	return nil
}
