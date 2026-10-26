package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/crm"
)

// ---------- «Онлайн-запись» ----------

const bookingHourPx = 48

// bookingCol is a resource in the day's grid: its working hours and its bookings, placed by minutes.
type bookingCol struct {
	R        booking.Resource
	Off      bool // the resource does not work that day
	OffTop   int  // the hours before its work (px)
	OffBot   int  // the top of the hours after its work (px)
	Load     int  // percent of its working time booked that day
	Bookings []bookingItem
}

type bookingItem struct {
	B                 booking.Booking
	Top, Height, Mins int
	Late              bool // its time and LateMinutes have passed, the client has not come
	Extra             bool // the column is one of the booking's other resources
}

// bookingPage: «Онлайн-запись» — the resources of the day (?date=, ?type=) side by side on an hour grid with their
// bookings (one of more resources is in each of their columns); a click on a free time is a new booking, on a
// booking — its card; above the grid — the waiting list, its clients are dragged onto a free time.
func (s *Server) bookingPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if d, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), time.Local); err == nil {
		day = d
	}
	typ := r.URL.Query().Get("type")
	types, err := s.Booking.Types(r.Context())
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	all, err := s.Booking.Resources(r.Context(), "")
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	res := all
	if typ != "" {
		res = nil
		for _, x := range all {
			if x.TypeID == typ {
				res = append(res, x)
			}
		}
	}
	wait, err := s.Booking.Waitlist(r.Context())
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	list, err := s.Booking.Bookings(r.Context(), day, day.AddDate(0, 0, 1))
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	// the grid spans the working hours of the resources that work that day (9–18 when none does)
	h0, h1 := 24, 0
	for _, x := range res {
		if x.WorksOn(day) {
			h0, h1 = min(h0, x.From/60), max(h1, (x.To+59)/60)
		}
	}
	if h0 >= h1 {
		h0, h1 = 9, 18
	}
	px := func(t time.Time) int {
		m := int(t.Sub(day).Minutes()) - h0*60
		return max(0, min(m, (h1-h0)*60)) * bookingHourPx / 60
	}
	counts, counted := map[string]int{}, map[string]bool{}
	cols := make([]bookingCol, 0, len(res))
	for _, x := range res {
		c := bookingCol{R: x, Off: !x.WorksOn(day),
			OffTop: max(0, x.From-h0*60) * bookingHourPx / 60, OffBot: max(0, x.To-h0*60) * bookingHourPx / 60}
		if work, busy := booking.Load(x, list, day, day.AddDate(0, 0, 1), time.Local); work > 0 {
			c.Load = (busy*100 + work/2) / work
		}
		for _, b := range list {
			if b.Takes(x.ID) {
				top := px(b.Start)
				late := x.LateMinutes > 0 && (b.Status == "unconfirmed" || b.Status == "confirmed") &&
					now.After(b.Start.Add(time.Duration(x.LateMinutes)*time.Minute))
				c.Bookings = append(c.Bookings, bookingItem{b, top, max(18, px(b.End)-top), int(b.End.Sub(b.Start).Minutes()), late, b.ResourceID != x.ID})
				if !counted[b.ID] {
					counted[b.ID] = true
					counts[b.Status]++
					if late {
						counts["late"]++
					}
				}
			}
		}
		cols = append(cols, c)
	}
	var hours []int
	for h := h0; h < h1; h++ {
		hours = append(hours, h)
	}
	s.render(w, r, "booking", page{TitleKey: "booking.title", Nav: "booking", Data: map[string]any{
		"Day": day, "Today": day.Equal(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)),
		"Prev": day.AddDate(0, 0, -1).Format("2006-01-02"), "Next": day.AddDate(0, 0, 1).Format("2006-01-02"),
		"Types": types, "Type": typ, "Cols": cols, "All": all, "Wait": wait, "Hours": hours, "Hour0": h0, "HourPx": bookingHourPx,
		"GridPx": (h1 - h0) * bookingHourPx, "Statuses": booking.Statuses, "Counts": counts, "Admin": sess.Admin,
		"Open": r.URL.Query().Get("b"), "Preset": s.bookingCRMPreset(r, sess), "CRM": s.CRM != nil}})
}

