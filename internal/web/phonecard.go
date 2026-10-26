package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/telephony"
)

// ---------- «Карточка звонка» of the softphone: the client, their deals, the calls before ----------

// cardCalls is how many earlier calls with the number the card shows.
const cardCalls = 5

// phoneCard is the call card of the softphone for the number on the other end (JSON): the client of CRM (the card,
// its company, responsible and open deals — when the employee may read it; else the name only), «Создать лид» and
// «Создать контакт» for an unknown number, the last calls with it the employee may see and the comment written on the
// call so far. at is go24's time: the softphone sends it back with the comment, so it finds this very call.
func (s *Server) phoneCard(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if _, ok := s.phoneLine(ctx, sess.UserID); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tr(r, "err.telephony_no_number")})
		return
	}
	n := telephony.Dialable(r.FormValue("number"))
	if n == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "phone.bad_number")})
		return
	}
	card := map[string]any{"at": time.Now().Unix()}
	if _, colleague, err := s.Telephony.WhoIs(ctx, n); err != nil {
		s.jsonErr(w, r, err)
		return
	} else if colleague { // a colleague: no client, no card
		card["colleague"] = true
		writeJSON(w, http.StatusOK, card)
		return
	}
	entity, id, name, err := s.phoneClient(ctx, n)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if entity != "" {
		client, err := s.cardClient(r, sess, entity, id, name)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		card["client"] = client
	} else if s.CRM != nil {
		create := []map[string]string{}
		for _, e := range []string{"lead", "contact"} {
			if ok, err := s.CRM.Can(ctx, sess.UserID, e, "write", sess.UserID); err == nil && ok {
				create = append(create, map[string]string{"what": e, "title": s.tr(r, "phone.card_new_"+e)})
			}
		}
		card["create"] = create
	}
	// the calls with the number before, the employee's rights of the call log allowing
	list, err := s.Telephony.Calls(ctx, telephony.CallFilter{Number: telephony.BlackKey(n), Limit: 30})
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	tr := func(key string, args ...any) string { return s.tr(r, key, args...) }
	calls := []map[string]string{}
	for _, c := range list {
		if len(calls) == cardCalls {
			break
		}
		if !s.phoneSees(ctx, sess, c) {
			continue
		}
		_, dir, _ := c.Outside()
		if dir == "in" && c.Missed() {
			dir = "missed"
		}
		who := c.FromUser.Name
		if dir != "out" {
			who = c.ToUser.Name
		}
		calls = append(calls, map[string]string{"at": c.Started.Local().Format(tr("format.datetime")), "dir": dir, "kind": tr("tel.ev." + dir),
			"status": tr("tel.status." + c.Status), "time": formatDuration(tr, c.Seconds), "who": who, "comment": c.Comment})
	}
	card["calls"] = calls
	if card["comment"], err = s.Telephony.Pending(ctx, sess.UserID, n); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, card)
}

// cardClient is the client of the call card: what kind of card and its name; read — its link, responsible, company
// (of a contact), stage (of a lead) and open deals.
func (s *Server) cardClient(r *http.Request, sess auth.Session, entity, id, name string) (map[string]any, error) {
	ctx := r.Context()
	cl := map[string]any{"kind": s.tr(r, "crm."+entity), "name": name}
	if !s.crmCanRead(ctx, sess.UserID, entity, id) {
		return cl, nil
	}
	cl["url"] = telephony.Call{CRMEntity: entity, CRMID: id}.CRMPath()
	stage := func(st crm.Stage) string {
		if st.Code != "" {
			return s.tr(r, "crm.stage."+st.Code)
		}
		return st.Name
	}
	var company, contact string
	switch entity {
	case "contact":
		c, err := s.CRM.Contact(ctx, id)
		if err != nil {
			return nil, err
		}
		cl["responsible"] = c.Responsible.Name
		if c.Company.ID != "" {
			cl["company"], cl["company_url"] = c.Company.Name, "/crm/companies/"+c.Company.ID
		}
		contact = id
	case "company":
		c, err := s.CRM.Company(ctx, id)
		if err != nil {
			return nil, err
		}
		cl["responsible"] = c.Responsible.Name
		company = id
	case "lead":
		l, err := s.CRM.Lead(ctx, id)
		if err != nil {
			return nil, err
		}
		cl["responsible"], cl["stage"] = l.Responsible.Name, stage(l.Stage)
		return cl, nil
	}
	deals, err := s.CRM.Deals(ctx, sess.UserID, "", "", company, contact)
	if err != nil {
		return nil, err
	}
	open := []map[string]string{}
	for _, d := range deals {
		if d.Stage.Closed() || d.TypeID != "" {
			continue
		}
		open = append(open, map[string]string{"title": d.Title, "url": "/crm/deals/" + d.ID, "stage": stage(d.Stage),
			"color": d.Stage.Color, "amount": crm.Amount(d.Amount) + " " + d.Currency})
		if len(open) == 5 {
			break
		}
	}
	cl["deals"] = open
	return cl, nil
}

// phoneNote is «Комментарий к звонку» from the call card: the call that began at at (go24's time of the card) or later
// gets it — the one going on gets it when Asterisk reports its end.
func (s *Server) phoneNote(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if _, ok := s.phoneLine(ctx, sess.UserID); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tr(r, "err.telephony_no_number")})
		return
	}
	n := telephony.Dialable(r.FormValue("number"))
	at, _ := strconv.ParseInt(r.FormValue("at"), 10, 64)
	if n == "" || at <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "phone.bad_number")})
		return
	}
	text := strings.TrimSpace(r.FormValue("comment"))
	if len([]rune(text)) > 2000 {
		text = string([]rune(text)[:2000])
	}
	id, err := s.Telephony.SetComment(ctx, sess.UserID, n, time.Unix(at, 0), text)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "call": id})
}

// phoneCardCreate is «Создать лид» or «Создать контакт» (what=) of the call card for an unknown number: the card with
// the number, the employee responsible; the call goes into it when it ends (the client is found by the number then).
func (s *Server) phoneCardCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if _, ok := s.phoneLine(ctx, sess.UserID); !ok || s.CRM == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tr(r, "err.forbidden")})
		return
	}
	n := telephony.Dialable(r.FormValue("number"))
	if n == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "phone.bad_number")})
		return
	}
	if entity, id, _, err := s.phoneClient(ctx, n); err != nil {
		s.jsonErr(w, r, err)
		return
	} else if entity != "" { // made meanwhile (by a colleague, in another window)
		writeJSON(w, http.StatusOK, map[string]string{"url": telephony.Call{CRMEntity: entity, CRMID: id}.CRMPath()})
		return
	}
	what := r.FormValue("what")
	if what != "lead" && what != "contact" {
		http.Error(w, "bad kind", http.StatusBadRequest)
		return
	}
	if err := s.CRM.Check(ctx, sess.UserID, what, "write", sess.UserID); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	var id string
	var err error
	if what == "lead" {
		l := crm.Lead{Title: s.tr(r, "tel.lead_title", n), Phone: n, Source: s.tr(r, "tel.lead_source"), Responsible: crm.Ref{ID: sess.UserID}}
		if r.FormValue("dir") == "in" {
			l.Origin.Code = "CALL"
		}
		id, err = s.CRM.SaveLead(ctx, sess.UserID, l)
	} else {
		id, err = s.CRM.SaveContact(ctx, sess.UserID, crm.Contact{Name: n, Phone: n, Responsible: crm.Ref{ID: sess.UserID}})
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": telephony.Call{CRMEntity: what, CRMID: id}.CRMPath()})
}
