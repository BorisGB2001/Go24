package crm

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The catalog's dictionaries: «Единицы измерения» (by ОКЕИ; a product keeps its unit's symbol, as a
// product row does), «Свойства товаров» — a string, a number, a list or yes/no, filled in the product's card and
// shown as columns of the list — and the barcodes of a product (several to a product, a barcode to one product),
// which the catalog's search and the scanner of a document's rows find.

// Unit of measure. A built-in unit (with its ОКЕИ code) takes its name and symbol from the locale while they are empty.
type Unit struct {
	ID, Code, Name, Symbol string
	Default                bool
	Sort                   int
}

// Units of the dictionary in their order.
func (s *Store) Units(ctx context.Context) ([]Unit, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, code, name, symbol, is_default, sort FROM crm_units ORDER BY sort, code, name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Unit, error) {
		var u Unit
		err := r.Scan(&u.ID, &u.Code, &u.Name, &u.Symbol, &u.Default, &u.Sort)
		return u, err
	})
}

// SaveUnit creates (no ID) or changes a unit; a unit without an ОКЕИ code needs its name and symbol. The default
// unit (preselected for a new product) is one.
func (s *Store) SaveUnit(ctx context.Context, u Unit) (string, error) {
	for _, f := range []struct {
		v   *string
		max int
	}{{&u.Code, 10}, {&u.Name, 100}, {&u.Symbol, 20}} {
		if err := clean(f.v, f.max); err != nil {
			return "", err
		}
	}
	if u.Code == "" && (u.Name == "" || u.Symbol == "") {
		return "", i18n.E("err.crm_unit_name")
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if u.Default {
			if _, err := tx.Exec(ctx, `UPDATE crm_units SET is_default = false WHERE is_default AND id::text <> $1`, u.ID); err != nil {
				return err
			}
		}
		if u.ID == "" {
			// with no sort given a new unit goes last
			return tx.QueryRow(ctx, `INSERT INTO crm_units (code, name, symbol, is_default, sort)
				VALUES ($1, $2, $3, $4, CASE WHEN $5 = 0 THEN (SELECT coalesce(max(sort), 0) + 10 FROM crm_units) ELSE $5 END) RETURNING id`,
				u.Code, u.Name, u.Symbol, u.Default, u.Sort).Scan(&u.ID)
		}
		tag, err := tx.Exec(ctx, `UPDATE crm_units SET code = $2, name = $3, symbol = $4, is_default = $5, sort = $6 WHERE id::text = $1`,
			u.ID, u.Code, u.Name, u.Symbol, u.Default, u.Sort)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
		return err
	})
	return u.ID, sectionErr(err)
}

// DeleteUnit removes a unit from the dictionary (not the default one); products keep their symbol.
func (s *Store) DeleteUnit(ctx context.Context, id string) error {
	var def bool
	err := s.db.QueryRow(ctx, `DELETE FROM crm_units WHERE id::text = $1 AND NOT is_default RETURNING false`, id).Scan(&def)
	if errors.Is(err, pgx.ErrNoRows) {
		return i18n.E("err.crm_unit_default")
	}
	return sectionErr(err)
}

// Property kinds.
const (
	PropString = "string"
	PropNumber = "number"
	PropList   = "list"
	PropBool   = "bool"
)

// Prop is a property of the catalog's products.
type Prop struct {
	ID, Name, Kind string
	Options        []string // the values of a list
	InList         bool     // a column of the catalog's list
	Variant        bool     // «для вариаций»: filled in the variations, tells them apart
	Sort           int
}

// ProductProps are the catalog's properties in their order.
func (s *Store) ProductProps(ctx context.Context) ([]Prop, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, kind, options, in_list, variant, sort FROM crm_product_props ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Prop, error) {
		var p Prop
		err := r.Scan(&p.ID, &p.Name, &p.Kind, &p.Options, &p.InList, &p.Variant, &p.Sort)
		return p, err
	})
}

// SaveProp creates (no ID) or changes a property; a list needs its values (no more than 200, each once).
func (s *Store) SaveProp(ctx context.Context, p Prop) (string, error) {
	if err := clean(&p.Name, 200); err != nil || p.Name == "" {
		return "", i18n.E("err.crm_prop_name")
	}
	switch p.Kind {
	case PropString, PropNumber, PropBool:
		p.Options = nil
	case PropList:
		var opts []string
		for _, o := range p.Options {
			if err := clean(&o, 200); err != nil {
				return "", err
			}
			if o != "" && !slices.Contains(opts, o) {
				opts = append(opts, o)
			}
		}
		if len(opts) == 0 || len(opts) > 200 {
			return "", i18n.E("err.crm_prop_options")
		}
		p.Options = opts
	default:
		return "", i18n.E("err.crm_prop_kind")
	}
	if p.Options == nil {
		p.Options = []string{}
	}
	if p.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO crm_product_props (name, kind, options, in_list, variant, sort) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`, p.Name, p.Kind, p.Options, p.InList, p.Variant, p.Sort).Scan(&p.ID)
		return p.ID, err
	}
	// the kind is kept: the products' values were checked by it
	tag, err := s.db.Exec(ctx, `UPDATE crm_product_props SET name = $2, options = $3, in_list = $4, variant = $5, sort = $6
		WHERE id::text = $1`, p.ID, p.Name, p.Options, p.InList, p.Variant, p.Sort)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return p.ID, sectionErr(err)
}

// DeleteProp removes a property with its values in the products.
func (s *Store) DeleteProp(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM crm_product_props WHERE id::text = $1`, id)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
		if err != nil {
			return sectionErr(err)
		}
		_, err = tx.Exec(ctx, `UPDATE crm_products SET props = props - $1 WHERE props ? $1`, id)
		return err
	})
}

// cleanProps keeps the values of the known properties, checked by their kinds: a number as people type it, a value
// of the list, yes ("1") or nothing; empty values are dropped.
func cleanProps(props []Prop, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range props {
		v := strings.TrimSpace(in[p.ID])
		if v == "" {
			continue
		}
		switch p.Kind {
		case PropNumber:
			n, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(v, " ", ""), ",", "."), 64)
			if err != nil {
				return nil, i18n.E("err.crm_prop_number", p.Name)
			}
			v = strconv.FormatFloat(n, 'f', -1, 64)
		case PropList:
			if !slices.Contains(p.Options, v) {
				return nil, i18n.E("err.crm_prop_options")
			}
		case PropBool:
			if v == "0" {
				continue
			}
			v = "1"
		default:
			if len([]rune(v)) > 1000 {
				return nil, i18n.E("err.crm_field")
			}
		}
		out[p.ID] = v
	}
	return out, nil
}

// cleanBarcodes: each once, trimmed, no more than 50 of up to 64 characters.
func cleanBarcodes(in []string) ([]string, error) {
	var out []string
	for _, b := range in {
		b = strings.TrimSpace(b)
		if b == "" || slices.Contains(out, b) {
			continue
		}
		if len(b) > 64 || strings.ContainsAny(b, " \t") {
			return nil, i18n.E("err.crm_barcode", b)
		}
		out = append(out, b)
	}
	if len(out) > 50 {
		return nil, i18n.E("err.crm_rows")
	}
	return out, nil
}

// ProductByBarcode is the product with the barcode.
func (s *Store) ProductByBarcode(ctx context.Context, code string) (Product, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT product_id::text FROM crm_product_barcodes WHERE barcode = $1`, strings.TrimSpace(code)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	if err != nil {
		return Product{}, err
	}
	return s.Product(ctx, id)
}
