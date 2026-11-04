package web

import (
	"net/http"
	"net/url"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/sms"
)

// ---------- «SMS» of the CRM: the providers (/crm/sms) and an SMS to the client from the card ----------

// As «Центр сообщений»: administrators connect the providers (SMS.ru, SMSC, Twilio, an own gateway) and
// choose the default one, everyone sees which are there; from a card one writes to a phone of the client and the
// message lands in the card's timeline (crm/sms.go), the robot «Отправить SMS клиенту» does it by itself.

func (s *Server) crmSMSPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "sms", "Kinds": sms.Kinds, "Admin": sess.Admin}
	if err := s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	if s.SMS != nil {
		list, err := s.SMS.Senders(r.Context())
		if err != nil {
			s.taskErr(w, r, err, "/crm")
			return
		}
		data["Senders"] = list
	}
	s.render(w, r, "crmsms", page{TitleKey: "crm.sms.title", Nav: "crm_deals", Flash: r.URL.Query().Get("msg"), Data: data})
}

// crmSMSSave: the administrator adds or changes a provider, makes it the default one, deletes it or sends a test
// message through it.
func (s *Server) crmSMSSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/sms"
	if !sess.Admin || s.SMS == nil {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	id, msg := r.FormValue("id"), s.tr(r, "crm.saved")
	var err error
	switch r.FormValue("op") {
	case "save":
		_, err = s.SMS.SaveSender(r.Context(), sms.Sender{ID: id, Kind: r.FormValue("kind"), Name: r.FormValue("name"),
			Login: r.FormValue("login"), From: r.FormValue("sender"), Active: r.FormValue("active") != ""}, r.FormValue("secret"))
	case "default":
		err = s.SMS.SetDefault(r.Context(), id)
	case "delete":
		err = s.SMS.DeleteSender(r.Context(), id)
	case "test":
		_, err = s.SMS.Send(r.Context(), sms.Out{SenderID: id, Phone: r.FormValue("phone"), Text: s.tr(r, "crm.sms.test_text"), AuthorID: sess.UserID})
		msg = s.tr(r, "crm.sms.test_sent")
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, smsErr(err), back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// smsErr: a provider's own refusal is shown as it said it.
func smsErr(err error) error {
	if _, ok := err.(*i18n.Error); ok {
		return err
	}
	return i18n.E("err.sms_failed", err.Error())
}

// crmSMSSend: «SMS» of the card's timeline.
func (s *Server) crmSMSSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.crmCheck(r, sess, kind, id, "read"); err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	if err := s.CRM.SendSMSTo(r.Context(), sess.UserID, crmEntities[kind], id, r.FormValue("phone"), r.FormValue("text")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.sms.sent"))+"#timeline", http.StatusSeeOther)
}

// crmSMS reads a line «sms» of a card's timeline (nil — not one).
func crmSMS(body string) *crm.SMSEvent {
	if e, ok := crm.ParseSMSEvent(body); ok {
		return &e
	}
	return nil
}

// crmSMSForm is what the card's «SMS» offers: the client's phones (nil — no provider, no form).
func (s *Server) crmSMSForm(r *http.Request, entity, id string) (map[string]any, error) {
	if s.SMS == nil || !s.SMS.Ready(r.Context()) {
		return nil, nil
	}
	phones, err := s.CRM.ClientPhones(r.Context(), entity, id)
	return map[string]any{"Phones": phones, "Max": sms.MaxText}, err
}

// POST /api/v1/crm/{kind}/{id}/sms {"phone", "text"} — an SMS to the client from the card (no phone — the client's
// first one); the message is in the card's timeline.
func (s *Server) apiCRMSMS(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	var in struct {
		Phone string `json:"phone"`
		Text  string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	entity, id := crmEntities[r.PathValue("kind")], r.PathValue("id")
	if in.Phone == "" {
		phones, err := s.CRM.ClientPhones(r.Context(), entity, id)
		if err != nil {
			return err
		}
		if len(phones) == 0 {
			return i18n.E("err.robot_phone")
		}
		in.Phone = phones[0].Phone
	}
	if err := s.CRM.SendSMSTo(r.Context(), p.UserID, entity, id, in.Phone, in.Text); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"phone": sms.Normalize(in.Phone), "parts": sms.Parts(in.Text)})
	return nil
}

// GET /api/v1/crm/sms/senders — the SMS providers, the default one first (without their keys).
func (s *Server) apiSMSSenders(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var list []sms.Sender
	if s.SMS != nil {
		var err error
		if list, err = s.SMS.Senders(r.Context()); err != nil {
			return err
		}
	}
	if list == nil {
		list = []sms.Sender{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}
