package web

import (
	"net/http"
	"net/url"

	"go24/internal/auth"
	"go24/internal/i18n"
)

// crmStages: the administrator sets up the deal pipelines and the stages of deals and leads, right in the kanban.
func (s *Server) crmStages(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	pipeline, id := r.FormValue("pipeline"), r.FormValue("id")
	back := "/crm/leads"
	if r.FormValue("entity") == "deal" {
		back = "/crm/deals?pipeline=" + url.QueryEscape(pipeline)
	}
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	var err error
	switch r.FormValue("op") {
	case "add":
		err = s.CRM.AddStage(r.Context(), r.FormValue("entity"), pipeline, r.FormValue("name"), r.FormValue("color"), r.FormValue("semantic"))
	case "update":
		err = s.CRM.UpdateStage(r.Context(), id, r.FormValue("name"), r.FormValue("color"))
	case "left", "right":
		dir := 1
		if r.FormValue("op") == "left" {
			dir = -1
		}
		err = s.CRM.ShiftStage(r.Context(), id, dir)
	case "delete":
		err = s.CRM.DeleteStage(r.Context(), sess.UserID, id, r.FormValue("move_to"))
	case "pipeline_add": // of deals, or of the smart process
		if pipeline, err = s.CRM.AddPipelineOf(r.Context(), r.FormValue("type"), r.FormValue("name")); err == nil {
			back = "/crm/deals?pipeline=" + url.QueryEscape(pipeline)
		}
	case "pipeline_rename":
		err = s.CRM.RenamePipeline(r.Context(), pipeline, r.FormValue("name"))
	case "pipeline_delete":
		typ, _ := s.CRM.PipelineType(r.Context(), pipeline)
		if err = s.CRM.DeletePipeline(r.Context(), pipeline); err == nil {
			back = "/crm/deals?type=" + url.QueryEscape(typ)
		}
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
