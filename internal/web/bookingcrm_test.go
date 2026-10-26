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

	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBookingCRMHTTP: «Онлайн-запись» and CRM — a booking of a contact takes the contact's company and client, the
// contact's and the company's timelines get «Онлайн-запись» once, the card lists its bookings with «Записать» (the
// grid then offers the card's client); «Создать сделку» makes a deal of the client bound to the booking; a client
// who books by a link becomes a contact with the source «Онлайн-запись», the same phone — the same contact.
func TestBookingCRMHTTP(t *testing.T) {
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
	var boss, ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@go24.test') RETURNING id`).Scan(&ann))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), CRM: crm.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(s auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, s))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), ann: cookie(auth.Session{UserID: ann})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if who != "" {
			req.AddCookie(as[who])
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
	events := func(entity, id string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity = $1 AND entity_id::text = $2 AND kind = 'booking'`, entity, id).Scan(&n))
		return n
	}

	types, err := srv.Booking.Types(ctx)
	must(err)
	room, err := srv.Booking.SaveResource(ctx, boss, booking.Resource{TypeID: types[1].ID, Name: "Кабинет 1", Slot: 30,
		Days: []int{1, 2, 3, 4, 5, 6, 7}, From: 540, To: 1080})
	must(err)
	company, err := srv.CRM.SaveCompany(ctx, ann, crm.Company{Title: "ООО Ромашка", Responsible: crm.Ref{ID: ann}})
	must(err)
	contact, err := srv.CRM.SaveContact(ctx, ann, crm.Contact{Name: "Ольга Петрова", Phone: "+7 900 555-11-22",
		Email: "olga@x.test", Company: crm.Ref{ID: company}, Responsible: crm.Ref{ID: ann}})
	must(err)

	// a booking of the contact: no client typed — the contact's; its company too; back to the card it was made from
	day := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	book := url.Values{"date": {day}, "time": {"10:00"}, "minutes": {"30"}, "resource": {room}, "contact_id": {contact},
		"crm_back": {"/crm/contacts/" + contact}}
	if _, loc := do(ann, "POST", "/booking", book, 303); loc != "/crm/contacts/"+contact+"#bookings" {
		t.Fatalf("back to the card: %q", loc)
	}
	list, err := srv.Booking.OfCard(ctx, "contact", contact)
	must(err)
	if len(list) != 1 || list[0].ClientName != "Ольга Петрова" || list[0].ClientPhone != "+7 900 555-11-22" ||
		list[0].CompanyID != company || list[0].ContactName != "Ольга Петрова" {
		t.Fatalf("the booking of the contact: %+v", list)
	}
	b := list[0]
	if events("contact", contact) != 1 || events("company", company) != 1 {
		t.Fatalf("timeline: contact %d, company %d", events("contact", contact), events("company", company))
	}
	// saved again with the same cards — no new lines
	book.Set("client_name", "Ольга")
	book.Set("company_id", company)
	book.Del("crm_back")
	do(ann, "POST", "/booking/item/"+b.ID, book, 303)
	if events("contact", contact) != 1 || events("company", company) != 1 {
		t.Fatal("the same cards are written to the timeline again")
	}
	page, _ := do(ann, "GET", "/crm/contacts/"+contact, nil, 200)
	for _, want := range []string{`id="bookings"`, `href="/booking?crm=%2fcrm%2fcontacts%2f` + contact + `"`, "Кабинет 1",
		`/booking?date=` + day + `&b=` + b.ID, "Онлайн-запись"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the contact's card has no %q", want)
		}
	}
	// «Записать» from the card: the grid offers its client
	page, _ = do(ann, "GET", "/booking?date="+day+"&crm=/crm/contacts/"+contact, nil, 200)
	if !strings.Contains(page, `data-preset-contact-id="`+contact+`"`) || !strings.Contains(page, `data-preset-company="ООО Ромашка"`) ||
		!strings.Contains(page, "Запись для") || !strings.Contains(page, `data-contact-id="`+contact+`"`) {
		t.Fatal("the grid does not offer the card's client")
	}
	if page, _ = do(ann, "GET", "/booking?date="+day+"&crm=/crm/contacts/nope", nil, 200); strings.Contains(page, "Запись для") {
		t.Fatal("a card that is not there is offered")
	}

	// «Создать сделку»: the client's deal, bound; once more — the same deal
	_, loc := do(ann, "POST", "/booking/item/"+b.ID+"/deal", url.Values{}, 303)
	b, err = srv.Booking.Booking(ctx, b.ID)
	must(err)
	if b.DealID == "" || loc != "/crm/deals/"+b.DealID {
		t.Fatalf("the deal: %q, booking %+v", loc, b)
	}
	d, err := srv.CRM.Deal(ctx, b.DealID)
	must(err)
	if d.Contact.ID != contact || d.Company.ID != company || !strings.Contains(d.Title, "Кабинет 1") || d.Responsible.ID != ann {
		t.Fatalf("the deal: %+v", d)
	}
	if events("deal", d.ID) != 1 || events("contact", contact) != 1 {
		t.Fatal("the deal's timeline has no booking")
	}
	if _, loc := do(ann, "POST", "/booking/item/"+b.ID+"/deal", url.Values{}, 303); loc != "/crm/deals/"+d.ID {
		t.Fatalf("a second deal: %q", loc)
	}
	page, _ = do(ann, "GET", "/crm/deals/"+d.ID, nil, 200)
	if !strings.Contains(page, "Подтверждена") && !strings.Contains(page, "Не подтверждена") || !strings.Contains(page, "Онлайн-запись") {
		t.Fatal("the deal's timeline does not show the booking")
	}
	// a deal picked in the form brings its contact and company
	do(ann, "POST", "/booking", url.Values{"date": {day}, "time": {"11:00"}, "minutes": {"30"}, "resource": {room},
		"client_name": {"Кто-то"}, "deal_id": {d.ID}}, 303)
	if list, _ = srv.Booking.OfCard(ctx, "deal", d.ID); len(list) != 2 || list[0].ContactID != contact || list[0].CompanyID != company {
		t.Fatalf("the deal's bookings: %+v", list)
	}
	do(ann, "POST", "/booking", url.Values{"date": {day}, "time": {"12:00"}, "minutes": {"30"}, "resource": {room},
		"client_name": {"Кто-то"}, "contact_id": {"00000000-0000-0000-0000-000000000000"}}, 404)

	// a client by a link: a contact with the source «Онлайн-запись» of the responsible one; the same phone — the same contact
	_, err = srv.Booking.SaveLink(ctx, boss, booking.Link{Name: "Запись", DaysAhead: 7, ResponsibleID: ann, Active: true})
	must(err)
	links, err := srv.Booking.Links(ctx)
	must(err)
	tok := links[0].Token
	form := url.Values{"r": {room}, "date": {day}, "t": {"14:00"}, "name": {"Пётр Сидоров"}, "phone": {"+7 900 777-88-99"}, "email": {"petr@x.test"}}
	do("", "POST", "/bk/"+tok, form, 303)
	var petr, src, resp string
	must(pool.QueryRow(ctx, `SELECT x.id::text, coalesce(s.code, ''), x.responsible_id::text FROM crm_contacts x
		LEFT JOIN crm_dict s ON s.id = x.source_id WHERE x.name = 'Пётр Сидоров'`).Scan(&petr, &src, &resp))
	if src != "BOOKING" || resp != ann || events("contact", petr) != 1 {
		t.Fatalf("the client's contact: source %q, responsible %q, events %d", src, resp, events("contact", petr))
	}
	form.Set("t", "15:00")
	form.Set("name", "Пётр")
	do("", "POST", "/bk/"+tok, form, 303)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_contacts WHERE phone = '+7 900 777-88-99'`).Scan(&n))
	if list, _ = srv.Booking.OfCard(ctx, "contact", petr); n != 1 || len(list) != 2 || events("contact", petr) != 2 {
		t.Fatalf("the same client again: %d contacts, %d bookings", n, len(list))
	}
	// the contact deleted: the booking stays without it, the timeline line says it is gone when the booking is
	must(srv.Booking.DeleteBooking(ctx, list[0].ID))
	page, _ = do(ann, "GET", "/crm/contacts/"+petr, nil, 200)
	if !strings.Contains(page, "запись удалена") {
		t.Fatal("a deleted booking in the timeline")
	}
}
