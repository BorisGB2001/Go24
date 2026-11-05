package web

import (
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
)

// «Вставить свободное время», as Outlook's «Share availability»: the editor's button (static/js/mailavail.js) shows
// the user's free time of the nearest working days — the working hours and days off of one's calendar settings less
// the meetings, absences and bookings — the spans not shorter than the length asked; the user unticks those not to
// offer and the block «Мне удобно в это время» with the spans by day and the time zone goes into the letter's text.
//
//	GET /api/v1/mail/availability?from=2006-01-02&days=5&length=30   {tz, days: [{date, spans: [{start, end}]}]}

// Limits of «Свободное время»: working days shown, the length of a span, minutes.
const (
	availMaxDays   = 15
	availMaxLength = 8 * 60
	availGrid      = 15 * time.Minute // a span starts and ends on a quarter of an hour
)

func (s *Server) mailAvailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mail/availability", s.requireUser(s.mailAvail))
	mux.HandleFunc("POST /mail/availability", s.requireUser(s.mailAvailBlock))
	mux.HandleFunc("GET /api/v1/mail/availability", s.api("mail:read", s.apiMailAvail))
}

// availDay is one working day and its free spans.
type availDay struct {
	Day  time.Time
	Free []calendar.Slot
}

// freeSpans are the free spans, at least length long, of the working hours of the next days working days from
// from (none before now): the calendar's settings tell the hours and days off, busy — the person's busy time.
func freeSpans(busy []calendar.Slot, set calendar.Settings, from, now time.Time, days int, length time.Duration) []availDay {
	sort.Slice(busy, func(a, b int) bool { return busy[a].Start.Before(busy[b].Start) })
	if t := now.Truncate(availGrid); t.Before(now) {
		now = t.Add(availGrid)
	}
	var out []availDay
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local)
	for n, tries := 0, 0; n < days && tries < days*3+14; tries, day = tries+1, day.AddDate(0, 0, 1) {
		if set.Off(day) {
			continue
		}
		n++
		t, end := day.Add(time.Duration(set.WorkFrom)*time.Hour), day.Add(time.Duration(set.WorkTo)*time.Hour)
		if t.Before(now) {
			t = now
		}
		d := availDay{Day: day}
		for _, b := range busy {
			if !b.End.After(t) || !b.Start.Before(end) {
				continue
			}
			if b.Start.Sub(t) >= length {
				d.Free = append(d.Free, calendar.Slot{Start: t, End: b.Start.Truncate(availGrid)})
			}
			t = b.End
			if r := t.Truncate(availGrid); r.Before(t) {
				t = r.Add(availGrid)
			}
		}
		if end.Sub(t) >= length {
			d.Free = append(d.Free, calendar.Slot{Start: t, End: end})
		}
		// a span cut down to the grid may come out shorter than asked
		d.Free = filterSpans(d.Free, length)
		if len(d.Free) > 0 {
			out = append(out, d)
		}
	}
	return out
}

func filterSpans(list []calendar.Slot, length time.Duration) []calendar.Slot {
	out := list[:0]
	for _, sl := range list {
		if sl.End.Sub(sl.Start) >= length {
			out = append(out, sl)
		}
	}
	return out
}

// availOf is the user's free time asked by the query: from (a date, today by default), days, length (minutes).
func (s *Server) availOf(r *http.Request, userID string) ([]availDay, error) {
	now := time.Now()
	from := now
	if v := r.FormValue("from"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			return nil, i18n.E("err.bad_form")
		}
		from = t
	}
	days := atoi(r.FormValue("days"))
	if days <= 0 {
		days = 5
	}
	days = min(days, availMaxDays)
	length := time.Duration(max(15, min(atoi(r.FormValue("length")), availMaxLength))) * time.Minute
	if r.FormValue("length") == "" {
		length = 30 * time.Minute
	}
	set, err := s.Calendar.Settings(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	begin := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local)
	people, err := s.Calendar.Busy(r.Context(), userID, []string{userID}, begin, begin.AddDate(0, 0, days*3+14), "")
	if err != nil {
		return nil, err
	}
	var busy []calendar.Slot
	for _, p := range people {
		busy = append(busy, p.Slots...)
	}
	return freeSpans(busy, set, from, now, days, length), nil
}

