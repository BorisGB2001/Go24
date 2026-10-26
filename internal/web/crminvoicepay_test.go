package web

import (
	"context"
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
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestInvoicePaymentsHTTP: «Создать счёт» of a quote makes the invoice and shows it at the quote; the invoice's
// «Оплаты» take a part, then the rest — the invoice is «Оплачен», the deal's timeline tells; an overpayment is
// refused; an overdue invoice is marked in the list and found by «Просрочен»; the client's link shows the printed
// document without signing in until switched off.
func TestInvoicePaymentsHTTP(t *testing.T) {
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
	bundle, err := i18n.Load("")
	must(err)
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool), I18n: bundle,
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("Accept-Language", "ru")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	_, err = srv.CRM.SetProductRows(ctx, anna, "deal", deal, []crm.ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 3}})
	must(err)
	q, err := srv.CRM.MakeInvoice(ctx, anna, "quote", deal, 0)
	must(err)
	_, page := do(anna, "GET", "/crm/invoices/"+q, nil)
	if !strings.Contains(page, `value="to_invoice"`) || strings.Contains(page, `id="payments"`) {
		t.Fatal("a quote: no «Создать счёт» or payments shown")
	}
	code, _ := do(anna, "POST", "/crm/invoices/"+q, url.Values{"op": {"to_invoice"}})
	list, err := srv.CRM.Invoices(ctx, "", deal, "invoice", "")
	must(err)
	if code != http.StatusSeeOther || len(list) != 1 || list[0].SourceID != q {
		t.Fatalf("invoice of the quote: %d %+v", code, list)
	}
	inv := list[0].ID
	if _, page = do(anna, "GET", "/crm/invoices/"+q, nil); !strings.Contains(page, `href="/crm/invoices/`+inv+`"`) {
		t.Fatal("the quote doesn't show its invoice")
	}
	_, page = do(anna, "GET", "/crm/invoices/"+inv, nil)
	if !strings.Contains(page, `id="payments"`) || !strings.Contains(page, "Оплачено 0") ||
		!strings.Contains(page, `href="/crm/invoices/`+q+`"`) {
		t.Fatal("the invoice has no payments or no link to its quote")
	}
	do(anna, "POST", "/crm/invoices/"+inv, url.Values{"op": {"pay"}, "amount": {"3 000,01"}})
	if pays, _ := srv.CRM.Payments(ctx, inv); len(pays) != 0 {
		t.Fatal("an overpayment taken")
	}
	if code, _ = do(anna, "POST", "/crm/invoices/"+inv, url.Values{"op": {"pay"}, "amount": {"1 000,50"}, "paid_on": {"2026-10-01"}, "comment": {"аванс"}}); code != http.StatusSeeOther {
		t.Fatalf("payment: %d", code)
	}
	_, page = do(anna, "GET", "/crm/invoices/"+inv, nil)
	if !strings.Contains(page, "аванс") || !strings.Contains(page, "Осталось оплатить:") || !strings.Contains(page, `value="unpay"`) {
		t.Fatal("the payment isn't shown")
	}
	// overdue
	v, err := srv.CRM.Invoice(ctx, inv)
	must(err)
	past := time.Now().AddDate(0, 0, -3)
	v.DueDate = &past
	must(srv.CRM.SaveInvoice(ctx, v))
	if _, page = do(anna, "GET", "/crm/invoices?kind=invoice&status=overdue", nil); !strings.Contains(page, "inv-overdue") || !strings.Contains(page, "/crm/invoices/"+inv) {
		t.Fatal("no overdue invoice in the list")
	}
	if code, _ = do(anna, "POST", "/crm/invoices/"+inv, url.Values{"op": {"pay"}, "amount": {"1999.5"}}); code != http.StatusSeeOther {
		t.Fatalf("the rest: %d", code)
	}
	if v, _ = srv.CRM.Invoice(ctx, inv); v.Status != "paid" || v.Overdue() {
		t.Fatalf("paid in full %+v", v)
	}
	if _, page = do(anna, "GET", "/crm/deals/"+deal, nil); !strings.Contains(page, "оплата 1999.50 RUB") {
		t.Fatal("the deal's timeline has no payment")
	}

	// the client's link
	if code, _ = do(anna, "POST", "/crm/invoices/"+inv, url.Values{"op": {"public"}, "on": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("link on: %d", code)
	}
	v, _ = srv.CRM.Invoice(ctx, inv)
	if v.PublicToken == "" {
		t.Fatal("no token")
	}
	if _, page = do(anna, "GET", "/crm/invoices/"+inv, nil); !strings.Contains(page, "/q/"+v.PublicToken) {
		t.Fatal("the link isn't shown")
	}
	res, err := http.Get(hs.URL + "/q/" + v.PublicToken)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), "Лицензия") || !strings.Contains(string(raw), "data-crm-print") {
		t.Fatalf("public page: %d %s", res.StatusCode, raw)
	}
	do(anna, "POST", "/crm/invoices/"+inv, url.Values{"op": {"public"}})
	if res, err = http.Get(hs.URL + "/q/" + v.PublicToken); err != nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("the link after switched off: %v %v", res.StatusCode, err)
	}
	res.Body.Close()

	// a lead's invoice: made from the lead's card, shown there, leads back to the lead
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Responsible: crm.Ref{ID: anna}})
	must(err)
	if _, page = do(anna, "GET", "/crm/leads/"+lead, nil); !strings.Contains(page, `action="/crm/leads/`+lead+`/invoices"`) {
		t.Fatal("the lead has no «Предложения и счета»")
	}
	if code, _ = do(anna, "POST", "/crm/leads/"+lead+"/invoices", url.Values{"kind": {"invoice"}}); code != http.StatusSeeOther {
		t.Fatalf("an invoice of the lead: %d", code)
	}
	list, err = srv.CRM.Invoices(ctx, "", lead, "", "")
	must(err)
	if len(list) != 1 || list[0].LeadID != lead {
		t.Fatalf("the lead's invoices: %+v", list)
	}
	if _, page = do(anna, "GET", "/crm/invoices/"+list[0].ID, nil); !strings.Contains(page, `href="/crm/leads/`+lead+`">Заявка</a>`) {
		t.Fatal("the lead's invoice doesn't lead to the lead")
	}
	if _, page = do(anna, "GET", "/crm/leads/"+lead, nil); !strings.Contains(page, `href="/crm/invoices/`+list[0].ID+`"`) {
		t.Fatal("the lead doesn't show its invoice")
	}
	if code, _ = do(anna, "POST", "/crm/contacts/"+lead+"/invoices", url.Values{"kind": {"invoice"}}); code != http.StatusNotFound {
		t.Fatalf("an invoice of a contact: %d", code)
	}
	_ = eve
}
