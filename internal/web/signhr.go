package web

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/docgen"
	"go24/internal/i18n"
	"go24/internal/sign"
	"go24/internal/tasks"
)

// ---------- «Подпись для сотрудников» (КЭДО): an HR document sent to many employees (GO24DEV-620) ----------

// hrMax is the most employees one sending reaches.
const hrMax = 1000

// employeeFields are the placeholders of a template «Сотрудник», filled from the employee's profile.
var employeeFields = []string{"Name", "LastName", "FirstName", "SecondName", "Position", "Department", "Email", "Phone",
	"Birthday", "Date"}

// signHRAllowed: administrators and HR officers send HR documents.
func (s *Server) signHRAllowed(r *http.Request, sess auth.Session) bool {
	if sess.Admin {
		return true
	}
	if s.Company == nil {
		return false
	}
	h, err := s.Company.HROf(r.Context(), sess.UserID)
	return err == nil && h.On
}

// signHRTemplates are the templates «Сотрудник» the user may use.
func (s *Server) signHRTemplates(r *http.Request, sess auth.Session) ([]crm.DocTemplate, error) {
	list, err := s.CRM.DocTemplates(r.Context(), crm.TemplateEmployee)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, t := range list {
		if s.docTemplateAllowed(r, sess, t.ID) == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// signHRNew is the form of an HR document: a template «Сотрудник» or a Word file, the employees and departments it goes
// to, the company's representative who signs first.
func (s *Server) signHRNew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.signHRAllowed(r, sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	tpls, err := s.signHRTemplates(r, sess)
	if err != nil {
		s.fail(w, "sign hr", err)
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.fail(w, "sign hr", err)
		return
	}
	s.render(w, r, "signhr", page{TitleKey: "sign.hr.new", Nav: "sign", Data: map[string]any{"Templates": tpls, "Users": users,
		"Fields": employeeFields, "Admin": sess.Admin}})
}

// hrEmployee is an employee an HR document goes to, with the values of the template's placeholders.
type hrEmployee struct {
	tasks.User
	Values map[string]string
}

// signHRRecipients are the active employees chosen (to: user ids and «dept:» ids, a department with those under it),
// by name.
func (s *Server) signHRRecipients(r *http.Request, to []string) ([]hrEmployee, error) {
	var users, depts []string
	for _, v := range to {
		if d, ok := strings.CutPrefix(v, "dept:"); ok {
			depts = append(depts, d)
		} else if v != "" {
			users = append(users, v)
		}
	}
	rows, err := s.DB.Query(r.Context(), `WITH RECURSIVE d AS (
			SELECT id FROM departments WHERE id::text = ANY($2)
			UNION SELECT c.id FROM departments c JOIN d ON c.parent_id = d.id)
		SELECT u.id::text, u.name, u.email, u.last_name, u.first_name, u.second_name, u.position,
			coalesce(nullif(u.mobile_phone, ''), u.work_phone), u.birthday,
			coalesce((SELECT string_agg(dp.name, ', ' ORDER BY dp.name) FROM user_departments ud
				JOIN departments dp ON dp.id = ud.department_id WHERE ud.user_id = u.id), '')
		FROM users u WHERE u.active AND NOT u.guest AND (u.id::text = ANY($1)
			OR EXISTS (SELECT 1 FROM user_departments ud JOIN d ON d.id = ud.department_id WHERE ud.user_id = u.id))
		ORDER BY lower(u.name) LIMIT $3`, users, depts, hrMax+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hrEmployee
	for rows.Next() {
		var e hrEmployee
		var last, first, second, position, phone, dept string
		var birthday *time.Time
		if err := rows.Scan(&e.ID, &e.Name, &e.Email, &last, &first, &second, &position, &phone, &birthday, &dept); err != nil {
			return nil, err
		}
		e.Values = map[string]string{"Name": e.Name, "LastName": last, "FirstName": first, "SecondName": second,
			"Position": position, "Department": dept, "Email": e.Email, "Phone": phone, "Birthday": ""}
		if birthday != nil {
			e.Values["Birthday"] = birthday.Format(s.tr(r, "format.date"))
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > hrMax {
		return nil, i18n.E("sign.hr.err.many", hrMax)
	}
	return out, nil
}

// signHRCreate sends the HR document: each employee gets their own copy with their fields filled; the representative,
// when chosen, signs every copy first (one notification for the whole sending), then each employee signs theirs.
func (s *Server) signHRCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/hr"
	if !s.signHRAllowed(r, sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(signMax); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.taskErr(w, r, i18n.E("sign.err.file"), back)
		return
	}
	id, err := s.signHRSend(r, sess, r.FormValue("title"), r.FormValue("template"), r.FormValue("signer"), r.Form["to"],
		formRemind(r))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/sign/batch/"+id+"?msg="+url.QueryEscape(s.tr(r, "sign.hr.sent")), http.StatusSeeOther)
}

// signHRSend makes the sending of the template (tplID) or of the file uploaded («file» of the form); its id.
func (s *Server) signHRSend(r *http.Request, sess auth.Session, title, tplID, signerID string, to []string, remind int) (string, error) {
	var tpl []byte
	var t crm.DocTemplate
	name := ""
	if tplID != "" {
		err := s.docTemplateAllowed(r, sess, tplID)
		if err == nil {
			t, tpl, err = s.docTemplateOf(r, crm.TemplateEmployee, tplID)
		}
		if err != nil {
			return "", err
		}
		name = t.Name
	} else {
		f, h, err := r.FormFile("file")
		if err != nil {
			return "", i18n.E("sign.err.file")
		}
		defer f.Close()
		if tpl, err = io.ReadAll(io.LimitReader(f, signMax+1)); err != nil || len(tpl) > signMax || !strings.EqualFold(path.Ext(h.Filename), ".docx") {
			return "", i18n.E("sign.err.file")
		}
		name = strings.TrimSuffix(h.Filename, path.Ext(h.Filename))
	}
	if _, err := docgen.Names(tpl); err != nil {
		return "", i18n.E("sign.err.file")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = name
	}
	var signer tasks.User
	if signerID != "" {
		if err := s.DB.QueryRow(r.Context(), `SELECT id::text, name, email, guest FROM users WHERE id::text = $1 AND active`,
			signerID).Scan(&signer.ID, &signer.Name, &signer.Email, &signer.Guest); err != nil || signer.Guest {
			return "", sign.ErrParties
		}
	}
	people, err := s.signHRRecipients(r, to)
	if err != nil {
		return "", err
	}
	if len(people) == 0 {
		return "", i18n.E("sign.hr.err.none")
	}
	common := map[string]string{"Date": time.Now().Format(s.tr(r, "format.date"))}
	if err := s.myCompanyValues(r, common); err != nil {
		return "", err
	}
	images, err := s.docImages(r)
	if err != nil {
		return "", err
	}
	batch, err := s.Sign.NewBatch(r.Context(), sign.Batch{Title: title, TemplateID: t.ID, SignerID: signer.ID, AuthorID: sess.UserID})
	if err != nil {
		return "", err
	}
	made := 0
	for _, e := range people {
		values := map[string]string{}
		for k, v := range common {
			values[k] = v
		}
		for k, v := range e.Values {
			values[k] = v
		}
		if t.ID != "" {
			now := time.Now()
			if values["DocumentNumber"], err = s.CRM.NextDocNumber(r.Context(), t.ID, now); err != nil {
				break
			}
			values["DocumentDate"] = now.Format(s.tr(r, "format.date"))
		}
		var data []byte
		if data, err = docgen.Make(tpl, values, nil, images); err != nil {
			err = i18n.E("err.doc_not_docx")
			break
		}
		d := sign.Doc{Title: title + " — " + e.Name, AuthorID: sess.UserID, BatchID: batch, Hash: sign.Hash(data), RemindDays: remind}
		if d.FileID, err = s.signKeep(r, sess.UserID, data, title+" — "+e.Name+".docx"); err != nil {
			break
		}
		var parties []sign.Party
		if signer.ID != "" && signer.ID != e.ID {
			parties = append(parties, sign.Party{UserID: signer.ID, Name: signer.Name, Email: signer.Email})
		}
		parties = append(parties, sign.Party{UserID: e.ID, Name: e.Name, Email: e.Email, Side: "employee"})
		if d.ID, err = s.Sign.Create(r.Context(), d, parties); err != nil {
			_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, d.FileID)
			break
		}
		made++
	}
	if err != nil && made == 0 {
		_ = s.Sign.DropBatch(r.Context(), batch)
		return "", err
	}
	if err != nil {
		s.Log.Error("sign hr", "batch", batch, "made", made, "err", err)
	}
	s.Log.Info("sign hr send", "user_id", sess.UserID, "batch", batch, "copies", made)
	s.signHRTurns(r, batch, sess.UserID)
	return batch, nil
}

// signHRTurns tells those whose turn it is in the sending: the representative once for the whole sending, each
// employee of their copy (not who acts themselves).
func (s *Server) signHRTurns(r *http.Request, batch, actor string) {
	b, docs, err := s.Sign.Batch(r.Context(), batch)
	if err != nil {
		s.Log.Warn("sign hr turns", "err", err)
		return
	}
	if b.Ready > 0 && b.SignerID != actor {
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO notifications (user_id, actor_id, kind, body, url)
			VALUES ($1, $2, 'sign_request', $3, $4)`, b.SignerID, b.AuthorID, b.Title, "/sign/batch/"+b.ID); err != nil {
			s.Log.Warn("sign notify", "err", err)
		}
	}
	for _, d := range docs {
		if cur := d.Current(); d.Status == "signing" && cur != nil && cur.UserID != b.SignerID {
			s.signTurn(r, d, cur, actor)
		}
	}
}

// myCompanyValues puts the details of «Моя компания» ({MyCompany.…}).
func (s *Server) myCompanyValues(r *http.Request, v map[string]string) error {
	my, err := s.CRM.MyCompany(r.Context())
	if err != nil {
		return err
	}
	if err := s.party(r, v, "MyCompany", my, ""); err != nil && !errors.Is(err, crm.ErrNotFound) {
		return err
	}
	return nil
}

// signBatchOf is the sending of the path for its sender, its representative and administrators.
func (s *Server) signBatchOf(r *http.Request, sess auth.Session) (sign.Batch, []sign.Doc, error) {
	b, docs, err := s.Sign.Batch(r.Context(), r.PathValue("id"))
	if err == nil && b.AuthorID != sess.UserID && b.SignerID != sess.UserID && !sess.AdminActive() {
		err = sign.ErrNotFound
	}
	return b, docs, err
}

// signBatchPage is the sending: how many signed of all, each employee's copy with its state; «Подписать все» for the
// representative, «Напомнить» and «Отозвать» for the sender.
func (s *Server) signBatchPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	b, docs, err := s.signBatchOf(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/sign?f=hr")
		return
	}
	s.render(w, r, "signbatch", page{Title: b.Title, Nav: "sign", Data: map[string]any{"Batch": b, "Docs": docs,
		"Me": sess.UserID, "Author": b.AuthorID == sess.UserID || sess.AdminActive(),
		"Mine": b.SignerID == sess.UserID && b.Ready > 0}})
}

// signBatchSign: the representative signs every copy waiting for them (having read the document); each employee's
// turn comes.
func (s *Server) signBatchSign(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/batch/" + r.PathValue("id")
	b, docs, err := s.signBatchOf(r, sess)
	if err == nil && (b.SignerID != sess.UserID || b.Ready == 0) {
		err = sign.ErrTurn
	}
	if err == nil && r.FormValue("agree") != "1" {
		err = i18n.E("sign.err.agree")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	n := 0
	for _, d := range docs {
		if cur := d.Current(); d.Status == "signing" && cur != nil && cur.UserID == sess.UserID {
			if _, err := s.signDo(r, d, cur, "", sess.UserID, false, ""); err != nil {
				s.Log.Warn("sign hr batch", "doc", d.ID, "err", err)
				continue
			}
			n++
		}
	}
	s.Log.Info("sign hr batch sign", "user_id", sess.UserID, "batch", b.ID, "signed", n)
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "sign.hr.signed_all", n)), http.StatusSeeOther)
}

// signBatchRemind reminds those whose turn it is in the sending.
func (s *Server) signBatchRemind(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/batch/" + r.PathValue("id")
	b, docs, err := s.signBatchOf(r, sess)
	if err == nil && b.AuthorID != sess.UserID && !sess.AdminActive() {
		err = sign.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	n := 0
	if b.Ready > 0 {
		n++
	}
	var reminded []string
	for _, d := range docs {
		if cur := d.Current(); d.Status == "signing" && cur != nil {
			if reminded = append(reminded, cur.ID); cur.UserID != b.SignerID {
				n++
			}
		}
	}
	s.signHRTurns(r, b.ID, "")
	if err := s.Sign.Reminded(r.Context(), reminded...); err != nil {
		s.Log.Warn("sign reminded", "err", err)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "sign.hr.reminded", n)), http.StatusSeeOther)
}

// signBatchCancel recalls the copies not signed yet.
func (s *Server) signBatchCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/batch/" + r.PathValue("id")
	b, _, err := s.signBatchOf(r, sess)
	if err == nil && b.AuthorID != sess.UserID && !sess.AdminActive() {
		err = sign.ErrNotFound
	}
	n := 0
	if err == nil {
		n, err = s.Sign.CancelBatch(r.Context(), b.ID, sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("sign hr batch cancel", "user_id", sess.UserID, "batch", b.ID, "cancelled", n)
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "sign.hr.cancelled", n)), http.StatusSeeOther)
}
