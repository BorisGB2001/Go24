package bp

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"time"

	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// The steps of the «CRM» group of the designer on any card (CRMKinds): «Получить информацию об элементе CRM»
// (crmread) puts the card's fields into the process, «Конвертация лида» (crmconvert) makes a deal, a contact and a
// company of a lead, «Удалить элемент CRM» (crmdelete) moves the card to the CRM's recycle bin, «Смена воронки»
// (crmmove) moves a deal into a stage of another pipeline, «Копирование сделки» (crmcopy) copies it into a stage,
// «Изменение наблюдателей» (crmobservers) and «Завершение дел» (crmclose) — as the robots of the same names, «Смена
// ответственного» (crmresponsible) gives the card to one of the people, «Добавление товара» (crmproduct) adds a row of
// the catalog to a deal or a lead, «Изменить элемент CRM» (crmupdate) sets the fields of any card — a smart process's
// element too, being a deal. They act as robots do: without the rights of the starter, whom the card's timeline shows.

// stepCard is the card a step of CRMKinds works on, of the kind Entity ("" — the process's): of its Value ({…} filled —
// an id or a link /crm/…), or else the process's card, or else the one the process made last (of that kind).
func (s *Store) stepCard(ctx context.Context, x Instance, st Step, entity string) (string, error) {
	v := strings.TrimSpace(s.fillRaw(ctx, x, st.Value))
	if v == "" && st.Value == "" {
		if x.Entity == entity {
			return x.EntityID, nil
		}
		v = x.Values["created_card"]
	}
	if i := strings.Index(v, "/crm/"); i >= 0 {
		kind, id, _ := strings.Cut(strings.TrimPrefix(v[i:], "/crm/"), "/")
		if CardPath(entity, "") != "/crm/"+kind+"/" {
			return "", i18n.E("err.bp_crm_card")
		}
		v = id
	}
	if v == "" || strings.ContainsAny(v, "/ ") {
		return "", i18n.E("err.bp_crm_card")
	}
	return v, nil
}

