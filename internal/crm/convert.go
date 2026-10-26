package crm

import (
	"context"
	"slices"
	"strings"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Конвертация» of a lead: the lead gives a deal (in a chosen pipeline), a contact and a company —
// any of the three; the contact and the company are new ones made of the lead's fields, or existing clients. The
// lead's own fields go to the like-named fields (of the same kind) of the new cards, its products to the deal, and
// the lead goes to its success stage.

// ConvertTo is what to make of a lead: PipelineID "" — the first pipeline of deals; ContactID, CompanyID "" — new.
type ConvertTo struct {
	Deal, Contact, Company           bool
	PipelineID, ContactID, CompanyID string
}

// Converted is what a lead was converted into.
type Converted struct{ DealID, ContactID, CompanyID string }

// ConvertDefaults is what the dialog offers for the lead: a deal unless it has one, a contact and a company when the
// lead names them — the ones it was converted into, or clients found with its phone, e-mail, name or company title.
func (s *Store) ConvertDefaults(ctx context.Context, l Lead) (ConvertTo, error) {
	to := ConvertTo{Deal: l.DealID == "", ContactID: l.ContactID, CompanyID: l.CompanyID}
	if to.ContactID == "" && (l.Phone != "" || l.Email != "" || l.Name != "") {
		pk, em, nm := phoneKey(l.Phone), strings.ToLower(strings.TrimSpace(l.Email)), strings.ToLower(strings.TrimSpace(l.Name))
		err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM crm_contacts
			WHERE `+commMatch("contact", "crm_contacts", "$1", "$2")+` OR ($3 <> '' AND lower(name) = $3)
			ORDER BY `+commHas("contact", "crm_contacts", "phone", "$1")+` DESC, `+commHas("contact", "crm_contacts", "email", "$2")+` DESC, created_at LIMIT 1), '')`,
			pk, em, nm).Scan(&to.ContactID)
		if err != nil {
			return to, err
		}
	}
	if to.CompanyID == "" && strings.TrimSpace(l.CompanyTitle) != "" {
		err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM crm_companies WHERE lower(title) = lower($1)
			ORDER BY created_at LIMIT 1), '')`, strings.TrimSpace(l.CompanyTitle)).Scan(&to.CompanyID)
		if err != nil {
			return to, err
		}
	}
	to.Contact = l.Name != "" || to.ContactID != ""
	to.Company = l.CompanyTitle != "" || to.CompanyID != ""
	return to, nil
}

// ConvertLead converts the lead as the dialog offers it by default (ConvertDefaults) and gives the deal.
func (s *Store) ConvertLead(ctx context.Context, userID, id string) (string, error) {
	l, err := s.Lead(ctx, id)
	if err != nil {
		return "", err
	}
	if l.DealID != "" {
		return l.DealID, nil
	}
	to, err := s.ConvertDefaults(ctx, l)
	if err != nil {
		return "", err
	}
	got, err := s.ConvertLeadTo(ctx, userID, id, to)
	return got.DealID, err
}

