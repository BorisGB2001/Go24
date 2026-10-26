package crm

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Товары»: a catalog of products and services (administrators keep it) and the product rows of a deal
// or a lead — a product of the catalog or a free line, with its price, quantity, discount and VAT. A card with rows
// has its amount counted from them.
//
// «Вариации» (торговые предложения): a variation is a product of its own under the product it varies — its name,
// SKU, price, barcodes, photo and stock — and takes the product's section, unit and VAT. A product with variations
// is sold and kept on the warehouses by its variations: the pickers, the stock and its reports list them instead.

// The types of a catalog's product: a service is sold as a product is, but isn't kept on the
// warehouses — no stock, reserve, warehouse documents, shipments or variations.
const (
	KindProduct = "product"
	KindService = "service"
)

// Product of the catalog.
type Product struct {
	ID, Name, SKU, Currency, Unit, Description string
	Kind                                       string // «Тип»: KindProduct or KindService ("" — a product, when saved)
	Price                                      float64
	PurchasePrice                              *float64 // закупочная цена, nil: not given
	VATRate                                    *float64 // nil: no VAT
	VATIncluded, Active                        bool
	SectionID                                  string            // "" — the catalog's root
	SectionPath                                string            // the section's names from the top (filled by the lists)
	PhotoKey, PhotoType                        string            // the picture in the storage ("" — none)
	Props                                      map[string]string // «Свойства товаров»: a property's id → its value
	Barcodes                                   []string
	ParentID                                   string    // a variation's product ("" — not a variation)
	Variants                                   []Product // the variations of a product (filled by Catalog)
}

// Service tells a service from a product.
func (p Product) Service() bool { return p.Kind == KindService }

// ProductRow is a line of a card's products.
type ProductRow struct {
	ProductID, Name, Unit string
	Price, Quantity       float64
	Discount              float64 // percent
	VATRate               *float64
	VATIncluded           bool
	Service               bool // a catalog's service: never on a warehouse (filled by ProductRows)
	// a deal's row of a catalog product reserved on a warehouse («Складской учёт»): how many and till when (nil —
	// while the deal is open); Reserve below zero — the whole quantity (when saved)
	WarehouseID   string
	WarehouseName string   // (filled by ProductRows)
	Free          *float64 // available on the warehouse (filled for a deal's card)
	Reserve       float64
	ReserveUntil  *time.Time
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Short: more of the product is reserved on the row's warehouse than there is.
func (r ProductRow) Short() bool { return r.Free != nil && *r.Free < 0 }

// Net is the line's sum after the discount, as entered (VAT inside if included).
func (r ProductRow) Net() float64 { return round2(r.Price * r.Quantity * (1 - r.Discount/100)) }

// VAT is the line's VAT: the part of the sum if included, else on top of it.
func (r ProductRow) VAT() float64 {
	if r.VATRate == nil {
		return 0
	}
	if r.VATIncluded {
		return round2(r.Net() * *r.VATRate / (100 + *r.VATRate))
	}
	return round2(r.Net() * *r.VATRate / 100)
}

// Total is what the line costs, VAT included.
func (r ProductRow) Total() float64 {
	if r.VATIncluded {
		return r.Net()
	}
	return r.Net() + r.VAT()
}

// RowsTotal sums the lines: the whole and its VAT.
func RowsTotal(rows []ProductRow) (total, vat float64) {
	for _, r := range rows {
		total += r.Total()
		vat += r.VAT()
	}
	return round2(total), round2(vat)
}

// rowsAmount is the SQL of a card's amount when saved: counted from its rows if it has any, else the given one.
func rowsAmount(entity, given string) string {
	return `CASE WHEN EXISTS (SELECT 1 FROM crm_product_rows r WHERE r.entity = '` + entity + `' AND r.entity_id = crm_` + plural[entity] +
		`.id) THEN amount ELSE ` + given + ` END`
}

func cleanProduct(p *Product) error {
	if p.Kind == "" {
		p.Kind = KindProduct
	}
	if p.Kind != KindProduct && p.Kind != KindService {
		return i18n.E("err.crm_product_kind")
	}
	if err := clean(&p.Name, 300); err != nil || p.Name == "" {
		return i18n.E("err.crm_product_name")
	}
	if err := clean(&p.SKU, 100); err != nil {
		return err
	}
	if err := clean(&p.Unit, 20); err != nil {
		return err
	}
	if err := clean(&p.Description, 10000); err != nil {
		return err
	}
	if p.Price < 0 || p.Price > 1e15 || (p.PurchasePrice != nil && (*p.PurchasePrice < 0 || *p.PurchasePrice > 1e15)) {
		return i18n.E("err.crm_amount")
	}
	if p.VATRate != nil && (*p.VATRate < 0 || *p.VATRate > 100) {
		return i18n.E("err.crm_vat")
	}
	return nil
}

const productCols = `id, name, sku, currency, unit, description, price::float8, vat_rate::float8, vat_included, active,
	coalesce(section_id::text, ''), purchase_price::float8, photo_key, photo_type, props,
	coalesce((SELECT array_agg(b.barcode ORDER BY b.barcode) FROM crm_product_barcodes b WHERE b.product_id = crm_products.id), '{}'),
	coalesce(parent_id::text, ''), kind`

func scanProducts(rows pgx.Rows, err error) ([]Product, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Product, error) {
		var p Product
		err := r.Scan(&p.ID, &p.Name, &p.SKU, &p.Currency, &p.Unit, &p.Description, &p.Price, &p.VATRate, &p.VATIncluded, &p.Active,
			&p.SectionID, &p.PurchasePrice, &p.PhotoKey, &p.PhotoType, &p.Props, &p.Barcodes, &p.ParentID, &p.Kind)
		return p, err
	})
}

