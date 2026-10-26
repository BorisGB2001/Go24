package crm

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Справочники» of the portal CRM: the lists the cards choose from — the source of a lead, a deal and a contact
// («Источник»), the type of a contact, a company and a deal, a company's industry («Сфера деятельности»). Built-in
// items have a code and the name of the locale until renamed; administrators keep the lists.

// DictKinds are the dictionaries, in the order of their page.
var DictKinds = []string{"source", "contact_type", "company_type", "industry", "deal_type"}

// Dict is an item of a dictionary on a card: its id, and its own name or the code of a built-in one ("" — none).
type Dict struct{ ID, Code, Name string }

// Text is how the item reads: its name, or that of the locale (tr) for a built-in one.
func (d Dict) Text(tr func(key string) string) string {
	if d.Name != "" || d.Code == "" {
		return d.Name
	}
	return tr("crm.dict." + d.Code)
}

// DictItem is an item of a dictionary's page: Used — the cards that have it.
type DictItem struct {
	Dict
	Kind string
	Used int
}

// dictCols are the columns of a card's item joined as j; scanned into a Dict.
func dictCols(j string) string {
	return `coalesce(` + j + `.id::text, ''), coalesce(` + j + `.code, ''), coalesce(` + j + `.name, '')`
}

// dictJoin joins the item of the card's column as j.
func dictJoin(j, col string) string {
	return ` LEFT JOIN crm_dict ` + j + ` ON ` + j + `.id = ` + col
}

// Dicts are the items of every dictionary, in their order.
func (s *Store) Dicts(ctx context.Context) (map[string][]DictItem, error) {
	rows, err := s.db.Query(ctx, `SELECT j.kind, `+dictCols("j")+`,
			(SELECT count(*) FROM crm_leads WHERE source_id = j.id)::int + (SELECT count(*) FROM crm_deals WHERE source_id = j.id OR deal_type_id = j.id)::int
			+ (SELECT count(*) FROM crm_contacts WHERE source_id = j.id OR contact_type_id = j.id)::int
			+ (SELECT count(*) FROM crm_companies WHERE company_type_id = j.id OR industry_id = j.id)::int
		FROM crm_dict j ORDER BY j.kind, j.sort, j.code NULLS LAST, j.name`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (DictItem, error) {
		var it DictItem
		return it, r.Scan(&it.Kind, &it.ID, &it.Code, &it.Name, &it.Used)
	})
	out := map[string][]DictItem{}
	for _, it := range list {
		out[it.Kind] = append(out[it.Kind], it)
	}
	return out, err
}

func dictName(name *string) error {
	if err := clean(name, 100); err != nil {
		return err
	}
	if *name == "" {
		return i18n.E("err.crm_title")
	}
	return nil
}

// AddDictItem adds an item at the end of the dictionary.
func (s *Store) AddDictItem(ctx context.Context, kind, name string) (string, error) {
	if !slices.Contains(DictKinds, kind) {
		return "", i18n.E("err.bad_form")
	}
	if err := dictName(&name); err != nil {
		return "", err
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO crm_dict (kind, name, sort)
		VALUES ($1, $2, (SELECT coalesce(max(sort), 0) + 10 FROM crm_dict WHERE kind = $1)) RETURNING id`, kind, name).Scan(&id)
	return id, err
}

// RenameDictItem gives an item a name (a built-in one keeps its code).
func (s *Store) RenameDictItem(ctx context.Context, id, name string) error {
	if err := dictName(&name); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_dict SET name = $2 WHERE id::text = $1`, id, name)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// MoveDictItem moves an item up or down its dictionary.
func (s *Store) MoveDictItem(ctx context.Context, id string, up bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM crm_dict WHERE kind = (SELECT kind FROM crm_dict WHERE id::text = $1)
		ORDER BY sort, code NULLS LAST, name FOR UPDATE`, id)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	i := slices.Index(ids, id)
	if i < 0 {
		return ErrNotFound
	}
	j := i + 1
	if up {
		j = i - 1
	}
	if j < 0 || j >= len(ids) {
		return nil
	}
	ids[i], ids[j] = ids[j], ids[i]
	if _, err := tx.Exec(ctx, `UPDATE crm_dict d SET sort = u.n * 10 FROM unnest($1::text[]) WITH ORDINALITY AS u(id, n)
		WHERE d.id::text = u.id`, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteDictItem deletes an item; the cards that had it have none.
func (s *Store) DeleteDictItem(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_dict WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// DictCode is the id of a built-in item ("" when it is gone).
func (s *Store) DictCode(ctx context.Context, kind, code string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM crm_dict WHERE kind = $1 AND code = $2`, kind, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// dictItem checks a card's item: one of the dictionary, or none (""); a built-in one may be given by its code alone.
func (s *Store) dictItem(ctx context.Context, kind string, d Dict) (*string, error) {
	if d.ID == "" && d.Code != "" {
		id, err := s.DictCode(ctx, kind, d.Code)
		return optional(id), err
	}
	if d.ID == "" {
		return nil, nil
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_dict WHERE id::text = $1 AND kind = $2)`, d.ID, kind).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, i18n.E("err.crm_dict")
	}
	return &d.ID, nil
}
