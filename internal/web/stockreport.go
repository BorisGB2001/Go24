package web

import (
	"encoding/csv"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
)

// ---------- The reports of «Складской учёт»: «Остатки» on a date, «Движение товаров» over a period, a product's movements, «Продажи» ----------

// stockReportFilter reads a report's filter: «Остатки» on ?date= (today), «Движение товаров» from ?from= (the month's
// first day) to ?to= (today); the warehouse, the section, the search and a product's movements (?product=).
func stockReportFilter(q url.Values) crm.StockReportFilter {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	f := crm.StockReportFilter{Q: q.Get("q"), Section: q.Get("section"), Warehouse: q.Get("warehouse"), ProductID: q.Get("product"),
		Date: today, From: today.AddDate(0, 0, 1-today.Day()), To: today}
	for k, t := range map[string]*time.Time{"date": &f.Date, "from": &f.From, "to": &f.To} {
		if d := formDay(q.Get(k)); d != nil {
			*t = *d
		}
	}
	if f.To.Before(f.From) {
		f.From, f.To = f.To, f.From
	}
	return f
}

// stockReport is the page of a report (kind "stock", "moves" or "sales"; with ?product= — the product's movements);
// ?csv=1 gives it as a CSV file.
func (s *Server) stockReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind := r.PathValue("kind")
	if kind != "stock" && kind != "moves" && kind != "sales" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	f := stockReportFilter(q)
	data := s.stockData(r, sess, "reports")
	data["Report"], data["F"] = kind, f
	sections, err := s.CRM.Sections(r.Context())
	var whs []crm.Warehouse
	if err == nil {
		whs, err = s.CRM.Warehouses(r.Context())
	}
	if err == nil {
		switch {
		case kind == "moves" && f.ProductID != "":
			var p crm.Product
			if p, err = s.CRM.Product(r.Context(), f.ProductID); err == nil {
				data["Product"] = p
				data["Opening"], data["Moves"], err = s.CRM.ProductMoves(r.Context(), f)
			}
		case kind == "moves":
			data["Items"], err = s.CRM.MovementReport(r.Context(), f)
		case kind == "sales":
			data["Items"], err = s.CRM.SalesReport(r.Context(), f)
		default:
			data["Items"], err = s.CRM.StockReport(r.Context(), f)
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/store/docs")
		return
	}
	data["Sections"], data["Warehouses"] = sections, whs
	var sum float64
	switch list := data["Items"].(type) {
	case []crm.SalesLine:
		var t crm.SalesLine
		for _, x := range list {
			t.Revenue += x.Revenue
			t.Cost += x.Cost
			t.Profit += x.Profit
		}
		if t.Cost > 0 {
			t.Margin = math.Round(t.Profit/t.Cost*1000) / 10
		}
		data["Total"] = t
	case []crm.StockReportLine:
		for _, x := range list {
			sum += x.Sum
		}
	case []crm.MovementLine:
		for _, x := range list {
			sum += x.Sum
		}
	}
	data["Sum"] = sum
	csvq := r.URL.Query()
	csvq.Set("csv", "1")
	data["CSV"] = r.URL.Path + "?" + csvq.Encode()
	if q.Get("csv") == "1" {
		s.stockReportCSV(w, r, kind, data)
		return
	}
	title := map[string]string{"stock": "stock.stock_report", "moves": "stock.moves_report", "sales": "stock.sales_report"}[kind]
	s.render(w, r, "stockreport", page{TitleKey: title, Nav: "store_reports", Data: data})
}

// stockReportCSV gives the report as the page shows it, as a CSV file Excel opens.
func (s *Server) stockReportCSV(w http.ResponseWriter, r *http.Request, kind string, data map[string]any) {
	tr := func(k string) string { return s.tr(r, k) }
	num := func(v float64) string { return strings.ReplaceAll(strconv.FormatFloat(v, 'f', -1, 64), ".", ",") }
	day := func(t time.Time) string { return t.Format(tr("format.date")) }
	keys := func(k ...string) []string {
		for i := range k {
			k[i] = tr(k[i])
		}
		return k
	}
	var head []string
	var rows [][]string
	name := "stock"
	switch {
	case data["Moves"] != nil:
		name = "product-moves"
		head = keys("stock.doc_date", "stock.doc", "stock.kind_col", "stock.warehouse", "stock.in", "stock.out", "stock.balance")
		rows = append(rows, []string{"", tr("stock.opening"), "", "", "", "", num(data["Opening"].(float64))})
		for _, m := range data["Moves"].([]crm.StockMove) {
			rows = append(rows, []string{day(m.Date), "№" + strconv.Itoa(m.Number) + " " + m.Title, tr("stock.kind." + m.Kind), m.Warehouse.Name, num(m.In()), num(m.Out()), num(m.Balance)})
		}
	case kind == "sales":
		name = "sales"
		head = keys("stock.product", "crm.product_sku", "crm.section", "stock.shipments_count", "stock.sold", "stock.revenue", "stock.cost_sum", "stock.profit", "stock.margin")
		for _, x := range data["Items"].([]crm.SalesLine) {
			rows = append(rows, []string{x.Name, x.SKU, x.SectionPath, strconv.Itoa(x.Docs), num(x.Quantity), num(x.Revenue), num(x.Cost), num(x.Profit), num(x.Margin)})
		}
	case kind == "moves":
		name = "moves"
		head = keys("stock.product", "crm.product_sku", "stock.opening", "stock.in", "stock.out", "stock.closing", "stock.cost", "stock.sum")
		for _, x := range data["Items"].([]crm.MovementLine) {
			rows = append(rows, []string{x.Name, x.SKU, num(x.Opening), num(x.In), num(x.Out), num(x.Closing), num(x.Cost), num(x.Sum)})
		}
	default:
		whs := data["Warehouses"].([]crm.Warehouse)
		head = keys("stock.product", "crm.product_sku", "crm.section")
		for _, wh := range whs {
			head = append(head, wh.Name)
		}
		head = append(head, keys("stock.total", "stock.cost", "stock.sum")...)
		for _, x := range data["Items"].([]crm.StockReportLine) {
			row := []string{x.Name, x.SKU, x.SectionPath}
			for _, wh := range whs {
				row = append(row, num(x.On[wh.ID]))
			}
			rows = append(rows, append(row, num(x.Quantity), num(x.Cost), num(x.Sum)))
		}
	}
	if !data["Rights"].(crm.StockRights).PurchasePrice && data["Moves"] == nil { // the purchase prices are for those whose role shows them
		n := 2
		if kind == "sales" { // the cost, the profit and the profitability
			n = 3
		}
		head = head[:len(head)-n]
		for i := range rows {
			rows[i] = rows[i][:len(rows[i])-n]
		}
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+crm.ExportName(name, time.Now()))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(rows)
}