// Products of the catalog by name: the active ones, or all; q filters by name or SKU.
func (s *Store) Products(ctx context.Context, q string, all bool) ([]Product, error) {
	return s.ProductsOf(ctx, q, "", all)
}

// ProductsOf are the products on sale of a section and its subsections ("" — the whole catalog), by name, with
// their sections' paths: the products without variations and the variations (of an active product, unless all);
// the active ones, or all; q filters by name or SKU, or finds the product with the barcode.
func (s *Store) ProductsOf(ctx context.Context, q, section string, all bool) ([]Product, error) {
	return s.productsOf(ctx, q, section, "", all, false)
}

// Goods are ProductsOf without the services: what the warehouses keep.
func (s *Store) Goods(ctx context.Context, q, section string, all bool) ([]Product, error) {
	return s.productsOf(ctx, q, section, KindProduct, all, false)
}

// Catalog is the catalog's list: the products (not the variations) as ProductsOf filters them — q finds a product
// by its variations too — of the type kind ("" — any), each with its variations by name (the inactive ones too, if all).
func (s *Store) Catalog(ctx context.Context, q, section, kind string, all bool) ([]Product, error) {
	list, err := s.productsOf(ctx, q, section, kind, all, true)
	if err != nil || len(list) == 0 {
		return list, err
	}
	ids := make([]string, len(list))
	at := make(map[string]int, len(list))
	for i, p := range list {
		ids[i], at[p.ID] = p.ID, i
	}
	vars, err := scanProducts(s.db.Query(ctx, `SELECT `+productCols+` FROM crm_products WHERE parent_id::text = ANY($1) AND ($2 OR active)
		ORDER BY name`, ids, all))
	if err != nil {
		return nil, err
	}
	for _, v := range vars {
		i := at[v.ParentID]
		v.SectionPath = list[i].SectionPath
		list[i].Variants = append(list[i].Variants, v)
	}
	return list, nil
}

// Variants are the variations of a product by name, the inactive ones too.
func (s *Store) Variants(ctx context.Context, id string) ([]Product, error) {
	return scanProducts(s.db.Query(ctx, `SELECT `+productCols+` FROM crm_products WHERE parent_id::text = $1 ORDER BY name`, id))
}

