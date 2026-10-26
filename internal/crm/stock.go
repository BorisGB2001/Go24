package crm

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Складской учёт»: warehouses, the stock of catalog products on each and the stock documents —
// «Приход» from a supplier, «Оприходование», «Перемещение» from one warehouse to another, «Списание», «Реализация»
// of a deal (reserve.go). A document is
// a draft until it is conducted: then it changes the stock (there can't be less than nothing on a warehouse);
// cancelling the conducting gives the stock back, and the cancelled document can be changed and conducted again.
// Everyone sees the stock; administrators keep the warehouses, the documents are by the rights of the section's
// roles (stockaccess.go).

// Kinds of stock documents, in the order of the menu.
var StockKinds = []string{"arrival", "adjustment", "moving", "deduct", "shipment"}

// StockStatuses of a document.
var StockStatuses = []string{"draft", "conducted", "cancelled"}

// Warehouse is a «Склад».
type Warehouse struct {
	ID, Name, Address string
	Responsible       Ref
	Default, Active   bool
	Sort              int
	Products          int     // products on it (more than nothing)
	Value             float64 // their worth at the purchase prices
}

// StockDoc is a stock document.
type StockDoc struct {
	ID, Kind, Title, Status   string
	Number                    int
	Date                      time.Time
	Supplier                  Ref    // an arrival's supplier: a company or a contact of the CRM
	SupplierKind              string // "company" (or empty) or "contact"
	Deal                      Ref    // a shipment's («Реализация») deal
	IncomingNumber            string
	IncomingDate              *time.Time
	Reason, Comment, Currency string
	Amount                    float64
	Responsible, CreatedBy    Ref
	ConductedBy               Ref
	ConductedAt               *time.Time
	CreatedAt, UpdatedAt      time.Time
	Rows                      []StockRow // filled by StockDoc
}

// StockRow is a line of a stock document.
type StockRow struct {
	ProductID, Name string
	Unit            string // the product's (filled by StockDoc)
	Quantity, Price float64
	From, To        string // warehouses' ids, as the kind needs: an arrival and an adjustment go to, a deduct goes from
	FromName        string // (filled by StockDoc)
	ToName          string
}

// Sum of the row at its price.
func (r StockRow) Sum() float64 { return round2(r.Price * r.Quantity) }

// Editable: a draft or a cancelled document can be changed and deleted, a conducted one can't.
func (d StockDoc) Editable() bool { return d.Status != "conducted" }

