package web

import (
	"bytes"
	"net/http"
	"net/url"
	netmail "net/mail"
	"slices"
	"strings"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// The contact card of the mail, as Outlook's profile card: a click on the name of a letter's sender or of a
// recipient (static/js/menu.js, [data-card]) shows who the address is — an employee's card of the portal (position,
// departments, phones, chat and call), else the user's own contact and the CRM's contact — with «Написать письмо»,
// «Копировать адрес», «Добавить в контакты», «Добавить в избранное», «Все письма» and the latest letters with them.

// mailPersonCard: GET /mail/person?addr=&back= → the card's HTML.
func (s *Server) mailPersonCard(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	p, err := s.Mail.PersonOf(ctx, sess.UserID, r.URL.Query().Get("addr"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	data := map[string]any{"P": p, "Back": mailBack(r), "Ref": mail.PersonRef(p.Email)}
	if p.UserID != "" && s.knowsPerson(r, sess.UserID, p.UserID) {
		if prof, err := s.Company.Profile(ctx, p.UserID); err == nil {
			mxid, element := s.matrixOf(prof)
			if tasks.IsGuestCtx(ctx) {
				prof.Departments = nil
			}
			absent, _ := s.Company.AbsentNow(ctx, p.UserID)
			data["Emp"] = map[string]any{"P": prof, "MatrixID": mxid, "ElementURL": element, "Me": p.UserID == sess.UserID,
				"Absent": absent, "Calls": s.Calls != nil}
		} else {
			s.Log.Warn("mail person profile", "err", err)
		}
	}
	if s.CRM != nil && data["Emp"] == nil { // only what the user's CRM role lets them read
		if list, err := s.CRM.Contacts(ctx, sess.UserID, p.Email, ""); err == nil {
			if i := slices.IndexFunc(list, func(c crm.Contact) bool { return crmHasEmail(c, p.Email) }); i >= 0 {
				data["CRM"] = list[i]
			}
		} else {
			s.Log.Warn("mail person crm", "err", err)
		}
	}
	if refs, err := s.Mail.Favorites(ctx, sess.UserID); err == nil {
		data["Fav"] = slices.Contains(refs, mail.PersonRef(p.Email))
	}
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["mail"].ExecuteTemplate(&buf, "mperson", data); err != nil {
		s.fail(w, "mail person card", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// crmHasEmail: is the e-mail (lower case) the CRM contact's?
func crmHasEmail(c crm.Contact, email string) bool {
	if strings.EqualFold(strings.TrimSpace(c.Email), email) {
		return true
	}
	return slices.ContainsFunc(c.Comms, func(m crm.Comm) bool {
		return m.Kind == "email" && strings.EqualFold(strings.TrimSpace(m.Value), email)
	})
}

// mailPersonContact is «Добавить в контакты» / «Изменить контакт» of the card: POST /mail/person/contact (name,
// email, company, phone, back) — the user's contact with the address is made or changed.
func (s *Server) mailPersonContact(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, err := s.Mail.SaveContact(r.Context(), sess.UserID, mail.Contact{Name: r.FormValue("name"), Email: r.FormValue("email"),
		Company: r.FormValue("company"), Phone: r.FormValue("phone")}); err != nil {
		s.mailErr(w, r, err, mailBack(r))
		return
	}
	http.Redirect(w, r, mailBack(r), http.StatusSeeOther)
}

// mailAddrs parts a header's addresses (the templates' mailAddrs): each one's name and e-mail; a header net/mail
// cannot read is one item of its text, without an e-mail.
func mailAddrs(header string) []mailAddr {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	list, err := netmail.ParseAddressList(header)
	if err != nil {
		return []mailAddr{{Name: header}}
	}
	out := make([]mailAddr, 0, len(list))
	for _, a := range list {
		out = append(out, mailAddr{Name: a.Name, Email: a.Address})
	}
	return out
}

// mailCard is the address of the contact card of the e-mail, back to the page (the templates' mailCard).
func mailCard(email, back string) string {
	return "/mail/person?" + url.Values{"addr": {email}, "back": {back}}.Encode()
}
