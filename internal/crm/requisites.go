package crm

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Реквизиты»: a company or a contact keeps requisites by a preset — an organization (ИНН of ten digits,
// КПП, ОГРН), a sole trader (ИНН of twelve, ОГРНИП) or a private person — with the bank details. A card may have
// several; the default one (the first made, or the one marked) is the buyer on the client's quotes and invoices.

// RequisitePresets in their order; the first is a new one's.
var RequisitePresets = []string{"org", "ip", "person"}

// Requisite is one set of a client's details.
type Requisite struct {
	ID, Entity, EntityID, Preset, Name string
	INN, KPP, OGRN, Director, Address  string
	Bank, BIK, Account, CorrAccount    string
	Default                            bool
}

// HasBank tells whether any bank detail is filled.
func (q Requisite) HasBank() bool {
	return q.Bank != "" || q.BIK != "" || q.Account != "" || q.CorrAccount != ""
}

const requisiteSQL = `SELECT id::text, entity, entity_id::text, preset, name, inn, kpp, ogrn, director, address, bank, bik,
		account, corr_account, is_default FROM crm_requisites`

func scanRequisites(rows pgx.Rows, err error) ([]Requisite, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Requisite])
}

// Requisites of a company or a contact, the default first.
func (s *Store) Requisites(ctx context.Context, entity, id string) ([]Requisite, error) {
	return scanRequisites(s.db.Query(ctx, requisiteSQL+` WHERE entity = $1 AND entity_id::text = $2
		ORDER BY is_default DESC, created_at, id`, entity, id))
}

