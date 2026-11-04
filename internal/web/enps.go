package web

import (
	"net/http"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «eNPS» of the company: the surveys «would you recommend the company as a place to work» — those
// keeping them (an administrator, an HR officer with the right) make, finish and delete them and see the results,
// the employees answer the ones they are asked in, anonymously.

func (s *Server) enpsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /company/enps", s.requireUser(s.enpsPage))
	mux.HandleFunc("POST /company/enps", s.requireUser(s.enpsCreate))
	mux.HandleFunc("GET /company/enps/{id}", s.requireUser(s.enpsSurvey))
	mux.HandleFunc("POST /company/enps/{id}/answer", s.requireUser(s.enpsAnswer))
	mux.HandleFunc("POST /company/enps/{id}/close", s.requireUser(s.enpsClose))
	mux.HandleFunc("POST /company/enps/{id}/delete", s.requireUser(s.enpsDelete))
	mux.HandleFunc("GET /api/v1/enps", s.api("company:read", s.apiEnpsList))
	mux.HandleFunc("POST /api/v1/enps", s.api("company:write", s.apiEnpsCreate))
	mux.HandleFunc("GET /api/v1/enps/{id}", s.api("company:read", s.apiEnps))
	mux.HandleFunc("POST /api/v1/enps/{id}/answer", s.api("company:write", s.apiEnpsAnswer))
	mux.HandleFunc("POST /api/v1/enps/{id}/close", s.api("company:write", s.apiEnpsClose))
	mux.HandleFunc("DELETE /api/v1/enps/{id}", s.api("company:write", s.apiEnpsDelete))
}

// enpsLine is a survey of the list with its index (Index nil — too few answers to show it).
type enpsLine struct {
	company.Enps
	Index *int
}

// Shown: the index is shown; Value is it.
func (l enpsLine) Shown() bool { return l.Index != nil }
func (l enpsLine) Value() int {
	if l.Index == nil {
		return 0
	}
	return *l.Index
}

// enpsPage: GET /company/enps — the surveys (all of them for those keeping them, with the form of a new one; the
// person's own for the others).
func (s *Server) enpsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	can, err := s.Company.CanEnps(r.Context(), sess.UserID, sess.Admin)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	list, err := s.Company.Surveys(r.Context(), sess.UserID, can)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	lines := make([]enpsLine, 0, len(list))
	for _, e := range list {
		l := enpsLine{Enps: e}
		if can {
			if l.Index, err = s.Company.EnpsIndex(r.Context(), e.ID); err != nil {
				s.taskErr(w, r, err, "/company")
				return
			}
		}
		lines = append(lines, l)
	}
	day := today()
	data := map[string]any{"List": lines, "Can": can, "Today": day, "To": day.AddDate(0, 0, 13), "Min": company.EnpsMinGroup}
	if can {
		if _, data["Departments"], err = s.tree(r); err != nil {
			s.taskErr(w, r, err, "/company")
			return
		}
	}
	s.render(w, r, "enps", page{TitleKey: "enps.title", Nav: "enps", Data: data})
}

// enpsSurvey: GET /company/enps/{id} — the form of the answer for the person asked while the survey is open, the
// results for those keeping the surveys.
func (s *Server) enpsSurvey(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	e, can, err := s.enpsOf(r, sess.UserID, sess.Admin, id)
	if err != nil {
		s.taskErr(w, r, err, "/company/enps")
		return
	}
	day := today()
	data := map[string]any{"E": e, "Can": can, "Status": e.Status(day), "Min": company.EnpsMinGroup, "CommentMax": company.EnpsCommentMax,
		"Scores": [11]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, "Thanks": r.URL.Query().Get("thanks") == "1"}
	if can {
		res, err := s.Company.EnpsResultsOf(r.Context(), e)
		if err != nil {
			s.taskErr(w, r, err, "/company/enps")
			return
		}
		data["R"] = res
	}
	s.render(w, r, "enpssurvey", page{Title: s.tr(r, "enps.title") + " · " + e.Question, Nav: "enps", Data: data})
}

// enpsOf returns the survey as the person sees it: those keeping the surveys — any, the others — those they are asked in.
func (s *Server) enpsOf(r *http.Request, userID string, admin bool, id int64) (company.Enps, bool, error) {
	can, err := s.Company.CanEnps(r.Context(), userID, admin)
	if err != nil {
		return company.Enps{}, false, err
	}
	e, err := s.Company.Survey(r.Context(), userID, id)
	if err == nil && !e.Asked && !can {
		err = company.ErrNotFound
	}
	return e, can, err
}