// productsOf lists the products on sale (tops: the catalog's products instead) of a section and a type ("" — any), by name.
func (s *Store) productsOf(ctx context.Context, q, section, kind string, all, tops bool) ([]Product, error) {
	list, err := scanProducts(s.db.Query(ctx, `WITH RECURSIVE sub AS (SELECT id FROM crm_product_sections WHERE id::text = $3
			UNION SELECT x.id FROM crm_product_sections x JOIN sub ON x.parent_id = sub.id)
		SELECT `+productCols+` FROM crm_products
		WHERE ($1 OR (active AND (parent_id IS NULL OR (SELECT o.active FROM crm_products o WHERE o.id = crm_products.parent_id))))
			AND ($3 = '' OR section_id IN (SELECT id FROM sub)) AND ($6 = '' OR kind = $6)
			AND CASE WHEN $5 THEN parent_id IS NULL ELSE NOT EXISTS (SELECT 1 FROM crm_products v WHERE v.parent_id = crm_products.id) END
			AND ($2 = '%%' OR name ILIKE $2 OR sku ILIKE $2
				OR EXISTS (SELECT 1 FROM crm_product_barcodes b WHERE b.product_id = crm_products.id AND b.barcode = $4)
				OR ($5 AND EXISTS (SELECT 1 FROM crm_products v WHERE v.parent_id = crm_products.id AND ($1 OR v.active)
					AND (v.name ILIKE $2 OR v.sku ILIKE $2
						OR EXISTS (SELECT 1 FROM crm_product_barcodes b WHERE b.product_id = v.id AND b.barcode = $4)))))
		ORDER BY name LIMIT 1000`, all, like(q), section, strings.TrimSpace(q), tops, kind))
	if err != nil || len(list) == 0 {
		return list, err
	}
	sections, err := s.Sections(ctx)
	if err != nil {
		return nil, err
	}
	paths := SectionPaths(sections)
	for i := range list {
		list[i].SectionPath = paths[list[i].SectionID]
	}
	return list, nil
}

// SetProductPhoto puts (key "" — removes) the product's picture; answers the old one's key to delete from the storage.
func (s *Store) SetProductPhoto(ctx context.Context, id, key, contentType string) (old string, err error) {
	err = s.db.QueryRow(ctx, `UPDATE crm_products n SET photo_key = $2, photo_type = $3 FROM crm_products o
		WHERE n.id = o.id AND n.id::text = $1 RETURNING o.photo_key`, id, key, contentType).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return old, err
}

// Product is one product of the catalog.
func (s *Store) Product(ctx context.Context, id string) (Product, error) {
	list, err := scanProducts(s.db.Query(ctx, `SELECT `+productCols+` FROM crm_products WHERE id::text = $1`, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Product{}, err
	}
	return list[0], nil
}

// SaveProduct creates (no ID) or changes a product of the catalog with its properties' values and its barcodes (a
// barcode of another product is refused). A new variation names its product (ParentID, not a variation itself); a
// variation keeps its product, takes the product's section, unit and VAT (its currency when not given) and, without
// a name, is named by the product and its values of the properties «для вариаций». A product passes its section,
// unit and VAT to its variations. A service has no variations, and a product becomes a service only while it has
// none and no warehouse documents; its deals' reserves are dropped then.
func (s *Store) SaveProduct(ctx context.Context, userID string, p Product) (string, error) {
	props, err := s.ProductProps(ctx)
	if err != nil {
		return "", err
	}
	if p.ID != "" {
		var kind string
		if err := s.db.QueryRow(ctx, `SELECT coalesce(parent_id::text, ''), kind FROM crm_products WHERE id::text = $1`, p.ID).Scan(&p.ParentID, &kind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				err = ErrNotFound
			}
			return "", err
		}
		if p.Kind == "" { // not given: as it was
			p.Kind = kind
		}
	}
	if p.ParentID != "" {
		parent, err := s.Product(ctx, p.ParentID)
		if err != nil {
			return "", err
		}
		if parent.ParentID != "" {
			return "", i18n.E("err.crm_variant_parent")
		}
		if parent.Service() {
			return "", i18n.E("err.crm_service_variant")
		}
		p.Kind = KindProduct
		p.SectionID, p.Unit, p.VATRate, p.VATIncluded = parent.SectionID, parent.Unit, parent.VATRate, parent.VATIncluded
		if p.Currency == "" {
			p.Currency = parent.Currency
		}
		if strings.TrimSpace(p.Name) == "" {
			p.Name = VariantName(parent.Name, props, p.Props)
		}
	}
	if err := cleanProduct(&p); err != nil {
		return "", err
	}
	if err := s.currency(ctx, &p.Currency); err != nil {
		return "", err
	}
	if p.Props, err = cleanProps(props, p.Props); err != nil {
		return "", err
	}
	if p.Barcodes, err = cleanBarcodes(p.Barcodes); err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if p.ID == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO crm_products (name, sku, currency, unit, description, price, vat_rate, vat_included, created_by,
				section_id, purchase_price, props, parent_id, kind) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
				p.Name, p.SKU, p.Currency, p.Unit, p.Description, p.Price, p.VATRate, p.VATIncluded, userID, optional(p.SectionID), p.PurchasePrice,
				p.Props, optional(p.ParentID), p.Kind).Scan(&p.ID); err != nil {
				return err
			}
		} else {
			if p.Service() {
				var kept bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_products WHERE parent_id::text = $1)
					OR EXISTS (SELECT 1 FROM crm_stock_doc_rows WHERE product_id::text = $1)
					OR EXISTS (SELECT 1 FROM crm_stock WHERE product_id::text = $1 AND quantity <> 0)`, p.ID).Scan(&kept); err != nil {
					return err
				}
				if kept {
					return i18n.E("err.crm_service_stock")
				}
				if _, err := tx.Exec(ctx, `UPDATE crm_product_rows SET warehouse_id = NULL, reserve = 0, reserve_until = NULL
					WHERE product_id::text = $1`, p.ID); err != nil {
					return err
				}
			}
			tag, err := tx.Exec(ctx, `UPDATE crm_products SET name = $2, sku = $3, currency = $4, unit = $5, description = $6, price = $7,
				vat_rate = $8, vat_included = $9, active = $10, section_id = $11, purchase_price = $12, props = $13, kind = $14 WHERE id::text = $1`,
				p.ID, p.Name, p.SKU, p.Currency, p.Unit, p.Description, p.Price, p.VATRate, p.VATIncluded, p.Active, optional(p.SectionID),
				p.PurchasePrice, p.Props, p.Kind)
			if err == nil && tag.RowsAffected() == 0 {
				err = ErrNotFound
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE crm_products SET section_id = $2, unit = $3, vat_rate = $4, vat_included = $5 WHERE parent_id::text = $1`,
				p.ID, optional(p.SectionID), p.Unit, p.VATRate, p.VATIncluded); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM crm_product_barcodes WHERE product_id = $1`, p.ID); err != nil {
			return err
		}
		for _, b := range p.Barcodes {
			if _, err := tx.Exec(ctx, `INSERT INTO crm_product_barcodes (barcode, product_id) VALUES ($1, $2)`, b, p.ID); err != nil {
				var pe interface{ SQLState() string }
				if errors.As(err, &pe) && pe.SQLState() == "23505" {
					return i18n.E("err.crm_barcode_taken", b)
				}
				return err
			}
		}
		return nil
	})
	return p.ID, sectionErr(err)
}

// VariantName names a variation: its product's name and, in brackets, its values of the properties «для вариаций»
// (a yes/no property — by its name).
func VariantName(product string, props []Prop, values map[string]string) string {
	var parts []string
	for _, pr := range props {
		v := strings.TrimSpace(values[pr.ID])
		if !pr.Variant || v == "" || (pr.Kind == PropBool && v == "0") {
			continue
		}
		if pr.Kind == PropBool {
			v = pr.Name
		}
		parts = append(parts, v)
	}
	if len(parts) == 0 {
		return product
	}
	return product + " (" + strings.Join(parts, ", ") + ")"
}

// DeleteProduct removes a product (with its variations) from the catalog; the cards keep their lines (by name and price).
func (s *Store) DeleteProduct(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM crm_products WHERE id::text = $1`, id)
	return err
}

