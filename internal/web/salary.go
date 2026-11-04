package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «Зарплата и отпуск» (GO24DEV-895): the card of the profile (the vacation balance of the year, the
// payslips), the page «Расчётные листки» of an HR officer with the right «salary» (the month's payslips of everyone,
// many files at once — whose by the name of the file), the company's default of the yearly vacation.

// maxPayslipSize limits a payslip's file.
const maxPayslipSize = 10 << 20

// salaryCard is what the profile's card «Зарплата и отпуск» shows to the viewer; nil — the viewer sees none of it.
type salaryCard struct {
	Year        int
	Balance     *company.VacationBalance // nil — the viewer does not see the vacation
	CanVacation bool                     // changes the days
	Payslips    []company.Payslip        // nil with Own and CanSalary false — not shown
	Own         bool                     // the viewer's own payslips
	CanSalary   bool                     // uploads and removes
	Month       string                   // the month offered for an upload: the last one, 2006-01
}

// salaryOf gathers the card of the person for the viewer.
func (s *Server) salaryOf(ctx context.Context, sess auth.Session, id string) (*salaryCard, error) {
	now := time.Now()
	c := &salaryCard{Year: now.Year(), Own: sess.UserID == id, Month: now.AddDate(0, -1, 0).Format("2006-01")}
	sees, err := s.Company.SeesVacation(ctx, sess.UserID, id, sess.Admin)
	if err != nil {
		return nil, err
	}
	if sees {
		b, err := s.Company.VacationBalanceOf(ctx, id, c.Year, now)
		if err != nil && err != company.ErrNotFound {
			return nil, err
		}
		if err == nil {
			c.Balance = &b
		}
		if c.CanVacation, err = s.Company.CanVacation(ctx, sess.UserID, sess.Admin); err != nil {
			return nil, err
		}
	}
	if c.CanSalary, err = s.Company.CanSalary(ctx, sess.UserID, sess.Admin); err != nil {
		return nil, err
	}
	if c.Own || c.CanSalary {
		if c.Payslips, err = s.Company.Payslips(ctx, company.PayslipFilter{UserID: id}); err != nil {
			return nil, err
		}
	}
	if c.Balance == nil && !c.Own && !c.CanSalary {
		return nil, nil
	}
	return c, nil
}

// userVacation: POST /u/{id}/vacation (year, days — empty for the company's default, carried) — an administrator or
// an HR officer with the right «absences» sets the employee's vacation days of the year.
func (s *Server) userVacation(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/u/" + id // the card is #psalary; a message comes as ?msg=
	ok, err := s.Company.CanVacation(r.Context(), sess.UserID, sess.Admin)
	if err != nil || !ok {
		s.taskErr(w, r, orForbidden(err), back)
		return
	}
	year, _ := strconv.Atoi(r.FormValue("year"))
	var days *int
	if v := strings.TrimSpace(r.FormValue("days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			s.taskErr(w, r, i18n.E("err.vacation_days"), back)
			return
		}
		days = &n
	}
	carried := 0
	if v := strings.TrimSpace(r.FormValue("carried")); v != "" {
		if carried, err = strconv.Atoi(v); err != nil {
			s.taskErr(w, r, i18n.E("err.vacation_days"), back)
			return
		}
	}
	if err := s.Company.SetVacationBalance(r.Context(), sess.UserID, id, year, days, carried); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("vacation balance", "user_id", sess.UserID, "target", id, "year", year, "days", days, "carried", carried)
	http.Redirect(w, r, back+"#psalary", http.StatusSeeOther)
}

// orForbidden: the error, or «forbidden» when there is none.
func orForbidden(err error) error {
	if err != nil {
		return err
	}
	return company.ErrForbidden
}

// vacationSettings: POST /company/vacation-settings (days) — the company's yearly vacation by default.
func (s *Server) vacationSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/company/absences/report"
	ok, err := s.Company.CanVacation(r.Context(), sess.UserID, sess.Admin)
	if err != nil || !ok {
		s.taskErr(w, r, orForbidden(err), back)
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.FormValue("days")))
	if err != nil {
		s.taskErr(w, r, i18n.E("err.vacation_days"), back)
		return
	}
	if err := s.Company.SaveVacationSettings(r.Context(), company.VacationSettings{Days: days}, sess.UserID); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// storePayslip checks the file is a PDF of an allowed size and stores it as the person's payslip of the month.
