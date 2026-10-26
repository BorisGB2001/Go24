package crm

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Comm is one of a contact's, company's or lead's phones or e-mails «Телефон» and «E-mail» fields with
// a type and «+ добавить». The first of a kind is the card's main one (its phone and email columns).
type Comm struct {
	Kind  string `json:"-"`    // phone | email
	Type  string `json:"type"` // CommTypes
	Value string `json:"value"`
}

// CommTypes are the types of the phones and the e-mails, the default first.
var CommTypes = map[string][]string{"phone": {"work", "mobile", "home", "other"}, "email": {"work", "home", "other"}}

// maxComms is how many phones (and e-mails) a card may have.
const maxComms = 10

// commKey is how a phone or an e-mail is matched: phoneKey (the last 10 digits; "" — too short to match), the
// e-mail in lower case.
func commKey(kind, v string) string {
	if kind == "phone" {
		return phoneKey(v)
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// commMatch is the condition «the card (alias.id of the entity) has the phone key p or the e-mail e» over all its
// phones and e-mails (p and e are SQL parameters; "" matches nothing).
func commMatch(entity, alias, p, e string) string {
	return `EXISTS (SELECT 1 FROM crm_comms m WHERE m.entity = '` + entity + `' AND m.entity_id = ` + alias + `.id AND m.key <> ''
		AND ((m.kind = 'phone' AND m.key = ` + p + `) OR (m.kind = 'email' AND m.key = ` + e + `)))`
}

// commHas is commMatch of one kind: k is the SQL parameter of the key.
func commHas(entity, alias, kind, k string) string {
	return `EXISTS (SELECT 1 FROM crm_comms m WHERE m.entity = '` + entity + `' AND m.entity_id = ` + alias + `.id AND m.key <> ''
		AND m.kind = '` + kind + `' AND m.key = ` + k + `)`
}

// commLike is the list search «q» (the SQL parameter of an ILIKE pattern) over the card's phones and e-mails: as
// typed and, for a phone, by its digits.
func commLike(entity, alias, q string) string {
	return `EXISTS (SELECT 1 FROM crm_comms m WHERE m.entity = '` + entity + `' AND m.entity_id = ` + alias + `.id
		AND (m.value ILIKE ` + q + ` OR (m.kind = 'phone' AND regexp_replace(m.value, '\D', '', 'g') ILIKE ` + q + `)))`
}

// Comms are the card's phones and e-mails in their order.
func (s *Store) Comms(ctx context.Context, entity, id string) ([]Comm, error) {
	rows, err := s.db.Query(ctx, `SELECT kind, type, value FROM crm_comms WHERE entity = $1 AND entity_id::text = $2 ORDER BY kind DESC, sort`,
		entity, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Comm])
}

// CommsOf are the phones (kind phone) or the e-mails (email) of the list.
func CommsOf(list []Comm, kind string) []Comm {
	var out []Comm
	for _, c := range list {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// normComms is what a card saves, and its main phone and e-mail. Given (a form's, a call's) are the phones and
// e-mails as they are. Without them — or with the very ones the card has (it was loaded, changed and saved back, as
// robots, business processes and open lines do) — the card's main phone and e-mail decide: a changed one replaces the
// first of its kind, an emptied one drops it, a new one goes first.
func normComms(old []Comm, phone, email string, given []Comm) (string, string, []Comm, error) {
	if given == nil || slices.Equal(given, old) {
		given = slices.Clone(old)
		for kind, main := range map[string]string{"phone": strings.TrimSpace(phone), "email": strings.TrimSpace(email)} {
			first := slices.IndexFunc(given, func(c Comm) bool { return c.Kind == kind && strings.TrimSpace(c.Value) != "" })
			switch {
			case first < 0 && main != "":
				given = append([]Comm{{Kind: kind, Value: main}}, given...)
			case first >= 0 && main == "":
				given = slices.Delete(given, first, first+1)
			case first >= 0 && strings.TrimSpace(given[first].Value) != main:
				given[first].Value = main
			}
		}
	}
	var out []Comm
	seen := map[string]bool{}
	for _, c := range given {
		c.Value = strings.TrimSpace(c.Value)
		types, ok := CommTypes[c.Kind]
		if !ok || c.Value == "" {
			continue
		}
		if err := clean(&c.Value, 300); err != nil {
			return "", "", nil, err
		}
		if !slices.Contains(types, c.Type) {
			c.Type = types[0]
		}
		k := c.Kind + ":" + strings.ToLower(c.Value)
		if key := commKey(c.Kind, c.Value); key != "" {
			k = c.Kind + ":" + key
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		if len(CommsOf(out, c.Kind)) == maxComms {
			return "", "", nil, i18n.E("err.crm_comms_many")
		}
		out = append(out, c)
	}
	phone, email = "", ""
	if ps := CommsOf(out, "phone"); len(ps) > 0 {
		phone = ps[0].Value
	}
	if es := CommsOf(out, "email"); len(es) > 0 {
		email = es[0].Value
	}
	return phone, email, out, nil
}

// setComms keeps the card's phones and e-mails (normComms').
func (s *Store) setComms(ctx context.Context, entity, id string, list []Comm) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM crm_comms WHERE entity = $1 AND entity_id::text = $2`, entity, id); err != nil {
			return err
		}
		n := map[string]int{}
		for _, c := range list {
			if _, err := tx.Exec(ctx, `INSERT INTO crm_comms (entity, entity_id, kind, type, value, key, sort) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				entity, id, c.Kind, c.Type, c.Value, commKey(c.Kind, c.Value), n[c.Kind]); err != nil {
				return err
			}
			n[c.Kind]++
		}
		return nil
	})
}

// commsText is a card's phones (or e-mails) in one line, for its history; main — when the card has none loaded.
func commsText(list []Comm, kind, main string) string {
	var vals []string
	for _, c := range CommsOf(list, kind) {
		vals = append(vals, c.Value)
	}
	if len(vals) == 0 {
		return main
	}
	return strings.Join(vals, ", ")
}