// ValidStockKind: one of StockKinds.
func ValidStockKind(kind string) bool {
	for _, k := range StockKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// usesFrom and usesTo: which warehouses a kind's rows have.
func usesFrom(kind string) bool { return kind == "moving" || kind == "deduct" || kind == "shipment" }
func usesTo(kind string) bool   { return kind != "deduct" && kind != "shipment" }

// ---------- warehouses ----------

// Warehouses are all the warehouses: the default one first, then by sort and name; with what is on them.
func (s *Store) Warehouses(ctx context.Context) ([]Warehouse, error) {
	rows, err := s.db.Query(ctx, `SELECT w.id::text, w.name, w.address, coalesce(w.responsible_id::text, ''), coalesce(u.name, ''),
			w.is_default, w.active, w.sort,
			(SELECT count(*) FROM crm_stock k WHERE k.warehouse_id = w.id AND k.quantity > 0),
			coalesce((SELECT sum(k.quantity * coalesce(p.purchase_price, 0)) FROM crm_stock k JOIN crm_products p ON p.id = k.product_id
				WHERE k.warehouse_id = w.id), 0)::float8
		FROM crm_warehouses w LEFT JOIN users u ON u.id = w.responsible_id
		ORDER BY w.is_default DESC, w.sort, lower(w.name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Warehouse, error) {
		var w Warehouse
		err := r.Scan(&w.ID, &w.Name, &w.Address, &w.Responsible.ID, &w.Responsible.Name, &w.Default, &w.Active, &w.Sort, &w.Products, &w.Value)
		return w, err
	})
}

// SaveWarehouse creates (no ID) or changes a warehouse; the first one made is the default. Making one the default
// takes it from the other. Its storekeeper without a role of the section gets the «Кладовщик» one.
func (s *Store) SaveWarehouse(ctx context.Context, w Warehouse) (string, error) {
	if err := clean(&w.Name, 200); err != nil || w.Name == "" {
		return "", i18n.E("err.stock_warehouse_name")
	}
	if err := clean(&w.Address, 500); err != nil {
		return "", err
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var none bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM crm_warehouses WHERE is_default AND id::text <> $1)`, w.ID).Scan(&none); err != nil {
			return err
		}
		if none {
			w.Default, w.Active = true, true
		}
		if w.Default {
			if _, err := tx.Exec(ctx, `UPDATE crm_warehouses SET is_default = false WHERE is_default AND id::text <> $1`, w.ID); err != nil {
				return err
			}
		}
		if w.ID == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO crm_warehouses (name, address, responsible_id, is_default, active, sort)
				VALUES ($1, $2, $3, $4, true, $5) RETURNING id`, w.Name, w.Address, optional(w.Responsible.ID), w.Default, w.Sort).Scan(&w.ID); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE crm_warehouses SET name = $2, address = $3, responsible_id = $4, is_default = $5, active = $6 OR $5,
				sort = $7 WHERE id::text = $1`, w.ID, w.Name, w.Address, optional(w.Responsible.ID), w.Default, w.Active, w.Sort)
			if err == nil && tag.RowsAffected() == 0 {
				err = ErrNotFound
			}
			if err != nil {
				return err
			}
		}
		// the storekeeper without a role of the section gets the «Кладовщик» one: they keep the documents
		_, err := tx.Exec(ctx, `INSERT INTO crm_stock_role_members (role_id, user_id) SELECT r.id, $1::uuid FROM crm_stock_roles r
			WHERE r.code = 'storekeeper' AND $1::text IS NOT NULL AND NOT EXISTS (SELECT 1 FROM crm_stock_role_members WHERE user_id = $1::uuid)`,
			optional(w.Responsible.ID))
		return err
	})
	return w.ID, sectionErr(err)
}

