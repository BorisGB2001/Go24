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

// REST of «Зарплата и отпуск» (GO24DEV-895), with the rights of the profile's card:
//
//	GET  /api/v1/users/{id}/vacation?year=   — the vacation balance (the employee, their managers, HR officers)
//	PUT  /api/v1/users/{id}/vacation         — {"year","days" (null — the company's), "carried"} (who keeps vacations)
//	GET  /api/v1/payslips?user_id=&month=    — the payslips: one's own, or anyone's for who keeps them
//	POST /api/v1/payslips                    — multipart user_id, month (2006-01), file (PDF)
//	GET  /api/v1/payslips/{id}/file          — the PDF
//	DELETE /api/v1/payslips/{id}

func (s *Server) apiSalaryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/users/{id}/vacation", s.api("company:read", s.apiVacation))
	mux.HandleFunc("PUT /api/v1/users/{id}/vacation", s.api("company:write", s.apiVacationSet))
	mux.HandleFunc("GET /api/v1/payslips", s.api("company:read", s.apiPayslips))
	mux.HandleFunc("POST /api/v1/payslips", s.api("company:write", s.apiPayslipAdd))
	mux.HandleFunc("GET /api/v1/payslips/{id}/file", s.api("company:read", s.apiPayslipFile))
	mux.HandleFunc("DELETE /api/v1/payslips/{id}", s.api("company:write", s.apiPayslipDelete))
}

// apiWhose is the employee of the path, «me» — the key owner.
func apiWhose(r *http.Request, p apikeys.Principal) string {
	if id := r.PathValue("id"); id != "me" {
		return id
	}
	return p.UserID
}

func (s *Server) apiVacation(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := apiWhose(r, p)
	ok, err := s.Company.SeesVacation(r.Context(), p.UserID, id, s.apiAdmin(r.Context(), p.UserID))
	if err != nil {
		return err
	}
	if !ok {
		return company.ErrNotFound
	}
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		year = time.Now().Year()
	}
	b, err := s.Company.VacationBalanceOf(r.Context(), id, year, time.Now())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, b)
	return nil
}

func (s *Server) apiVacationSet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ok, err := s.Company.CanVacation(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err != nil {
		return err
	}
	if !ok {
		return company.ErrForbidden
	}
	var in struct {
		Year    int  `json:"year"`
		Days    *int `json:"days"`
		Carried int  `json:"carried"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Year == 0 {
		in.Year = time.Now().Year()
	}
	id := apiWhose(r, p)
	if err := s.Company.SetVacationBalance(r.Context(), p.UserID, id, in.Year, in.Days, in.Carried); err != nil {
		return err
	}
	b, err := s.Company.VacationBalanceOf(r.Context(), id, in.Year, time.Now())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, b)
	return nil
}

func (s *Server) apiPayslips(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	f := company.PayslipFilter{UserID: q.Get("user_id")}
	if f.UserID == "me" {
		f.UserID = p.UserID
	}
	if v := q.Get("month"); v != "" {
		m, ok := parseMonth(v)
		if !ok {
			return i18n.E("err.bad_form")
		}
		f.Month = m
	}
	keeper, err := s.Company.CanSalary(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err != nil {
		return err
	}
	if !keeper {
		f.UserID = p.UserID // one's own only
	}
	list, err := s.Company.Payslips(r.Context(), f)
	if err != nil {
		return err
	}
	if list == nil {
		list = []company.Payslip{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) apiPayslipAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ok, err := s.Company.CanSalary(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err != nil {
		return err
	}
	if !ok {
		return company.ErrForbidden
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPayslipSize+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return i18n.E("err.payslip_file")
	}
	defer r.MultipartForm.RemoveAll()
	month, ok := parseMonth(r.FormValue("month"))
	f, hdr, err := r.FormFile("file")
	if err != nil || !ok {
		return i18n.E("err.payslip_file")
	}
	defer f.Close()
	id, err := s.storePayslip(r.Context(), p.UserID, r.FormValue("user_id"), month, f, hdr)
	if err != nil {
		return err
	}
	ps, err := s.Company.Payslip(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, ps)
	return nil
}

func (s *Server) apiPayslipFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ps, err := s.Company.Payslip(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	sess := auth.Session{UserID: p.UserID, Admin: s.apiAdmin(r.Context(), p.UserID)}
	ok, err := s.canPayslip(r.Context(), sess, ps)
	if err != nil {
		return err
	}
	if !ok {
		return company.ErrNotFound
	}
	s.servePayslip(w, r, p.UserID, ps)
	return nil
}

func (s *Server) apiPayslipDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ok, err := s.Company.CanSalary(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
	if err != nil {
		return err
	}
	if !ok {
		return company.ErrForbidden
	}
	if err := s.Company.DeletePayslip(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	s.Log.Info("api payslip deleted", "user_id", p.UserID, "id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}
