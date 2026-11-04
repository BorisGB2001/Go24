package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «Контроль времени»: the pages tell the server the person is active (timecontrol.js), an absence
// is asked about in the top of the page, managers see the absences of their people in a report.

// workAlive: POST /worktime/alive — the person is active; {"idle": {…}} when an absence has just ended.
func (s *Server) workAlive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	if !sched.TimeControl {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	now := time.Now()
	idle, err := s.Company.Alive(r.Context(), sess.UserID, now, sched.IdleAfter(), sched.TimeControlSince)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.errText(r, err)})
		return
	}
	if idle == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	s.Log.Info("timecontrol idle", "user_id", sess.UserID, "secs", idle.Secs())
	writeJSON(w, http.StatusOK, map[string]any{"idle": map[string]any{
		"id": idle.ID, "from": idle.Started.Local().Format("15:04"), "to": idle.Ended.Local().Format("15:04"),
		"dur": formatDuration(func(k string, a ...any) string { return s.tr(r, k, a...) }, idle.Secs()), "ask": sched.IdleAsk,
	}})
}

// answerIdle: POST /worktime/idle/{id} — where the person was: kind work|personal and the reason.
func (s *Server) answerIdle(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/worktime"
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		err = i18n.E("err.bad_form")
	} else {
		err = s.Company.AnswerIdle(r.Context(), sess.UserID, id, r.FormValue("kind"), r.FormValue("reason"), time.Now())
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.errText(r, err)})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// timeControl: GET /worktime/timecontrol?month=2026-10&dept=ID — the absences of the people the user sees (as the
// «Таблица рабочего времени»: an administrator — everyone, a manager — their people, an employee — themself).
func (s *Server) timeControl(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	now := time.Now()
	month := sheetMonth(r, now)
	dept := r.URL.Query().Get("dept")
	people, err := s.sheetPeople(r.Context(), sess, dept)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	ids := make([]string, len(people))
	for i, p := range people {
		ids[i] = p.ID
	}
	idles, err := s.Company.Idles(r.Context(), ids, month, month.AddDate(0, 1, 0))
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	_, depts, err := s.Company.Tree(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	rows := company.IdleReport(people, idles)
	if r.URL.Query().Get("all") == "" { // only those with absences, as the report shows them
		kept := rows[:0]
		for _, row := range rows {
			if len(row.Idles) > 0 {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	s.render(w, r, "timecontrol", page{TitleKey: "tc.title", Nav: "timeman", Data: map[string]any{
		"Rows": rows, "Month": month, "Dept": dept, "Depts": depts, "All": r.URL.Query().Get("all") != "",
		"Prev": month.AddDate(0, -1, 0).Format("2006-01"), "Next": month.AddDate(0, 1, 0).Format("2006-01"),
		"Sched": sched, "Me": sess.UserID,
	}})
}

type apiIdleOut struct {
	ID       int64      `json:"id"`
	UserID   string     `json:"user_id"`
	Name     string     `json:"name"`
	Day      string     `json:"day"`
	Start    time.Time  `json:"start"`
	End      time.Time  `json:"end"`
	Secs     int        `json:"secs"`
	Kind     string     `json:"kind"` // "" — not answered, work, personal
	Reason   string     `json:"reason"`
	Answered *time.Time `json:"answered_at"`
}

// GET /api/v1/worktime/timecontrol?user_id=&from=&to= — the absences of «Контроль времени»
// (timeman.timecontrol.reports.get): own, or of a person below the key owner (anyone's — for administrators);
// without user_id — of everyone the key owner sees, as the report.
func (s *Server) apiIdles(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to, err := apiDates(r)
	if err != nil {
		return err
	}
	admin := s.apiAdmin(r.Context(), p.UserID)
	var ids []string
	switch uid := r.URL.Query().Get("user_id"); uid {
	case "":
		people, err := s.sheetPeople(r.Context(), auth.Session{UserID: p.UserID, Admin: admin}, "")
		if err != nil {
			return err
		}
		for _, x := range people {
			ids = append(ids, x.ID)
		}
	case "me", p.UserID:
		ids = []string{p.UserID}
	default:
		ok, err := s.Company.Sees(r.Context(), p.UserID, uid, admin)
		if err != nil {
			return err
		}
		if !ok {
			return i18n.E("err.forbidden")
		}
		ids = []string{uid}
	}
	idles, err := s.Company.Idles(r.Context(), ids, from, to.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	out := make([]apiIdleOut, 0, len(idles))
	for _, i := range idles {
		out = append(out, apiIdleOut{i.ID, i.UserID, i.Name, i.Day.Format(time.DateOnly), i.Started, i.Ended, i.Secs(), i.Kind,
			i.Reason, i.AnsweredAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/worktime/timecontrol/{id} {"kind": "work"|"personal", "reason"} — the key owner tells where they were.
func (s *Server) apiAnswerIdle(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return i18n.E("err.bad_form")
	}
	var in struct{ Kind, Reason string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Company.AnswerIdle(r.Context(), p.UserID, id, in.Kind, in.Reason, time.Now()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiTimeControlSettings struct {
	Active      bool       `json:"active"`
	IdleMinutes int        `json:"idle_minutes"`
	Ask         bool       `json:"ask"`
	Since       *time.Time `json:"since,omitempty"`
}

// GET /api/v1/worktime/timecontrol/settings — «Контроль времени» of «Настройки → Рабочее время»
// (timeman.timecontrol.settings.get).
func (s *Server) apiTimeControlSettings(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiTimeControlSettings{sched.TimeControl, int(sched.IdleAfter().Minutes()), sched.IdleAsk,
		sched.TimeControlSince})
	return nil
}

// PUT /api/v1/worktime/timecontrol/settings {"active", "idle_minutes", "ask"} — administrators and those with the
// settings of working time (timeman.timecontrol.settings.set).
func (s *Server) apiTimeControlSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if ok, err := s.Company.WorktimeSettings(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID)); err != nil || !ok {
		return i18n.E("err.forbidden")
	}
	var in apiTimeControlSettings
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		return err
	}
	sched.SetTimeControl(sched, in.Active, in.Ask, in.IdleMinutes, time.Now())
	if err := s.Tasks.SetSchedule(r.Context(), p.UserID, sched); err != nil {
		return err
	}
	s.Log.Info("api time control saved", "user_id", p.UserID, "active", in.Active)
	return s.apiTimeControlSettings(w, r, p)
}