func (s *Server) storePayslip(ctx context.Context, actor, userID string, month time.Time, f multipart.File, hdr *multipart.FileHeader) (string, error) {
	if hdr.Size == 0 || hdr.Size > maxPayslipSize {
		return "", i18n.E("err.payslip_file")
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if http.DetectContentType(head[:n]) != "application/pdf" {
		return "", i18n.E("err.payslip_file")
	}
	key := "go24/payslips/" + userID + "/" + month.Format("2006-01") + "-" + randomHex(8) + ".pdf"
	if err := s.Storages.Put(ctx, actor, false, "", key, io.MultiReader(bytes.NewReader(head[:n]), f), hdr.Size, "application/pdf"); err != nil {
		return "", err
	}
	return s.Company.SavePayslip(ctx, actor, userID, month, key, hdr.Filename, hdr.Size)
}

func parseMonth(v string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01", v, time.Local)
	return t, err == nil && t.Year() >= 2000 && t.Year() <= 2100
}

// userPayslip: POST /u/{id}/payslips (month 2006-01, file) — an HR officer uploads the employee's payslip.
func (s *Server) userPayslip(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/u/" + id // the card is #psalary; a message comes as ?msg=
	ok, err := s.Company.CanSalary(r.Context(), sess.UserID, sess.Admin)
	if err != nil || !ok {
		s.taskErr(w, r, orForbidden(err), back)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPayslipSize+1<<20)
	month, ok := parseMonth(r.FormValue("month"))
	f, hdr, err := r.FormFile("file")
	if err != nil || !ok {
		s.taskErr(w, r, i18n.E("err.payslip_file"), back)
		return
	}
	defer f.Close()
	if _, err := s.storePayslip(r.Context(), sess.UserID, id, month, f, hdr); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#psalary", http.StatusSeeOther)
}

// canPayslip: the payslip's employee, or who keeps the payslips.
func (s *Server) canPayslip(ctx context.Context, sess auth.Session, p company.Payslip) (bool, error) {
	if p.UserID == sess.UserID {
		return true, nil
	}
	return s.Company.CanSalary(ctx, sess.UserID, sess.Admin)
}

// payslipFile: GET /company/payslips/{id}/file — the PDF, in the browser; the employee's opening is noted.
func (s *Server) payslipFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Company.Payslip(r.Context(), r.PathValue("id"))
	if err == nil {
		var ok bool
		if ok, err = s.canPayslip(r.Context(), sess, p); err == nil && !ok {
			err = company.ErrNotFound // another's payslip is not even there
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/u/"+sess.UserID)
		return
	}
	s.servePayslip(w, r, sess.UserID, p)
}

func (s *Server) servePayslip(w http.ResponseWriter, r *http.Request, viewer string, p company.Payslip) {
	obj, err := s.Storages.Open(r.Context(), nil, p.FileKey)
	if err != nil {
		s.fail(w, "open payslip", err)
		return
	}
	defer obj.Close()
	if viewer == p.UserID {
		if err := s.Company.SeePayslip(r.Context(), p.ID); err != nil {
			s.Log.Error("payslip seen", "err", err)
		}
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(p.FileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.Copy(w, obj)
}

// payslipDelete: POST /company/payslips/{id}/delete — who keeps the payslips removes one.
func (s *Server) payslipDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/company/payslips"
	}
	ok, err := s.Company.CanSalary(r.Context(), sess.UserID, sess.Admin)
	if err == nil && !ok {
		err = company.ErrForbidden
	}
	if err == nil {
		err = s.Company.DeletePayslip(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// payslipRow is an employee of the month's page: their payslip, if any.
type payslipRow struct {
	company.PayslipPerson
	Payslip *company.Payslip
}

// payslipsPage: GET /company/payslips?month=2026-10 — «Расчётные листки» of the month: everyone, who has got theirs
// (and opened it), an upload for each and for many at once.
func (s *Server) payslipsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ok, err := s.Company.CanSalary(r.Context(), sess.UserID, sess.Admin)
	if err != nil || !ok {
		s.taskErr(w, r, orForbidden(err), "/company")
		return
	}
	q := r.URL.Query()
	month, ok := parseMonth(q.Get("month"))
	if !ok {
		month = time.Now().AddDate(0, -1, 0)
	}
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.Local)
	people, err := s.Company.PayslipPeople(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	list, err := s.Company.Payslips(r.Context(), company.PayslipFilter{Month: month})
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	byUser := map[string]*company.Payslip{}
	for i := range list {
		byUser[list[i].UserID] = &list[i]
	}
	rows := make([]payslipRow, 0, len(people))
	for _, p := range people {
		rows = append(rows, payslipRow{PayslipPerson: p, Payslip: byUser[p.ID]})
	}
	s.render(w, r, "payslips", page{TitleKey: "payslip.title", Nav: "payslips", Data: map[string]any{
		"Month": month, "Prev": month.AddDate(0, -1, 0), "Next": month.AddDate(0, 1, 0), "Rows": rows, "Uploaded": len(list),
		"Unmatched": q["unmatched"], "Done": q.Get("done"),
	}})
}

// payslipsUpload: POST /company/payslips (month, files) — many payslips at once: whose each is, by the name of the
// file (the e-mail or the name in it); the files no one matched are listed back.
func (s *Server) payslipsUpload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ok, err := s.Company.CanSalary(r.Context(), sess.UserID, sess.Admin)
	if err != nil || !ok {
		s.taskErr(w, r, orForbidden(err), "/company")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 200<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.taskErr(w, r, i18n.E("err.payslip_file"), "/company/payslips")
		return
	}
	month, ok := parseMonth(r.FormValue("month"))
	back := "/company/payslips?month=" + r.FormValue("month")
	if !ok {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/company/payslips")
		return
	}
	people, err := s.Company.PayslipPeople(r.Context())
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	v := url.Values{"month": {r.FormValue("month")}}
	done := 0
	for _, hdr := range r.MultipartForm.File["files"] {
		who := r.FormValue("user") // one file for the person chosen
		if who == "" {
			who = company.MatchPayslipFile(hdr.Filename, people)
		}
		if who == "" {
			v.Add("unmatched", hdr.Filename)
			continue
		}
		f, err := hdr.Open()
		if err == nil {
			_, err = s.storePayslip(r.Context(), sess.UserID, who, month, f, hdr)
			f.Close()
		}
		if err != nil {
			s.Log.Warn("payslip upload", "file", hdr.Filename, "err", err)
			v.Add("unmatched", hdr.Filename)
			continue
		}
		done++
	}
	v.Set("done", strconv.Itoa(done))
	http.Redirect(w, r, "/company/payslips?"+v.Encode(), http.StatusSeeOther)
}
