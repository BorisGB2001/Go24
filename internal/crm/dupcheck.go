package crm

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// «Контроль дубликатов» while typing: the form of a new lead, contact or company (and of a card being
// changed) shows «Найдены совпадения» — the cards the viewer may read with a phone, an e-mail, the name (a person's
// name, a company's title) or the ИНН typed in it; a lead is matched with contacts and companies too.

// DupInput is what is typed in a card's form.
type DupInput struct {
	Name, Company string // a person's name (a contact's, a lead's); a company's title (a company's, a lead's)
	Phones, Emails []string
	INN            string
}

// DupHit is a card like the one typed.
type DupHit struct {
	Entity, ID, Title, Responsible string
	By                             []string // phone | email | name | inn, what matched
}

// Path is the card's page.
func (d DupHit) Path() string { return "/crm/" + plural[d.Entity] + "/" + d.ID }

// DupCheckLimit is how many cards are shown at most.
const DupCheckLimit = 20

// DupCheck are the cards the viewer may read like the one of the entity (lead, contact or company; id — the card
// being changed, "" for a new one) by what is typed in its form, the newest first.
func (s *Store) DupCheck(ctx context.Context, viewer, entity, id string, in DupInput) ([]DupHit, error) {
	var phones, emails []string
	for _, p := range in.Phones {
		if k := phoneKey(p); k != "" && !slices.Contains(phones, k) {
			phones = append(phones, k)
		}
	}
	for _, e := range in.Emails {
		if k := strings.ToLower(strings.TrimSpace(e)); strings.Contains(k, "@") && !slices.Contains(emails, k) {
			emails = append(emails, k)
		}
	}
	name, company := strings.ToLower(strings.TrimSpace(in.Name)), strings.ToLower(strings.TrimSpace(in.Company))
	inn := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, in.INN)
	if len(inn) < 10 { // an ИНН has 10 or 12 digits
		inn = ""
	}
	if entity == "contact" {
		company = ""
	} else if entity == "company" {
		name = ""
	}
	if len(phones) == 0 && len(emails) == 0 && name == "" && company == "" && inn == "" {
		return nil, nil
	}
	// $1 phones, $2 e-mails, $3 name, $4 company, $5 ИНН, $6 the card itself (entity:id), $7 the viewer
	comm := func(e, a string) string {
		return `coalesce((SELECT array_agg(DISTINCT m.kind) FROM crm_comms m WHERE m.entity = '` + e + `' AND m.entity_id = ` + a + `.id
			AND m.key <> '' AND ((m.kind = 'phone' AND m.key = ANY($1)) OR (m.kind = 'email' AND m.key = ANY($2)))), '{}')`
	}
	innOf := func(e, a string) string {
		return `EXISTS (SELECT 1 FROM crm_requisites q WHERE q.entity = '` + e + `' AND q.entity_id = ` + a + `.id AND $5 <> '' AND q.inn = $5)`
	}
	parts := []string{}
	for _, e := range []string{"contact", "company", "lead"} {
		sc, err := s.viewScope(ctx, viewer, e, "c", 7)
		if err != nil {
			return nil, err
		}
		title, names, live := "c.name", `$3 <> '' AND lower(btrim(c.name)) = $3`, "true"
		inns := innOf(e, "c")
		switch e {
		case "company":
			title, names = "c.title", `$4 <> '' AND lower(btrim(c.title)) = $4`
		case "lead":
			title, live, inns = "c.title", "c.deal_id IS NULL", "false" // not converted yet
			names = `($3 <> '' AND lower(btrim(c.name)) = $3) OR ($4 <> '' AND lower(btrim(c.company_title)) = $4)`
		}
		parts = append(parts, `SELECT '`+e+`' AS entity, c.id::text AS id, `+title+` AS title, coalesce(u.name, '') AS resp,
			`+comm(e, "c")+` AS comms, (`+names+`) AS by_name, `+inns+` AS by_inn, c.created_at
			FROM crm_`+plural[e]+` c LEFT JOIN users u ON u.id = c.responsible_id
			WHERE `+live+` AND '`+e+`:' || c.id::text <> $6 AND (`+sc+`)`)
	}
	rows, err := s.db.Query(ctx, `SELECT entity, id, title, resp, comms, by_name, by_inn FROM (`+strings.Join(parts, " UNION ALL ")+`) x
		WHERE cardinality(comms) > 0 OR by_name OR by_inn ORDER BY created_at DESC, id LIMIT `+strconv.Itoa(DupCheckLimit),
		phones, emails, name, company, inn, entity+":"+id, viewer)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DupHit, error) {
		var h DupHit
		var comms []string
		var byName, byINN bool
		if err := r.Scan(&h.Entity, &h.ID, &h.Title, &h.Responsible, &comms, &byName, &byINN); err != nil {
			return h, err
		}
		for _, k := range []string{"phone", "email"} {
			if slices.Contains(comms, k) {
				h.By = append(h.By, k)
			}
		}
		if byName {
			h.By = append(h.By, "name")
		}
		if byINN {
			h.By = append(h.By, "inn")
		}
		return h, nil
	})
}
