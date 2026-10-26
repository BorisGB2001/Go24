package crm

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// The reports of «Складской учёт»: «Остатки» on a date by warehouses and sections, valued at the average purchase
// price; «Движение товаров» over a period — what was at its start, what came, what left and what is at its end; and
// a product's movements document by document with what was left after each; «Продажи» over a period — what the
// conducted «Реализации» took, the revenue, the cost and the profit. Only conducted documents move the
// stock, on their documents' dates: the stock on a date is today's less what the documents after it brought.

// StockMove is a product coming to (Quantity above zero) or leaving (below zero) a warehouse by a conducted document.
type StockMove struct {
	Date               time.Time
	DocID, Kind, Title string
	Number             int
	ProductID          string
	Warehouse          Ref
	Quantity, Price    float64
	Balance            float64 // left after it: on the report's warehouse, or on all of them (filled by ProductMoves)
}

// In is what came by the movement, Out — what left.
func (m StockMove) In() float64  { return max(m.Quantity, 0) }
func (m StockMove) Out() float64 { return max(-m.Quantity, 0) }

// StockReportFilter: the products of a section ("" — all) matching Q, on one warehouse ("" — all).
type StockReportFilter struct {
	Q, Section, Warehouse string
	Date, From, To        time.Time // «Остатки» on Date; «Движение товаров» from From to To, the days included
	ProductID             string    // ProductMoves: the product
}

// StockReportLine is a product's line of «Остатки»: its quantity on the date (by warehouses and on the report's
// warehouse or all together), the average purchase price (the product's purchase price when nothing came at a price)
// and their sum.
type StockReportLine struct {
	Product
	On                  map[string]float64
	Quantity, Cost, Sum float64
}

// MovementLine is a product's line of «Движение товаров».
type MovementLine struct {
	Product
	Opening, In, Out, Closing float64
	Cost, Sum                 float64 // the average purchase price at the end and the closing worth
}

// SalesLine is a product's line of «Продажи»: how much the conducted shipments of the period took, at what sum (in
// the base currency, by the rates), the cost at the average purchase price at the period's end, the profit and the
// profitability (the profit to the cost, percent; 0 without the cost).
type SalesLine struct {
	Product
	Quantity, Revenue, Cost, Profit, Margin float64
	Docs                                    int // the shipments
}

// dayOf: the date's day (a report's dates are days, the documents' too).
func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// stockMoves are the movements of the products (nil — all) on a warehouse ("" — all) with the documents' dates
// after `after` (nil — from the start) and up to `upto` (nil — till the end), in the documents' order.
func (s *Store) stockMoves(ctx context.Context, ids []string, warehouse string, after, upto *time.Time) ([]StockMove, error) {
	rows, err := s.db.Query(ctx, `SELECT d.doc_date, d.id::text, d.kind, d.title, d.number, m.product_id, m.wh, coalesce(w.name, ''), m.q, m.price
		FROM (SELECT doc_id, product_id::text, to_warehouse::text AS wh, quantity::float8 AS q, price::float8, sort, id, 1 AS side
				FROM crm_stock_doc_rows WHERE to_warehouse IS NOT NULL AND product_id IS NOT NULL
			UNION ALL SELECT doc_id, product_id::text, from_warehouse::text, -quantity::float8, price::float8, sort, id, 0
				FROM crm_stock_doc_rows WHERE from_warehouse IS NOT NULL AND product_id IS NOT NULL) m
			JOIN crm_stock_docs d ON d.id = m.doc_id LEFT JOIN crm_warehouses w ON w.id::text = m.wh
		WHERE d.status = 'conducted' AND ($1::text[] IS NULL OR m.product_id = ANY($1)) AND ($2 = '' OR m.wh = $2)
			AND ($3::date IS NULL OR d.doc_date > $3) AND ($4::date IS NULL OR d.doc_date <= $4)
		ORDER BY d.doc_date, d.number, m.sort, m.id, m.side`, ids, warehouse, after, upto)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StockMove, error) {
		var m StockMove
		err := r.Scan(&m.Date, &m.DocID, &m.Kind, &m.Title, &m.Number, &m.ProductID, &m.Warehouse.ID, &m.Warehouse.Name, &m.Quantity, &m.Price)
		return m, err
	})
}

