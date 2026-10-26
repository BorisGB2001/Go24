package web

import (
	"context"
	"net/http"
	"strings"

	"go24/internal/auth"
	"go24/internal/mail"
)

// The address book of a letter, as Outlook's: «Кому», «Копия» and «Скрытая» open it with a tab per source — go24's
// employees, the user's personal contacts (mail_contacts), the CRM, the recent correspondents, the contact lists and
// departments; the same sources are offered while an address is typed (mailRcpt), each marked with its own kind:
// employee, contact, crm_contact, crm_company, crm_lead, history, list, dept.

// mailBookSources are the tabs of the address book ("" — all of them).
var mailBookSources = map[string]bool{"": true, "employee": true, "contact": true, "crm": true, "history": true, "list": true}

// mailFind finds the recipients matching q in the source src ("" — everywhere, a few of each), at most limit, each
// address once. people — only people (an @mention): no lists and departments.
func (s *Server) mailFind(ctx context.Context, userID, q, src string, people bool, limit int) []map[string]any {
	out := []map[string]any{}
	seen := map[string]bool{}
	each := func(n int) int { // how many one source may give
		if src == "" {
			return n
		}
		return limit
	}
	add := func(item map[string]any, email string, max *int) {
		k := strings.ToLower(strings.TrimSpace(email))
		if k == "" || seen[k] || *max <= 0 || len(out) >= limit {
			return
		}
		seen[k] = true
		*max--
		out = append(out, item)
	}
	if (src == "" || src == "list") && !people {
		if list, err := s.Mail.Groups(ctx, userID, q, each(4)); err == nil {
			for _, g := range list {
				if len(out) < limit {
					out = append(out, map[string]any{"name": g.Name, "email": "", "kind": g.Kind, "members": g.Members})
				}
			}
		} else {
			s.Log.Warn("mail recipient groups", "err", err)
		}
	}
	if src == "" || src == "contact" {
		if list, err := s.Mail.Contacts(ctx, userID, q, each(6)); err == nil {
			n := each(6)
			for _, c := range list {
				add(map[string]any{"name": c.Name, "email": c.Email, "kind": "contact", "id": c.ID, "company": c.Company,
					"phone": c.Phone}, c.Email, &n)
			}
		} else {
			s.Log.Warn("mail contacts", "err", err)
		}
	}
	addPeople := func(list []mail.Recipient, err error, n int, what string) {
		if err != nil {
			s.Log.Warn(what, "err", err)
			return
		}
		for _, x := range list {
			add(map[string]any{"name": x.Name, "email": x.Email, "kind": x.Kind}, x.Email, &n)
		}
	}
	if src == "" || src == "history" {
		list, err := s.Mail.Correspondents(ctx, userID, q, each(6))
		addPeople(list, err, each(6), "mail recipients")
	}
	if src == "" || src == "employee" {
		list, err := s.Mail.Employees(ctx, q, each(6))
		addPeople(list, err, each(6), "mail employees")
	}
	if (src == "" || src == "crm") && s.CRM != nil { // only what the user's CRM role lets them read
		n := each(6)
		if list, err := s.CRM.Contacts(ctx, userID, q, ""); err == nil {
			for _, c := range list {
				add(map[string]any{"name": c.Name, "email": c.Email, "kind": "crm_contact"}, c.Email, &n)
			}
		}
		if list, err := s.CRM.Companies(ctx, userID, q); err == nil {
			for _, c := range list {
				add(map[string]any{"name": c.Title, "email": c.Email, "kind": "crm_company"}, c.Email, &n)
			}
		}
		if list, err := s.CRM.Leads(ctx, userID, q); err == nil {
			for _, l := range list {
				name := l.Name
				if name == "" {
					name = l.Title
				}
				add(map[string]any{"name": name, "email": l.Email, "kind": "crm_lead"}, l.Email, &n)
			}
		}
	}
	return out
}

// mailBook is a tab of the address book: GET /mail/book?src=&q= → [{name, email, kind, …}]; an empty q lists the
// source from its beginning.
func (s *Server) mailBook(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	src := r.URL.Query().Get("src")
	if !mailBookSources[src] {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.mailFind(r.Context(), sess.UserID, strings.TrimSpace(r.URL.Query().Get("q")), src, false, 100))
}

// mailContactSave adds a personal contact (POST /mail/contacts) or changes one (POST /mail/contacts/{id}/save):
// name, email, company, phone → the contact.
func (s *Server) mailContactSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, err := s.Mail.SaveContact(r.Context(), sess.UserID, mail.Contact{ID: r.PathValue("id"), Name: r.FormValue("name"),
		Email: r.FormValue("email"), Company: r.FormValue("company"), Phone: r.FormValue("phone")})
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// mailContactDelete deletes a personal contact: POST /mail/contacts/{id}/delete.
func (s *Server) mailContactDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeleteContact(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
