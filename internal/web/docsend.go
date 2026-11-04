package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/sms"
)

// ---------- «Отправить» a card's document to the client by SMS or into the open line's chat ----------

// As in the document generator: besides a letter, the document goes to the client as its public link (switched
// on, when it is off) with a line of text — by SMS through the CRM's SMS provider (the card's line «SMS») or into the
// client's latest dialogue of the open lines (the card's line «Документ отправлен в чат»).

// docSentEvent is the body of a card's line «doc_sent».
type docSentEvent struct {
	Name string `json:"name"`
	Chat string `json:"chat"`
}

// crmDocSent reads a line «doc_sent» of a card's timeline (nil — not one).
func crmDocSent(body string) *docSentEvent {
	var e docSentEvent
	if json.Unmarshal([]byte(body), &e) != nil || e.Chat == "" {
		return nil
	}
	return &e
}

// docTitle is the document's name with its number.
func docTitle(d crm.Document) string {
	if d.Number != "" {
		return d.TemplateName + " № " + d.Number
	}
	return d.TemplateName
}

// docSend sends the document by via ("sms" — to the phone, the client's first one when none is given; "chat"); text
// "" — the usual one. It is where the document went: the phone or the dialogue.
func (s *Server) docSend(r *http.Request, userID string, d crm.Document, via, phone, text string) (string, error) {
	ctx := r.Context()
	if d.Entity == crm.TemplateInvoice {
		return "", crm.ErrNotFound
	}
	var chat string
	var err error
	switch via {
	case "sms":
		if s.SMS == nil || s.CRM.SendSMS == nil || !s.SMS.Ready(ctx) {
			return "", i18n.E("err.sms_no_sender")
		}
		if phone = strings.TrimSpace(phone); phone == "" {
			phones, err := s.CRM.ClientPhones(ctx, d.Entity, d.EntityID)
			if err != nil {
				return "", err
			}
			if len(phones) == 0 {
				return "", i18n.E("err.robot_phone")
			}
			phone = phones[0].Phone
		}
		if sms.Normalize(phone) == "" {
			return "", i18n.E("err.sms_phone")
		}
	case "chat":
		if s.OpenLines != nil {
			if chat, err = s.OpenLines.ChatOf(ctx, d.Entity, d.EntityID); err != nil {
				return "", err
			}
		}
		if chat == "" {
			return "", i18n.E("err.doc_send_chat")
		}
	default:
		return "", i18n.E("err.doc_send_via")
	}
	if text = strings.TrimSpace(text); text == "" {
		text = s.tr(r, "docs.send_text", docTitle(d))
	}
	if len([]rune(text)) > 500 {
		return "", i18n.E("err.doc_send_text")
	}
	token := d.PublicToken
	if token == "" {
		if token, err = s.CRM.SetDocumentPublic(ctx, d.ID, true); err != nil {
			return "", err
		}
	}
	text += "\n" + strings.TrimRight(s.BaseURL, "/") + "/d/" + token
	if via == "sms" {
		return sms.Normalize(phone), s.CRM.SendSMSTo(ctx, userID, d.Entity, d.EntityID, phone, text)
	}
	if err := s.OpenLines.Reply(ctx, userID, chat, text); err != nil {
		return "", err
	}
	b, _ := json.Marshal(docSentEvent{Name: docTitle(d), Chat: chat})
	return chat, s.CRM.AddEvent(ctx, d.Entity, d.EntityID, userID, "doc_sent", string(b))
}

// crmDocumentSend is «Отправить клиенту» of a card's document.
func (s *Server) crmDocumentSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.crmCheck(r, sess, kind, id, "write"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	doc, err := s.CRM.Document(r.Context(), docEntity(kind), id, r.PathValue("doc"))
	via := r.FormValue("via")
	if err == nil {
		_, err = s.docSend(r, sess.UserID, doc, via, r.FormValue("phone"), r.FormValue("text"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("crm document sent", "user_id", sess.UserID, "doc", doc.ID, "via", via)
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "docs.sent_"+via))+"#documents", http.StatusSeeOther)
}

// POST /api/v1/documents/{id}/send {"via": "sms" | "chat", "phone", "text"} — the document to the client as its public
// link: by SMS (no phone — the client's first one) or into the client's latest dialogue of the open lines.
func (s *Server) apiDocSend(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Via   string `json:"via"`
		Phone string `json:"phone"`
		Text  string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, err := s.apiDoc(r, p, "write")
	if err != nil {
		return err
	}
	to, err := s.docSend(r, p.UserID, d, in.Via, in.Phone, in.Text)
	if err == nil {
		d, err = s.CRM.DocumentByID(r.Context(), d.ID)
	}
	if err != nil {
		return err
	}
	s.Log.Info("crm document sent", "user_id", p.UserID, "doc", d.ID, "via", in.Via, "api", true)
	out := map[string]any{"via": in.Via, "document": s.apiDocumentOf(d)}
	if in.Via == "sms" {
		out["phone"] = to
	} else {
		out["chat"] = to
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
