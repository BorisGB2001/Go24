package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCatalogAPIWrite: through REST an administrator makes a section and a subsection, a product in it with
// a purchase price, barcodes and a property, a variation of it and a service; a change takes the fields given only
// (null VAT — none); the product moves to another section, the section to the root; the purchase price is answered
// to the administrator only; an employee's key may read but not change; a warehouse is made, becomes the default and
// is removed; a product with its variations and a section are removed (the products go to the parent).
func TestCatalogAPIWrite(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Storages: storages.NewStore(pool, nil, nil),
		Keys: apikeys.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s3ep := os.Getenv("GO24_TEST_S3")
	if s3ep != "" {
		s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
	props, err := srv.CRM.SaveProp(ctx, crm.Prop{Name: "Цвет", Kind: crm.PropString, Variant: true})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	tokens := map[string]string{}
	for _, u := range []string{anna, eve} {
		tokens[u], err = srv.Keys.Create(ctx, u, "t", apikeys.AllScopes, time.Hour)
		must(err)
	}
	call := func(who, method, path string, body any, out any) int {
		t.Helper()
		var rd io.Reader
		if b, ok := body.([]byte); ok {
			rd = bytes.NewReader(b)
		} else if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/"+path, rd)
		req.Header.Set("Authorization", "Bearer "+tokens[who])
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if out != nil && res.StatusCode < 300 {
			must(json.Unmarshal(raw, out))
		}
		return res.StatusCode
	}
	type section struct {
		ID, Name, Path string
		ParentID       string `json:"parent_id"`
	}
	type product struct {
		ID, Name, Type, SKU, Currency, Unit, Section string
		SectionID                                    string `json:"section_id"`
		ParentID                                     string `json:"parent_id"`
		Price                                        float64
		PurchasePrice                                *float64 `json:"purchase_price"`
		VATRate                                      *float64 `json:"vat_rate"`
		VATIncluded                                  bool     `json:"vat_included"`
		Active                                       bool
		Photo                                        string `json:"photo_url"`
		Props                                        map[string]string
		Barcodes                                     []string
	}

	var hw, pr section
	if code := call(anna, "POST", "crm/product-sections", map[string]any{"name": "Оборудование"}, &hw); code != http.StatusCreated || hw.ID == "" {
		t.Fatal("section", code)
	}
	if code := call(anna, "POST", "crm/product-sections", map[string]any{"name": "Принтеры", "parent_id": hw.ID}, &pr); code != http.StatusCreated ||
		pr.Path != "Оборудование / Принтеры" {
		t.Fatal("subsection", code, pr)
	}
	if code := call(eve, "POST", "crm/product-sections", map[string]any{"name": "Чужой"}, nil); code != http.StatusForbidden {
		t.Fatal("an employee made a section", code)
	}
	if code := call(anna, "PUT", "crm/product-sections/"+hw.ID, map[string]any{"parent_id": pr.ID}, nil); code != http.StatusUnprocessableEntity {
		t.Fatal("a section into its subsection", code)
	}

	var lamp product
	code := call(anna, "POST", "crm/products", map[string]any{"name": "Принтер HP", "sku": "HP-1", "price": 1000, "purchase_price": 700,
		"vat_rate": 20, "vat_included": true, "section_id": pr.ID, "barcodes": []string{"4600000000017"}, "props": map[string]string{props: "чёрный"}}, &lamp)
	if code != http.StatusCreated || lamp.Section != "Оборудование / Принтеры" || lamp.PurchasePrice == nil || *lamp.PurchasePrice != 700 ||
		lamp.Type != "product" || !lamp.Active || len(lamp.Barcodes) != 1 || lamp.Props[props] != "чёрный" || lamp.Currency == "" {
		t.Fatal("product", code, lamp)
	}
	var got product
	if call(eve, "GET", "crm/products/"+lamp.ID, nil, &got); got.Name != "Принтер HP" || got.PurchasePrice != nil {
		t.Fatal("an employee's read: no purchase price", got)
	}
	if code := call(eve, "PUT", "crm/products/"+lamp.ID, map[string]any{"price": 1}, nil); code != http.StatusForbidden {
		t.Fatal("an employee changed a product", code)
	}
	// the fields given only: the price, no VAT, the root section; the rest stays
	got = product{}
	if code := call(anna, "PUT", "crm/products/"+lamp.ID, map[string]any{"price": 1200, "vat_rate": nil, "section_id": ""}, &got); code != http.StatusOK ||
		got.Price != 1200 || got.VATRate != nil || got.SectionID != "" || got.SKU != "HP-1" || len(got.Barcodes) != 1 || got.PurchasePrice == nil {
		t.Fatal("partial change", code, got)
	}
	if code := call(anna, "PUT", "crm/products/"+lamp.ID, map[string]any{"name": " "}, nil); code != http.StatusUnprocessableEntity {
		t.Fatal("no name", code)
	}
	if code := call(anna, "PUT", "crm/products/"+lamp.ID, map[string]any{"nonsense": 1}, nil); code != http.StatusUnprocessableEntity {
		t.Fatal("unknown field", code)
	}

	var red product
	if code := call(anna, "POST", "crm/products", map[string]any{"parent_id": lamp.ID, "price": 1300, "props": map[string]string{props: "красный"}}, &red); code != http.StatusCreated ||
		red.Name != "Принтер HP (красный)" || red.ParentID != lamp.ID {
		t.Fatal("variation", code, red)
	}
	var list []product
	if call(eve, "GET", "crm/products?parent_id="+lamp.ID, nil, &list); len(list) != 1 || list[0].ID != red.ID {
		t.Fatal("variations", list)
	}
	var svc product
	if code := call(anna, "POST", "crm/products", map[string]any{"name": "Доставка", "type": "service", "price": 500, "section_id": hw.ID}, &svc); code != http.StatusCreated ||
		svc.Type != "service" {
		t.Fatal("service", code, svc)
	}
	if code := call(anna, "POST", "crm/products", map[string]any{"parent_id": svc.ID}, nil); code != http.StatusUnprocessableEntity {
		t.Fatal("a variation of a service", code)
	}

	if s3ep != "" {
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		var buf bytes.Buffer
		must(png.Encode(&buf, img))
		if code := call(anna, "PUT", "crm/products/"+lamp.ID+"/photo", buf.Bytes(), &got); code != http.StatusOK || got.Photo == "" {
			t.Fatal("photo", code, got)
		}
		if code := call(anna, "PUT", "crm/products/"+lamp.ID+"/photo", []byte("not a picture"), nil); code != http.StatusUnprocessableEntity {
			t.Fatal("not a picture", code)
		}
		got = product{}
		if call(anna, "DELETE", "crm/products/"+lamp.ID+"/photo", nil, &got); got.Photo != "" {
			t.Fatal("photo removed", got)
		}
	}

	type warehouse struct {
		ID, Name, Address string
		Default, Active   bool
		ResponsibleID     string `json:"responsible_id"`
	}
	var main, north warehouse
	if code := call(anna, "POST", "store/warehouses", map[string]any{"name": "Основной"}, &main); code != http.StatusCreated || !main.Default {
		t.Fatal("first warehouse is the default", code, main)
	}
	if code := call(anna, "POST", "store/warehouses", map[string]any{"name": "Северный", "responsible_id": eve}, &north); code != http.StatusCreated || north.Default {
		t.Fatal("warehouse", code, north)
	}
	if code := call(eve, "POST", "store/warehouses", map[string]any{"name": "Свой"}, nil); code != http.StatusForbidden {
		t.Fatal("an employee made a warehouse", code)
	}
	if call(anna, "PUT", "store/warehouses/"+north.ID, map[string]any{"default": true, "address": "Мурманск"}, &north); !north.Default ||
		north.Name != "Северный" || north.ResponsibleID != eve || north.Address != "Мурманск" {
		t.Fatal("warehouse changed", north)
	}
	if code := call(anna, "DELETE", "store/warehouses/"+north.ID, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatal("the default warehouse removed", code)
	}
	if code := call(anna, "DELETE", "store/warehouses/"+main.ID, nil, nil); code != http.StatusNoContent {
		t.Fatal("warehouse removed", code)
	}

	if code := call(anna, "DELETE", "crm/products/"+lamp.ID, nil, nil); code != http.StatusNoContent {
		t.Fatal("product removed", code)
	}
	if code := call(anna, "GET", "crm/products/"+red.ID, nil, nil); code != http.StatusNotFound {
		t.Fatal("variation removed with its product", code)
	}
	if code := call(anna, "DELETE", "crm/product-sections/"+hw.ID, nil, nil); code != http.StatusNoContent {
		t.Fatal("section removed", code)
	}
	got = product{}
	if call(anna, "GET", "crm/products/"+svc.ID, nil, &got); got.SectionID != "" {
		t.Fatal("the products of a removed section go to its parent", got)
	}
	var secs []section
	if call(anna, "GET", "crm/product-sections", nil, &secs); len(secs) != 1 || secs[0].ID != pr.ID || secs[0].ParentID != "" {
		t.Fatal("the subsection goes to the root", secs)
	}
}