// ConvertLeadTo makes of the lead what to asks. A deal it already has is kept (not made again).
func (s *Store) ConvertLeadTo(ctx context.Context, userID, id string, to ConvertTo) (Converted, error) {
	l, err := s.Lead(ctx, id)
	if err != nil {
		return Converted{}, err
	}
	if !to.Deal && !to.Contact && !to.Company {
		return Converted{}, i18n.E("err.crm_convert_nothing")
	}
	got := Converted{DealID: l.DealID}
	var made []string // "entity:id" of the cards made or chosen now, for the lead's timeline
	if to.Company {
		if to.CompanyID != "" {
			c, err := s.Company(ctx, to.CompanyID)
			if err != nil {
				return got, err
			}
			got.CompanyID = c.ID
		} else {
			title := strings.TrimSpace(l.CompanyTitle)
			if title == "" {
				title = l.Title
			}
			if got.CompanyID, err = s.SaveCompany(ctx, userID, Company{Title: title, Responsible: l.Responsible}); err != nil {
				return got, err
			}
			if err := s.copyLeadFields(ctx, id, "company", got.CompanyID); err != nil {
				return got, err
			}
		}
		if got.CompanyID != l.CompanyID { // a card already linked is not told of again
			made = append(made, "company:"+got.CompanyID)
		}
	}
	if to.Contact {
		if to.ContactID != "" {
			c, err := s.Contact(ctx, to.ContactID)
			if err != nil {
				return got, err
			}
			got.ContactID = c.ID
		} else {
			name := strings.TrimSpace(l.Name)
			if name == "" {
				name = l.Title
			}
			if got.ContactID, err = s.SaveContact(ctx, userID, Contact{Name: name, Phone: l.Phone, Email: l.Email, Comms: l.Comms,
				Company: Ref{ID: got.CompanyID}, Origin: l.Origin, Responsible: l.Responsible}); err != nil {
				return got, err
			}
			if err := s.copyLeadFields(ctx, id, "contact", got.ContactID); err != nil {
				return got, err
			}
		}
		if got.ContactID != l.ContactID { // a card already linked is not told of again
			made = append(made, "contact:"+got.ContactID)
		}
	}
	if to.Deal && l.DealID == "" {
		pipeline := to.PipelineID
		if err := s.db.QueryRow(ctx, `SELECT id FROM crm_pipelines WHERE type_id IS NULL AND ($1 = '' OR id::text = $1)
			ORDER BY sort, created_at LIMIT 1`, pipeline).Scan(&pipeline); err != nil {
			return got, i18n.E("err.crm_stage")
		}
		if got.DealID, err = s.SaveDeal(ctx, userID, Deal{Title: l.Title, PipelineID: pipeline, Amount: l.Amount, Currency: l.Currency,
			Company: Ref{ID: got.CompanyID}, Contact: Ref{ID: got.ContactID}, Origin: l.Origin, Responsible: l.Responsible,
			Comment: l.Comment}); err != nil {
			return got, err
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO crm_product_rows (entity, entity_id, product_id, name, price, quantity, unit, discount,
			vat_rate, vat_included, sort) SELECT 'deal', $2, product_id, name, price, quantity, unit, discount, vat_rate, vat_included, sort
			FROM crm_product_rows WHERE entity = 'lead' AND entity_id = $1`, id, got.DealID); err != nil { // the lead's products go to the deal
			return got, err
		}
		if err := s.copyLeadFields(ctx, id, "deal", got.DealID); err != nil {
			return got, err
		}
		if _, err := s.db.Exec(ctx, `UPDATE crm_invoices SET deal_id = $2, lead_id = NULL, updated_at = now() WHERE lead_id = $1`,
			id, got.DealID); err != nil { // and its quotes and invoices
			return got, err
		}
		made = append(made, "deal:"+got.DealID)
	}
	if _, err := s.db.Exec(ctx, `UPDATE crm_leads SET deal_id = coalesce(nullif($2, '')::uuid, deal_id),
		contact_id = coalesce(nullif($3, '')::uuid, contact_id), company_id = coalesce(nullif($4, '')::uuid, company_id), updated_at = now(),
		stage_id = coalesce((SELECT id FROM crm_stages WHERE entity = 'lead' AND semantic = 'success' ORDER BY sort LIMIT 1), stage_id)
		WHERE id = $1`, id, got.DealID, got.ContactID, got.CompanyID); err != nil {
		return got, err
	}
	for _, m := range made {
		entity, cardID, _ := strings.Cut(m, ":")
		if err := s.event(ctx, s.db, "lead", id, userID, "converted", m); err != nil {
			return got, err
		}
		if err := s.event(ctx, s.db, entity, cardID, userID, "converted", "lead:"+id); err != nil {
			return got, err
		}
	}
	if nl, err := s.Lead(ctx, id); err == nil && nl.Stage.ID != l.Stage.ID {
		s.runRobots(ctx, userID, nl.Stage.ID, leadCard(nl))
	}
	return got, nil
}

// copyLeadFields gives the card the values of the lead's own fields that it has too: the same name and kind (and,
// for a list, the same item).
func (s *Store) copyLeadFields(ctx context.Context, leadID, entity, id string) error {
	ts := s.Tasks
	if ts == nil {
		ts = tasks.NewStore(s.db)
	}
	from, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["lead"], leadID)
	if err != nil || len(from) == 0 {
		return err
	}
	defs, err := ts.FieldsOf(ctx, tasks.CRMFieldSets[entity])
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, v := range from {
		if v.Value == "" {
			continue
		}
		for _, f := range defs {
			if strings.EqualFold(f.Name, v.Name) && f.Kind == v.Kind && (f.Kind != "list" || slices.Contains(f.Options, v.Value)) {
				values[f.ID] = v.Value
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	return ts.SetFieldValuesOf(ctx, tasks.CRMFieldSets[entity], id, values)
}
