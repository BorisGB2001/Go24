package web

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «План продаж» of the CRM (/crm/plan): everyone sees the plans and their progress, administrators set them ----------

// planQuery reads the pipeline, the period's kind and a day of it (today by default) of the request.
func (s *Server) planQuery(r *http.Request) (crm.Pipeline, []crm.Pipeline, string, time.Time, error) {
	pipes, err := s.CRM.Pipelines(r.Context())
	if err != nil || len(pipes) == 0 {
		return crm.Pipeline{}, nil, "", time.Time{}, err
	}
	cur := pipes[0]
	for _, p := range pipes {
		if p.ID == r.FormValue("pipeline") {
			cur = p
		}
	}
	period := r.FormValue("period")
	if _, ok := crm.PlanStart(period, time.Now()); !ok {
		period = "month"
	}
	day := time.Now()
	if t, err := time.ParseInLocation(time.DateOnly, r.FormValue("start"), time.Local); err == nil {
		day = t
	}
	return cur, pipes, period, day, nil
}

// planLabel names a period: «Октябрь 2026», «IV квартал 2026», «II полугодие 2026», «2026 год».
func (s *Server) planLabel(r *http.Request, period string, start time.Time) string {
	roman := []string{"I", "II", "III", "IV"}
	switch period {
	case "quarter":
		return s.tr(r, "crm.plan.label.quarter", roman[(int(start.Month())-1)/3], start.Year())
	case "half":
		return s.tr(r, "crm.plan.label.half", roman[(int(start.Month())-1)/6], start.Year())
	case "year":
		return s.tr(r, "crm.plan.label.year", start.Year())
	}
	return s.tr(r, "crm.plan.label.month", s.tr(r, fmt.Sprintf("month_full.%d", start.Month())), start.Year())
}

func (s *Server) crmPlan(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cur, pipes, period, day, err := s.planQuery(r)
	if err != nil || len(pipes) == 0 {
		s.taskErr(w, r, err, "/crm")
		return
	}
	plan, err := s.CRM.SalesPlan(r.Context(), cur.ID, period, day)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	plan.Company.Name = s.tr(r, "crm.plan.company")
	data := map[string]any{"Kind": "plan", "Plan": plan, "Pipelines": pipes, "Pipeline": cur, "Periods": crm.PlanPeriods,
		"Label": s.planLabel(r, period, plan.Start), "Prev": plan.Start.AddDate(0, -1, 0).Format(time.DateOnly),
		"Next": plan.End.Format(time.DateOnly), "Admin": sess.Admin, "Start": plan.Start.Format(time.DateOnly),
		"Query": template.URL(url.Values{"pipeline": {cur.ID}, "period": {period}}.Encode())}
	if sess.Admin && r.URL.Query().Get("edit") != "" { // the form: every department and employee may get a target
		if data["Users"], err = s.Tasks.ListUsers(r.Context()); err == nil {
			_, data["Departments"], err = s.tree(r)
		}
		if err != nil {
			s.taskErr(w, r, err, "/crm/plan")
			return
		}
		targets := map[string]float64{}
		for _, x := range plan.Departments {
			targets["dept:"+x.ID] = x.Target
		}
		for _, x := range plan.Users {
			targets["user:"+x.ID] = x.Target
		}
		data["Targets"], data["Edit"] = targets, true
	}
	if err = s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmplan", page{TitleKey: "crm.plan", Nav: "crm_plan", Data: data})
}

// crmPlanSave: the administrator sets the plan's metric and targets (an empty or zero target is none).
func (s *Server) crmPlanSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/crm/plan")
		return
	}
	cur, _, period, day, err := s.planQuery(r)
	if err != nil {
		s.taskErr(w, r, err, "/crm/plan")
		return
	}
	back := "/crm/plan?" + url.Values{"pipeline": {cur.ID}, "period": {period}, "start": {day.Format(time.DateOnly)}}.Encode()
	t := crm.PlanTargets{Metric: r.FormValue("metric"), Company: formNumber(r.FormValue("company")),
		Departments: map[string]float64{}, Users: map[string]float64{}}
	if r.FormValue("op") != "delete" {
		for kind, m := range map[string]map[string]float64{"dept": t.Departments, "user": t.Users} {
			ids, amounts := r.Form[kind+"_id"], r.Form[kind+"_amount"]
			for i, id := range ids {
				if i < len(amounts) && id != "" {
					m[id] += formNumber(amounts[i])
				}
			}
		}
	} else {
		t.Company = 0
	}
	if err = s.CRM.SaveSalesPlan(r.Context(), sess.UserID, cur.ID, period, day, t); err != nil {
		s.taskErr(w, r, err, back+"&edit=1")
		return
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}
