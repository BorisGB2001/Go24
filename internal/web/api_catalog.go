package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"

	"go24/internal/apikeys"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- REST of the catalog and the warehouses: changes (catalog.product.*, catalog.product.offer.*,
// catalog.section.*, catalog.store.*) ----------
//
// The catalog and the warehouses are kept by administrators, as on the pages. A change takes the fields given only
// (as catalog.product.update does): the others stay as they are.

// apiProduct is a product of the catalog.
type apiProduct struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	SKU           string            `json:"sku,omitempty"`
	Price         float64           `json:"price"`
	Currency      string            `json:"currency"`
	PurchasePrice *float64          `json:"purchase_price,omitempty"` // to those who see the purchase prices
	Unit          string            `json:"unit,omitempty"`
	VATRate       *float64          `json:"vat_rate"`
	VATIncluded   bool              `json:"vat_included"`
	Description   string            `json:"description,omitempty"`
	Active        bool              `json:"active"`
	SectionID     string            `json:"section_id,omitempty"`
	Section       string            `json:"section,omitempty"`
	Photo         string            `json:"photo_url,omitempty"`
	Props         map[string]string `json:"props"`
	Barcodes      []string          `json:"barcodes"`
	ParentID      string            `json:"parent_id,omitempty"`
}

func (s *Server) apiProductOut(x crm.Product, purchase bool) apiProduct {
	photo := ""
	if x.PhotoKey != "" {
		photo = s.BaseURL + "/crm/products/" + x.ID + "/photo"
	}
	out := apiProduct{x.ID, x.Name, x.Kind, x.SKU, x.Price, x.Currency, nil, x.Unit, x.VATRate, x.VATIncluded, x.Description, x.Active,
		x.SectionID, x.SectionPath, photo, x.Props, x.Barcodes, x.ParentID}
	if purchase {
		out.PurchasePrice = x.PurchasePrice
	}
	if out.Props == nil {
		out.Props = map[string]string{}
	}
	if out.Barcodes == nil {
		out.Barcodes = []string{}
	}
	return out
}

// apiCatalogAdmin refuses a key whose owner doesn't keep the catalog.
func (s *Server) apiCatalogAdmin(ctx context.Context, p apikeys.Principal) error {
	if !s.apiAdmin(ctx, p.UserID) {
		return i18n.E("err.forbidden")
	}
	return nil
}

// apiProductAnswer answers the product as it is now, with its section's path.
func (s *Server) apiProductAnswer(w http.ResponseWriter, r *http.Request, p apikeys.Principal, id string, code int) error {
	x, err := s.CRM.Product(r.Context(), id)
	if err != nil {
		return err
	}
	if x.SectionID != "" {
		sections, err := s.CRM.Sections(r.Context())
		if err != nil {
			return err
		}
		x.SectionPath = crm.SectionPaths(sections)[x.SectionID]
	}
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, code, s.apiProductOut(x, rights.PurchasePrice))
	return nil
}

// GET /api/v1/crm/products/{id} — a product, a service or a variation (catalog.product.get).
func (s *Server) apiCRMProduct(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiProductAnswer(w, r, p, r.PathValue("id"), http.StatusOK)
}

