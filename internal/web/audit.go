package web

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
)

// audit writes an event of «Журнал событий» with the request's address and browser; a failure is only logged —
// the action itself is done.
func (s *Server) audit(r *http.Request, e audit.Event) {
	if s.Audit == nil {
		return
	}
	if err := s.Audit.Add(r.Context(), audit.From(r, e)); err != nil {
		s.Log.Warn("audit", "kind", e.Kind, "err", err)
	}
}

// auditFilter reads ?kind=&user=&from=&to=&before= of the page and the API.
func auditFilter(q url.Values) audit.Filter {
	f := audit.Filter{Kind: q.Get("kind"), UserID: q.Get("user")}
	f.From, _ = time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local)
	f.To, _ = time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local)
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	return f
}

// auditPage is «Журнал событий» (administrators): the events by the filter, a hundred a page («Ещё» — older ones),
// ?format=csv — the whole selection for Excel; POST sets how long the log is kept.
func (s *Server) auditPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Audit == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		days, _ := strconv.Atoi(r.FormValue("days"))
		if err := s.Audit.SetDays(r.Context(), days, sess.UserID); err != nil {
			s.taskErr(w, r, err, "/settings/events")
			return
		}
		s.audit(r, audit.Event{Kind: "audit_days", UserID: sess.UserID, Target: strconv.Itoa(days)})
		http.Redirect(w, r, "/settings/events?msg="+url.QueryEscape(s.tr(r, "audit.days_saved")), http.StatusSeeOther)
		return
	}
	q := r.URL.Query()
	f := auditFilter(q)
	csvOut := q.Get("format") == "csv"
	if csvOut {
		f.Limit = 1000
	}
	list, err := s.Audit.List(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/settings")
		return
	}
	if csvOut {
		s.auditCSV(w, r, list)
		return
	}
	days, err := s.Audit.Days(r.Context())
	if err != nil {
		s.fail(w, "audit days", err)
		return
	}
	more := ""
	if len(list) == 100 && f.Limit <= 0 {
		v := url.Values{}
		for k, x := range q {
			v[k] = x
		}
		v.Set("before", strconv.FormatInt(list[len(list)-1].ID, 10))
		more = "/settings/events?" + v.Encode()
	}
	csvQ := url.Values{}
	for k, x := range q {
		if k != "before" {
			csvQ[k] = x
		}
	}
	csvQ.Set("format", "csv")
	s.render(w, r, "audit", page{TitleKey: "audit.title", Nav: "audit", Flash: q.Get("msg"), Data: map[string]any{
		"Events": list, "Filter": f, "Kinds": audit.Kinds, "Days": days, "DaysChoice": audit.DaysChoice,
		"More": more, "CSV": "/settings/events?" + csvQ.Encode(),
		"Filtered": f.Kind != "" || f.UserID != "" || !f.From.IsZero() || !f.To.IsZero()}})
}

// auditCSV: the events for Excel.
func (s *Server) auditCSV(w http.ResponseWriter, r *http.Request, list []audit.Event) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=events-"+time.Now().Format(time.DateOnly)+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	head := []string{"audit.col.at", "audit.col.event", "audit.col.user", "audit.col.target", "audit.col.ip", "audit.col.agent"}
	for i, k := range head {
		head[i] = s.tr(r, k)
	}
	_ = cw.Write(head)
	for _, e := range list {
		_ = cw.Write([]string{e.At.In(time.Local).Format("02.01.2006 15:04:05"), s.tr(r, "audit.kind."+e.Kind), e.Name, e.Target, e.IP, e.Agent})
	}
	cw.Flush()
}

// apiAudit: GET /api/v1/settings/events — the log for administrators' keys, ?kind=&user=&from=&to=&before=&limit=.
func (s *Server) apiAudit(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Audit == nil || !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	list, err := s.Audit.List(r.Context(), auditFilter(r.URL.Query()))
	if err != nil {
		return err
	}
	if list == nil {
		list = []audit.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": list})
	return nil
}
