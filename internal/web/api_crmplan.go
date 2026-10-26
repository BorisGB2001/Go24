package web

import (
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// apiPlanRow is a line of «План продаж» in REST: the target (0 — none), the won deals and their sum, the open deals
// to be closed in the period and their sum, the fact by the plan's metric and its percent of the target.
type apiPlanRow struct {
	ID        string  `json:"id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Target    float64 `json:"target"`
	Fact      float64 `json:"fact"`
	Percent   int     `json:"percent"`
	Won       int     `json:"won"`
	WonSum    float64 `json:"won_sum"`
	InWork    int     `json:"in_work"`
	InWorkSum float64 `json:"in_work_sum"`
}

func planRowJSON(x crm.PlanRow) apiPlanRow {
	return apiPlanRow{x.ID, x.Name, x.Target, x.Fact(), x.Percent(), x.Won, x.WonSum, x.InWork, x.InWorkSum}
}

// GET /api/v1/crm/plan?pipeline=&period=month|quarter|half|year&start=YYYY-MM-DD — «План продаж» of a deal pipeline
// (the first one by default) for the period holding start (today by default): metric (sum — of the won deals in the
// base currency, count — their number), the company's, the departments' and the employees' lines.
// PUT /api/v1/crm/plan {pipeline, period, start, metric, company, departments: {ID: target}, users: {ID: target}} —
// administrators set it, all the targets replaced; no targets removes the plan.
func (s *Server) apiCRMPlan(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Pipeline    string             `json:"pipeline"`
		Period      string             `json:"period"`
		Start       string             `json:"start"`
		Metric      string             `json:"metric"`
		Company     float64            `json:"company"`
		Departments map[string]float64 `json:"departments"`
		Users       map[string]float64 `json:"users"`
	}
	q := r.URL.Query()
	in.Pipeline, in.Period, in.Start = q.Get("pipeline"), q.Get("period"), q.Get("start")
	if r.Method == http.MethodPut {
		if !s.apiAdmin(r.Context(), p.UserID) {
			return i18n.E("err.forbidden")
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	pipes, err := s.CRM.Pipelines(r.Context())
	if err != nil {
		return err
	}
	if in.Pipeline == "" && len(pipes) > 0 {
		in.Pipeline = pipes[0].ID
	}
	known := false
	for _, x := range pipes {
		known = known || x.ID == in.Pipeline
	}
	if !known {
		return crm.ErrNotFound
	}
	if in.Period == "" {
		in.Period = "month"
	}
	day := time.Now()
	if in.Start != "" {
		if day, err = time.ParseInLocation(time.DateOnly, in.Start, time.Local); err != nil {
			return i18n.E("err.bad_form")
		}
	}
	if r.Method == http.MethodPut {
		if in.Metric == "" {
			in.Metric = "sum"
		}
		if err = s.CRM.SaveSalesPlan(r.Context(), p.UserID, in.Pipeline, in.Period, day,
			crm.PlanTargets{Metric: in.Metric, Company: in.Company, Departments: in.Departments, Users: in.Users}); err != nil {
			return err
		}
	}
	plan, err := s.CRM.SalesPlan(r.Context(), in.Pipeline, in.Period, day)
	if err != nil {
		return err
	}
	out := struct {
		Pipeline    string       `json:"pipeline"`
		Period      string       `json:"period"`
		Start       string       `json:"start"`
		End         string       `json:"end"` // the last day
		Metric      string       `json:"metric"`
		Currency    string       `json:"currency"`
		Set         bool         `json:"set"` // a plan is set for the period
		Company     apiPlanRow   `json:"company"`
		Departments []apiPlanRow `json:"departments"`
		Users       []apiPlanRow `json:"users"`
	}{plan.PipelineID, plan.Period, plan.Start.Format(time.DateOnly), plan.End.AddDate(0, 0, -1).Format(time.DateOnly),
		plan.Metric, plan.Currency, plan.ID != "", planRowJSON(plan.Company), []apiPlanRow{}, []apiPlanRow{}}
	for _, x := range plan.Departments {
		out.Departments = append(out.Departments, planRowJSON(x))
	}
	for _, x := range plan.Users {
		out.Users = append(out.Users, planRowJSON(x))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
