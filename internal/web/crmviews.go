package web

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/worktime"
)

// ---------- the views «Дела» and «Календарь» of deals, leads and smart processes ----------

// crmViews are the views of the deal and lead lists in the order of the switch.
var crmViews = []string{"kanban", "list", "activities", "calendar"}

// crmCard is a deal or a lead of the views: its tile and the dates the calendar may show.
type crmCard struct {
	ID      string
	Item    any // crm.Deal or crm.Lead, for the template crmkcard
	Sum     float64
	Created time.Time
	Close   *time.Time // a deal's «Дата завершения»
}

// crmDeedCol is a column of «Дела»: the cards whose nearest open deed is due in the period («none» — without deeds).
type crmDeedCol struct {
	Period string
	Items  []crmCard
	Sum    float64
}

// crmDay is a day of the calendar's month.
type crmDay struct {
	Date        time.Time
	Out, Today bool // a day of the next or previous month; today
	Items      []crmCard
}

// crmCardsOf are the listed deals or leads as cards (the sums in the base currency).
func crmCardsOf(items any, sums map[string]float64) []crmCard {
	var out []crmCard
	switch list := items.(type) {
	case []crm.Deal:
		for _, d := range list {
			out = append(out, crmCard{ID: d.ID, Item: d, Sum: sums[d.ID], Created: d.CreatedAt, Close: d.CloseDate})
		}
	case []crm.Lead:
		for _, l := range list {
			out = append(out, crmCard{ID: l.ID, Item: l, Sum: sums[l.ID], Created: l.CreatedAt})
		}
	}
	return out
}

// crmDeedColumns lays the cards out by the deadline of their nearest open deed, the nearest first.
func crmDeedColumns(cards []crmCard, next map[string]*crm.Activity, now time.Time) []crmDeedCol {
	cols := make([]crmDeedCol, len(tasks.DeadlinePeriods))
	index := map[string]int{}
	for i, p := range tasks.DeadlinePeriods {
		cols[i].Period, index[p] = p, i
	}
	for _, c := range cards {
		var d *time.Time
		if a := next[c.ID]; a != nil {
			d = &a.Deadline
		}
		i := index[tasks.DeadlinePeriod(d, now)]
		cols[i].Items, cols[i].Sum = append(cols[i].Items, c), cols[i].Sum+c.Sum
	}
	for _, col := range cols {
		slices.SortStableFunc(col.Items, func(a, b crmCard) int {
			if next[a.ID] == nil || next[b.ID] == nil {
				return 0
			}
			return next[a.ID].Deadline.Compare(next[b.ID].Deadline)
		})
	}
	return cols
}

// crmDateField is a date the calendar shows the cards by: "created", "close" (deals) or "f:"+an own date field.
type crmDateField struct {
	Key, Name string // Name — of an own field; the built-in ones are "crm.f."+Key
	Movable   bool   // dragging a card to a day changes it
}

// crmMonthStart is the first day of the month of ?month=YYYY-MM (this month by default).
func crmMonthStart(v string, now time.Time) time.Time {
	if t, err := time.ParseInLocation("2006-01", v, now.Location()); err == nil {
		return t
	}
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
}

// crmMonth is the weeks (Monday first) of the month starting at first with the cards on their days; day says the
// date of a card ("" — none).
func crmMonth(first time.Time, cards []crmCard, day func(crmCard) string, now time.Time) [][]crmDay {
	start := first.AddDate(0, 0, -((int(first.Weekday()) + 6) % 7))
	byDay := map[string][]crmCard{}
	for _, c := range cards {
		if d := day(c); d != "" {
			byDay[d] = append(byDay[d], c)
		}
	}
	today := now.Format(time.DateOnly)
	var weeks [][]crmDay
	for d := start; len(weeks) == 0 || d.Month() == first.Month(); {
		week := make([]crmDay, 7)
		for i := range week {
			key := d.Format(time.DateOnly)
			week[i] = crmDay{Date: d, Out: d.Month() != first.Month(), Today: key == today, Items: byDay[key]}
			d = d.AddDate(0, 0, 1)
		}
		weeks = append(weeks, week)
	}
	return weeks
}

