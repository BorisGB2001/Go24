package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/marketing"
	"go24/internal/telephony"
)

// ---------- Calls in CRM: the client by the number, «Звонок» in the card's timeline ----------

// phoneEvent is a call in a CRM card's timeline (crm_timeline kind "call", its body as JSON).
type phoneEvent struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"` // in, out, missed
	Number string `json:"number"`
	Sec    int    `json:"sec,omitempty"`
	Status string `json:"status"`
	Rec    bool   `json:"rec,omitempty"`
	Note   string `json:"note,omitempty"` // «Комментарий к звонку»
}

// Duration of the conversation as m:ss.
func (e phoneEvent) Duration() string { return telephony.Call{Seconds: e.Sec}.Duration() }

// crmCall reads a timeline line «Звонок» (nil — not one).
func crmCall(body string) *phoneEvent {
	var e phoneEvent
	if json.Unmarshal([]byte(body), &e) != nil || e.ID == "" {
		return nil
	}
	return &e
}

// portalTr is a text in the portal's language (for what go24 writes by itself, with nobody's request).
func (s *Server) portalTr(key string, args ...any) string {
	return s.I18n.T(s.portalLang(), key, args...)
}

// portalLang is the portal's language ("ru-RU").
func (s *Server) portalLang() string {
	if s.Portal != nil {
		if l := s.Portal.Current().Lang; s.I18n.Supported(l) {
			return l
		}
	}
	return s.I18n.Match("")
}

// phoneClient is the client's CRM card with the phone: a contact, then a company, then a lead not converted yet.
func (s *Server) phoneClient(ctx context.Context, number string) (entity, id, name string, err error) {
	if s.CRM == nil {
		return "", "", "", nil
	}
	list, err := s.CRM.Duplicates(ctx, "lead", "", "", number, "")
	if err != nil {
		return "", "", "", err
	}
	for _, want := range []string{"contact", "company", "lead"} {
		for _, d := range list {
			if d.Entity == want && d.By == "phone" {
				return d.Entity, d.ID, d.Title, nil
			}
		}
	}
	return "", "", "", nil
}

