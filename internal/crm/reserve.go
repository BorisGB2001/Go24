package crm

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The reserve and «Реализация» of the «Складской учёт»: a deal's product row is reserved on a warehouse,
// how many and till when. A reserve holds while the deal is open (its stage isn't a success or a failure) and its
// date hasn't passed; what the deal's conducted shipments took from that warehouse is no longer reserved. The
// available stock is what is on a warehouse less the reserves. A shipment is made from a deal's products — what
// isn't shipped yet — and, conducted, takes them from the warehouses.

// reservesSQL: products' (nil — all) reserves by warehouses; with $2 a deal's own reserves aren't counted.
const reservesSQL = `WITH r AS (
		SELECT p.entity_id AS deal, p.product_id, p.warehouse_id, sum(p.reserve) AS q
		FROM crm_product_rows p JOIN crm_deals d ON d.id = p.entity_id JOIN crm_stages st ON st.id = d.stage_id
		WHERE p.entity = 'deal' AND p.reserve > 0 AND p.warehouse_id IS NOT NULL AND p.product_id IS NOT NULL
			AND st.semantic = 'process' AND (p.reserve_until IS NULL OR p.reserve_until >= CURRENT_DATE)
			AND ($1::text[] IS NULL OR p.product_id::text = ANY($1)) AND ($2 = '' OR p.entity_id::text <> $2)
		GROUP BY 1, 2, 3),
	sh AS (
		SELECT d.deal_id AS deal, x.product_id, x.from_warehouse AS warehouse_id, sum(x.quantity) AS q
		FROM crm_stock_docs d JOIN crm_stock_doc_rows x ON x.doc_id = d.id
		WHERE d.kind = 'shipment' AND d.status = 'conducted' AND d.deal_id IN (SELECT deal FROM r)
		GROUP BY 1, 2, 3)
	SELECT r.product_id::text, r.warehouse_id::text, sum(greatest(r.q - coalesce(sh.q, 0), 0))::float8
	FROM r LEFT JOIN sh ON sh.deal = r.deal AND sh.product_id = r.product_id AND sh.warehouse_id = r.warehouse_id
	GROUP BY 1, 2 HAVING sum(greatest(r.q - coalesce(sh.q, 0), 0)) > 0`

// Reserves are the products' (nil — all) reserved quantities by warehouses; but not those of the deal "but".
func (s *Store) Reserves(ctx context.Context, ids []string, but string) (map[string]map[string]float64, error) {
	rows, err := s.db.Query(ctx, reservesSQL, ids, but)
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

// Available is how much of the products is free on each warehouse: what is there less the reserves (of all the
// deals but "but"); below zero when more is reserved than there is.
func (s *Store) Available(ctx context.Context, ids []string, but string) (map[string]map[string]float64, error) {
	on, err := s.stockOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	res, err := s.Reserves(ctx, ids, but)
	if err != nil {
		return nil, err
	}
	for p, by := range res {
		if on[p] == nil {
			on[p] = map[string]float64{}
		}
		for w, n := range by {
			on[p][w] = round3(on[p][w] - n)
		}
	}
	return on, nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// shipped: what of the deal's products its shipments took or are to take (the conducted ones and the drafts;
// done — only the conducted ones), by products.
func (s *Store) shipped(ctx context.Context, q pgx.Tx, dealID string, done bool) (map[string]float64, error) {
	rows, err := q.Query(ctx, `SELECT x.product_id::text, sum(x.quantity)::float8 FROM crm_stock_docs d JOIN crm_stock_doc_rows x ON x.doc_id = d.id
		WHERE d.kind = 'shipment' AND d.deal_id::text = $1 AND x.product_id IS NOT NULL
			AND (d.status = 'conducted' OR (NOT $2 AND d.status = 'draft')) GROUP BY 1`, dealID, done)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]float64{}
	for rows.Next() {
		var p string
		var n float64
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		m[p] = n
	}
	return m, rows.Err()
}

// Unshipped is how much of a deal's catalog products (not the services) its conducted shipments haven't taken yet.
func (s *Store) Unshipped(ctx context.Context, dealID string) (float64, error) {
	var left float64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		done, err := s.shipped(ctx, tx, dealID, true)
		if err != nil {
			return err
		}
		need := map[string]float64{}
		rows, err := tx.Query(ctx, `SELECT r.product_id::text, sum(r.quantity)::float8 FROM crm_product_rows r JOIN crm_products p ON p.id = r.product_id
			WHERE r.entity = 'deal' AND r.entity_id::text = $1 AND p.kind = 'product' GROUP BY 1`, dealID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			var n float64
			if err := rows.Scan(&p, &n); err != nil {
				rows.Close()
				return err
			}
			need[p] = n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for p, n := range need {
			if n > done[p] {
				left += n - done[p]
			}
		}
		return nil
	})
	return round3(left), err
}

// NewShipment makes a draft «Реализация» of a deal: its catalog products (not the services) that no shipment (conducted or a draft)
// takes yet, from their rows' warehouses (the default one for a row without), at the rows' prices with the
// discount and VAT. Nothing left to ship — an error.
func (s *Store) NewShipment(ctx context.Context, userID, dealID string) (string, error) {
	var d StockDoc
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var title, currency string
		err := tx.QueryRow(ctx, `SELECT title, currency FROM crm_deals WHERE id::text = $1`, dealID).Scan(&title, &currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		taken, err := s.shipped(ctx, tx, dealID, false)
		if err != nil {
			return err
		}
		var def string
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM crm_warehouses WHERE is_default), '')`).Scan(&def); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.product_id::text, r.name, r.price::float8, r.quantity::float8, r.discount::float8, r.vat_rate::float8,
				r.vat_included, coalesce(r.warehouse_id::text, '')
			FROM crm_product_rows r JOIN crm_products p ON p.id = r.product_id
			WHERE r.entity = 'deal' AND r.entity_id::text = $1 AND p.kind = 'product' ORDER BY r.sort, r.id`, dealID)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProductRow, error) {
			var x ProductRow
			err := r.Scan(&x.ProductID, &x.Name, &x.Price, &x.Quantity, &x.Discount, &x.VATRate, &x.VATIncluded, &x.WarehouseID)
			return x, err
		})
		if err != nil {
			return err
		}
		d = StockDoc{Kind: "shipment", Title: title, Currency: currency, Deal: Ref{ID: dealID}, Responsible: Ref{ID: userID}, Date: time.Now()}
		for _, x := range list {
			n := x.Quantity
			if t := taken[x.ProductID]; t > 0 { // the earlier shipments take from the first rows of a product
				use := min(t, n)
				taken[x.ProductID] -= use
				n = round3(n - use)
			}
			if n <= 0 {
				continue
			}
			from := x.WarehouseID
			if from == "" {
				from = def
			}
			d.Rows = append(d.Rows, StockRow{ProductID: x.ProductID, Quantity: n, Price: round2(x.Total() / x.Quantity), From: from})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(d.Rows) == 0 {
		return "", i18n.E("err.stock_shipped_all")
	}
	return s.SaveStockDoc(ctx, userID, d)
}
