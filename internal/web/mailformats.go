package web

import (
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: «Условное форматирование» ----------

// As Outlook's «Параметры → Почта → Условное форматирование»: /mail/settings?tab=formats — the rules in their order
// (on and off, up and down, deleted), edit=ID|new — a rule's form: its conditions (those of the rules, «Не
// прочитано»), the colour and the font of the letters it takes (mail/formats.go).

// formatOfForm reads a rule of «Условное форматирование» of the form.
func formatOfForm(r *http.Request) mail.Format {
	return mail.Format{ID: r.PathValue("id"), Name: r.FormValue("name"), Enabled: r.FormValue("enabled") != "", Unread: r.FormValue("unread") != "",
		If: mail.Cond{From: r.FormValue("if_from"), To: r.FormValue("if_to"), Subject: r.FormValue("if_subject"), Body: r.FormValue("if_body"),
			Attach: r.FormValue("if_attach") != "", Importance: r.FormValue("if_importance"), Sensitivity: r.FormValue("if_sensitivity"), ToMe: r.FormValue("if_to_me") != "",
			CcMe: r.FormValue("if_cc_me") != "", OnlyMe: r.FormValue("if_only_me") != ""},
		Look: mail.Look{Color: r.FormValue("color"), Bold: r.FormValue("bold") != "", Italic: r.FormValue("italic") != "", Underline: r.FormValue("underline") != ""}}
}

// mailFormatsData fills the tab: the rules, the one being edited.
func (s *Server) mailFormatsData(r *http.Request, userID string, d *mailSettingsData) error {
	var err error
	d.Importances, d.Colors = mail.Importances, mail.Colors
	if d.Formats, err = s.Mail.Formats(r.Context(), userID); err != nil {
		return err
	}
	switch id := r.URL.Query().Get("edit"); id {
	case "":
	case "new":
		d.Format = &mail.Format{Enabled: true, Look: mail.Look{Color: "red"}}
	default:
		f, err := s.Mail.Format(r.Context(), userID, id)
		if err != nil {
			return err
		}
		d.Format = &f
	}
	return nil
}

func (s *Server) mailFormatSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	f := formatOfForm(r)
	if _, err := s.Mail.SaveFormat(r.Context(), sess.UserID, f); err != nil {
		edit := f.ID
		if edit == "" {
			edit = "new"
		}
		s.mailErr(w, r, err, settingsURL("formats", "edit", edit))
		return
	}
	s.Log.Info("mail format saved", "user_id", sess.UserID, "format", f.ID)
	http.Redirect(w, r, settingsURL("formats", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailFormatDo: do=delete, up, down, on, off for a rule of the user.
func (s *Server) mailFormatDo(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("id")
	var err error
	switch r.FormValue("do") {
	case "delete":
		err = s.Mail.DeleteFormat(ctx, sess.UserID, id)
	case "up":
		err = s.Mail.MoveFormat(ctx, sess.UserID, id, -1)
	case "down":
		err = s.Mail.MoveFormat(ctx, sess.UserID, id, 1)
	case "on", "off":
		err = s.Mail.EnableFormat(ctx, sess.UserID, id, r.FormValue("do") == "on")
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.mailErr(w, r, err, settingsURL("formats"))
		return
	}
	http.Redirect(w, r, settingsURL("formats"), http.StatusSeeOther)
}

func (s *Server) apiMailFormats(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Formats(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []mail.Format{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) apiMailFormatSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		mail.Format
		Move int `json:"move"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if id != "" && in.Move != 0 {
		if err := s.Mail.MoveFormat(r.Context(), p.UserID, id, in.Move); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
		return nil
	}
	in.Format.ID = id
	id, err := s.Mail.SaveFormat(r.Context(), p.UserID, in.Format)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

func (s *Server) apiMailFormatDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeleteFormat(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}
