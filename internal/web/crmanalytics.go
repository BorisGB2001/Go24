package web

import (
	"net/http"
	"time"

	"go24/internal/auth"
)

// crmAnalytics is the «Аналитика» tab: the sales funnel, the closed deals, the managers and the leads of a pipeline
// over a period (this month by default), counting the cards the person may read.
func (s *Server) crmAnalytics(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 1, 0)
	if t, err := time.ParseInLocation("2006-01-02", q.Get("from"), time.Local); err == nil {
		from = t
	}
	if t, err := time.ParseInLocation("2006-01-02", q.Get("to"), time.Local); err == nil {
		to = t.AddDate(0, 0, 1)
	}
	pipes, err := s.CRM.Pipelines(r.Context())
	if err != nil || len(pipes) == 0 {
		s.taskErr(w, r, err, "/crm")
		return
	}
	cur := pipes[0]
	for _, p := range pipes {
		if p.ID == q.Get("pipeline") {
			cur = p
		}
	}
	rep, err := s.CRM.Analytics(r.Context(), sess.UserID, cur.ID, from, to)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	last := to.AddDate(0, 0, -1)
	data := map[string]any{"Kind": "analytics", "Report": rep, "Pipelines": pipes, "Pipeline": cur, "From": &from, "To": &last}
	if err = s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmanalytics", page{TitleKey: "crm.analytics", Nav: "crm_analytics", Data: data})
}

// share is a part of a whole in percent (for the bars).
func share(part, whole int) int {
	if whole <= 0 {
		return 0
	}
	return part * 100 / whole
}