// enpsKeeper: the person keeps the surveys (or the error page is shown).
func (s *Server) enpsKeeper(w http.ResponseWriter, r *http.Request, sess auth.Session, back string) bool {
	can, err := s.Company.CanEnps(r.Context(), sess.UserID, sess.Admin)
	if err == nil && !can {
		err = company.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, back)
	}
	return err == nil
}

// enpsCreate: POST /company/enps (question, from, to, dept — several, none: the whole company).
func (s *Server) enpsCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.enpsKeeper(w, r, sess, "/company/enps") {
		return
	}
	from, err1 := time.ParseInLocation(time.DateOnly, r.FormValue("from"), time.Local)
	to, err2 := time.ParseInLocation(time.DateOnly, r.FormValue("to"), time.Local)
	if err1 != nil || err2 != nil {
		s.taskErr(w, r, i18n.E("err.enps_dates"), "/company/enps")
		return
	}
	r.ParseForm()
	id, err := s.Company.CreateEnps(r.Context(), sess.UserID, company.EnpsInput{Question: r.FormValue("question"), From: from, To: to,
		Departments: r.Form["dept"]})
	if err != nil {
		s.taskErr(w, r, err, "/company/enps")
		return
	}
	s.Log.Info("enps created", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, "/company/enps/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// enpsAnswer: POST /company/enps/{id}/answer (score 0–10, comment).
func (s *Server) enpsAnswer(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/company/enps/" + r.PathValue("id")
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	score, err := strconv.Atoi(r.FormValue("score"))
	if err != nil {
		s.taskErr(w, r, i18n.E("err.enps_score"), back)
		return
	}
	if err := s.Company.AnswerEnps(r.Context(), sess.UserID, id, score, r.FormValue("comment"), time.Now()); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("enps answered", "id", id) // not who: the answer is anonymous
	http.Redirect(w, r, back+"?thanks=1", http.StatusSeeOther)
}

// enpsClose: POST /company/enps/{id}/close — finishes the survey now.
func (s *Server) enpsClose(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/company/enps/" + r.PathValue("id")
	if !s.enpsKeeper(w, r, sess, back) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Company.CloseEnps(r.Context(), id); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("enps closed", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// enpsDelete: POST /company/enps/{id}/delete — the survey with its answers.
func (s *Server) enpsDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.enpsKeeper(w, r, sess, "/company/enps") {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Company.DeleteEnps(r.Context(), id); err != nil {
		s.taskErr(w, r, err, "/company/enps")
		return
	}
	s.Log.Info("enps deleted", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, "/company/enps", http.StatusSeeOther)
}

// REST

type apiEnpsGroup struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	Answers    int    `json:"answers"`
	Promoters  int    `json:"promoters"`
	Passives   int    `json:"passives"`
	Detractors int    `json:"detractors"`
	Index      int    `json:"enps"`
}

type apiEnpsResults struct {
	Shown       bool           `json:"shown"` // false — fewer answers than min_answers: only their count
	MinAnswers  int            `json:"min_answers"`
	Total       apiEnpsGroup   `json:"total"`
	Scores      []int          `json:"scores"` // answers by score 0…10
	Departments []apiEnpsGroup `json:"departments"`
	Hidden      int            `json:"hidden"` // answers of the departments too small to show
	Comments    []string       `json:"comments"`
	PrevID      int64          `json:"prev_id,omitempty"`
	PrevIndex   *int           `json:"prev_enps,omitempty"`
}

type apiEnpsOut struct {
	ID          int64           `json:"id"`
	Question    string          `json:"question"`
	From        string          `json:"from"`
	To          string          `json:"to"`
	Status      string          `json:"status"` // coming, open, closed
	Departments []apiDeptRef    `json:"departments"`
	CreatedBy   *apiDeptRef     `json:"created_by,omitempty"`
	Audience    int             `json:"audience"`
	Answers     int             `json:"answers"`
	Asked       bool            `json:"asked"`
	Answered    bool            `json:"answered"`
	Index       *int            `json:"enps,omitempty"`
	Results     *apiEnpsResults `json:"results,omitempty"`
}

func apiEnpsGroupOf(g company.EnpsGroup) apiEnpsGroup {
	return apiEnpsGroup{ID: g.ID, Name: g.Name, Answers: g.Answers, Promoters: g.Promoters, Passives: g.Passives, Detractors: g.Detractors, Index: g.Index}
}

func apiEnpsOf(e company.Enps) apiEnpsOut {
	o := apiEnpsOut{ID: e.ID, Question: e.Question, From: e.From.Format(time.DateOnly), To: e.To.Format(time.DateOnly), Status: e.Status(today()),
		Departments: []apiDeptRef{}, Audience: e.Audience, Answers: e.Answers, Asked: e.Asked, Answered: e.Answered}
	for _, d := range e.Departments {
		o.Departments = append(o.Departments, apiDeptRef{d.ID, d.Name})
	}
	if e.ByID != "" {
		o.CreatedBy = &apiDeptRef{e.ByID, e.ByName}
	}
	return o
}

// GET /api/v1/enps — the surveys: all of them with the index for those keeping them, the key owner's own for the others.
func (s *Server) apiEnpsList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	can, err := s.Company.CanEnps(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err != nil {
		return err
	}
	list, err := s.Company.Surveys(r.Context(), p.UserID, can)
	if err != nil {
		return err
	}
	out := make([]apiEnpsOut, 0, len(list))
	for _, e := range list {
		o := apiEnpsOf(e)
		if can {
			if o.Index, err = s.Company.EnpsIndex(r.Context(), e.ID); err != nil {
				return err
			}
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/enps/{id} — a survey, with its results for those keeping the surveys.
func (s *Server) apiEnps(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	e, can, err := s.enpsOf(r, p.UserID, s.apiAdmin(r.Context(), p.UserID), id)
	if err != nil {
		return err
	}
	o := apiEnpsOf(e)
	if can {
		res, err := s.Company.EnpsResultsOf(r.Context(), e)
		if err != nil {
			return err
		}
		out := apiEnpsResults{Shown: res.Shown, MinAnswers: company.EnpsMinGroup, Total: apiEnpsGroupOf(res.EnpsGroup), Scores: res.Scores[:],
			Departments: []apiEnpsGroup{}, Hidden: res.Hidden, Comments: res.Comments, PrevIndex: res.PrevIndex}
		if out.Comments == nil {
			out.Comments = []string{}
		}
		for _, g := range res.Departments {
			out.Departments = append(out.Departments, apiEnpsGroupOf(g))
		}
		if res.Prev != nil {
			out.PrevID = res.Prev.ID
		}
		if res.Shown {
			o.Index = &res.Index
		}
		o.Results = &out
	}
	writeJSON(w, http.StatusOK, o)
	return nil
}

// apiEnpsKeeper: the key owner keeps the surveys.
func (s *Server) apiEnpsKeeper(r *http.Request, p apikeys.Principal) error {
	ok, err := s.Company.CanEnps(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err == nil && !ok {
		err = i18n.E("err.forbidden")
	}
	return err
}

// POST /api/v1/enps {"question", "from", "to", "department_ids"} — a new survey (the whole company without departments).
func (s *Server) apiEnpsCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiEnpsKeeper(r, p); err != nil {
		return err
	}
	var in struct {
		Question      string   `json:"question"`
		From          string   `json:"from"`
		To            string   `json:"to"`
		DepartmentIDs []string `json:"department_ids"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Question == "" {
		in.Question = s.tr(r, "enps.question_default")
	}
	from, to := today(), today().AddDate(0, 0, 13)
	var err error
	if in.From != "" {
		if from, err = time.ParseInLocation(time.DateOnly, in.From, time.Local); err != nil {
			return i18n.E("err.enps_dates")
		}
	}
	if in.To != "" {
		if to, err = time.ParseInLocation(time.DateOnly, in.To, time.Local); err != nil {
			return i18n.E("err.enps_dates")
		}
	}
	id, err := s.Company.CreateEnps(r.Context(), p.UserID, company.EnpsInput{Question: in.Question, From: from, To: to, Departments: in.DepartmentIDs})
	if err != nil {
		return err
	}
	s.Log.Info("api enps created", "user_id", p.UserID, "id", id)
	e, err := s.Company.Survey(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, apiEnpsOf(e))
	return nil
}

// POST /api/v1/enps/{id}/answer {"score", "comment"} — the key owner's anonymous answer.
func (s *Server) apiEnpsAnswer(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Score   *int   `json:"score"`
		Comment string `json:"comment"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Score == nil {
		return i18n.E("err.enps_score")
	}
	if err := s.Company.AnswerEnps(r.Context(), p.UserID, id, *in.Score, in.Comment, time.Now()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// POST /api/v1/enps/{id}/close — finishes the survey now.
func (s *Server) apiEnpsClose(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiEnpsKeeper(r, p); err != nil {
		return err
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Company.CloseEnps(r.Context(), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// DELETE /api/v1/enps/{id}
func (s *Server) apiEnpsDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiEnpsKeeper(r, p); err != nil {
		return err
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Company.DeleteEnps(r.Context(), id); err != nil {
		return err
	}
	s.Log.Info("api enps deleted", "user_id", p.UserID, "id", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
