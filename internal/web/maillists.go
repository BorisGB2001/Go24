package web

import (
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/mail"
)

// ---------- «Почта»: «Списки контактов» ----------

// As Outlook's contact lists: /mail/settings?tab=lists&edit=ID|new makes and changes them; in «Кому» a list (or a
// department) is offered by its name and turns into its people (mailRcpt); «Сохранить как список» of the letter
// being written keeps its addresses as a new list (POST /mail/lists with Accept: application/json).

// mailListsData fills the tab of the contact lists: the lists, the one being edited (edit=ID|new).
func (s *Server) mailListsData(r *http.Request, userID string, d *mailSettingsData) error {
	var err error
	if d.Lists, err = s.Mail.ContactLists(r.Context(), userID); err != nil {
		return err
	}
	switch id := r.URL.Query().Get("edit"); id {
	case "":
	case "new":
		d.List = &mail.ContactList{}
	default:
		c, err := s.Mail.ContactListOf(r.Context(), userID, id)
		if err != nil {
			return err
		}
		d.List = &c
	}
	return nil
}

// mailListSave makes (POST /mail/lists) or changes (POST /mail/lists/{id}/save) a contact list: name and members —
// the addresses as typed.
func (s *Server) mailListSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c := mail.ContactList{ID: r.PathValue("id"), Name: r.FormValue("name")}
	members, err := mail.Members(r.FormValue("members"))
	if err == nil {
		c.Members = members
		c.ID, err = s.Mail.SaveContactList(r.Context(), sess.UserID, c)
	}
	asJSON := r.Header.Get("Accept") == "application/json"
	if err != nil {
		if asJSON {
			s.jsonErr(w, r, err)
			return
		}
		edit := c.ID
		if edit == "" {
			edit = "new"
		}
		s.mailErr(w, r, err, settingsURL("lists", "edit", edit))
		return
	}
	if asJSON {
		writeJSON(w, http.StatusOK, c)
		return
	}
	http.Redirect(w, r, settingsURL("lists", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailListDelete deletes a contact list of the user.
func (s *Server) mailListDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeleteContactList(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, settingsURL("lists"))
		return
	}
	http.Redirect(w, r, settingsURL("lists"), http.StatusSeeOther)
}

// apiMailLists is the user's contact lists: GET /api/v1/mail/lists → [{id, name, members: [{name, email}]}].
func (s *Server) apiMailLists(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.ContactLists(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []mail.ContactList{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

// apiMailListSave makes (POST /api/v1/mail/lists) or changes (POST /api/v1/mail/lists/{id}) a contact list:
// {name, members: [{name, email}]} → {id}.
func (s *Server) apiMailListSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in mail.ContactList
	if err := readJSON(r, &in); err != nil {
		return err
	}
	members, err := mail.Members(in.String()) // checked as if typed
	if err != nil {
		return err
	}
	in.ID, in.Members = r.PathValue("id"), members
	id, err := s.Mail.SaveContactList(r.Context(), p.UserID, in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

func (s *Server) apiMailListDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeleteContactList(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}
