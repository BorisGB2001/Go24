package crm

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Разделы каталога товаров: the catalog is a tree of sections (a section in a section, any depth); a product
// is in one section or in the catalog's root. Deleting a section moves its products and subsections to its parent.

// Section of the catalog.
type Section struct {
	ID, ParentID, Name string
	Sort               int
	Path               string // the names from the top: «Оборудование / Принтеры»
	Depth              int    // 0 for a top section
	Products           int    // products right in the section
}

// Sections are the whole tree in its order: a section, then its subsections (by sort, then name).
func (s *Store) Sections(ctx context.Context) ([]Section, error) {
	rows, err := s.db.Query(ctx, `SELECT x.id::text, coalesce(x.parent_id::text, ''), x.name, x.sort,
		(SELECT count(*) FROM crm_products p WHERE p.section_id = x.id) FROM crm_product_sections x`)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Section, error) {
		var x Section
		err := r.Scan(&x.ID, &x.ParentID, &x.Name, &x.Sort, &x.Products)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Sort != all[j].Sort {
			return all[i].Sort < all[j].Sort
		}
		return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name)
	})
	kids := map[string][]Section{}
	for _, x := range all {
		kids[x.ParentID] = append(kids[x.ParentID], x)
	}
	out := make([]Section, 0, len(all))
	var walk func(parent, path string, depth int)
	walk = func(parent, path string, depth int) {
		for _, x := range kids[parent] {
			x.Depth, x.Path = depth, x.Name
			if path != "" {
				x.Path = path + " / " + x.Name
			}
			out = append(out, x)
			walk(x.ID, x.Path, depth+1)
		}
	}
	walk("", "", 0)
	return out, nil
}

// SectionPaths are the sections' paths by their ids.
func SectionPaths(list []Section) map[string]string {
	m := make(map[string]string, len(list))
	for _, x := range list {
		m[x.ID] = x.Path
	}
	return m
}

// SaveSection creates (no ID) or changes a section: its name, parent and sort. A section can't go into itself or
// into one of its subsections.
func (s *Store) SaveSection(ctx context.Context, x Section) (string, error) {
	if err := clean(&x.Name, 200); err != nil || x.Name == "" {
		return "", i18n.E("err.crm_section_name")
	}
	if x.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO crm_product_sections (parent_id, name, sort) VALUES ($1, $2, $3) RETURNING id`,
			optional(x.ParentID), x.Name, x.Sort).Scan(&x.ID)
		return x.ID, sectionErr(err)
	}
	if x.ParentID != "" {
		var loop bool
		err := s.db.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id FROM crm_product_sections WHERE id::text = $1
			UNION SELECT s.id, s.parent_id FROM crm_product_sections s JOIN up ON s.id = up.parent_id)
			SELECT EXISTS (SELECT 1 FROM up WHERE id::text = $2)`, x.ParentID, x.ID).Scan(&loop)
		if err != nil {
			return "", err
		}
		if loop {
			return "", i18n.E("err.crm_section_loop")
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_product_sections SET parent_id = $2, name = $3, sort = $4 WHERE id::text = $1`,
		x.ID, optional(x.ParentID), x.Name, x.Sort)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return x.ID, sectionErr(err)
}

// sectionErr: a parent that is gone (or not an id) is "not found".
func sectionErr(err error) error {
	var pe interface{ SQLState() string }
	if errors.As(err, &pe) && (pe.SQLState() == "23503" || pe.SQLState() == "22P02") {
		return ErrNotFound
	}
	return err
}

// DeleteSection removes a section; its products and subsections go to its parent.
func (s *Store) DeleteSection(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var parent *string
		err := tx.QueryRow(ctx, `SELECT parent_id FROM crm_product_sections WHERE id::text = $1 FOR UPDATE`, id).Scan(&parent)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm_product_sections SET parent_id = $2 WHERE parent_id::text = $1`, id, parent); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm_products SET section_id = $2 WHERE section_id::text = $1`, id, parent); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM crm_product_sections WHERE id::text = $1`, id)
		return err
	})
}