// stockOn: the products' (nil — all) quantities by warehouses at the end of the day — today's stock less what
// came after it and plus what left.
func (s *Store) stockOn(ctx context.Context, ids []string, day time.Time) (map[string]map[string]float64, error) {
	on, err := s.stockOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	day = dayOf(day)
	later, err := s.stockMoves(ctx, ids, "", &day, nil)
	if err != nil {
		return nil, err
	}
	for _, m := range later {
		if on[m.ProductID] == nil {
			on[m.ProductID] = map[string]float64{}
		}
		on[m.ProductID][m.Warehouse.ID] = round3(on[m.ProductID][m.Warehouse.ID] - m.Quantity)
	}
	for p, by := range on {
		for w, n := range by {
			if n == 0 {
				delete(by, w)
			}
		}
		if len(by) == 0 {
			delete(on, p)
		}
	}
	return on, nil
}

// AverageCosts are the products' average purchase prices on the day: what came by the conducted arrivals and
// adjustments at a price up to it, weighted by the quantities.
func (s *Store) AverageCosts(ctx context.Context, day time.Time) (map[string]float64, error) {
	rows, err := s.db.Query(ctx, `SELECT r.product_id::text, (sum(r.quantity * r.price) / sum(r.quantity))::float8
		FROM crm_stock_doc_rows r JOIN crm_stock_docs d ON d.id = r.doc_id
		WHERE d.status = 'conducted' AND d.kind IN ('arrival', 'adjustment') AND r.price > 0 AND r.product_id IS NOT NULL AND d.doc_date <= $1
		GROUP BY r.product_id`, dayOf(day))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]float64{}
	for rows.Next() {
		var id string
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		m[id] = round2(v)
	}
	return m, rows.Err()
}

// costOf: the average purchase price, else the product's purchase price.
func costOf(p Product, avg map[string]float64) float64 {
	if v, ok := avg[p.ID]; ok {
		return v
	}
	if p.PurchasePrice != nil {
		return *p.PurchasePrice
	}
	return 0
}

// StockReport is «Остатки» on f.Date: the products that were on the warehouse (or any), with their worth.
func (s *Store) StockReport(ctx context.Context, f StockReportFilter) ([]StockReportLine, error) {
	list, err := s.Goods(ctx, f.Q, f.Section, true)
	if err != nil {
		return nil, err
	}
	on, err := s.stockOn(ctx, nil, f.Date)
	if err != nil {
		return nil, err
	}
	avg, err := s.AverageCosts(ctx, f.Date)
	if err != nil {
		return nil, err
	}
	out := []StockReportLine{}
	for _, p := range list {
		x := StockReportLine{Product: p, On: on[p.ID]}
		for w, n := range x.On {
			if f.Warehouse == "" || w == f.Warehouse {
				x.Quantity += n
			}
		}
		if x.Quantity = round3(x.Quantity); x.Quantity == 0 {
			continue
		}
		x.Cost = costOf(p, avg)
		x.Sum = round2(x.Quantity * x.Cost)
		out = append(out, x)
	}
	return out, nil
}