// phoneOwner: Asterisk asks whose client calls a line with «Звонок клиента из CRM — сначала ответственному» (go24-owner;
// the key is ASTERISK_SECRET) — the inner number of the client's card's responsible ("" — none).
func (s *Server) phoneOwner(w http.ResponseWriter, r *http.Request) {
	if s.Telephony == nil || s.Telephony.Key == "" || subtle.ConstantTimeCompare([]byte(r.FormValue("key")), []byte(s.Telephony.Key)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	n, err := s.phoneOwnerNumber(r.Context(), r.FormValue("from"))
	if err != nil {
		s.Log.Error("telephony owner", "err", err)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, n)
}

// phoneOwnerNumber is the inner number of the responsible for the client with the phone ("" — none).
func (s *Server) phoneOwnerNumber(ctx context.Context, number string) (string, error) {
	entity, id, _, err := s.phoneClient(ctx, number)
	if err != nil || entity == "" {
		return "", err
	}
	owner, err := s.CRM.Responsible(ctx, entity, id)
	if err != nil || owner == "" {
		return "", err
	}
	return s.Telephony.NumberOf(ctx, owner)
}

// phoneToCRM puts a call with an outside number into the client's card; an unknown number makes a lead or a deal with
// a contact, as the line's «Создавать в CRM» says (else the administrators' «Создавать лид по неизвестному номеру» —
// for incoming calls), its responsible the employee of the call.
func (s *Server) phoneToCRM(ctx context.Context, callID string) error {
	if s.CRM == nil || callID == "" {
		return nil
	}
	c, err := s.Telephony.CallByID(ctx, callID)
	if err != nil {
		return err
	}
	number, dir, emp := c.Outside()
	if number == "" {
		return nil
	}
	entity, id, _, err := s.phoneClient(ctx, number)
	if err != nil {
		return err
	}
	if entity == "" {
		if entity, id, err = s.phoneNewClient(ctx, c, number, dir, emp); err != nil || entity == "" {
			return err
		}
	}
	if err := s.phoneBind(ctx, c, entity, id, emp); err != nil {
		return err
	}
	if err := s.phoneRecall(ctx, c, entity, id); err != nil {
		return err
	}
	return s.phoneTrigger(ctx, c, entity, id)
}

// phoneNewClient makes the card of a number unknown to CRM, as the call's line says (Trunk.Creates): a lead or a deal
// with a contact, the source «Звонок» or the line's advertising channel (with its utm_source); entity "" — none.
func (s *Server) phoneNewClient(ctx context.Context, c telephony.Call, number, dir, emp string) (entity, id string, err error) {
	lead, err := s.Telephony.LeadAuto(ctx)
	if err != nil {
		return "", "", err
	}
	var t telephony.Trunk
	if c.Line != "" {
		trunks, err := s.Telephony.Trunks(ctx)
		if err != nil {
			return "", "", err
		}
		t, _ = telephony.TrunkOf(trunks, c.Line)
	}
	what := t.Creates(dir, lead)
	if what == "" {
		return "", "", nil
	}
	ad, err := s.phoneAdSource(ctx, t)
	if err != nil {
		return "", "", err
	}
	origin := crm.Dict{}
	if dir == "in" {
		origin.Code = "CALL"
	}
	if ad.CRMSource != "" {
		origin = crm.Dict{ID: ad.CRMSource}
	}
	switch what {
	case telephony.CRMLead:
		l := crm.Lead{Title: s.portalTr("tel.lead_title", number), Phone: number, Source: s.portalTr("tel.lead_source"),
			Origin: origin, Responsible: crm.Ref{ID: emp}}
		if ad.ID != "" {
			l.Source = s.portalTr("tel.lead_source_ad", ad.Name)
		}
		id, err = s.CRM.SaveLead(ctx, emp, l)
	case telephony.CRMDeal:
		id, err = s.phoneNewDeal(ctx, emp, number, s.portalTr("tel.deal_title", number), origin)
	}
	if err != nil {
		return "", "", err
	}
	if len(ad.UTM) > 0 {
		if err := s.Marketing.SetUTM(ctx, what, id, marketing.UTM{Source: ad.UTM[0], Medium: "calltracking"}); err != nil {
			return "", "", err
		}
	}
	return what, id, nil
}

// phoneNewDeal is a new contact with the phone and its deal in the first pipeline, both the employee's.
func (s *Server) phoneNewDeal(ctx context.Context, emp, number, title string, origin crm.Dict) (string, error) {
	contact, err := s.CRM.SaveContact(ctx, emp, crm.Contact{Name: number, Phone: number, Origin: origin, Responsible: crm.Ref{ID: emp}})
	if err != nil {
		return "", err
	}
	pipes, err := s.CRM.Pipelines(ctx)
	if err != nil {
		return "", err
	}
	if len(pipes) == 0 {
		return "", fmt.Errorf("telephony: no pipeline for a deal")
	}
	return s.CRM.SaveDeal(ctx, emp, crm.Deal{Title: title, PipelineID: pipes[0].ID, Origin: origin, Contact: crm.Ref{ID: contact},
		Responsible: crm.Ref{ID: emp}})
}

// phoneAdSource is «Колл-трекинг»: the advertising channel whose number the external line is (ID "" — none).
func (s *Server) phoneAdSource(ctx context.Context, t telephony.Trunk) (marketing.AdSource, error) {
	if s.Marketing == nil || t.AdSource == "" {
		return marketing.AdSource{}, nil
	}
	srcs, err := s.Marketing.AdSources(ctx)
	if err != nil {
		return marketing.AdSource{}, err
	}
	for _, a := range srcs {
		if a.ID == t.AdSource {
			return a, nil
		}
	}
	return marketing.AdSource{}, nil
}

// phoneRecall is «Перезвонить»: a client's missed call plans a call deed on the card (the
// administrators' setting); a conversation with the client, either way, closes it.
func (s *Server) phoneRecall(ctx context.Context, c telephony.Call, entity, id string) error {
	number, dir, emp := c.Outside()
	if !c.Missed() {
		_, err := s.CRM.CalledBack(ctx, entity, id, emp)
		return err
	}
	if dir != "in" {
		return nil
	}
	if on, err := s.Telephony.Recalling(ctx); err != nil || !on {
		return err
	}
	at := c.Started
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.CRM.MissedCall(ctx, entity, id, number, emp, s.portalTr("tel.recall_subject", number), at)
	return err
}

// phoneBind writes the call in the card's timeline and remembers the card in the log.
func (s *Server) phoneBind(ctx context.Context, c telephony.Call, entity, id, author string) error {
	number, dir, _ := c.Outside()
	if dir == "in" && c.Missed() {
		dir = "missed"
	}
	body, err := json.Marshal(phoneEvent{ID: c.ID, Dir: dir, Number: number, Sec: c.Seconds, Status: c.Status, Rec: c.Record != "", Note: c.Comment})
	if err != nil {
		return err
	}
	if err := s.CRM.AddEvent(ctx, entity, id, author, "call", string(body)); err != nil {
		return err
	}
	return s.Telephony.SetClient(ctx, c.ID, entity, id)
}

// phoneTrigger tells the client's card of an incoming call: «Входящий звонок» or «Пропущенный звонок» triggers.
func (s *Server) phoneTrigger(ctx context.Context, c telephony.Call, entity, id string) error {
	if _, dir, _ := c.Outside(); dir != "in" {
		return nil
	}
	return s.CRM.FireCall(ctx, entity, id, c.Missed())
}

// phoneCallCRM is «Создать лид» or «Создать сделку» (what=lead|deal) for a call of the log with an outside number
// not yet in CRM: the card gets the number (a deal — with a new contact) and the call.
func (s *Server) phoneCallCRM(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Telephony == nil || s.CRM == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/telephony/calls")
		return
	}
	ctx := r.Context()
	c, err := s.Telephony.CallByID(ctx, r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony call", err)
		return
	}
	if !s.phoneSees(ctx, sess, c) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if p := c.CRMPath(); p != "" { // already someone's call
		http.Redirect(w, r, p, http.StatusSeeOther)
		return
	}
	number, dir, _ := c.Outside()
	if number == "" {
		s.taskErr(w, r, i18n.E("err.telephony_inner"), "/telephony/calls")
		return
	}
	var entity, id string
	switch r.FormValue("what") {
	case "lead":
		l := crm.Lead{Title: s.tr(r, "tel.lead_title", number), Phone: number, Source: s.tr(r, "tel.lead_source"), Responsible: crm.Ref{ID: sess.UserID}}
		if dir == "in" {
			l.Origin.Code = "CALL"
		}
		entity = "lead"
		id, err = s.CRM.SaveLead(ctx, sess.UserID, l)
	case "deal":
		entity = "deal"
		id, err = s.phoneNewDeal(ctx, sess.UserID, number, s.tr(r, "tel.deal_title", number), crm.Dict{})
	default:
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err == nil {
		err = s.phoneBind(ctx, c, entity, id, sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/telephony/calls")
		return
	}
	c.CRMEntity, c.CRMID = entity, id
	http.Redirect(w, r, c.CRMPath(), http.StatusSeeOther)
}

// crmCanRead: may the person see the CRM card (a recorded call in its timeline is theirs to listen to)?
func (s *Server) crmCanRead(ctx context.Context, userID, entity, id string) bool {
	tbl := map[string]string{"deal": "crm_deals", "lead": "crm_leads", "contact": "crm_contacts", "company": "crm_companies"}[entity]
	if s.CRM == nil || tbl == "" || id == "" {
		return false
	}
	var resp string
	if err := s.DB.QueryRow(ctx, `SELECT responsible_id::text FROM `+tbl+` WHERE id::text = $1`, id).Scan(&resp); err != nil {
		return false
	}
	ok, err := s.CRM.Can(ctx, userID, entity, "read", resp)
	return err == nil && ok
}
