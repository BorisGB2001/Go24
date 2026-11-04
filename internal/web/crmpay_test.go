package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/paysys"
	"go24/internal/tasks"
)

// TestCRMPayHTTP: only an administrator connects a payment service (a fake ЮKassa over HTTP here); the invoice by
// the client's link offers «Оплатить» for what is left, the client goes to the service's page; back while the
// service waits — «не подтверждён», the invoice unpaid; the service's notice confirms the payment once, the invoice
// takes it and is «Оплачен», nothing is left to pay; a notice of a payment go24 does not know changes nothing.
func TestCRMPayHTTP(t *testing.T) {
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
	var mu sync.Mutex
	status := "pending"
	var created map[string]any
	var auth1 string
	yoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&created)
			u, p, _ := r.BasicAuth()
			auth1 = u + ":" + p
			_, _ = w.Write([]byte(`{"id":"y-77","status":"pending","confirmation":{"confirmation_url":"https://yoomoney.example/pay/y-77"}}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/y-77") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"id":"y-77","status":"` + status + `","amount":{"value":"2000.00","currency":"RUB"}}`))
	}))
	defer yoo.Close()
	defer func(u string) { paysys.YooKassaURL = u }(paysys.YooKassaURL)
	paysys.YooKassaURL = yoo.URL + "/v3/payments"

	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Pay: paysys.NewStore(pool, "secret"), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseURL: "https://go24.test"}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	_, err = srv.CRM.SetProductRows(ctx, anna, "deal", deal, []crm.ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 3}})
	must(err)
	inv, err := srv.CRM.MakeInvoice(ctx, anna, "invoice", deal, 5)
	must(err)
	must(srv.CRM.AddPayment(ctx, anna, inv, crm.Payment{Amount: 1000}))
	token, err := srv.CRM.SetInvoicePublic(ctx, inv, true)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(cookie *http.Cookie, method, path, ctype, body string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := client.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Location")
	}
	form := func(cookie *http.Cookie, path string, f url.Values) (int, string, string) {
		return do(cookie, "POST", path, "application/x-www-form-urlencoded", f.Encode())
	}
	admin, worker := cookieOf(anna, true), cookieOf(petr, false)
	pub := "/q/" + token

	if _, body, _ := do(nil, "GET", pub, "", ""); strings.Contains(body, pub+"/pay") {
		t.Fatal("no service — no «Оплатить»")
	}
	add := url.Values{"op": {"save"}, "kind": {"yookassa"}, "name": {"ЮKassa"}, "login": {"shop1"}, "secret": {"live_KEY"}, "active": {"1"}}
	form(worker, "/crm/paysystems", add)
	if list, _ := srv.Pay.Systems(ctx); len(list) != 0 {
		t.Fatal("the services are the administrators'")
	}
	if c, _, loc := form(admin, "/crm/paysystems", add); c != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("add a service: %d %s", c, loc)
	}
	list, _ := srv.Pay.Systems(ctx)
	if len(list) != 1 {
		t.Fatalf("the service: %+v", list)
	}
	sys := list[0].ID
	if _, body, _ := do(worker, "GET", "/crm/paysystems", "", ""); !strings.Contains(body, "ЮKassa") || strings.Contains(body, "live_KEY") || strings.Contains(body, "/notify") {
		t.Fatal("everyone sees the services, without keys; the notification address is the administrators'")
	}
	if _, body, _ := do(nil, "GET", pub, "", ""); !strings.Contains(body, `action="`+pub+`/pay"`) || !strings.Contains(body, `value="`+sys+`"`) || !strings.Contains(body, "2 000") && !strings.Contains(body, "2000") {
		t.Fatalf("«Оплатить» for what is left:\n%s", body)
	}

	// the client pays: what is left, the shop's key, back to the invoice
	c, _, loc := form(nil, pub+"/pay", url.Values{"system": {sys}})
	if c != http.StatusSeeOther || loc != "https://yoomoney.example/pay/y-77" || auth1 != "shop1:live_KEY" ||
		created["amount"].(map[string]any)["value"] != "2000.00" {
		t.Fatalf("to the service: %d %s %s %v", c, loc, auth1, created)
	}
	ret := created["confirmation"].(map[string]any)["return_url"].(string)
	if !strings.HasPrefix(ret, "https://go24.test"+pub+"?pay=") {
		t.Fatalf("return URL %s", ret)
	}
	pay := strings.TrimPrefix(ret, "https://go24.test")
	if _, body, _ := do(nil, "GET", pay, "", ""); !strings.Contains(body, `class="paywait"`) {
		t.Fatalf("back while the service waits:\n%s", body)
	}
	if v, _ := srv.CRM.Invoice(ctx, inv); v.Status == "paid" {
		t.Fatal("not paid yet")
	}

	// the service's notice: checked at the service, taken once
	mu.Lock()
	status = "succeeded"
	mu.Unlock()
	notice := `{"type":"notification","event":"payment.succeeded","object":{"id":"y-77","status":"succeeded"}}`
	for i := 0; i < 2; i++ {
		if c, body, _ := do(nil, "POST", "/pay/"+sys+"/notify", "application/json", notice); c != http.StatusOK || body != "OK" {
			t.Fatalf("notice: %d %s", c, body)
		}
	}
	if c, body, _ := do(nil, "POST", "/pay/"+sys+"/notify", "application/json", `{"object":{"id":"y-unknown"}}`); c != http.StatusOK || body != "OK" {
		t.Fatalf("a notice of another payment: %d %s", c, body)
	}
	v, err := srv.CRM.Invoice(ctx, inv)
	must(err)
	payments, err := srv.CRM.Payments(ctx, inv)
	must(err)
	if v.Status != "paid" || v.Paid != 3000 || len(payments) != 2 || payments[1].Comment != "ЮKassa · y-77" || payments[1].By.ID != anna {
		t.Fatalf("the invoice took the payment once: %+v %+v", v, payments)
	}
	if _, body, _ := do(nil, "GET", pay, "", ""); !strings.Contains(body, `class="payok"`) || strings.Contains(body, pub+"/pay") {
		t.Fatalf("paid — nothing left to pay:\n%s", body)
	}
	if c, _, loc := form(nil, pub+"/pay", url.Values{"system": {sys}}); c != http.StatusSeeOther || loc != pub {
		t.Fatalf("a paid invoice is not paid again: %d %s", c, loc)
	}
	if _, body, _ := do(admin, "GET", "/crm/invoices/"+inv, "", ""); !strings.Contains(body, "y-77") {
		t.Fatal("the invoice's card lists the online payment")
	}
}