// availJSON is the free time as the editor and REST give it: dates and "15:04" times of the portal's time zone.
func availJSON(list []availDay) map[string]any {
	type span struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	type day struct {
		Date  string `json:"date"`
		Spans []span `json:"spans"`
	}
	out := []day{}
	for _, d := range list {
		x := day{Date: d.Day.Format("2006-01-02"), Spans: []span{}}
		for _, sl := range d.Free {
			x.Spans = append(x.Spans, span{sl.Start.In(time.Local).Format("15:04"), sl.End.In(time.Local).Format("15:04")})
		}
		out = append(out, x)
	}
	return map[string]any{"tz": time.Local.String(), "days": out}
}

// mailAvail is the editor's free time; each day has its label («чт, 05.11»).
func (s *Server) mailAvail(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.availOf(r, sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := availJSON(list)
	lang := s.lang(r)
	labels := []string{}
	for _, d := range list {
		labels = append(labels, s.availDayLabel(lang, d.Day))
	}
	out["labels"] = labels
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) availDayLabel(lang string, day time.Time) string {
	return s.I18n.T(lang, "wd."+strconv.Itoa(calendar.Weekday(day))) + ", " + day.Format("02.01.2006")
}

// mailAvailBlock is the block of the spans chosen (span=2006-01-02T15:04/15:04…) for the letter's text.
func (s *Server) mailAvailBlock(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	var spans []calendar.Slot
	for _, v := range r.Form["span"] {
		a, b, ok := strings.Cut(v, "/")
		st, err1 := time.ParseInLocation("2006-01-02T15:04", a, time.Local)
		en, err2 := time.ParseInLocation("15:04", b, time.Local)
		if !ok || err1 != nil || err2 != nil {
			s.jsonErr(w, r, i18n.E("err.bad_form"))
			return
		}
		en = time.Date(st.Year(), st.Month(), st.Day(), en.Hour(), en.Minute(), 0, 0, time.Local)
		if !en.After(st) {
			s.jsonErr(w, r, i18n.E("err.bad_form"))
			return
		}
		spans = append(spans, calendar.Slot{Start: st, End: en})
	}
	if len(spans) == 0 {
		s.jsonErr(w, r, i18n.E("err.mail_avail_none"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": s.availBlock(s.lang(r), spans)})
}

// availBlock is the block of the letter's text: the head, the spans of each day on a line, the time zone.
func (s *Server) availBlock(lang string, spans []calendar.Slot) string {
	sort.Slice(spans, func(a, b int) bool { return spans[a].Start.Before(spans[b].Start) })
	esc := template.HTMLEscapeString
	var b strings.Builder
	b.WriteString(`<div style="border:1px solid #c5cfdb;border-radius:8px;padding:12px 16px;margin:12px 0">`)
	b.WriteString(`<p style="margin:0 0 6px"><b>🕒 ` + esc(s.I18n.T(lang, "mail.avail.block_head")) + `</b></p><ul style="margin:0 0 8px">`)
	for i := 0; i < len(spans); {
		day := spans[i].Start
		var times []string
		for ; i < len(spans) && sameDay(spans[i].Start, day); i++ {
			times = append(times, spans[i].Start.Format("15:04")+"–"+spans[i].End.Format("15:04"))
		}
		b.WriteString(`<li><b>` + esc(s.availDayLabel(lang, day)) + `</b>: ` + esc(strings.Join(times, ", ")) + `</li>`)
	}
	b.WriteString(`</ul><p style="margin:0;color:#6b7785;font-size:90%">` + esc(s.I18n.T(lang, "mail.avail.block_tz", time.Local.String())) + `</p></div><p></p>`)
	return b.String()
}

func (s *Server) apiMailAvail(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.availOf(r, p.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, availJSON(list))
	return nil
}