// POST /api/v1/crm/products — a new product {name, type, sku, price, currency, purchase_price, unit, vat_rate (null —
// no VAT), vat_included, description, active, section_id, props: {property id: value}, barcodes: [...]}; with
// parent_id — a variation of that product (it takes the product's section, unit and VAT; without a name it is named
// by the properties «для вариаций»). PUT /api/v1/crm/products/{id} changes the fields given.
func (s *Server) apiCRMProductSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiCatalogAdmin(r.Context(), p); err != nil {
		return err
	}
	var in struct {
		Name          *string           `json:"name"`
		Type          *string           `json:"type"`
		SKU           *string           `json:"sku"`
		Price         *float64          `json:"price"`
		Currency      *string           `json:"currency"`
		PurchasePrice optionalNumber    `json:"purchase_price"`
		Unit          *string           `json:"unit"`
		VATRate       optionalNumber    `json:"vat_rate"`
		VATIncluded   *bool             `json:"vat_included"`
		Description   *string           `json:"description"`
		Active        *bool             `json:"active"`
		SectionID     *string           `json:"section_id"`
		Props         map[string]string `json:"props"`
		Barcodes      []string          `json:"barcodes"`
		ParentID      string            `json:"parent_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	x := crm.Product{Active: true, ParentID: in.ParentID}
	if id := r.PathValue("id"); id != "" {
		var err error
		if x, err = s.CRM.Product(r.Context(), id); err != nil {
			return err
		}
		if in.ParentID != "" && in.ParentID != x.ParentID {
			return i18n.E("err.bad_form")
		}
	}
	set(&x.Name, in.Name)
	set(&x.Kind, in.Type)
	set(&x.SKU, in.SKU)
	set(&x.Price, in.Price)
	set(&x.Currency, in.Currency)
	set(&x.Unit, in.Unit)
	set(&x.VATIncluded, in.VATIncluded)
	set(&x.Description, in.Description)
	set(&x.Active, in.Active)
	set(&x.SectionID, in.SectionID)
	if in.PurchasePrice.Given {
		x.PurchasePrice = in.PurchasePrice.Value
	}
	if in.VATRate.Given {
		x.VATRate = in.VATRate.Value
	}
	if in.Props != nil { // the values given; "" clears one
		if x.Props == nil {
			x.Props = map[string]string{}
		}
		for k, v := range in.Props {
			x.Props[k] = v
		}
	}
	if in.Barcodes != nil {
		x.Barcodes = in.Barcodes
	}
	id, err := s.CRM.SaveProduct(r.Context(), p.UserID, x)
	if err != nil {
		return err
	}
	code := http.StatusOK
	if r.Method == http.MethodPost {
		code = http.StatusCreated
	}
	return s.apiProductAnswer(w, r, p, id, code)
}

// DELETE /api/v1/crm/products/{id} — removes a product with its variations (catalog.product.delete); the cards keep
// their lines.
func (s *Server) apiCRMProductDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiCatalogAdmin(r.Context(), p); err != nil {
		return err
	}
	if _, err := s.CRM.Product(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	if err := s.CRM.DeleteProduct(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// PUT /api/v1/crm/products/{id}/photo — the product's picture: the body is the image itself (JPEG, PNG, WebP or
// GIF, up to 5 MB); DELETE removes it.
func (s *Server) apiCRMProductPhoto(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiCatalogAdmin(r.Context(), p); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := s.CRM.Product(r.Context(), id); err != nil {
		return err
	}
	if r.Method == http.MethodDelete {
		if _, err := s.CRM.SetProductPhoto(r.Context(), id, "", ""); err != nil {
			return err
		}
	} else {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPhotoSize+1))
		if err != nil || len(data) == 0 || len(data) > maxPhotoSize {
			return i18n.E("err.photo")
		}
		if err := s.storeProductPhoto(r.Context(), p.UserID, id, bytes.NewReader(data), int64(len(data))); err != nil {
			return err
		}
	}
	return s.apiProductAnswer(w, r, p, id, http.StatusOK)
}

// POST /api/v1/crm/product-sections — a new section {name, parent_id, sort} (catalog.section.add);
// PUT /api/v1/crm/product-sections/{id} changes the fields given ("parent_id": "" — to the catalog's root).
func (s *Server) apiCRMSectionSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiCatalogAdmin(r.Context(), p); err != nil {
		return err
	}
	var in struct {
		Name     *string `json:"name"`
		ParentID *string `json:"parent_id"`
		Sort     *int    `json:"sort"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	list, err := s.CRM.Sections(r.Context())
	if err != nil {
		return err
	}
	x := crm.Section{ID: r.PathValue("id")}
	if x.ID != "" {
		i := slices.IndexFunc(list, func(y crm.Section) bool { return y.ID == x.ID })
		if i < 0 {
			return crm.ErrNotFound
		}
		x = list[i]
	}
	set(&x.Name, in.Name)
	set(&x.ParentID, in.ParentID)
	set(&x.Sort, in.Sort)
	id, err := s.CRM.SaveSection(r.Context(), x)
	if err != nil {
		return err
	}
	if list, err = s.CRM.Sections(r.Context()); err != nil {
		return err
	}
	for _, y := range list {
		if y.ID == id {
			code := http.StatusOK
			if r.Method == http.MethodPost {
				code = http.StatusCreated
			}
			writeJSON(w, code, map[string]any{"id": y.ID, "parent_id": y.ParentID, "name": y.Name, "path": y.Path, "sort": y.Sort})
			return nil
		}
	}
	return crm.ErrNotFound
}