// crmViewData fills in the data of «Дела» or «Календарь» for crmList; ids are the listed cards.
func (s *Server) crmViewData(r *http.Request, data map[string]any, kind, view string, ids []string) error {
	cards := crmCardsOf(data["Items"], data["Sums"].(map[string]float64))
	now := time.Now()
	if view == "activities" {
		next, _ := data["Next"].(map[string]*crm.Activity)
		data["DeedCols"] = crmDeedColumns(cards, next, now)
		return nil
	}
	entity := crmEntities[kind]
	fields, err := s.crmFieldsOf(r, tasks.CRMFieldSets[entity])
	if err != nil {
		return err
	}
	dates := []crmDateField{{Key: "created"}}
	if entity == "deal" {
		dates = append(dates, crmDateField{Key: "close", Movable: true})
	}
	for _, f := range fields {
		if f.Kind == "date" {
			dates = append(dates, crmDateField{Key: "f:" + f.ID, Name: f.Name, Movable: true})
		}
	}
	cur := dates[0]
	if entity == "deal" {
		cur = dates[1] //: deals by their closing date
	}
	for _, d := range dates {
		if d.Key == r.URL.Query().Get("date") {
			cur = d
		}
	}
	var vals map[string]map[string]string
	if strings.HasPrefix(cur.Key, "f:") {
		if vals, err = s.Tasks.ValuesOf(r.Context(), tasks.CRMFieldSets[entity], ids); err != nil {
			return err
		}
	}
	day := func(c crmCard) string {
		switch {
		case cur.Key == "created":
			return c.Created.In(now.Location()).Format(time.DateOnly)
		case cur.Key == "close":
			if c.Close == nil {
				return ""
			}
			return c.Close.Format(time.DateOnly)
		}
		return vals[c.ID][strings.TrimPrefix(cur.Key, "f:")]
	}
	first := crmMonthStart(r.URL.Query().Get("month"), now)
	undated := 0
	for _, c := range cards {
		if day(c) == "" {
			undated++
		}
	}
	data["Dates"], data["Date"], data["Weeks"], data["Month"] = dates, cur, crmMonth(first, cards, day, now), first
	data["Prev"], data["NextMonth"], data["Undated"] = first.AddDate(0, -1, 0).Format("2006-01"), first.AddDate(0, 1, 0).Format("2006-01"), undated
	return nil
}

// crmDeedPeriod is a card dropped into a column of «Дела»: its nearest open deed gets the deadline of the period
// (as «Сроки» of tasks); a card without deeds gets a new one. JSON {card: the tile} or an error.
func (s *Server) crmDeedPeriod(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	entity, period := crmEntities[kind], r.FormValue("period")
	err := s.crmCheck(r, sess, kind, id, "write")
	if err == nil && entity != "deal" && entity != "lead" {
		err = i18n.E("err.bad_form")
	}
	var next map[string]*crm.Activity
	if err == nil {
		next, err = s.CRM.NextActivities(r.Context(), entity, []string{id})
	}
	var sched worktime.Schedule
	if err == nil {
		sched, err = s.Tasks.Schedule(r.Context())
	}
	var deadline *time.Time
	if err == nil && period == "none" {
		err = i18n.E("err.crm_deed_none")
	}
	if err == nil {
		deadline, err = tasks.PeriodDeadline(period, time.Now(), sched)
	}
	if err == nil {
		if a := next[id]; a != nil {
			a.Deadline = *deadline
			err = s.CRM.UpdateActivity(r.Context(), *a, false)
		} else {
			_, err = s.CRM.AddActivity(r.Context(), sess.UserID, crm.Activity{Entity: entity, EntityID: id, Kind: "todo",
				Subject: s.tr(r, "crm.deed_default"), Deadline: *deadline, Responsible: crm.Ref{ID: sess.UserID}})
		}
	}
	var card string
	if err == nil {
		card, err = s.crmCardHTML(r, sess, kind, id)
	}
	if err != nil {
		s.crmJSONErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"card": card})
}

// crmDateMove is a card dropped onto a day of «Календарь»: the date field shown gets the day (204 or an error).
func (s *Server) crmDateMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	entity, field := crmEntities[kind], r.FormValue("date")
	err := s.crmCheck(r, sess, kind, id, "write")
	day, perr := time.ParseInLocation(time.DateOnly, r.FormValue("day"), time.Local)
	if err == nil && (perr != nil || (entity != "deal" && entity != "lead")) {
		err = i18n.E("err.bad_form")
	}
	var before map[string]string
	if err == nil {
		before, err = s.CRM.CardSnapshot(r.Context(), entity, id)
	}
	switch {
	case err != nil:
	case field == "close" && entity == "deal":
		var d crm.Deal
		if d, err = s.CRM.Deal(r.Context(), id); err == nil {
			d.CloseDate = &day
			_, err = s.CRM.SaveDeal(r.Context(), sess.UserID, d)
		}
	case strings.HasPrefix(field, "f:"):
		var fields []tasks.Field
		if fields, err = s.crmFieldsOf(r, tasks.CRMFieldSets[entity]); err == nil {
			fid := strings.TrimPrefix(field, "f:")
			err = i18n.E("err.bad_form")
			for _, f := range fields {
				if f.ID == fid && f.Kind == "date" {
					err = s.CRM.SetFields(r.Context(), sess.UserID, entity, id, map[string]string{fid: day.Format(time.DateOnly)})
				}
			}
		}
	default:
		err = i18n.E("err.bad_form")
	}
	if err == nil {
		if err := s.CRM.FireChanged(r.Context(), entity, id, before); err != nil { // the date is saved anyway
			s.Log.Error("crm field trigger", "err", err, "card", id)
		}
		if err := s.CRM.CheckInfoRequests(r.Context(), sess.UserID, entity, id); err != nil {
			s.Log.Error("crm info request", "err", err, "card", id)
		}
	}
	if err != nil {
		s.crmJSONErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
