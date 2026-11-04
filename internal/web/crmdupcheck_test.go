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
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMDupCheck: «Контроль дубликатов» while a form is filled — a new contact's phone in any format finds the
// contact and the lead with it, a lead's name and company's title find the contact and the company, the ИНН of a
// requisite finds its company, the card being changed is not its own match, and a manager reading only their own
// cards sees no one else's.
func TestCRMDupCheck(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	type item struct{ URL, Title, Kind, Responsible, By string }
	type answer struct {
		Count int
		Title string
		Items []item
	}
	check := func(who string, q url.Values) answer {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+"/crm/dupcheck?"+q.Encode(), nil)
		req.AddCookie(cookie(who))
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%v: %d", q, res.StatusCode)
		}
		var a answer
		must(json.NewDecoder(res.Body).Decode(&a))
		if a.Count != len(a.Items) {
			t.Fatalf("count %d of %d", a.Count, len(a.Items))
		}
		return a
	}
	ivan, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Иван Петров", Phone: "+7 900 123-45-67", Responsible: crm.Ref{ID: anna}})
	must(err)
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка с сайта", Phone: "8 (900) 1234567", Responsible: crm.Ref{ID: anna}})
	must(err)
	romashka, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "Ромашка", Email: "info@romashka.ru", Responsible: crm.Ref{ID: anna}})
	must(err)
	_, err = srv.CRM.SaveRequisite(ctx, crm.Requisite{Entity: "company", EntityID: romashka, Preset: "org", Name: "ООО «Ромашка»", INN: "7707083893"})
	must(err)

	a := check(anna, url.Values{"entity": {"contact"}, "phone": {"89001234567"}})
	if a.Count != 2 || a.Items[0].URL != "/crm/leads/"+lead || a.Items[1].URL != "/crm/contacts/"+ivan || a.Items[1].Responsible != "Анна" ||
		!strings.Contains(a.Title, "2") || a.Items[0].By == "" {
		t.Fatalf("by phone: %+v", a)
	}
	if a := check(anna, url.Values{"entity": {"contact"}, "id": {ivan}, "name": {"иван петров "}, "phone": {"+79001234567"}}); a.Count != 1 || a.Items[0].URL != "/crm/leads/"+lead {
		t.Fatalf("the card itself: %+v", a)
	}
	a = check(anna, url.Values{"entity": {"lead"}, "name": {"Иван Петров"}, "company": {"ромашка"}, "email": {"INFO@romashka.ru"}})
	if a.Count != 2 || a.Items[0].URL != "/crm/companies/"+romashka || a.Items[1].URL != "/crm/contacts/"+ivan || strings.Count(a.Items[0].By, ",") != 1 {
		t.Fatalf("a lead's name and company: %+v", a)
	}
	if a := check(anna, url.Values{"entity": {"company"}, "name": {"Иван Петров"}}); a.Count != 0 { // a company has no person's name
		t.Fatalf("a company by a person's name: %+v", a)
	}
	if a := check(anna, url.Values{"entity": {"contact"}, "inn": {"7707 083 893"}}); a.Count != 1 || a.Items[0].URL != "/crm/companies/"+romashka {
		t.Fatalf("by ИНН: %+v", a)
	}
	if a := check(anna, url.Values{"entity": {"company"}, "id": {romashka}, "inn": {"7707083893"}}); a.Count != 0 {
		t.Fatalf("the company's own ИНН: %+v", a)
	}
	if a := check(anna, url.Values{"entity": {"contact"}, "phone": {"12-34"}, "email": {"нет"}}); a.Count != 0 {
		t.Fatalf("nothing to look for: %+v", a)
	}
	// Eva reads only her own cards: Anna's are not shown to her
	_, err = pool.Exec(ctx, `UPDATE crm_roles SET perms = jsonb_set(jsonb_set(jsonb_set(perms, '{contact,read}', '"own"'),
		'{lead,read}', '"own"'), '{company,read}', '"own"') WHERE is_default`)
	must(err)
	if a := check(eve, url.Values{"entity": {"contact"}, "phone": {"89001234567"}}); a.Count != 0 {
		t.Fatalf("someone else's cards: %+v", a)
	}
	if a := check(anna, url.Values{"entity": {"contact"}, "phone": {"89001234567"}}); a.Count != 2 {
		t.Fatalf("own cards: %+v", a)
	}
	req, _ := http.NewRequest("GET", hs.URL+"/crm/dupcheck?entity=deal", nil)
	req.AddCookie(cookie(anna))
	res, err := http.DefaultClient.Do(req)
	must(err)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a deal: %d", res.StatusCode)
	}
}
