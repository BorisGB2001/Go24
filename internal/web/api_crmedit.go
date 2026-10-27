package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- REST API: changing and deleting CRM cards, as crm.*.update and crm.*.delete ----------

// crmInputOf is the card as apiCRMInput: a change starts from it, so the fields not given stay as they are.
func crmInputOf(item any) apiCRMInput {
	var in apiCRMInput
	refs := func(list []crm.Ref) []string {
		out := []string{}
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	comms := func(list []crm.Comm) ([]crm.Comm, []crm.Comm) {
		return append([]crm.Comm{}, crm.CommsOf(list, "phone")...), append([]crm.Comm{}, crm.CommsOf(list, "email")...)
	}
	switch x := item.(type) {
	case crm.Deal:
		in = apiCRMInput{Title: x.Title, Amount: x.Amount, Currency: x.Currency, PipelineID: x.PipelineID, StageID: x.Stage.ID,
			CompanyID: x.Company.ID, ContactIDs: refs(x.Contacts), ResponsibleID: x.Responsible.ID, ObserverIDs: refs(x.Observers),
			CloseDate: x.CloseDate, Comment: x.Comment, Origin: x.Origin.ID, DealType: x.DealType.ID}
	case crm.Lead:
		in = apiCRMInput{Title: x.Title, Name: x.Name, CompanyTitle: x.CompanyTitle, Source: x.Source, Amount: x.Amount, Currency: x.Currency,
			StageID: x.Stage.ID, ResponsibleID: x.Responsible.ID, ObserverIDs: refs(x.Observers), Comment: x.Comment, Origin: x.Origin.ID}
		in.Phones, in.Emails = comms(x.Comms)
	case crm.Contact:
		in = apiCRMInput{Name: x.Name, Position: x.Position, CompanyID: x.Company.ID, ResponsibleID: x.Responsible.ID, Comment: x.Comment,
			Origin: x.Origin.ID, ContactType: x.ContactType.ID}
		in.Phones, in.Emails = comms(x.Comms)
	case crm.Company:
		in = apiCRMInput{Title: x.Title, Website: x.Website, Address: x.Address, ResponsibleID: x.Responsible.ID, Comment: x.Comment,
			CompanyType: x.CompanyType.ID, Industry: x.Industry.ID}
		in.Phones, in.Emails = comms(x.Comms)
	}
	return in
}

// mainComm puts value first among the card's phones (or e-mails) in place of the main one; "" removes the main one.
func mainComm(list []crm.Comm, value string) []crm.Comm {
	if len(list) > 0 {
		list = list[1:]
	}
	if value == "" {
		return append([]crm.Comm{}, list...)
	}
	return append([]crm.Comm{{Value: value}}, list...)
}

// PATCH /api/v1/crm/{kind}/{id} — changes only the fields given (as POST /api/v1/crm/{kind}); "stage_id" moves a deal or
// a lead to that stage, "phone" and "email" replace the main phone and e-mail.
func (s *Server) apiCRMUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	item, err := s.apiCRMItem(r, p, "write")
	if err != nil {
		return err
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return i18n.E("err.bad_json", err.Error())
	}
	var given map[string]json.RawMessage
	if err := json.Unmarshal(body, &given); err != nil {
		return i18n.E("err.bad_json", err.Error())
	}
	in := crmInputOf(item)
	if _, ok := given["contact_id"]; ok && given["contact_ids"] == nil {
		in.ContactIDs = nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return i18n.E("err.bad_json", err.Error())
	}
	if _, ok := given["phone"]; ok && given["phones"] == nil {
		in.Phones = mainComm(in.Phones, in.Phone)
	}
	if _, ok := given["email"]; ok && given["emails"] == nil {
		in.Emails = mainComm(in.Emails, in.Email)
	}
	if in.ResponsibleID == "" {
		in.ResponsibleID = p.UserID
	}
	if _, ok := given["responsible_id"]; ok {
		if err := s.CRM.Check(r.Context(), p.UserID, crmEntities[kind], "write", in.ResponsibleID); err != nil {
			return err
		}
	}
	ref := crm.Ref{ID: in.ResponsibleID}
	phone, email := in.phoneEmail()
	switch x := item.(type) {
	case crm.Deal:
		d := crm.Deal{ID: id, Title: in.Title, PipelineID: x.PipelineID, Amount: in.Amount, Currency: in.Currency, Company: crm.Ref{ID: in.CompanyID},
			Contact: crm.Ref{ID: in.ContactID}, Responsible: ref, CloseDate: in.CloseDate, Comment: in.Comment, Origin: dictIn(in.Origin),
			DealType: dictIn(in.DealType), Observers: observerRefs(in.ObserverIDs)}
		if d.Observers == nil {
			d.Observers = []crm.Ref{}
		}
		for _, c := range in.ContactIDs {
			d.Contacts = append(d.Contacts, crm.Ref{ID: c})
		}
		if _, err = s.CRM.SaveDeal(r.Context(), p.UserID, d); err == nil && in.StageID != x.Stage.ID {
			_, err = s.CRM.MoveDeal(r.Context(), p.UserID, id, in.StageID)
		}
		if _, ok := given["parent_ids"]; ok && err == nil {
			for _, pid := range in.ParentIDs {
				if err = s.apiCRMReadable(r, p, pid); err != nil {
					return err
				}
			}
			err = s.CRM.SetParents(r.Context(), p.UserID, id, in.ParentIDs)
		}
	case crm.Lead:
		l := crm.Lead{ID: id, Title: in.Title, Name: in.Name, CompanyTitle: in.CompanyTitle, Phone: phone, Email: email, Comms: in.comms(),
			Source: in.Source, Amount: in.Amount, Currency: in.Currency, Responsible: ref, Comment: in.Comment, Origin: dictIn(in.Origin),
			Observers: observerRefs(in.ObserverIDs)}
		if l.Observers == nil {
			l.Observers = []crm.Ref{}
		}
		if _, err = s.CRM.SaveLead(r.Context(), p.UserID, l); err == nil && in.StageID != x.Stage.ID {
			_, err = s.CRM.MoveLead(r.Context(), p.UserID, id, in.StageID)
		}
	case crm.Contact:
		_, err = s.CRM.SaveContact(r.Context(), p.UserID, crm.Contact{ID: id, Name: in.Name, Position: in.Position, Phone: phone, Email: email,
			Comms: in.comms(), Company: crm.Ref{ID: in.CompanyID}, Responsible: ref, Comment: in.Comment, Origin: dictIn(in.Origin),
			ContactType: dictIn(in.ContactType)})
	case crm.Company:
		_, err = s.CRM.SaveCompany(r.Context(), p.UserID, crm.Company{ID: id, Title: in.Title, Phone: phone, Email: email, Comms: in.comms(),
			Website: in.Website, Address: in.Address, Responsible: ref, Comment: in.Comment, CompanyType: dictIn(in.CompanyType),
			Industry: dictIn(in.Industry)})
	}
	if err == nil && in.UTM != nil && (kind == "deals" || kind == "leads") {
		err = s.setUTM(r.Context(), crmEntities[kind], id, in.UTM.utm())
	}
	if err != nil {
		return err
	}
	if item, err = s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	return s.writeCRMCard(w, r, http.StatusOK, item)
}

// DELETE /api/v1/crm/{kind}/{id} — the card goes to the CRM's recycle bin (it is restored from there).
func (s *Server) apiCRMDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	ctx, id := r.Context(), r.PathValue("id")
	var err error
	switch crmEntities[r.PathValue("kind")] {
	case "deal":
		err = s.CRM.DeleteDeal(ctx, p.UserID, id)
	case "lead":
		err = s.CRM.DeleteLead(ctx, p.UserID, id)
	case "contact":
		err = s.CRM.DeleteContact(ctx, p.UserID, id)
	case "company":
		err = s.CRM.DeleteCompany(ctx, p.UserID, id)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
