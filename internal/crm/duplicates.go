package crm

import (
	"context"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Контроль дубликатов»: a lead, a contact or a company with the same phone (its last ten digits), the
// same e-mail, the same name (a company's title) or the ИНН of its requisites as another card is shown as a possible duplicate on its card; two
// contacts or two companies are merged into one — the deals, contacts, deeds, timeline, requisites and fields of the other go to
// the kept one, and the other is deleted.

// Duplicate is a card that looks like another one.
type Duplicate struct {
	Entity, ID, Title, Phone, Email string
	By                              string // phone | email | name
}

// Path is the card's page.
func (d Duplicate) Path() string { return "/crm/" + plural[d.Entity] + "/" + d.ID }

// phoneKey is the last ten digits of a phone ("" if fewer than seven): +7 (900) 123-45-67 and 89001234567 match.
func phoneKey(p string) string {
	d := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, p)
	if len(d) < 7 {
		return ""
	}
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	return d
}

// phoneSQL is the SQL of phoneKey on a column.
func phoneSQL(col string) string {
	return `right(regexp_replace(` + col + `, '\D', '', 'g'), 10)`
}

// Duplicates are the leads, contacts and companies like the card (entity lead, contact or company; id ""
// for a card not yet made) by its phone, e-mail and name.
func (s *Store) Duplicates(ctx context.Context, entity, id, name, phone, email string) ([]Duplicate, error) {
	pk, em, nm := phoneKey(phone), strings.ToLower(strings.TrimSpace(email)), strings.ToLower(strings.TrimSpace(name))
	if pk == "" && em == "" && nm == "" && id == "" {
		return nil, nil
	}
	by := func(entity, alias string) string { // which of the card's phones or e-mails matched
		return `CASE WHEN ` + commHas(entity, alias, "phone", "$1") + ` THEN 'phone' WHEN ` + commHas(entity, alias, "email", "$2") + ` THEN 'email'`
	}
	rows, err := s.db.Query(ctx, `
		SELECT 'contact', x.id::text, x.name, x.phone, x.email, `+by("contact", "x")+` ELSE 'name' END
		FROM crm_contacts x WHERE NOT ($5 = 'contact' AND x.id::text = $4)
			AND (`+commMatch("contact", "x", "$1", "$2")+` OR ($5 = 'contact' AND $3 <> '' AND lower(x.name) = $3))
		UNION ALL
		SELECT 'company', c.id::text, c.title, c.phone, c.email, `+by("company", "c")+` ELSE 'name' END
		FROM crm_companies c WHERE NOT ($5 = 'company' AND c.id::text = $4)
			AND (`+commMatch("company", "c", "$1", "$2")+` OR ($5 = 'company' AND $3 <> '' AND lower(c.title) = $3))
		UNION ALL
		SELECT 'lead', l.id::text, l.title, l.phone, l.email, `+by("lead", "l")+` ELSE 'email' END
		FROM crm_leads l WHERE NOT ($5 = 'lead' AND l.id::text = $4) AND l.deal_id IS NULL AND `+commMatch("lead", "l", "$1", "$2")+`
		LIMIT 20`, pk, em, nm, id, entity)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Duplicate])
	if err != nil || id == "" || (entity != "contact" && entity != "company") {
		return list, err
	}
	byINN, err := s.sameINN(ctx, entity, id) // and the clients with the ИНН of the card's requisites
	for _, d := range byINN {
		if !slices.ContainsFunc(list, func(o Duplicate) bool { return o.Entity == d.Entity && o.ID == d.ID }) {
			list = append(list, d)
		}
	}
	return list, err
}