// bookingSave adds a booking or changes one (id): resource, the other resources (extra), the services (svc), date,
// time, minutes, the client, a comment, a status; wait — the waiting list entry the client is booked from.
func (s *Server) bookingSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	date := r.FormValue("date")
	back := "/booking?date=" + url.QueryEscape(date)
	start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+r.FormValue("time"), time.Local)
	mins, _ := strconv.Atoi(r.FormValue("minutes"))
	if err != nil || mins <= 0 {
		s.taskErr(w, r, booking.ErrTime, back)
		return
	}
	b := booking.Booking{ID: r.PathValue("id"), ResourceID: r.FormValue("resource"), Start: start,
		End: start.Add(time.Duration(mins) * time.Minute), ClientName: r.FormValue("client_name"),
		ClientPhone: r.FormValue("client_phone"), ClientEmail: r.FormValue("client_email"),
		Note: strings.TrimSpace(r.FormValue("note")), Status: r.FormValue("status"), Extras: r.Form["extra"]}
	var old booking.Booking
	if b.ID != "" {
		if old, err = s.Booking.Booking(r.Context(), b.ID); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
	}
	b.Services = booking.Keep(r.Form["svc"], old.Services)
	if err = s.bookingCRM(r, sess, &b); err == nil {
		b.ID, err = s.Booking.SaveBooking(r.Context(), sess.UserID, b)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.bookingEvents(r.Context(), sess.UserID, old, b)
	if id := r.FormValue("wait"); id != "" && old.ID == "" { // «Записать» from the waiting list: the client is off it
		if err := s.Booking.DeleteWait(r.Context(), id); err != nil && !errors.Is(err, booking.ErrNotFound) {
			s.Log.Warn("booking waitlist", "err", err)
		}
	}
	http.Redirect(w, r, bookingCardBack(r, back), http.StatusSeeOther)
}