// apiStockReport is «Остатки» on ?date= by warehouses with the average purchase price and the worth.
func (s *Server) apiStockReport(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	q.Set("section", q.Get("section_id"))
	q.Set("warehouse", q.Get("warehouse_id"))
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	list, err := s.CRM.StockReport(r.Context(), stockReportFilter(q))
	if err != nil {
		return err
	}
	type line struct {
		ProductID string             `json:"product_id"`
		Name      string             `json:"name"`
		SKU       string             `json:"sku,omitempty"`
		Quantity  float64            `json:"quantity"`
		On        map[string]float64 `json:"warehouses"`
		Cost      float64            `json:"cost"`
		Sum       float64            `json:"sum"`
	}
	out := make([]line, 0, len(list))
	for _, x := range list {
		on := x.On
		if on == nil {
			on = map[string]float64{}
		}
		if !rights.PurchasePrice { // the purchase prices are for those whose role shows them
			x.Cost, x.Sum = 0, 0
		}
		out = append(out, line{x.ID, x.Name, x.SKU, x.Quantity, on, x.Cost, x.Sum})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiStockMoves is «Движение товаров» from ?from= to ?to=; with ?product_id= — the product's movements.
func (s *Server) apiStockMoves(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	q.Set("section", q.Get("section_id"))
	q.Set("warehouse", q.Get("warehouse_id"))
	q.Set("product", q.Get("product_id"))
	f := stockReportFilter(q)
	if f.ProductID != "" {
		opening, moves, err := s.CRM.ProductMoves(r.Context(), f)
		if err != nil {
			return err
		}
		type move struct {
			Date      string  `json:"date"`
			DocID     string  `json:"doc_id"`
			Kind      string  `json:"kind"`
			Number    int     `json:"number"`
			Title     string  `json:"title,omitempty"`
			Warehouse string  `json:"warehouse_id"`
			Quantity  float64 `json:"quantity"`
			Balance   float64 `json:"balance"`
		}
		out := make([]move, 0, len(moves))
		for _, m := range moves {
			out = append(out, move{m.Date.Format(time.DateOnly), m.DocID, m.Kind, m.Number, m.Title, m.Warehouse.ID, m.Quantity, m.Balance})
		}
		writeJSON(w, http.StatusOK, map[string]any{"opening": opening, "moves": out})
		return nil
	}
	list, err := s.CRM.MovementReport(r.Context(), f)
	if err != nil {
		return err
	}
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	type line struct {
		ProductID string  `json:"product_id"`
		Name      string  `json:"name"`
		SKU       string  `json:"sku,omitempty"`
		Opening   float64 `json:"opening"`
		In        float64 `json:"in"`
		Out       float64 `json:"out"`
		Closing   float64 `json:"closing"`
		Cost      float64 `json:"cost"`
		Sum       float64 `json:"sum"`
	}
	out := make([]line, 0, len(list))
	for _, x := range list {
		if !rights.PurchasePrice {
			x.Cost, x.Sum = 0, 0
		}
		out = append(out, line{x.ID, x.Name, x.SKU, x.Opening, x.In, x.Out, x.Closing, x.Cost, x.Sum})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiStockSales is «Продажи» from ?from= to ?to=: what the conducted shipments took, the revenue in the base currency,
// and to those who see the purchase prices — the cost, the profit and the profitability.
func (s *Server) apiStockSales(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	q.Set("section", q.Get("section_id"))
	q.Set("warehouse", q.Get("warehouse_id"))
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	list, err := s.CRM.SalesReport(r.Context(), stockReportFilter(q))
	if err != nil {
		return err
	}
	type line struct {
		ProductID string   `json:"product_id"`
		Name      string   `json:"name"`
		SKU       string   `json:"sku,omitempty"`
		Docs      int      `json:"shipments"`
		Quantity  float64  `json:"quantity"`
		Revenue   float64  `json:"revenue"`
		Cost      *float64 `json:"cost,omitempty"`
		Profit    *float64 `json:"profit,omitempty"`
		Margin    *float64 `json:"margin,omitempty"`
	}
	out := make([]line, 0, len(list))
	for _, x := range list {
		l := line{ProductID: x.ID, Name: x.Name, SKU: x.SKU, Docs: x.Docs, Quantity: x.Quantity, Revenue: x.Revenue}
		if rights.PurchasePrice {
			l.Cost, l.Profit, l.Margin = &x.Cost, &x.Profit, &x.Margin
		}
		out = append(out, l)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
