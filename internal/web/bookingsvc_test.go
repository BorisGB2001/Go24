package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBookingServicesHTTP: «Услуги» of «Онлайн-запись» — the resource's form offers the catalog's services with their
// minutes; a booking of the grid takes services of its resource (shown with the total); by a link the client picks
// a service, its slots are for its minutes and its page shows it with the price; «Создать сделку» makes a deal with
// the services as products; REST shows and takes the services.
func TestBookingServicesHTTP(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Босс', 'boss@go24.test', '{admins}') RETURNING id`).Scan(&boss))
	keys := apikeys.NewStore(pool)
	key, err := keys.Create(ctx, boss, "k", []string{"booking:read", "booking:write"}, time.Hour)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), CRM: crm.NewStore(pool), Keys: keys,
		AdminGroups: []string{"admins"}, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(signed bool, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if signed {
			req.AddCookie(cookie)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header.Get("Location")
	}
	call := func(method, path string, body any, want int, v any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, hs.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.StatusCode, out, want)
		}
		if v != nil {
			must(json.Unmarshal(out, v))
		}
	}
	product := func(name, kind string, price float64) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO crm_products (name, kind, price, vat_rate, created_by) VALUES ($1, $2, $3, 20, $4) RETURNING id`,
			name, kind, price, boss).Scan(&id))
		return id
	}
	cut, color := product("Стрижка", "service", 1500), product("Окрашивание", "service", 3000)
	product("Расчёска", "product", 200)

	// the resource's form: only the catalog's services
	page, _ := do(true, "GET", "/booking/resources?edit=new", nil, http.StatusOK)
	if !strings.Contains(page, "Стрижка") || strings.Contains(page, "Расчёска") {
		t.Fatalf("the form's services: %.2000s", page)
	}
	types, err := srv.Booking.Types(ctx)
	must(err)
	do(true, "POST", "/booking/resources", url.Values{"name": {"Мастер"}, "type": {types[0].ID}, "slot": {"30"}, "from": {"09:00"},
		"to": {"12:00"}, "days": {"1", "2", "3", "4", "5", "6", "7"}, "svc": {color, cut}, "svc_min_" + color: {"90"}, "svc_min_" + cut: {"30"}}, http.StatusSeeOther)
	list, err := srv.Booking.Resources(ctx, "")
	must(err)
	res := list[0]
	if len(res.Services) != 2 || res.Services[0].ProductID != color || res.Services[0].Minutes != 90 {
		t.Fatalf("the resource's services: %+v", res.Services)
	}
	if page, _ = do(true, "GET", "/booking/resources", nil, http.StatusOK); !strings.Contains(page, "Окрашивание, Стрижка") {
		t.Fatalf("the list of resources: %.3000s", page)
	}

	// the grid: a booking of both services, the total on it
	day := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	do(true, "POST", "/booking", url.Values{"resource": {res.ID}, "date": {day}, "time": {"09:00"}, "minutes": {"120"},
		"client_name": {"Анна"}, "client_phone": {"+7 900 111-22-33"}, "svc": {color, cut}, "status": {"unconfirmed"}}, http.StatusSeeOther)
	page, _ = do(true, "GET", "/booking?date="+day, nil, http.StatusOK)
	if !strings.Contains(page, `data-services="`+color+","+cut+`"`) || !strings.Contains(page, "Окрашивание, Стрижка · 4 500 RUB") ||
		!strings.Contains(page, `data-svc-res="`+res.ID+`"`) {
		t.Fatalf("the grid: %.5000s", page)
	}
	var bid string
	must(pool.QueryRow(ctx, `SELECT id FROM bookings`).Scan(&bid))

	// «Создать сделку»: the services as the deal's products
	_, loc := do(true, "POST", "/booking/item/"+bid+"/deal", url.Values{}, http.StatusSeeOther)
	deal := strings.TrimPrefix(loc, "/crm/deals/")
	rows, err := srv.CRM.ProductRows(ctx, "deal", deal)
	must(err)
	if len(rows) != 2 || rows[0].Name != "Окрашивание" || rows[0].Price != 3000 || rows[1].ProductID != cut || rows[0].VATRate == nil {
		t.Fatalf("the deal's products: %+v", rows)
	}
	var amount float64
	must(pool.QueryRow(ctx, `SELECT amount::float8 FROM crm_deals WHERE id::text = $1`, deal).Scan(&amount))
	if amount != 4500 {
		t.Fatalf("the deal's amount: %v", amount)
	}

	// by a link: the service first, its slots, the client's page with it
	l, err := srv.Booking.SaveLink(ctx, boss, booking.Link{Name: "Салон", DaysAhead: 7, Active: true})
	must(err)
	link, err := srv.Booking.Link(ctx, l)
	must(err)
	page, _ = do(false, "GET", "/bk/"+link.Token, nil, http.StatusOK)
	if !strings.Contains(page, "?r="+res.ID+"&s="+cut) || strings.Contains(page, "bk-slot") {
		t.Fatalf("the service step: %.4000s", page)
	}
	page, _ = do(false, "GET", "/bk/"+link.Token+"?s="+color+"&date="+day, nil, http.StatusOK)
	if strings.Contains(page, ">09:00<") || strings.Contains(page, ">11:00<") {
		t.Fatalf("90 minutes after the booking till 11:00: %.6000s", page)
	}
	page, _ = do(false, "GET", "/bk/"+link.Token+"?s="+cut+"&date="+day+"&t=11:30", nil, http.StatusOK)
	if !strings.Contains(page, `name="s" value="`+cut+`"`) || !strings.Contains(page, "1 500 RUB") {
		t.Fatalf("the client's form: %.6000s", page)
	}
	_, loc = do(false, "POST", "/bk/"+link.Token, url.Values{"r": {res.ID}, "s": {cut}, "date": {day}, "t": {"11:30"},
		"name": {"Ира"}, "phone": {"+7 900 000-00-02"}}, http.StatusSeeOther)
	if !strings.HasPrefix(loc, "/bk/b/") {
		t.Fatalf("booked by a link: %s", loc)
	}
	if page, _ = do(false, "GET", loc, nil, http.StatusOK); !strings.Contains(page, "Стрижка · 1 500 RUB") || !strings.Contains(page, "11:30–12:00") {
		t.Fatalf("the client's page: %.4000s", page)
	}

	// REST: the services of resources and bookings
	var ar apiBkResource
	call("GET", "/api/v1/booking/resources/"+res.ID, nil, 200, &ar)
	if len(ar.Services) != 2 || ar.Services[1].Name != "Стрижка" || ar.Services[1].Minutes != 30 {
		t.Fatalf("REST resource: %+v", ar)
	}
	var slots struct {
		Minutes int         `json:"minutes"`
		Free    []time.Time `json:"free"`
	}
	call("GET", "/api/v1/booking/resources/"+res.ID+"/slots?date="+day+"&service_id="+color, nil, 200, &slots)
	if slots.Minutes != 90 || len(slots.Free) != 0 {
		t.Fatalf("REST slots: %+v", slots)
	}
	start, _ := time.ParseInLocation("2006-01-02 15:04", day+" 11:00", time.Local)
	var ab apiBkBooking
	call("POST", "/api/v1/booking/bookings", map[string]any{"resource_id": res.ID, "start": start, "client_name": "Олег",
		"service_ids": []string{cut}}, 201, &ab)
	if ab.End.Sub(ab.Start) != 30*time.Minute || ab.Amount != 1500 || len(ab.Services) != 1 {
		t.Fatalf("REST booking: %+v", ab)
	}
	call("POST", "/api/v1/booking/bookings", map[string]any{"resource_id": res.ID, "start": start.AddDate(0, 0, 1), "client_name": "Олег",
		"service_ids": []string{"00000000-0000-0000-0000-000000000000"}}, 422, nil)
}