// bookingStatus: «Подтвердить», «Клиент пришёл», «Не пришёл», «Отменить» (status=…).
func (s *Server) bookingStatus(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	b, err := s.Booking.Booking(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	back := "/booking?date=" + b.Start.Local().Format("2006-01-02")
	if err := s.bookingSetStatus(r.Context(), sess.UserID, b.ID, r.FormValue("status")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) bookingDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	b, err := s.Booking.Booking(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	if err := s.Booking.DeleteBooking(r.Context(), b.ID); err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	http.Redirect(w, r, "/booking?date="+b.Start.Local().Format("2006-01-02"), http.StatusSeeOther)
}

// bookingResources: the resources and their types; administrators add and change them (?edit=id, ?edit=new).
func (s *Server) bookingResources(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	types, err := s.Booking.Types(r.Context())
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	res, err := s.Booking.Resources(r.Context(), "")
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	data := map[string]any{"Types": types, "Resources": res, "Admin": sess.Admin, "Days": []int{1, 2, 3, 4, 5, 6, 7}}
	if e := r.URL.Query().Get("edit"); e != "" && sess.Admin {
		ed := booking.Resource{Slot: 30, Days: []int{1, 2, 3, 4, 5}, From: 9 * 60, To: 18 * 60, NotifyMail: true, RemindHours: 24, LateMinutes: 15}
		if len(types) > 0 {
			ed.TypeID = types[0].ID
		}
		if e != "new" {
			if ed, err = s.Booking.Resource(r.Context(), e); err != nil {
				s.taskErr(w, r, err, "/booking/resources")
				return
			}
		}
		users, err := s.Tasks.ListUsers(r.Context())
		if err != nil {
			s.fail(w, "booking", err)
			return
		}
		data["Edit"], data["Users"] = ed, users
		if s.CRM != nil { // «Услуги»: the catalog's services, those of the resource first with their minutes
			cat, err := s.CRM.Catalog(r.Context(), "", "", crm.KindService, false)
			if err != nil {
				s.fail(w, "booking", err)
				return
			}
			var svcs []bookingSvc
			for _, x := range ed.Services {
				svcs = append(svcs, bookingSvc{x, true})
			}
			for _, p := range cat {
				if _, ok := ed.Service(p.ID); !ok {
					svcs = append(svcs, bookingSvc{booking.Service{ProductID: p.ID, Name: p.Name, Price: p.Price, Currency: p.Currency, Minutes: ed.Slot}, false})
				}
			}
			data["Services"] = svcs
		}
	}
	s.render(w, r, "bookingres", page{TitleKey: "booking.resources", Nav: "booking", Data: data})
}

// bookingSum: the total of booking services with its currency, "1 500.00 RUB".
func bookingSum(list []booking.Service) string {
	sum, cur := booking.Total(list)
	return strings.TrimSpace(crm.Amount(sum) + " " + cur)
}

// bookingSvc is a service of the catalog in a resource's form: On — the resource provides it.
type bookingSvc struct {
	booking.Service
	On bool
}

// bookingResourceSave: a resource from its form (administrators); the hours come as HH:MM; the services — svc (their
// products, in order) with svc_min_<product> minutes each.
func (s *Server) bookingResourceSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/booking/resources")
		return
	}
	hm := func(v string) int {
		var h, m int
		if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil {
			return -1
		}
		return h*60 + m
	}
	slot, _ := strconv.Atoi(r.FormValue("slot"))
	remind, _ := strconv.Atoi(r.FormValue("remind_hours"))
	late, _ := strconv.Atoi(r.FormValue("late_minutes"))
	x := booking.Resource{ID: r.PathValue("id"), TypeID: r.FormValue("type"), Name: r.FormValue("name"),
		Description: strings.TrimSpace(r.FormValue("description")), UserID: r.FormValue("user"), Slot: slot,
		From: hm(r.FormValue("from")), To: hm(r.FormValue("to")), NotifyMail: r.FormValue("notify_mail") == "1",
		RemindHours: remind, LateMinutes: late, Overbooking: r.FormValue("overbooking") == "1"}
	if x.To == 0 && r.FormValue("to") != "" { // «до 24:00» typed as 00:00
		x.To = 24 * 60
	}
	for _, id := range r.Form["svc"] {
		m, _ := strconv.Atoi(r.FormValue("svc_min_" + id))
		x.Services = append(x.Services, booking.Service{ProductID: id, Minutes: m})
	}
	for _, d := range r.Form["days"] {
		if n, err := strconv.Atoi(d); err == nil {
			x.Days = append(x.Days, n)
		}
	}
	back := "/booking/resources?edit=" + url.QueryEscape(map[bool]string{true: "new", false: x.ID}[x.ID == ""])
	if _, err := s.Booking.SaveResource(r.Context(), sess.UserID, x); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/booking/resources?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

func (s *Server) bookingResourceDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Booking.DeleteResource(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/booking/resources")
		return
	}
	http.Redirect(w, r, "/booking/resources", http.StatusSeeOther)
}

// bookingTypeSave adds a resource type of its own or renames one; bookingTypeDelete removes an unused one.
func (s *Server) bookingTypeSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if _, err := s.Booking.SaveType(r.Context(), r.PathValue("id"), r.FormValue("name")); err != nil {
		s.taskErr(w, r, err, "/booking/resources")
		return
	}
	http.Redirect(w, r, "/booking/resources", http.StatusSeeOther)
}

func (s *Server) bookingTypeDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Booking.DeleteType(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/booking/resources")
		return
	}
	http.Redirect(w, r, "/booking/resources", http.StatusSeeOther)
}