// Requisite is one by its id.
func (s *Store) Requisite(ctx context.Context, id string) (Requisite, error) {
	list, err := scanRequisites(s.db.Query(ctx, requisiteSQL+` WHERE id::text = $1`, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Requisite{}, err
	}
	return list[0], nil
}

// Buyer is the default requisite of a deal's client: its company's, else its contact's (nil if none).
func (s *Store) Buyer(ctx context.Context, companyID, contactID string) (*Requisite, error) {
	list, err := scanRequisites(s.db.Query(ctx, requisiteSQL+` WHERE is_default AND ((entity = 'company' AND entity_id::text = $1)
		OR (entity = 'contact' AND entity_id::text = $2)) ORDER BY entity = 'company' DESC LIMIT 1`, companyID, contactID))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// digits tells whether v is empty or of n digits.
func digits(v string, n ...int) bool {
	if v == "" {
		return true
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	for _, k := range n {
		if len(v) == k {
			return true
		}
	}
	return false
}

// check trims the requisite's fields and tells the first wrong one.
func (q *Requisite) check() error {
	for _, f := range []*string{&q.Name, &q.INN, &q.KPP, &q.OGRN, &q.Director, &q.Address, &q.Bank, &q.BIK, &q.Account, &q.CorrAccount} {
		*f = strings.TrimSpace(*f)
		if len([]rune(*f)) > 300 {
			return i18n.E("err.crm_field")
		}
	}
	for _, f := range []*string{&q.INN, &q.KPP, &q.OGRN, &q.BIK, &q.Account, &q.CorrAccount} {
		*f = strings.NewReplacer(" ", "", "-", "").Replace(*f)
	}
	inn, ogrn := []int{10}, []int{13}
	switch q.Preset {
	case "org":
	case "ip":
		inn, ogrn = []int{12}, []int{15}
	case "person":
		inn, ogrn = []int{12}, nil
	default:
		return i18n.E("err.bad_form")
	}
	switch {
	case q.Name == "":
		return i18n.E("err.crm_req_name")
	case !digits(q.INN, inn...):
		return i18n.E("err.crm_req_inn")
	case q.Preset != "org" && q.KPP != "", !digits(q.KPP, 9):
		return i18n.E("err.crm_req_kpp")
	case !digits(q.OGRN, ogrn...):
		return i18n.E("err.crm_req_ogrn")
	case !digits(q.BIK, 9):
		return i18n.E("err.crm_req_bik")
	case !digits(q.Account, 20), !digits(q.CorrAccount, 20):
		return i18n.E("err.crm_req_account")
	}
	return nil
}

// SaveRequisite makes (q.ID "") or changes a requisite of a company or a contact; the card's first one, or one
// marked default, becomes the default. It answers the requisite's id.
func (s *Store) SaveRequisite(ctx context.Context, q Requisite) (string, error) {
	if q.Entity != "company" && q.Entity != "contact" {
		return "", i18n.E("err.bad_form")
	}
	if err := q.check(); err != nil {
		return "", err
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if q.ID == "" {
			return tx.QueryRow(ctx, `INSERT INTO crm_requisites (entity, entity_id, preset, name, inn, kpp, ogrn, director, address,
				bank, bik, account, corr_account, is_default) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
				$14 OR NOT EXISTS (SELECT 1 FROM crm_requisites WHERE entity = $1 AND entity_id = $2::uuid)) RETURNING id::text`,
				q.Entity, q.EntityID, q.Preset, q.Name, q.INN, q.KPP, q.OGRN, q.Director, q.Address, q.Bank, q.BIK, q.Account,
				q.CorrAccount, q.Default).Scan(&q.ID)
		}
		tag, err := tx.Exec(ctx, `UPDATE crm_requisites SET preset = $4, name = $5, inn = $6, kpp = $7, ogrn = $8, director = $9,
			address = $10, bank = $11, bik = $12, account = $13, corr_account = $14, is_default = is_default OR $15
			WHERE id::text = $1 AND entity = $2 AND entity_id::text = $3`, q.ID, q.Entity, q.EntityID, q.Preset, q.Name, q.INN,
			q.KPP, q.OGRN, q.Director, q.Address, q.Bank, q.BIK, q.Account, q.CorrAccount, q.Default)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
		return err
	})
	if err == nil && q.Default {
		err = s.SetDefaultRequisite(ctx, q.ID)
	}
	return q.ID, err
}

// SetDefaultRequisite makes the requisite its card's default one.
func (s *Store) SetDefaultRequisite(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE crm_requisites q SET is_default = (q.id = o.id) FROM crm_requisites o
		WHERE o.id::text = $1 AND q.entity = o.entity AND q.entity_id = o.entity_id`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// DeleteRequisite removes a requisite; if it was the default, the card's oldest other one becomes the default.
func (s *Store) DeleteRequisite(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var entity, of string
		var def bool
		err := tx.QueryRow(ctx, `DELETE FROM crm_requisites WHERE id::text = $1 RETURNING entity, entity_id::text, is_default`, id).
			Scan(&entity, &of, &def)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || !def {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE crm_requisites SET is_default = true WHERE id = (SELECT id FROM crm_requisites
			WHERE entity = $1 AND entity_id::text = $2 ORDER BY created_at, id LIMIT 1)`, entity, of)
		return err
	})
}

// sameINN are the companies and contacts other than the card whose requisites have an ИНН of the card's ones.
func (s *Store) sameINN(ctx context.Context, entity, id string) ([]Duplicate, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT q.entity, q.entity_id::text, coalesce(c.title, x.name), coalesce(c.phone, x.phone), coalesce(c.email, x.email), 'inn'
		FROM crm_requisites m JOIN crm_requisites q ON q.inn = m.inn AND NOT (q.entity = m.entity AND q.entity_id = m.entity_id)
		LEFT JOIN crm_companies c ON q.entity = 'company' AND c.id = q.entity_id
		LEFT JOIN crm_contacts x ON q.entity = 'contact' AND x.id = q.entity_id
		WHERE m.entity = $1 AND m.entity_id::text = $2 AND m.inn <> '' AND coalesce(c.id, x.id) IS NOT NULL LIMIT 20`, entity, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Duplicate])
}