// crmStep does a step of CRMKinds and says what for the process's history.
func (s *Store) crmStep(ctx context.Context, x Instance, st Step) (string, error) {
	entity := cmp.Or(st.Entity, x.Entity)
	switch st.Kind {
	case "crmconvert":
		entity = "lead"
	case "crmmove", "crmcopy":
		entity = "deal"
	}
	id, err := s.stepCard(ctx, x, st, entity)
	if err != nil {
		return "", err
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	switch st.Kind {
	case "crmupdate":
		return "crmupdate:" + CardPath(entity, id), s.updateCardOf(ctx, x, entity, id, st.Sets)
	case "crmread":
		got, err := s.crmInfo(ctx, entity, id)
		if err != nil {
			return "", err
		}
		for _, k := range CRMInfo {
			x.Values[CRMInfoPrefix+k] = got[k]
		}
		return "crmread:" + CardPath(entity, id), s.saveValues(ctx, x)
	case "crmdelete":
		admin := tasks.WithAdmin(ctx) // as a robot: whatever the starter's rights
		switch entity {
		case "deal":
			err = s.CRM.DeleteDeal(admin, x.StartedBy.ID, id)
		case "lead":
			err = s.CRM.DeleteLead(admin, x.StartedBy.ID, id)
		case "contact":
			err = s.CRM.DeleteContact(admin, x.StartedBy.ID, id)
		case "company":
			err = s.CRM.DeleteCompany(admin, x.StartedBy.ID, id)
		}
		return "crmdelete:" + CardPath(entity, id), err
	case "crmmove":
		_, err := s.CRM.MoveDeal(ctx, x.StartedBy.ID, id, st.Stage)
		return "crmmove:" + CardPath(entity, id), err
	case "crmcopy": // the copy's fields are set as by «Изменить карточку»; its link — {created_card}
		copied, err := s.CRM.TunnelDeal(ctx, x.StartedBy.ID, id, st.Stage)
		if err != nil {
			return "", err
		}
		x.Values["created_card"] = CardPath(entity, copied)
		if err := s.saveValues(ctx, x); err != nil {
			return "", err
		}
		if len(st.Sets) > 0 {
			if err := s.updateCardOf(ctx, x, entity, copied, st.Sets); err != nil {
				return "", err
			}
		}
		return "crmcopy:" + CardPath(entity, copied), nil
	case "crmobservers":
		var users []string
		if len(st.Users) > 0 { // the people of the step: employees, the starter, a user field (all of a multiple one)
			if users, err = s.people(ctx, x, Step{To: st.Users[0], Users: st.Users[1:]}); err != nil {
				return "", err
			}
		}
		return "crmobservers:" + CardPath(entity, id), s.CRM.ChangeObservers(ctx, x.StartedBy.ID, entity, id, st.Mode, users)
	case "crmclose":
		n, err := s.CRM.CloseActivities(ctx, x.StartedBy.ID, entity, id, st.Deed)
		return "crmclose:" + strconv.Itoa(n) + ":" + CardPath(entity, id), err
	case "crmresponsible": // at random or in turn, the absent skipped (To — the reserve when all are)
		user, err := s.pick(ctx, x, st)
		if err != nil {
			return "", err
		}
		s.log(ctx, x.ID, x.Step, user, "picked", "")
		return "crmresponsible:" + CardPath(entity, id), s.updateCardOf(ctx, x, entity, id, []Assign{{Field: "responsible", Value: user}})
	case "crmproduct":
		return "crmproduct:" + CardPath(entity, id), s.addProductRow(ctx, x, st, entity, id)
	}
	l, err := s.CRM.Lead(ctx, id)
	if err != nil {
		return "", err
	}
	to, err := s.CRM.ConvertDefaults(ctx, l)
	if err != nil {
		return "", err
	}
	if len(st.Into) > 0 { // the cards chosen; a contact and a company — the clients found, or new ones
		to.Deal, to.Contact, to.Company = contains(st.Into, "deal"), contains(st.Into, "contact"), contains(st.Into, "company")
	}
	got, err := s.CRM.ConvertLeadTo(ctx, x.StartedBy.ID, id, to)
	if err != nil {
		return "", err
	}
	for kind, v := range map[string]string{"deal": got.DealID, "contact": got.ContactID, "company": got.CompanyID} {
		x.Values["converted_"+kind], x.Values["converted_"+kind+"_id"] = "", v
		if v != "" {
			x.Values["converted_"+kind] = CardPath(kind, v)
		}
	}
	if got.DealID != "" {
		x.Values["created_card"] = CardPath("deal", got.DealID)
	}
	return "crmconvert:" + CardPath("lead", id), s.saveValues(ctx, x)
}

// crmInfo are the fields of CRMInfo of a card (by their keys without the prefix).
func (s *Store) crmInfo(ctx context.Context, entity, id string) (map[string]string, error) {
	out := map[string]string{"id": id, "link": CardPath(entity, id)}
	sum := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	link := func(kind string, r crm.Ref) string {
		if r.ID == "" {
			return ""
		}
		return CardPath(kind, r.ID)
	}
	switch entity {
	case "deal":
		d, err := s.CRM.Deal(ctx, id)
		if err != nil {
			return nil, err
		}
		out["title"], out["amount"], out["stage"], out["responsible"], out["comment"] = d.Title, sum(d.Amount), d.Stage.Name, d.Responsible.ID, d.Comment
		out["contact"], out["company"] = link("contact", d.Contact), link("company", d.Company)
		if d.CloseDate != nil {
			out["close_date"] = d.CloseDate.Format(time.DateOnly)
		}
	case "lead":
		l, err := s.CRM.Lead(ctx, id)
		if err != nil {
			return nil, err
		}
		out["title"], out["name"], out["company_title"], out["phone"], out["email"] = l.Title, l.Name, l.CompanyTitle, l.Phone, l.Email
		out["amount"], out["stage"], out["responsible"], out["comment"] = sum(l.Amount), l.Stage.Name, l.Responsible.ID, l.Comment
		out["contact"], out["company"] = link("contact", crm.Ref{ID: l.ContactID}), link("company", crm.Ref{ID: l.CompanyID})
	case "contact":
		c, err := s.CRM.Contact(ctx, id)
		if err != nil {
			return nil, err
		}
		out["title"], out["name"], out["position"], out["phone"], out["email"] = c.Name, c.Name, c.Position, c.Phone, c.Email
		out["responsible"], out["comment"], out["company"] = c.Responsible.ID, c.Comment, link("company", c.Company)
	case "company":
		c, err := s.CRM.Company(ctx, id)
		if err != nil {
			return nil, err
		}
		out["title"], out["phone"], out["email"], out["website"], out["address"] = c.Title, c.Phone, c.Email, c.Website, c.Address
		out["responsible"], out["comment"] = c.Responsible.ID, c.Comment
	}
	return out, nil
}

// addProductRow puts the step's product at the end of the card's rows: its name, unit and VAT of the catalog, the price
// the step's or the catalog's, the quantity and the discount of the step ({…} filled, formulas counted).
func (s *Store) addProductRow(ctx context.Context, x Instance, st Step, entity, id string) error {
	p, err := s.CRM.Product(ctx, st.Product)
	if err != nil {
		return err
	}
	row := crm.ProductRow{ProductID: p.ID, Name: p.Name, Unit: p.Unit, Price: p.Price, Quantity: 1, VATRate: p.VATRate, VATIncluded: p.VATIncluded}
	for _, f := range []struct {
		v  string
		to *float64
	}{{st.Price, &row.Price}, {st.Quantity, &row.Quantity}, {st.Discount, &row.Discount}} {
		if f.v == "" {
			continue
		}
		v, err := s.value(ctx, x, f.v)
		if err != nil {
			return err
		}
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if *f.to, err = number(v); err != nil {
			return i18n.E("err.crm_amount")
		}
	}
	rows, err := s.CRM.ProductRows(ctx, entity, id)
	if err != nil {
		return err
	}
	_, err = s.CRM.SetProductRows(ctx, x.StartedBy.ID, entity, id, append(rows, row))
	return err
}