// ProductRows of a card, in their order.
func (s *Store) ProductRows(ctx context.Context, entity, id string) ([]ProductRow, error) {
	rows, err := s.db.Query(ctx, `SELECT coalesce(r.product_id::text, ''), r.name, r.unit, r.price::float8, r.quantity::float8, r.discount::float8,
		r.vat_rate::float8, r.vat_included, coalesce(r.warehouse_id::text, ''), coalesce(w.name, ''), r.reserve::float8, r.reserve_until,
		coalesce(p.kind = 'service', false)
		FROM crm_product_rows r LEFT JOIN crm_warehouses w ON w.id = r.warehouse_id LEFT JOIN crm_products p ON p.id = r.product_id
		WHERE r.entity = $1 AND r.entity_id::text = $2 ORDER BY r.sort, r.id`, entity, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProductRow, error) {
		var x ProductRow
		err := r.Scan(&x.ProductID, &x.Name, &x.Unit, &x.Price, &x.Quantity, &x.Discount, &x.VATRate, &x.VATIncluded, &x.WarehouseID,
			&x.WarehouseName, &x.Reserve, &x.ReserveUntil, &x.Service)
		return x, err
	})
}

// rowTables: the cards that have product rows, and their tables.
var rowTables = map[string]string{"deal": "crm_deals", "lead": "crm_leads", "quote": "crm_invoices", "invoice": "crm_invoices"}

