package crm

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Поиск дубликатов»: all the groups of contacts, companies or leads with the same phone, e-mail, name
// (a company's title, a lead's contact name) or ИНН of their requisites, to clean the base — the cards of a group are
// merged into one (contacts and companies) or marked «Не дубликаты» and no longer shown together.

// DupCriteria are the criteria of the search for each kind of card.
var DupCriteria = map[string][]string{
	"contact": {"phone", "email", "name", "inn"},
	"company": {"phone", "email", "name", "inn"},
	"lead":    {"phone", "email", "name"},
}

// DupLimit is how many groups are shown.
const DupLimit = 100

// DupCard is a card of a group.
type DupCard struct {
	ID, Title, Phone, Email string
	Responsible             Ref
	CreatedAt               time.Time
}

// DupGroup is the cards sharing a value (as typed in one of them), the oldest first.
type DupGroup struct {
	Value string
	Cards []DupCard
}

// dupTitle is the column of a card's name compared by «name».
var dupTitle = map[string]string{"contact": "name", "company": "title", "lead": "name"}

// DuplicateGroups are the groups of the entity's cards the viewer may read alike by the criterion, the groups with the
// newest cards first, at most DupLimit; total is the number of all of them.
func (s *Store) DuplicateGroups(ctx context.Context, viewer, entity, by string) (groups []DupGroup, total int, err error) {
	if !slices.Contains(DupCriteria[entity], by) {
		return nil, 0, i18n.E("err.bad_form")
	}
	tbl := "crm_" + plural[entity]
	var keys string // (key, value, id) of every card
	switch by {
	case "phone", "email":
		keys = `SELECT m.key, m.value, m.entity_id AS id FROM crm_comms m WHERE m.entity = '` + entity + `' AND m.kind = '` + by + `' AND m.key <> ''`
	case "name":
		keys = `SELECT lower(btrim(` + dupTitle[entity] + `)), btrim(` + dupTitle[entity] + `), id FROM ` + tbl + ` WHERE btrim(` + dupTitle[entity] + `) <> ''`
	case "inn":
		keys = `SELECT inn, inn, entity_id FROM crm_requisites WHERE entity = '` + entity + `' AND inn <> ''`
	}
	sc, err := s.viewScope(ctx, viewer, entity, "c", 1)
	if err != nil {
		return nil, 0, err
	}
	live := "true"
	if entity == "lead" {
		live = "c.deal_id IS NULL" // not converted yet
	}
	rows, err := s.db.Query(ctx, `
		WITH k (key, value, id) AS (`+keys+`),
		v AS (SELECT DISTINCT ON (k.key, k.id) k.key, k.value, k.id FROM k JOIN `+tbl+` c ON c.id = k.id
			WHERE `+live+` AND (`+sc+`) ORDER BY k.key, k.id, k.value),
		g AS (SELECT key FROM v GROUP BY key HAVING count(*) > 1)
		SELECT v.key, v.value, c.id::text, c.`+map[string]string{"contact": "name", "company": "title", "lead": "title"}[entity]+`, c.phone, c.email,
			c.responsible_id::text, coalesce(u.name, ''), c.created_at
		FROM v JOIN g ON g.key = v.key JOIN `+tbl+` c ON c.id = v.id LEFT JOIN users u ON u.id = c.responsible_id
		ORDER BY v.key, c.created_at, c.id`, viewer)
	if err != nil {
		return nil, 0, err
	}
	type row struct {
		Key, Value string
		Card       DupCard
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Key, &r.Value, &r.Card.ID, &r.Card.Title, &r.Card.Phone, &r.Card.Email, &r.Card.Responsible.ID,
			&r.Card.Responsible.Name, &r.Card.CreatedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	ignored, err := s.dupIgnored(ctx, entity)
	if err != nil {
		return nil, 0, err
	}
	for i := 0; i < len(list); {
		j := i
		for j < len(list) && list[j].Key == list[i].Key {
			j++
		}
		g := DupGroup{Value: list[i].Value}
		for a := i; a < j; a++ { // a card stays while it is not «Не дубликаты» with some other one of the group
			for b := i; b < j; b++ {
				if a != b && !ignored[dupPair(list[a].Card.ID, list[b].Card.ID)] {
					g.Cards = append(g.Cards, list[a].Card)
					break
				}
			}
		}
		if len(g.Cards) > 1 {
			groups = append(groups, g)
		}
		i = j
	}
	newest := func(g DupGroup) time.Time {
		return slices.MaxFunc(g.Cards, func(a, b DupCard) int { return a.CreatedAt.Compare(b.CreatedAt) }).CreatedAt
	}
	slices.SortStableFunc(groups, func(a, b DupGroup) int { return newest(b).Compare(newest(a)) })
	total = len(groups)
	if len(groups) > DupLimit {
		groups = groups[:DupLimit]
	}
	return groups, total, nil
}

// dupPair is the key of two cards in either order.
func dupPair(a, b string) [2]string {
	if b < a {
		a, b = b, a
	}
	return [2]string{a, b}
}

// dupIgnored are the pairs of the entity's cards marked «Не дубликаты».
func (s *Store) dupIgnored(ctx context.Context, entity string) (map[[2]string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT a::text, b::text FROM crm_dup_ignored WHERE entity = $1`, entity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]bool{}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out[[2]string{a, b}] = true
	}
	return out, rows.Err()
}

// NotDuplicates marks the cards «Не дубликаты» of each other: they are no longer shown together in the search.
func (s *Store) NotDuplicates(ctx context.Context, userID, entity string, ids []string) error {
	if DupCriteria[entity] == nil || len(ids) < 2 {
		return i18n.E("err.bad_form")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		for i, a := range ids {
			for _, b := range ids[i+1:] {
				if a == b {
					continue
				}
				p := dupPair(a, b)
				if _, err := tx.Exec(ctx, `INSERT INTO crm_dup_ignored (entity, a, b, created_by) VALUES ($1, $2::uuid, $3::uuid, $4::uuid)
					ON CONFLICT DO NOTHING`, entity, p[0], p[1], userID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
