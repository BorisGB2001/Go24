package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/crm"
)

// «Роботы» of the CRM (administrators): the stages of a pipeline (or of leads) with their robots.
func (s *Server) crmRobots(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	entity := q.Get("entity")
	var stages []crm.Stage
	var pipes []crm.Pipeline
	var typ *crm.Type
	var err error
	pipeline := ""
	if entity == "lead" {
		stages, err = s.CRM.LeadStages(r.Context())
	} else {
		entity = "deal" // of deals, or of a smart process
		if typ, err = s.crmTypeOf(r, q.Get("pipeline"), q.Get("type")); err == nil {
			pipes, err = s.CRM.PipelinesOf(r.Context(), typeID(typ))
		}
		if err == nil && len(pipes) > 0 {
			cur := pipes[0]
			for _, p := range pipes {
				if p.ID == q.Get("pipeline") {
					cur = p
				}
			}
			stages, pipeline = cur.Stages, cur.ID
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	ids := make([]string, len(stages))
	for i, st := range stages {
		ids[i] = st.ID
	}
	robots, err := s.CRM.Robots(r.Context(), ids)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	byStage := map[string][]crm.Robot{}
	for _, rb := range robots {
		byStage[rb.StageID] = append(byStage[rb.StageID], rb)
	}
	triggers, err := s.CRM.Triggers(r.Context(), ids)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	trByStage := map[string][]crm.Trigger{}
	for _, tr := range triggers {
		trByStage[tr.StageID] = append(trByStage[tr.StageID], tr)
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	types, err := s.CRM.Types(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	kinds, tunnels, err := s.crmTunnels(r, pipes, pipeline)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	var links []booking.Link // the booking robot sends one of them
	if s.Booking != nil {
		if links, err = s.Booking.Links(r.Context()); err != nil {
			s.taskErr(w, r, err, "/crm")
			return
		}
	} else {
		kinds = slices.DeleteFunc(slices.Clone(kinds), func(k string) bool { return k == "booking" })
	}
	s.render(w, r, "robots", page{TitleKey: "robots.title", Nav: "crm_deals", Data: map[string]any{
		"Entity": entity, "Pipelines": pipes, "Pipeline": pipeline, "Stages": stages, "Robots": byStage, "Tunnels": tunnels,
		"Kinds": kinds, "Triggers": trByStage, "TriggerKinds": crm.TriggerKinds, "Acts": crm.ActivityKinds, "Projects": projects, "Users": users, "Back": r.URL.RequestURI(),
		"Types": types, "Type": typ, "BookingLinks": links}})
}

// crmTunnel is a sales tunnel («Туннель продаж»): a copy or move robot from a stage of one pipeline into another's.
type crmTunnel struct {
	Robot            crm.Robot
	FromPipe, ToPipe crm.Pipeline
	From, To         crm.Stage
}

// crmTunnels are the robot kinds the pipeline's stages may have (the tunnels only where there is another pipeline)
// and the tunnels of all the pipelines of the kind of cards.
func (s *Server) crmTunnels(r *http.Request, pipes []crm.Pipeline, pipeline string) ([]string, []crmTunnel, error) {
	if len(pipes) < 2 {
		kinds := []string{}
		for _, k := range crm.RobotKinds {
			if !slices.Contains(crm.TunnelKinds, k) {
				kinds = append(kinds, k)
			}
		}
		return kinds, nil, nil
	}
	type at struct {
		p  crm.Pipeline
		st crm.Stage
	}
	where := map[string]at{}
	var ids []string
	for _, p := range pipes {
		for _, st := range p.Stages {
			where[st.ID] = at{p, st}
			ids = append(ids, st.ID)
		}
	}
	robots, err := s.CRM.Robots(r.Context(), ids)
	if err != nil {
		return nil, nil, err
	}
	var out []crmTunnel
	for _, rb := range robots {
		from, to := where[rb.StageID], where[rb.Params.Stage]
		if slices.Contains(crm.TunnelKinds, rb.Kind) && to.p.ID != "" && (from.p.ID == pipeline || to.p.ID == pipeline) {
			out = append(out, crmTunnel{Robot: rb, FromPipe: from.p, From: from.st, ToPipe: to.p, To: to.st})
		}
	}
	return crm.RobotKinds, out, nil
}

func (s *Server) crmRobotAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if len(back) < 1 || back[0] != '/' || (len(back) > 1 && back[1] == '/') {
		back = "/crm/robots"
	}
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	delay, _ := strconv.Atoi(r.FormValue("delay"))
	to := r.FormValue("to")
	if r.FormValue("kind") == "responsible" {
		to = r.FormValue("new_responsible")
	}
	err := s.CRM.AddRobot(r.Context(), sess.UserID, r.FormValue("stage"), r.FormValue("kind"), crm.RobotParams{
		Text: r.FormValue("text"), Details: r.FormValue("details"), Project: r.FormValue("project"), To: to, Days: days,
		Act: r.FormValue("act"), Stage: r.FormValue("to_stage"), Link: r.FormValue("link"), Delay: delay, Unit: r.FormValue("unit"),
		Min: formNumber(r.FormValue("min")), Max: formNumber(r.FormValue("max"))})
	if err != nil {
		http.Redirect(w, r, back+sepFor(back)+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) crmRobotDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := r.FormValue("back")
	if len(back) < 1 || back[0] != '/' || (len(back) > 1 && back[1] == '/') {
		back = "/crm/robots"
	}
	if err := s.CRM.DeleteRobot(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/crm/robots")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// crmTriggerAdd sets a trigger on a stage, crmTriggerDelete removes one (administrators).
func (s *Server) crmTriggerAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.crmTriggerDo(w, r, sess, func() error {
		return s.CRM.AddTrigger(r.Context(), sess.UserID, r.FormValue("stage"), r.FormValue("kind"))
	})
}

func (s *Server) crmTriggerDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.crmTriggerDo(w, r, sess, func() error { return s.CRM.DeleteTrigger(r.Context(), r.PathValue("id")) })
}

func (s *Server) crmTriggerDo(w http.ResponseWriter, r *http.Request, sess auth.Session, do func() error) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := r.FormValue("back")
	if len(back) < 1 || back[0] != '/' || (len(back) > 1 && back[1] == '/') {
		back = "/crm/robots"
	}
	if err := do(); err != nil {
		back += sepFor(back) + "msg=" + url.QueryEscape(s.errText(r, err))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// sepFor is how a query is added to a path: "?" or "&".
func sepFor(path string) string {
	for _, c := range path {
		if c == '?' {
			return "&"
		}
	}
	return "?"
}
