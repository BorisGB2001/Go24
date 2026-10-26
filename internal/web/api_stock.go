package web

import (
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- REST of «Складской учёт» (catalog.store.*, catalog.document.*, catalog.storeproduct.*) ----------

// apiStockRights are the key owner's rights in «Складской учёт».
func (s *Server) apiStockRights(r *http.Request, p apikeys.Principal) (crm.StockRights, error) {
	return s.CRM.StockRightsOf(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID))
}

// GET /api/v1/store/warehouses — the warehouses.
func (s *Server) apiStockWarehouses(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.Warehouses(r.Context())
	if err != nil {
		return err
	}
	type warehouse struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Address       string `json:"address,omitempty"`
		ResponsibleID string `json:"responsible_id,omitempty"`
		Default       bool   `json:"default"`
		Active        bool   `json:"active"`
		Products      int    `json:"products"`
	}
	out := make([]warehouse, 0, len(list))
	for _, x := range list {
		out = append(out, warehouse{x.ID, x.Name, x.Address, x.Responsible.ID, x.Default, x.Active, x.Products})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/store/stock?section_id=&q=&all=1 — the stock: the products that are on a warehouse (all=1 — every
// active product), with their quantities by warehouses' ids.
func (s *Server) apiStock(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	list, err := s.CRM.Stock(r.Context(), q.Get("q"), q.Get("section_id"), false, q.Get("all") != "1")
	if err != nil {
		return err
	}
	type line struct {
		ProductID string             `json:"product_id"`
		Name      string             `json:"name"`
		SKU       string             `json:"sku,omitempty"`
		Total     float64            `json:"total"`
		On        map[string]float64 `json:"warehouses"`
		Reserved  float64            `json:"reserved"`
		Available float64            `json:"available"`
		Reserves  map[string]float64 `json:"reserved_on,omitempty"` // by warehouses' ids
	}
	out := make([]line, 0, len(list))
	for _, x := range list {
		on := x.On
		if on == nil {
			on = map[string]float64{}
		}
		out = append(out, line{x.ID, x.Name, x.SKU, x.Total, on, x.Reserve, x.Free(), x.Reserved})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiStockRow struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name,omitempty"`
	Quantity  float64 `json:"quantity"`
	Price     float64 `json:"price"`
	From      string  `json:"from_warehouse_id,omitempty"`
	To        string  `json:"to_warehouse_id,omitempty"`
}

type apiStockDoc struct {
	ID             string        `json:"id"`
	Kind           string        `json:"kind"`
	Number         int           `json:"number"`
	Title          string        `json:"title,omitempty"`
	Status         string        `json:"status"`
	Date           string        `json:"date"`
	SupplierID     string        `json:"supplier_id,omitempty"`
	DealID         string        `json:"deal_id,omitempty"` // a shipment's
	IncomingNumber string        `json:"incoming_number,omitempty"`
	IncomingDate   string        `json:"incoming_date,omitempty"`
	Reason         string        `json:"reason,omitempty"`
	Comment        string        `json:"comment,omitempty"`
	Currency       string        `json:"currency"`
	Amount         float64       `json:"amount"`
	ResponsibleID  string        `json:"responsible_id"`
	Rows           []apiStockRow `json:"rows,omitempty"`
	URL            string        `json:"url"`
}

func (s *Server) apiStockDocOut(d crm.StockDoc) apiStockDoc {
	out := apiStockDoc{ID: d.ID, Kind: d.Kind, Number: d.Number, Title: d.Title, Status: d.Status, Date: d.Date.Format("2006-01-02"),
		SupplierID: d.Supplier.ID, DealID: d.Deal.ID, IncomingNumber: d.IncomingNumber, Reason: d.Reason, Comment: d.Comment, Currency: d.Currency,
		Amount: d.Amount, ResponsibleID: d.Responsible.ID, URL: s.BaseURL + "/store/docs/" + d.ID}
	if d.IncomingDate != nil {
		out.IncomingDate = d.IncomingDate.Format("2006-01-02")
	}
	for _, r := range d.Rows {
		out.Rows = append(out.Rows, apiStockRow{r.ProductID, r.Name, r.Quantity, r.Price, r.From, r.To})
	}
	return out
}

// GET /api/v1/store/docs?kind=&status=&q=&product_id=&deal_id= — the documents, the latest first (without rows).
func (s *Server) apiStockDocs(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	list, err := s.CRM.StockDocs(r.Context(), crm.StockFilter{Kind: q.Get("kind"), Status: q.Get("status"), Q: q.Get("q"), ProductID: q.Get("product_id"),
		DealID: q.Get("deal_id"), Kinds: rights.Kinds("read"), Warehouses: rights.WarehouseIDs()})
	if err != nil {
		return err
	}
	out := make([]apiStockDoc, 0, len(list))
	for _, d := range list {
		out = append(out, s.apiStockDocOut(d))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/store/docs/{id} — a document with its rows.
func (s *Server) apiStockDoc(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	d, err := s.CRM.StockDoc(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if !rights.Doc(d, "read") {
		return crm.ErrNotFound
	}
	writeJSON(w, http.StatusOK, s.apiStockDocOut(d))
	return nil
}

// POST /api/v1/store/docs — a new document {kind, rows: [{product_id, quantity, price, from_warehouse_id,
// to_warehouse_id}], …}; "conduct": true conducts it at once. PUT /api/v1/store/docs/{id} changes one not conducted.
func (s *Server) apiStockDocSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	var in struct {
		apiStockDoc
		Conduct bool `json:"conduct"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d := crm.StockDoc{ID: r.PathValue("id"), Kind: in.Kind, Title: in.Title, Supplier: crm.Ref{ID: in.SupplierID}, Deal: crm.Ref{ID: in.DealID}, IncomingNumber: in.IncomingNumber,
		IncomingDate: formDay(in.IncomingDate), Reason: in.Reason, Comment: in.Comment, Currency: in.Currency, Responsible: crm.Ref{ID: in.ResponsibleID}}
	if t := formDay(in.Date); t != nil {
		d.Date = *t
	} else if in.Date != "" {
		return i18n.E("err.bad_form")
	}
	for _, x := range in.Rows {
		d.Rows = append(d.Rows, crm.StockRow{ProductID: x.ProductID, Quantity: x.Quantity, Price: x.Price, From: x.From, To: x.To})
	}
	if err := s.CRM.CheckStockDoc(r.Context(), rights, d.ID, d.Kind, "write", d.Rows); err != nil {
		return err
	}
	if in.Conduct {
		if err := s.CRM.CheckStockDoc(r.Context(), rights, d.ID, d.Kind, "conduct", nil); err != nil {
			return err
		}
	}
	id, err := s.CRM.SaveStockDoc(r.Context(), p.UserID, d)
	if err == nil && in.Conduct {
		err = s.CRM.ConductStockDoc(r.Context(), p.UserID, id)
	}
	if err != nil {
		return err
	}
	if d, err = s.CRM.StockDoc(r.Context(), id); err != nil {
		return err
	}
	code := http.StatusOK
	if r.Method == http.MethodPost {
		code = http.StatusCreated
	}
	writeJSON(w, code, s.apiStockDocOut(d))
	return nil
}

// POST /api/v1/store/docs/{id}/conduct and …/cancel — conduct a document or cancel its conducting.
func (s *Server) apiStockDocMove(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	id, op := r.PathValue("id"), r.PathValue("op")
	if op == "conduct" || op == "cancel" {
		if err := s.CRM.CheckStockDoc(r.Context(), rights, id, "", op, nil); err != nil {
			return err
		}
	}
	if op == "conduct" {
		err = s.CRM.ConductStockDoc(r.Context(), p.UserID, id)
	} else if op == "cancel" {
		err = s.CRM.CancelStockDoc(r.Context(), p.UserID, id)
	} else {
		return crm.ErrNotFound
	}
	if err != nil {
		return err
	}
	d, err := s.CRM.StockDoc(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiStockDocOut(d))
	return nil
}

// DELETE /api/v1/store/docs/{id} — removes a document that isn't conducted.
func (s *Server) apiStockDocDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	if err := s.CRM.CheckStockDoc(r.Context(), rights, r.PathValue("id"), "", "delete", nil); err != nil {
		return err
	}
	if err := s.CRM.DeleteStockDoc(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// POST /api/v1/crm/deals/{id}/shipment — «Реализация» of the deal's products not shipped yet, a draft (the deal is
// readable by the key's owner, who keeps the documents); answers the document.
func (s *Server) apiDealShipment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if rights, err := s.apiStockRights(r, p); err != nil {
		return err
	} else if !rights.Can("shipment", "write") {
		return i18n.E("err.forbidden")
	}
	r.SetPathValue("kind", "deals")
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	id, err := s.CRM.NewShipment(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	d, err := s.CRM.StockDoc(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.apiStockDocOut(d))
	return nil
}