// DeleteWarehouse removes a warehouse that nothing is on and no document names; else it can only be made inactive.
// The default one stays: another must become the default first.
func (s *Store) DeleteWarehouse(ctx context.Context, id string) error {
	var def bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_warehouses WHERE id::text = $1 AND is_default)`, id).Scan(&def); err != nil {
		return err
	}
	if def {
		return i18n.E("err.stock_warehouse_default")
	}
	var used bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stock WHERE warehouse_id::text = $1 AND quantity > 0)
		OR EXISTS (SELECT 1 FROM crm_stock_doc_rows WHERE from_warehouse::text = $1 OR to_warehouse::text = $1)`, id).Scan(&used)
	if err != nil {
		return err
	}
	if used {
		return i18n.E("err.stock_warehouse_used")
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_warehouses WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// ---------- the stock ----------

// StockLine is a product's stock: on each warehouse and all together.
type StockLine struct {
	Product
	On       map[string]float64 // by warehouses' ids
	Total    float64
	Reserved map[string]float64 // by deals, by warehouses' ids
	Reserve  float64            // all together
}

// Free is the available quantity: on the warehouses less the reserves.
func (l StockLine) Free() float64 { return round3(l.Total - l.Reserve) }

// Stock is what is on the warehouses: the products (not the services) of a section and its subsections ("" — the whole catalog)
// matching q, the active ones or all, with their quantities by warehouses. only: just those that are somewhere.
func (s *Store) Stock(ctx context.Context, q, section string, all, only bool) ([]StockLine, error) {
	list, err := s.Goods(ctx, q, section, all)
	if err != nil {
		return nil, err
	}
	on, err := s.stockOf(ctx, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.Reserves(ctx, nil, "")
	if err != nil {
		return nil, err
	}
	out := make([]StockLine, 0, len(list))
	for _, p := range list {
		x := StockLine{Product: p, On: on[p.ID], Reserved: res[p.ID]}
		for _, n := range x.On {
			x.Total += n
		}
		for _, n := range x.Reserved {
			x.Reserve += n
		}
		if only && x.Total == 0 {
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

// ProductStock is a product's quantities by warehouses (none: nothing anywhere).
func (s *Store) ProductStock(ctx context.Context, productID string) (map[string]float64, error) {
	on, err := s.stockOf(ctx, []string{productID})
	return on[productID], err
}

// StockTotals are the products' quantities on all the warehouses together.
func (s *Store) StockTotals(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.Query(ctx, `SELECT product_id::text, sum(quantity)::float8 FROM crm_stock GROUP BY product_id HAVING sum(quantity) > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]float64{}
	for rows.Next() {
		var id string
		var n float64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		m[id] = n
	}
	return m, rows.Err()
}

// stockOf: products' (nil — all) quantities by warehouses, more than nothing.
func (s *Store) stockOf(ctx context.Context, ids []string) (map[string]map[string]float64, error) {
	rows, err := s.db.Query(ctx, `SELECT product_id::text, warehouse_id::text, quantity::float8 FROM crm_stock
		WHERE quantity > 0 AND ($1::text[] IS NULL OR product_id::text = ANY($1))`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]map[string]float64{}
	for rows.Next() {
		var p, w string
		var n float64
		if err := rows.Scan(&p, &w, &n); err != nil {
			return nil, err
		}
		if m[p] == nil {
			m[p] = map[string]float64{}
		}
		m[p][w] = n
	}
	return m, rows.Err()
}

// ---------- documents ----------

// StockFilter of the documents' list.
type StockFilter struct {
	Kind, Status, Q string
	ProductID       string // documents with this product
	DealID          string // a deal's shipments
	Limit           int
	// what the viewer may read (StockRights): the kinds and the warehouses, nil — all; a document without warehouses is
	// seen by any
	Kinds, Warehouses []string
}

const stockDocCols = `d.id::text, d.kind, d.number, d.title, d.status, d.doc_date, coalesce(d.supplier_id::text, d.supplier_contact_id::text, ''),
	coalesce(c.title, sc.name, ''), CASE WHEN d.supplier_contact_id IS NOT NULL THEN 'contact' WHEN d.supplier_id IS NOT NULL THEN 'company' ELSE '' END,
	d.incoming_number, d.incoming_date, d.reason, d.comment, d.currency, d.amount::float8, d.responsible_id::text, ur.name,
	d.created_by::text, uc.name, coalesce(d.conducted_by::text, ''), coalesce(ux.name, ''), d.conducted_at, d.created_at, d.updated_at,
	coalesce(d.deal_id::text, ''), coalesce(dl.title, '')`

const stockDocFrom = ` FROM crm_stock_docs d LEFT JOIN crm_companies c ON c.id = d.supplier_id
	LEFT JOIN crm_contacts sc ON sc.id = d.supplier_contact_id JOIN users ur ON ur.id = d.responsible_id
	JOIN users uc ON uc.id = d.created_by LEFT JOIN users ux ON ux.id = d.conducted_by LEFT JOIN crm_deals dl ON dl.id = d.deal_id`

func scanStockDocs(rows pgx.Rows, err error) ([]StockDoc, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StockDoc, error) {
		var d StockDoc
		err := r.Scan(&d.ID, &d.Kind, &d.Number, &d.Title, &d.Status, &d.Date, &d.Supplier.ID, &d.Supplier.Name, &d.SupplierKind, &d.IncomingNumber,
			&d.IncomingDate, &d.Reason, &d.Comment, &d.Currency, &d.Amount, &d.Responsible.ID, &d.Responsible.Name, &d.CreatedBy.ID,
			&d.CreatedBy.Name, &d.ConductedBy.ID, &d.ConductedBy.Name, &d.ConductedAt, &d.CreatedAt, &d.UpdatedAt, &d.Deal.ID, &d.Deal.Name)
		return d, err
	})
}

// StockDocs are the documents, the latest first.
func (s *Store) StockDocs(ctx context.Context, f StockFilter) ([]StockDoc, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 500
	}
	return scanStockDocs(s.db.Query(ctx, `SELECT `+stockDocCols+stockDocFrom+`
		WHERE ($1 = '' OR d.kind = $1) AND ($2 = '' OR d.status = $2)
			AND ($3 = '%%' OR d.title ILIKE $3 OR d.number::text = $4 OR d.incoming_number ILIKE $3 OR c.title ILIKE $3 OR sc.name ILIKE $3)
			AND ($5 = '' OR EXISTS (SELECT 1 FROM crm_stock_doc_rows r WHERE r.doc_id = d.id AND r.product_id::text = $5))
			AND ($7 = '' OR d.deal_id::text = $7) AND ($8::text[] IS NULL OR d.kind = ANY($8))
			AND ($9::text[] IS NULL OR NOT EXISTS (SELECT 1 FROM crm_stock_doc_rows r WHERE r.doc_id = d.id AND (r.from_warehouse IS NOT NULL OR r.to_warehouse IS NOT NULL))
				OR EXISTS (SELECT 1 FROM crm_stock_doc_rows r WHERE r.doc_id = d.id AND (r.from_warehouse::text = ANY($9) OR r.to_warehouse::text = ANY($9))))
		ORDER BY d.doc_date DESC, d.number DESC LIMIT $6`, f.Kind, f.Status, like(f.Q), f.Q, f.ProductID, f.Limit, f.DealID, f.Kinds, f.Warehouses))
}

// StockDoc is one document with its rows.
func (s *Store) StockDoc(ctx context.Context, id string) (StockDoc, error) {
	list, err := scanStockDocs(s.db.Query(ctx, `SELECT `+stockDocCols+stockDocFrom+` WHERE d.id::text = $1`, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return StockDoc{}, err
	}
	d := list[0]
	rows, err := s.db.Query(ctx, `SELECT coalesce(r.product_id::text, ''), r.name, coalesce(p.unit, ''), r.quantity::float8, r.price::float8,
			coalesce(r.from_warehouse::text, ''), coalesce(wf.name, ''), coalesce(r.to_warehouse::text, ''), coalesce(wt.name, '')
		FROM crm_stock_doc_rows r LEFT JOIN crm_products p ON p.id = r.product_id
			LEFT JOIN crm_warehouses wf ON wf.id = r.from_warehouse LEFT JOIN crm_warehouses wt ON wt.id = r.to_warehouse
		WHERE r.doc_id::text = $1 ORDER BY r.sort, r.id`, id)
	if err != nil {
		return StockDoc{}, err
	}
	d.Rows, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (StockRow, error) {
		var x StockRow
		err := r.Scan(&x.ProductID, &x.Name, &x.Unit, &x.Quantity, &x.Price, &x.From, &x.FromName, &x.To, &x.ToName)
		return x, err
	})
	return d, err
}

// SaveStockDoc creates (no ID) or changes a document that isn't conducted, with its rows. A row needs a catalog
// product and the warehouses its kind moves it between (the default one when none is given).
func (s *Store) SaveStockDoc(ctx context.Context, userID string, d StockDoc) (string, error) {
	if !ValidStockKind(d.Kind) && d.ID == "" {
		return "", ErrNotFound
	}
	for _, f := range []struct {
		v   *string
		max int
	}{{&d.Title, 300}, {&d.IncomingNumber, 100}, {&d.Reason, 500}, {&d.Comment, 10000}} {
		if err := clean(f.v, f.max); err != nil {
			return "", err
		}
	}
	if len(d.Rows) > 500 {
		return "", i18n.E("err.crm_rows")
	}
	if err := s.currency(ctx, &d.Currency); err != nil {
		return "", err
	}
	if d.Responsible.ID == "" {
		d.Responsible.ID = userID
	}
	if d.Date.IsZero() {
		d.Date = time.Now()
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if d.ID != "" {
			var status string
			err := tx.QueryRow(ctx, `SELECT kind, status FROM crm_stock_docs WHERE id::text = $1 FOR UPDATE`, d.ID).Scan(&d.Kind, &status)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if status == "conducted" {
				return i18n.E("err.stock_conducted")
			}
		}
		var def string
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM crm_warehouses WHERE is_default), '')`).Scan(&def); err != nil {
			return err
		}
		var total float64
		for i := range d.Rows {
			r := &d.Rows[i]
			if r.ProductID == "" {
				return i18n.E("err.stock_product")
			}
			var name, kind string
			var purchase *float64
			var price float64
			err := tx.QueryRow(ctx, `SELECT name, purchase_price::float8, price::float8, kind FROM crm_products WHERE id::text = $1`,
				r.ProductID).Scan(&name, &purchase, &price, &kind)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return sectionErr(err)
			}
			if kind == KindService {
				return i18n.E("err.stock_service", name)
			}
			r.Name = name
			if r.Quantity <= 0 || r.Quantity > 1e12 || r.Price < 0 || r.Price > 1e15 {
				return i18n.E("err.stock_quantity")
			}
			if r.Price == 0 && d.Kind == "shipment" { // a shipment is at the selling price
				r.Price = price
			} else if r.Price == 0 && purchase != nil && d.Kind != "moving" {
				r.Price = *purchase
			}
			if !usesFrom(d.Kind) {
				r.From = ""
			} else if r.From == "" {
				r.From = def
			}
			if !usesTo(d.Kind) {
				r.To = ""
			} else if r.To == "" {
				r.To = def
			}
			if (usesFrom(d.Kind) && r.From == "") || (usesTo(d.Kind) && r.To == "") {
				return i18n.E("err.stock_no_warehouse")
			}
			if d.Kind == "moving" && r.From == r.To {
				return i18n.E("err.stock_same_warehouse")
			}
			total += r.Sum()
		}
		if d.ID == "" {
			var deal *string
			if d.Kind == "shipment" {
				deal = optional(d.Deal.ID)
			}
			err := tx.QueryRow(ctx, `INSERT INTO crm_stock_docs (kind, number, title, doc_date, supplier_id, incoming_number, incoming_date,
					reason, comment, currency, amount, responsible_id, created_by, deal_id, supplier_contact_id)
				VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM crm_stock_docs), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
				d.Kind, d.Title, d.Date, supplier(d, "company"), d.IncomingNumber, d.IncomingDate, d.Reason, d.Comment, d.Currency, round2(total),
				d.Responsible.ID, userID, deal, supplier(d, "contact")).Scan(&d.ID)
			if err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE crm_stock_docs SET title = $2, doc_date = $3, supplier_id = $4, incoming_number = $5,
					incoming_date = $6, reason = $7, comment = $8, currency = $9, amount = $10, responsible_id = $11, supplier_contact_id = $12,
					updated_at = now()
				WHERE id::text = $1`, d.ID, d.Title, d.Date, supplier(d, "company"), d.IncomingNumber, d.IncomingDate, d.Reason, d.Comment, d.Currency,
				round2(total), d.Responsible.ID, supplier(d, "contact")); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM crm_stock_doc_rows WHERE doc_id::text = $1`, d.ID); err != nil {
				return err
			}
		}
		for i, r := range d.Rows {
			if _, err := tx.Exec(ctx, `INSERT INTO crm_stock_doc_rows (doc_id, product_id, name, quantity, price, from_warehouse, to_warehouse, sort)
				VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)`, d.ID, r.ProductID, r.Name, r.Quantity, r.Price, optional(r.From), optional(r.To), i); err != nil {
				return err
			}
		}
		return nil
	})
	return d.ID, sectionErr(err)
}

// supplier: an arrival's supplier if it is of the kind ("company" or "contact"); other documents have none.
func supplier(d StockDoc, kind string) *string {
	if d.Kind != "arrival" || (d.SupplierKind == "contact") != (kind == "contact") {
		return nil
	}
	return optional(d.Supplier.ID)
}

// DeleteStockDoc removes a document that isn't conducted.
func (s *Store) DeleteStockDoc(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_stock_docs WHERE id::text = $1 AND status <> 'conducted'`, id)
	if err == nil && tag.RowsAffected() == 0 {
		var conducted bool
		if s.db.QueryRow(ctx, `SELECT true FROM crm_stock_docs WHERE id::text = $1`, id).Scan(&conducted) == nil {
			return i18n.E("err.stock_conducted")
		}
		err = ErrNotFound
	}
	return err
}

// ConductStockDoc conducts a document («Провести»): its rows change the stock, all of them or none. There can't be
// less than nothing on a warehouse. An arrival's prices become the products' purchase prices.
func (s *Store) ConductStockDoc(ctx context.Context, userID, id string) error {
	return s.moveStock(ctx, userID, id, true)
}

// CancelStockDoc cancels the conducting («Отменить проведение»): the stock goes back as it was before the document
// (if what it brought is still there).
func (s *Store) CancelStockDoc(ctx context.Context, userID, id string) error {
	return s.moveStock(ctx, userID, id, false)
}

func (s *Store) moveStock(ctx context.Context, userID, id string, conduct bool) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var kind, status string
		err := tx.QueryRow(ctx, `SELECT kind, status FROM crm_stock_docs WHERE id::text = $1 FOR UPDATE`, id).Scan(&kind, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if conduct == (status == "conducted") {
			if conduct {
				return i18n.E("err.stock_conducted")
			}
			return i18n.E("err.stock_not_conducted")
		}
		rows, err := tx.Query(ctx, `SELECT coalesce(product_id::text, ''), name, quantity::float8, price::float8, coalesce(from_warehouse::text, ''),
			coalesce(to_warehouse::text, '') FROM crm_stock_doc_rows WHERE doc_id::text = $1 ORDER BY sort, id`, id)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StockRow, error) {
			var x StockRow
			err := r.Scan(&x.ProductID, &x.Name, &x.Quantity, &x.Price, &x.From, &x.To)
			return x, err
		})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return i18n.E("err.stock_empty")
		}
		for _, r := range list {
			if r.ProductID == "" {
				return i18n.E("err.stock_product_gone", r.Name)
			}
			take, put := r.From, r.To // conducting takes from From and puts to To; cancelling — the other way
			if !conduct {
				take, put = r.To, r.From
			}
			if take != "" {
				tag, err := tx.Exec(ctx, `UPDATE crm_stock SET quantity = quantity - $3 WHERE product_id::text = $1 AND warehouse_id::text = $2
					AND quantity >= $3`, r.ProductID, take, r.Quantity)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					var wh string
					_ = tx.QueryRow(ctx, `SELECT name FROM crm_warehouses WHERE id::text = $1`, take).Scan(&wh)
					return i18n.E("err.stock_short", r.Name, wh)
				}
			}
			if put != "" {
				if _, err := tx.Exec(ctx, `INSERT INTO crm_stock (product_id, warehouse_id, quantity) VALUES ($1::uuid, $2::uuid, $3)
					ON CONFLICT (product_id, warehouse_id) DO UPDATE SET quantity = crm_stock.quantity + excluded.quantity`,
					r.ProductID, put, r.Quantity); err != nil {
					return err
				}
			}
			if conduct && kind == "arrival" && r.Price > 0 {
				if _, err := tx.Exec(ctx, `UPDATE crm_products SET purchase_price = $2 WHERE id::text = $1`, r.ProductID, r.Price); err != nil {
					return err
				}
			}
		}
		if conduct {
			_, err = tx.Exec(ctx, `UPDATE crm_stock_docs SET status = 'conducted', conducted_by = $2, conducted_at = now(), updated_at = now()
				WHERE id::text = $1`, id, userID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE crm_stock_docs SET status = 'cancelled', updated_at = now() WHERE id::text = $1`, id)
		}
		return err
	})
}
