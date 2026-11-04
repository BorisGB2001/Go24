package crm

import (
	"context"

	"go24/internal/tasks"
)

// Copy makes a new card of the same kind from a card «Копировать»: its fields, own fields, phones and
// e-mails, client, contacts, observers, product rows (without their reserves) and the parents of a smart process
// element go to the copy; the timeline, activities, documents and invoices stay with the original. The copy is
// created as any new card: its robots run. resp — the copy's responsible ("" — the original's).
func (s *Store) Copy(ctx context.Context, userID, entity, id, resp string) (string, error) {
	var newID string
	var err error
	switch entity {
	case "deal":
		var d Deal
		if d, err = s.Deal(ctx, id); err != nil {
			return "", err
		}
		d.ID, d.ClosedAt = "", nil
		if resp != "" {
			d.Responsible = Ref{ID: resp}
		}
		if d.Observers == nil {
			d.Observers = []Ref{}
		}
		if newID, err = s.SaveDeal(ctx, userID, d); err == nil && d.TypeID != "" {
			var parents []Deal
			if parents, err = s.Parents(ctx, id); err == nil && len(parents) > 0 {
				ids := make([]string, len(parents))
				for i, p := range parents {
					ids[i] = p.ID
				}
				err = s.SetParents(ctx, userID, newID, ids)
			}
		}
	case "lead":
		var l Lead
		if l, err = s.Lead(ctx, id); err != nil {
			return "", err
		}
		l.ID, l.DealID, l.ContactID, l.CompanyID, l.Repeat = "", "", "", "", false
		if resp != "" {
			l.Responsible = Ref{ID: resp}
		}
		if l.Observers == nil {
			l.Observers = []Ref{}
		}
		newID, err = s.SaveLead(ctx, userID, l)
	case "contact":
		var c Contact
		if c, err = s.Contact(ctx, id); err != nil {
			return "", err
		}
		c.ID = ""
		if resp != "" {
			c.Responsible = Ref{ID: resp}
		}
		newID, err = s.SaveContact(ctx, userID, c)
	case "company":
		var c Company
		if c, err = s.Company(ctx, id); err != nil {
			return "", err
		}
		c.ID = ""
		if resp != "" {
			c.Responsible = Ref{ID: resp}
		}
		newID, err = s.SaveCompany(ctx, userID, c)
	default:
		return "", ErrNotFound
	}
	if err != nil {
		return newID, err
	}
	if err := s.copyFields(ctx, entity, id, newID); err != nil {
		return newID, err
	}
	if entity == "deal" || entity == "lead" {
		rows, err := s.ProductRows(ctx, entity, id)
		if err != nil || len(rows) == 0 {
			return newID, err
		}
		for i := range rows {
			rows[i].Reserve, rows[i].ReserveUntil = 0, nil
		}
		if _, err := s.SetProductRows(ctx, userID, entity, newID, rows); err != nil {
			return newID, err
		}
	}
	return newID, nil
}

// copyFields puts a card's own field values on its copy (without logging them as changes: the copy is new).
func (s *Store) copyFields(ctx context.Context, entity, from, to string) error {
	set, ts := tasks.CRMFieldSets[entity], s.Tasks
	if ts == nil {
		ts = tasks.NewStore(s.db)
	}
	vals, err := ts.FieldValuesOf(ctx, set, from)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, v := range vals {
		if v.Value != "" {
			values[v.ID] = v.Value
		}
	}
	if len(values) == 0 {
		return nil
	}
	return ts.SetFieldValuesOf(ctx, set, to, values)
}