// MovementReport is «Движение товаров» from f.From to f.To: the products that were on the warehouse (or any) or
// moved. On all the warehouses together a transfer between them neither comes nor leaves.
func (s *Store) MovementReport(ctx context.Context, f StockReportFilter) ([]MovementLine, error) {
	list, err := s.Goods(ctx, f.Q, f.Section, true)
	if err != nil {
		return nil, err
	}
	from, to := dayOf(f.From).AddDate(0, 0, -1), dayOf(f.To)
	on, err := s.stockOn(ctx, nil, from)
	if err != nil {
		return nil, err
	}
	moves, err := s.stockMoves(ctx, nil, f.Warehouse, &from, &to)
	if err != nil {
		return nil, err
	}
	avg, err := s.AverageCosts(ctx, to)
	if err != nil {
		return nil, err
	}
	in, out := map[string]float64{}, map[string]float64{}
	for _, m := range moves {
		switch {
		case f.Warehouse == "" && m.Kind == "moving":
		case m.Quantity > 0:
			in[m.ProductID] += m.Quantity
		default:
			out[m.ProductID] -= m.Quantity
		}
	}
	lines := []MovementLine{}
	for _, p := range list {
		x := MovementLine{Product: p, In: round3(in[p.ID]), Out: round3(out[p.ID])}
		for w, n := range on[p.ID] {
			if f.Warehouse == "" || w == f.Warehouse {
				x.Opening += n
			}
		}
		x.Opening = round3(x.Opening)
		x.Closing = round3(x.Opening + x.In - x.Out)
		if x.Opening == 0 && x.In == 0 && x.Out == 0 {
			continue
		}
		x.Cost = costOf(p, avg)
		x.Sum = round2(x.Closing * x.Cost)
		lines = append(lines, x)
	}
	return lines, nil
}

// ProductMoves are a product's movements from f.From to f.To on the warehouse (or all), each with what was left
// after it, and what was at the start.
func (s *Store) ProductMoves(ctx context.Context, f StockReportFilter) (float64, []StockMove, error) {
	from, to := dayOf(f.From).AddDate(0, 0, -1), dayOf(f.To)
	on, err := s.stockOn(ctx, []string{f.ProductID}, from)
	if err != nil {
		return 0, nil, err
	}
	moves, err := s.stockMoves(ctx, []string{f.ProductID}, f.Warehouse, &from, &to)
	if err != nil {
		return 0, nil, err
	}
	var opening float64
	for w, n := range on[f.ProductID] {
		if f.Warehouse == "" || w == f.Warehouse {
			opening += n
		}
	}
	opening = round3(opening)
	left := opening
	for i := range moves {
		left = round3(left + moves[i].Quantity)
		moves[i].Balance = left
	}
	return opening, moves, nil
}

// SalesReport is «Продажи» from f.From to f.To: the products the conducted shipments took from the warehouse (or
// any), the most sold by revenue first.
func (s *Store) SalesReport(ctx context.Context, f StockReportFilter) ([]SalesLine, error) {
	list, err := s.Goods(ctx, f.Q, f.Section, true)
	if err != nil {
		return nil, err
	}
	from, to := dayOf(f.From).AddDate(0, 0, -1), dayOf(f.To)
	rows, err := s.db.Query(ctx, `SELECT r.product_id::text, sum(r.quantity)::float8, sum(r.quantity * r.price * coalesce(c.rate, 1))::float8,
			count(DISTINCT d.id)::int
		FROM crm_stock_doc_rows r JOIN crm_stock_docs d ON d.id = r.doc_id LEFT JOIN crm_currencies c ON c.code = d.currency
		WHERE d.status = 'conducted' AND d.kind = 'shipment' AND r.product_id IS NOT NULL AND d.doc_date > $1 AND d.doc_date <= $2
			AND ($3 = '' OR r.from_warehouse::text = $3)
		GROUP BY r.product_id`, from, to, f.Warehouse)
	if err != nil {
		return nil, err
	}
	type sold struct {
		q, sum float64
		docs   int
	}
	by := map[string]sold{}
	for rows.Next() {
		var id string
		var x sold
		if err := rows.Scan(&id, &x.q, &x.sum, &x.docs); err != nil {
			rows.Close()
			return nil, err
		}
		by[id] = x
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	avg, err := s.AverageCosts(ctx, to)
	if err != nil {
		return nil, err
	}
	out := []SalesLine{}
	for _, p := range list {
		x, ok := by[p.ID]
		if !ok {
			continue
		}
		l := SalesLine{Product: p, Quantity: round3(x.q), Revenue: round2(x.sum), Docs: x.docs}
		l.Cost = round2(l.Quantity * costOf(p, avg))
		l.Profit = round2(l.Revenue - l.Cost)
		if l.Cost > 0 {
			l.Margin = math.Round(l.Profit/l.Cost*1000) / 10
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Revenue > out[j].Revenue })
	return out, nil
}