// SetProductRows replaces the lines of a deal, a lead, a quote or an invoice and counts its amount from them (with no lines the amount
// stays as it was). A line of a catalog product without a name takes the product's name, price, unit and VAT.
// Only a deal's line of a catalog product (not a service) is reserved: on its warehouse (the default one when a
// reserve is given without it), no more than its quantity.
func (s *Store) SetProductRows(ctx context.Context, userID, entity, id string, rows []ProductRow) (float64, error) {
	tbl := rowTables[entity]
	if tbl == "" {
		return 0, ErrNotFound
	}
	if len(rows) > 500 {
		return 0, i18n.E("err.crm_rows")
	}
	var ids []string
	for _, r := range rows {
		if r.ProductID != "" {
			ids = append(ids, r.ProductID)
		}
	}
	services := map[string]bool{}
	if len(ids) > 0 {
		rs, err := s.db.Query(ctx, `SELECT id::text FROM crm_products WHERE id::text = ANY($1) AND kind = 'service'`, ids)
		if err != nil {
			return 0, err
		}
		list, err := pgx.CollectRows(rs, pgx.RowTo[string])
		if err != nil {
			return 0, err
		}
		for _, id := range list {
			services[id] = true
		}
	}
	var set *StockSettings // read once a row needs it
	for i := range rows {
		r := &rows[i]
		if r.ProductID != "" && r.Name == "" {
			p, err := s.Product(ctx, r.ProductID)
			if err != nil {
				return 0, err
			}
			r.Name, r.Price, r.Unit, r.VATRate, r.VATIncluded = p.Name, p.Price, p.Unit, p.VATRate, p.VATIncluded
		}
		r.Service = services[r.ProductID]
		if err := clean(&r.Name, 300); err != nil || r.Name == "" {
			return 0, i18n.E("err.crm_product_name")
		}
		if err := clean(&r.Unit, 20); err != nil {
			return 0, err
		}
		if r.Quantity == 0 {
			r.Quantity = 1
		}
		if r.Price < 0 || r.Price > 1e15 || r.Quantity < 0 || r.Quantity > 1e12 {
			return 0, i18n.E("err.crm_amount")
		}
		if r.Discount < 0 || r.Discount > 100 || (r.VATRate != nil && (*r.VATRate < 0 || *r.VATRate > 100)) {
			return 0, i18n.E("err.crm_vat")
		}
		if entity != "deal" || r.ProductID == "" || r.Service {
			r.WarehouseID, r.Reserve, r.ReserveUntil = "", 0, nil
			continue
		}
		auto := r.Reserve < 0 // no reserve given: the whole quantity on the row's warehouse (any — with «Резервировать автоматически»)
		if auto {
			if set == nil {
				got, err := s.StockSettings(ctx)
				if err != nil {
					return 0, err
				}
				set = &got
			}
			r.Reserve = r.Quantity
			if r.WarehouseID == "" && !set.AutoReserve {
				r.Reserve = 0
			}
		}
		if r.Reserve > r.Quantity {
			return 0, i18n.E("err.stock_reserve")
		}
		if r.Reserve > 0 && r.WarehouseID == "" {
			if err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM crm_warehouses WHERE is_default), '')`).Scan(&r.WarehouseID); err != nil {
				return 0, err
			}
			if r.WarehouseID == "" {
				r.Reserve = 0
			}
		}
		if r.Reserve == 0 {
			r.ReserveUntil = nil
		} else if auto && r.ReserveUntil == nil {
			r.ReserveUntil = set.reserveUntil()
		}
	}
	total, _ := RowsTotal(rows)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var card string
		err := tx.QueryRow(ctx, `SELECT id FROM `+tbl+` WHERE id::text = $1 FOR UPDATE`, id).Scan(&card)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM crm_product_rows WHERE entity = $1 AND entity_id::text = $2`, entity, id); err != nil {
			return err
		}
		for i, r := range rows {
			if _, err := tx.Exec(ctx, `INSERT INTO crm_product_rows (entity, entity_id, product_id, name, price, quantity, unit, discount,
				vat_rate, vat_included, sort, warehouse_id, reserve, reserve_until) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
				entity, id, optional(r.ProductID), r.Name, r.Price, r.Quantity, r.Unit, r.Discount, r.VATRate, r.VATIncluded, i,
				optional(r.WarehouseID), r.Reserve, r.ReserveUntil); err != nil {
				var pe interface{ SQLState() string }
				if errors.As(err, &pe) && pe.SQLState() == "23503" { // the product is gone
					return ErrNotFound
				}
				return err
			}
		}
		if len(rows) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE `+tbl+` SET amount = $2, updated_at = now() WHERE id::text = $1`, id, total)
		return err
	})
	return total, err
}