// Merge joins the card other into keep (two contacts or two companies): its deals, contacts, deeds, timeline and own
// field values go to keep, its empty fields fill keep's empty ones, and other is deleted.
func (s *Store) Merge(ctx context.Context, userID, entity, keep, other string) error {
	if (entity != "contact" && entity != "company") || keep == other {
		return i18n.E("err.bad_form")
	}
	tbl, ref := "crm_"+plural[entity], entity+"_id"
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+tbl+` WHERE id::text IN ($1, $2)`, keep, other).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return ErrNotFound
		}
		fill := `phone = CASE WHEN k.phone = '' THEN o.phone ELSE k.phone END, email = CASE WHEN k.email = '' THEN o.email ELSE k.email END,
			comment = CASE WHEN k.comment = '' THEN o.comment WHEN o.comment = '' THEN k.comment ELSE k.comment || E'\n' || o.comment END`
		if entity == "contact" {
			fill += `, position = CASE WHEN k.position = '' THEN o.position ELSE k.position END, company_id = coalesce(k.company_id, o.company_id)`
		} else {
			fill += `, website = CASE WHEN k.website = '' THEN o.website ELSE k.website END, address = CASE WHEN k.address = '' THEN o.address ELSE k.address END`
		}
		for _, q := range []string{
			`UPDATE ` + tbl + ` k SET ` + fill + `, updated_at = now() FROM ` + tbl + ` o WHERE k.id::text = $1 AND o.id::text = $2`,
			`UPDATE crm_deals SET ` + ref + ` = $1::uuid WHERE ` + ref + `::text = $2`,
			`UPDATE crm_leads SET ` + ref + ` = $1::uuid WHERE ` + ref + `::text = $2`, // what leads were converted into
			`UPDATE crm_activities SET entity_id = $1::uuid WHERE entity = '` + entity + `' AND entity_id::text = $2`,
			`UPDATE crm_timeline SET entity_id = $1::uuid WHERE entity = '` + entity + `' AND entity_id::text = $2`,
			`UPDATE crm_mail m SET entity_id = $1::uuid WHERE entity = '` + entity + `' AND entity_id::text = $2
				AND NOT EXISTS (SELECT 1 FROM crm_mail k WHERE k.entity = m.entity AND k.entity_id::text = $1 AND k.message_id = m.message_id)`,
			`DELETE FROM crm_mail WHERE entity = '` + entity + `' AND entity_id::text = $2 AND $1 <> ''`,
			`UPDATE crm_requisites SET entity_id = $1::uuid, is_default = is_default AND NOT EXISTS (SELECT 1 FROM crm_requisites k
				WHERE k.entity = '` + entity + `' AND k.entity_id = $1::uuid AND k.is_default) WHERE entity = '` + entity + `' AND entity_id::text = $2`,
			`UPDATE crm_comms m SET entity_id = $1::uuid, sort = m.sort + 100 WHERE entity = '` + entity + `' AND entity_id::text = $2
				AND NOT EXISTS (SELECT 1 FROM crm_comms k WHERE k.entity = m.entity AND k.entity_id::text = $1 AND k.kind = m.kind
					AND ((k.key <> '' AND k.key = m.key) OR lower(k.value) = lower(m.value)))`, // its other phones and e-mails, after keep's
			`DELETE FROM crm_comms WHERE entity = '` + entity + `' AND entity_id::text = $2 AND $1 <> ''`,
			`INSERT INTO crm_` + entity + `_field_values (` + ref + `, field_id, value) SELECT $1::uuid, field_id, value
				FROM crm_` + entity + `_field_values WHERE ` + ref + `::text = $2 ON CONFLICT DO NOTHING`,
		} {
			if _, err := tx.Exec(ctx, q, keep, other); err != nil {
				return err
			}
		}
		if entity == "company" {
			if _, err := tx.Exec(ctx, `UPDATE crm_contacts SET company_id = $1::uuid WHERE company_id::text = $2`, keep, other); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `INSERT INTO crm_deal_contacts (deal_id, contact_id, sort) SELECT deal_id, $1::uuid, sort
			FROM crm_deal_contacts WHERE contact_id::text = $2 ON CONFLICT DO NOTHING`, keep, other); err != nil { // the deals the other was one of the contacts of
			return err
		}
		var otherName string
		if err := tx.QueryRow(ctx, `SELECT `+map[string]string{"contact": "name", "company": "title"}[entity]+` FROM `+tbl+` WHERE id::text = $1`, other).Scan(&otherName); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+tbl+` WHERE id::text = $1`, other); err != nil {
			return err
		}
		return s.event(ctx, tx, entity, keep, userID, "merged", otherName)
	})
}
