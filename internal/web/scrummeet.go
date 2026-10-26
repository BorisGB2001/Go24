package web

import (
	"net/http"
	"slices"
	"time"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/tasks"
)

// ---------- «Мероприятия» of a scrum team: its meetings in the project's calendar ----------

// scrumMeeting is a template of a scrum meeting: «Ежедневный митинг», «Планирование спринта», «Обзор спринта»,
// «Ретроспектива» — its length, and the hour it is put at (on the day the active sprint ends for the review and
// the retrospective, on the next one for the planning).
type scrumMeeting struct {
	Kind    string
	Minutes int
	Hour    int
}

var scrumMeetings = []scrumMeeting{{"daily", 15, 10}, {"planning", 120, 10}, {"review", 60, 15}, {"retro", 60, 16}}

// scrumPrefill fills a new meeting of the project's calendar after the template kind (?scrum=): its title and
// agenda, the team invited, a video call, and for the daily one a repetition every day till the active sprint ends.
func (s *Server) scrumPrefill(r *http.Request, sess auth.Session, p tasks.Project, kind string, e *calendar.Event) {
	i := slices.IndexFunc(scrumMeetings, func(m scrumMeeting) bool { return m.Kind == kind })
	if i < 0 || !p.Scrum() {
		return
	}
	m := scrumMeetings[i]
	e.Title, e.Description = s.tr(r, "scrum.meet."+kind), s.tr(r, "scrum.meet."+kind+"_text")
	e.Video = true
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if now.Hour() >= m.Hour {
		day = day.AddDate(0, 0, 1)
	}
	sp, _ := s.Tasks.ActiveSprint(r.Context(), p.ID)
	if sp != nil && sp.DateEnd != nil && kind != "daily" {
		end := sp.DateEnd.Local()
		end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.Local)
		if kind == "planning" {
			end = end.AddDate(0, 0, 1)
		}
		if !end.Before(day) {
			day = end
		}
	}
	e.Start = day.Add(time.Duration(m.Hour) * time.Hour)
	e.End = e.Start.Add(time.Duration(m.Minutes) * time.Minute)
	if kind == "daily" {
		e.Repeat, e.Every = "daily", 1
		if sp != nil && sp.DateEnd != nil && !sp.DateEnd.Before(e.Start) {
			u := *sp.DateEnd
			e.Until = &u
		}
	}
	members, err := s.Tasks.Members(r.Context(), p.ID)
	if err != nil {
		return
	}
	e.Attendees = nil
	for _, mb := range members {
		if mb.ID != sess.UserID {
			e.Attendees = append(e.Attendees, calendar.Attendee{ID: mb.ID, Name: mb.Name})
		}
	}
}

// scrumUpcoming is the block «Мероприятия» of the planning: the templates and the next meetings of the project's
// calendar within two weeks.
func (s *Server) scrumUpcoming(r *http.Request, sess auth.Session, p tasks.Project) map[string]any {
	out := map[string]any{"Kinds": scrumMeetings}
	if s.Calendar == nil {
		return out
	}
	now := time.Now()
	list, err := s.Calendar.ProjectRange(r.Context(), sess.UserID, p.ID, now, now.AddDate(0, 0, 14))
	if err != nil {
		return out
	}
	var next []scrumEvent
	seen := map[string]bool{} // a repeated meeting (the daily one) shows its next occurrence only
	for _, e := range list {
		if e.Kind == "event" && !e.Hidden && e.End.After(now) && len(next) < 5 && !seen[e.ID] {
			seen[e.ID] = true
			next = append(next, scrumEvent{Event: e, Live: e.Start.Add(-15 * time.Minute).Before(now)})
		}
	}
	out["Next"] = next
	return out
}

// scrumEvent is a meeting of the block; Live: it goes on or begins within a quarter of an hour.
type scrumEvent struct {
	calendar.Event
	Live bool
}