// DELETE /api/v1/crm/product-sections/{id} — removes a section; its products and subsections go to its parent.
func (s *Server) apiCRMSectionDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiCatalogAdmin(r.Context(), p); err != nil {
		return err
	}
	if err := s.CRM.DeleteSection(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// POST /api/v1/store/warehouses — a new warehouse {name, address, responsible_id, default, active, sort}
// (catalog.store.add); PUT /api/v1/store/warehouses/{id} changes the fields given. The first warehouse is the
// default; making one the default takes it from the other; its storekeeper gets the role «Кладовщик».
func (s *Server) apiStockWarehouseSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiCatalogAdmin(r.Context(), p); err != nil {
		return err
	}
	var in struct {
		Name          *string `json:"name"`
		Address       *string `json:"address"`
		ResponsibleID *string `json:"responsible_id"`
		Default       *bool   `json:"default"`
		Active        *bool   `json:"active"`
		Sort          *int    `json:"sort"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	list, err := s.CRM.Warehouses(r.Context())
	if err != nil {
		return err
	}
	x := crm.Warehouse{ID: r.PathValue("id"), Active: true}
	if x.ID != "" {
		i := slices.IndexFunc(list, func(y crm.Warehouse) bool { return y.ID == x.ID })
		if i < 0 {
			return crm.ErrNotFound
		}
		x = list[i]
	}
	set(&x.Name, in.Name)
	set(&x.Address, in.Address)
	set(&x.Responsible.ID, in.ResponsibleID)
	set(&x.Default, in.Default)
	set(&x.Active, in.Active)
	set(&x.Sort, in.Sort)
	id, err := s.CRM.SaveWarehouse(r.Context(), x)
	if err != nil {
		return err
	}
	if list, err = s.CRM.Warehouses(r.Context()); err != nil {
		return err
	}
	if i := slices.IndexFunc(list, func(y crm.Warehouse) bool { return y.ID == id }); i >= 0 {
		y := list[i]
		code := http.StatusOK
		if r.Method == http.MethodPost {
			code = http.StatusCreated
		}
		writeJSON(w, code, map[string]any{"id": y.ID, "name": y.Name, "address": y.Address, "responsible_id": y.Responsible.ID,
			"default": y.Default, "active": y.Active, "sort": y.Sort, "products": y.Products})
		return nil
	}
	return crm.ErrNotFound
}

// DELETE /api/v1/store/warehouses/{id} — removes a warehouse that nothing is on and no document names (else it can
// only be made inactive); the default one stays.
func (s *Server) apiStockWarehouseDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiCatalogAdmin(r.Context(), p); err != nil {
		return err
	}
	if err := s.CRM.DeleteWarehouse(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// set takes a field's value when it is given.
func set[T any](to *T, v *T) {
	if v != nil {
		*to = *v
	}
}

// optionalNumber is a number field that may be absent (as it was), null (none) or a number.
type optionalNumber struct {
	Given bool
	Value *float64
}

func (o *optionalNumber) UnmarshalJSON(b []byte) error {
	o.Given = true
	return json.Unmarshal(b, &o.Value)
}
