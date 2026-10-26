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
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBookingHTTP: «Онлайн-запись» over HTTP — an administrator adds a resource with its schedule, an employee
// cannot; an employee books a client, the booking is on the day's grid, an overlap is refused with a message, the
// status is changed and the booking deleted; a resource with overbooking, a booking of two resources (in both
// columns); the waiting list — put on, dragged onto a time, back from a booking, booked from the dialog, removed.
func TestBookingHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool),
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
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
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
		return string(raw)
	}

	if !strings.Contains(do(ann, "GET", "/booking", nil, 200), "Ресурсов пока нет") {
		t.Fatal("no «Ресурсов пока нет» without resources")
	}
	types, err := srv.Booking.Types(ctx)
	must(err)
	room := url.Values{"name": {"Кабинет 1"}, "type": {types[1].ID}, "days": {"1", "2", "3", "4", "5", "6", "7"},
		"from": {"09:00"}, "to": {"18:00"}, "slot": {"30"}}
	do(ann, "POST", "/booking/resources", room, 403)
	do(boss, "GET", "/booking/resources?edit=new", nil, 200)
	if page := do(boss, "POST", "/booking/resources", url.Values{"name": {"Кабинет 1"}, "type": {types[1].ID}, "from": {"09:00"}, "to": {"18:00"}, "slot": {"30"}}, 422); !strings.Contains(page, "Проверьте расписание") {
		t.Fatalf("a resource without working days: %s", page)
	}
	do(boss, "POST", "/booking/resources", room, 200)
	res, err := srv.Booking.Resources(ctx, "")
	must(err)
	if len(res) != 1 || res[0].From != 540 || res[0].To != 1080 || len(res[0].Days) != 7 {
		t.Fatalf("resources: %+v", res)
	}
	if !strings.Contains(do(ann, "GET", "/booking/resources", nil, 200), "09:00–18:00") {
		t.Fatal("the schedule is not on the resources page")
	}

	day := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	book := url.Values{"date": {day}, "time": {"10:00"}, "minutes": {"60"}, "resource": {res[0].ID},
		"client_name": {"Иван Петров"}, "client_phone": {"+7 900 000-00-00"}}
	do(ann, "POST", "/booking", book, 200)
	page := do(ann, "GET", "/booking?date="+day, nil, 200)
	if !strings.Contains(page, "Иван Петров") || !strings.Contains(page, `data-minutes="60"`) || !strings.Contains(page, "10:00–11:00") {
		t.Fatalf("the booking is not on the grid: %s", page)
	}
	book.Set("time", "10:30")
	if page := do(ann, "POST", "/booking", book, 422); !strings.Contains(page, "уже занято") {
		t.Fatalf("an overlap: %s", page)
	}
	list, err := srv.Booking.Bookings(ctx, time.Now(), time.Now().AddDate(0, 0, 2))
	must(err)
	if len(list) != 1 || list[0].Status != "unconfirmed" {
		t.Fatalf("bookings: %+v", list)
	}
	do(ann, "POST", "/booking/item/"+list[0].ID+"/status", url.Values{"status": {"confirmed"}}, 200)
	if b, _ := srv.Booking.Booking(ctx, list[0].ID); b.Status != "confirmed" {
		t.Fatalf("status: %s", b.Status)
	}
	do(ann, "POST", "/booking/item/"+list[0].ID+"/delete", url.Values{}, 200)
	if _, err := srv.Booking.Booking(ctx, list[0].ID); err == nil {
		t.Fatal("the booking is not deleted")
	}
	// a master that allows overbooking, booked with the room: the room must be free, the master need not be
	master := url.Values{"name": {"Мастер"}, "type": {types[0].ID}, "days": {"1", "2", "3", "4", "5", "6", "7"},
		"from": {"09:00"}, "to": {"18:00"}, "slot": {"60"}, "overbooking": {"1"}}
	do(boss, "POST", "/booking/resources", master, 200)
	res, err = srv.Booking.Resources(ctx, types[0].ID)
	must(err)
	if len(res) != 1 || !res[0].Overbooking {
		t.Fatalf("overbooking: %+v", res)
	}
	if !strings.Contains(do(boss, "GET", "/booking/resources?edit="+res[0].ID, nil, 200), `name="overbooking" value="1" checked`) {
		t.Fatal("overbooking is not in the resource's form")
	}
	mb := url.Values{"date": {day}, "time": {"12:00"}, "minutes": {"60"}, "resource": {res[0].ID}, "extra": {list[0].ResourceID},
		"client_name": {"Ольга"}}
	do(ann, "POST", "/booking", mb, 200)
	mb.Set("client_name", "Глеб")
	if page := do(ann, "POST", "/booking", mb, 422); !strings.Contains(page, "уже занято") {
		t.Fatalf("the room taken twice: %s", page)
	}
	mb.Del("extra")
	do(ann, "POST", "/booking", mb, 200)
	page = do(ann, "GET", "/booking?date="+day, nil, 200)
	if !strings.Contains(page, "+ Кабинет 1") || !strings.Contains(page, "bk-extra") || strings.Count(page, "Ольга") < 2 || !strings.Contains(page, "Глеб") {
		t.Fatalf("a booking of two resources is not in both columns: %s", page)
	}

	// the waiting list: a client put on it, dragged onto 14:00 of the room — booked for a slot, off the list; back on
	// the list from the booking; booked from the dialog («Записать») — off the list; removed
	if page := do(ann, "POST", "/booking/wait", url.Values{"date": {day}, "note": {"утро"}}, 422); !strings.Contains(page, "клиент") {
		t.Fatalf("a waiting entry with no client: %s", page)
	}
	do(ann, "POST", "/booking/wait", url.Values{"date": {day}, "client_name": {"Мария"}, "client_phone": {"+7 901"}, "resource": {list[0].ResourceID}, "note": {"утро"}}, 200)
	wl, err := srv.Booking.Waitlist(ctx)
	must(err)
	if len(wl) != 1 || wl[0].ResourceName != "Кабинет 1" || wl[0].CreatedByName != "Анна" {
		t.Fatalf("waitlist: %+v", wl)
	}
	if page := do(ann, "GET", "/booking?date="+day, nil, 200); !strings.Contains(page, `data-wait="`+wl[0].ID+`"`) {
		t.Fatal("the waiting client is not on the page")
	}
	do(ann, "POST", "/booking/wait/"+wl[0].ID+"/book", url.Values{"resource": {list[0].ResourceID}, "date": {day}, "time": {"14:00"}}, 200)
	if wl, _ := srv.Booking.Waitlist(ctx); len(wl) != 0 {
		t.Fatalf("booked but still waiting: %+v", wl)
	}
	d0, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	all, err := srv.Booking.Bookings(ctx, d0.Add(14*time.Hour), d0.Add(15*time.Hour))
	must(err)
	if len(all) != 1 || all[0].ClientName != "Мария" || all[0].End.Sub(all[0].Start) != 30*time.Minute || all[0].Note != "утро" {
		t.Fatalf("booked from the list: %+v", all)
	}
	do(ann, "POST", "/booking/item/"+all[0].ID+"/wait", url.Values{}, 200)
	if _, err := srv.Booking.Booking(ctx, all[0].ID); err == nil {
		t.Fatal("the booking put back on the list is left")
	}
	wl, _ = srv.Booking.Waitlist(ctx)
	if len(wl) != 1 || wl[0].ClientName != "Мария" {
		t.Fatalf("back on the list: %+v", wl)
	}
	do(ann, "POST", "/booking", url.Values{"date": {day}, "time": {"15:00"}, "minutes": {"30"}, "resource": {list[0].ResourceID},
		"client_name": {"Мария"}, "wait": {wl[0].ID}}, 200)
	if wl, _ := srv.Booking.Waitlist(ctx); len(wl) != 0 {
		t.Fatalf("booked from the dialog but still waiting: %+v", wl)
	}
	do(ann, "POST", "/booking/wait", url.Values{"date": {day}, "client_name": {"Пётр"}}, 200)
	wl, _ = srv.Booking.Waitlist(ctx)
	do(ann, "POST", "/booking/wait/"+wl[0].ID, url.Values{"date": {day}, "client_name": {"Пётр Иванов"}}, 200)
	if w, _ := srv.Booking.Wait(ctx, wl[0].ID); w.ClientName != "Пётр Иванов" {
		t.Fatalf("a waiting entry changed: %+v", w)
	}
	do(ann, "POST", "/booking/wait/"+wl[0].ID+"/delete", url.Values{"date": {day}}, 200)
	if wl, _ := srv.Booking.Waitlist(ctx); len(wl) != 0 {
		t.Fatalf("a waiting entry deleted: %+v", wl)
	}
}

// TestBookingLinksHTTP: an administrator makes a link for clients, an employee cannot; a client without signing in
// opens it, sees the free slots of the day, books one — off to the booking's own page, the booking is «не
// подтверждена» on the grid, the responsible one is notified; the same slot again and a robot's form are refused,
// a switched off link is gone.
func TestBookingLinksHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), BaseURL: "https://go24.test",
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
	// do: who "" is a client without signing in; returns the page and where it redirects
	do := func(who, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if who != "" {
			req.AddCookie(as[who])
			req.Header.Set("X-Go24-Form", "1")
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

	types, err := srv.Booking.Types(ctx)
	must(err)
	all := []int{1, 2, 3, 4, 5, 6, 7}
	doc, err := srv.Booking.SaveResource(ctx, boss, booking.Resource{TypeID: types[0].ID, Name: "Врач Иванова", UserID: ann, Slot: 30, Days: all, From: 540, To: 660})
	must(err)
	_, err = srv.Booking.SaveResource(ctx, boss, booking.Resource{TypeID: types[1].ID, Name: "Кабинет", Slot: 60, Days: all, From: 540, To: 660})
	must(err)

	link := url.Values{"name": {"Запись к врачу"}, "description": {"Приём 30 минут"}, "resources": {doc}, "days_ahead": {"7"}, "active": {"1"}}
	do(ann, "POST", "/booking/links", link, 403)
	do(boss, "GET", "/booking/links?edit=new", nil, 200)
	do(boss, "POST", "/booking/links", link, 200)
	links, err := srv.Booking.Links(ctx)
	must(err)
	if len(links) != 1 || links[0].DaysAhead != 7 || len(links[0].Resources) != 1 {
		t.Fatalf("links: %+v", links)
	}
	tok := links[0].Token
	if page, _ := do(ann, "GET", "/booking/links", nil, 200); !strings.Contains(page, "https://go24.test/bk/"+tok) {
		t.Fatal("the link's address is not on the page")
	}

	// the client: one resource — straight to its days and slots
	day := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	page, _ := do("", "GET", "/bk/"+tok+"?date="+day, nil, 200)
	if !strings.Contains(page, "Запись к врачу") || !strings.Contains(page, "Приём 30 минут") || strings.Contains(page, "Кабинет") ||
		!strings.Contains(page, "&t=09%3a30") || !strings.Contains(page, "&t=10%3a30") {
		t.Fatalf("the public page: %s", page)
	}
	if page, _ := do("", "GET", "/bk/"+tok+"?date="+day+"&t=09:30", nil, 200); !strings.Contains(page, `name="phone"`) {
		t.Fatal("no form with a slot chosen")
	}
	form := url.Values{"r": {doc}, "date": {day}, "t": {"09:30"}, "name": {"Пётр Сидоров"}, "phone": {"+7 900 111-22-33"}, "email": {"p@go24.test"}}
	bot := url.Values{"website": {"http://spam"}}
	for k, v := range form {
		bot[k] = v
	}
	if _, loc := do("", "POST", "/bk/"+tok, bot, 303); !strings.HasPrefix(loc, "/bk/"+tok+"?") {
		t.Fatalf("a robot's form: %s", loc)
	}
	_, loc := do("", "POST", "/bk/"+tok, form, 303)
	if !strings.HasPrefix(loc, "/bk/b/") {
		t.Fatalf("after booking: %s", loc)
	}
	if page, _ := do("", "GET", loc, nil, 200); !strings.Contains(page, "Вы записаны") || !strings.Contains(page, "09:30–10:00") || !strings.Contains(page, "Не подтверждена") {
		t.Fatalf("the client's page: %s", page)
	}
	if page, _ := do("", "GET", "/bk/"+tok+"?date="+day, nil, 200); strings.Contains(page, "&t=09%3a30") {
		t.Fatal("the booked slot is still free")
	}
	_, loc = do("", "POST", "/bk/"+tok, form, 303)
	if page, _ := do("", "GET", loc, nil, 200); !strings.Contains(page, "уже недоступно") || !strings.Contains(page, "phone=%2B7") {
		t.Fatalf("the same slot again: %s %s", loc, page)
	}
	if page, _ := do(boss, "GET", "/booking?date="+day, nil, 200); !strings.Contains(page, "Пётр Сидоров") || !strings.Contains(page, "Клиент по ссылке «Запись к врачу»") {
		t.Fatalf("the booking is not on the grid: %s", page)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'booking_new' AND body LIKE 'Пётр Сидоров%'`, ann).Scan(&n))
	if n != 1 {
		t.Fatalf("notifications of the resource's employee: %d", n)
	}

	// the client confirms from the page, then cancels; the employee is told of both; then nothing to change
	var cli string
	must(pool.QueryRow(ctx, `SELECT client_token FROM bookings WHERE client_name = 'Пётр Сидоров'`).Scan(&cli))
	if page, _ := do("", "GET", "/bk/b/"+cli, nil, 200); !strings.Contains(page, "Подтвердить запись") || !strings.Contains(page, "Отменить запись") {
		t.Fatalf("no buttons on the client's page: %s", page)
	}
	do("", "POST", "/bk/b/"+cli+"/status", url.Values{"status": {"confirmed"}}, 303)
	do("", "POST", "/bk/b/"+cli+"/status", url.Values{"status": {"confirmed"}}, 303) // again: told once
	if page, _ := do("", "GET", "/bk/b/"+cli, nil, 200); strings.Contains(page, "Подтвердить запись") || !strings.Contains(page, "Подтверждена") {
		t.Fatalf("the client's page after confirming: %s", page)
	}
	do("", "POST", "/bk/b/"+cli+"/status", url.Values{"status": {"cancelled"}}, 303)
	_, loc = do("", "POST", "/bk/b/"+cli+"/status", url.Values{"status": {"confirmed"}}, 303)
	if page, _ := do("", "GET", loc, nil, 200); !strings.Contains(page, "уже не изменить") || strings.Contains(page, "Отменить запись") {
		t.Fatalf("a cancelled booking confirmed: %s", page)
	}
	do("", "POST", "/bk/b/nope/status", url.Values{"status": {"cancelled"}}, 404)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind IN ('booking_confirmed', 'booking_cancelled')`, ann).Scan(&n))
	if n != 2 {
		t.Fatalf("notifications of the client's confirm and cancel: %d", n)
	}
	// the resource's form keeps the letters' settings
	res := url.Values{"name": {"Врач Иванова"}, "type": {types[0].ID}, "user": {ann}, "slot": {"30"}, "from": {"09:00"}, "to": {"11:00"},
		"days": {"1", "2", "3", "4", "5", "6", "7"}, "remind_hours": {"3"}, "late_minutes": {"30"},
		"confirm_hours": {"48"}, "confirm_alert": {"6"}}
	do(boss, "POST", "/booking/resources/"+doc, res, 200)
	if r, _ := srv.Booking.Resource(ctx, doc); r.NotifyMail || r.RemindHours != 3 || r.LateMinutes != 30 || r.ConfirmHours != 48 || r.ConfirmAlert != 6 {
		t.Fatalf("the resource's letters: %+v", r)
	}
	if page, _ := do(boss, "GET", "/booking/resources?edit="+doc, nil, 200); !strings.Contains(page, `<option value="3" selected>`) ||
		!strings.Contains(page, `<option value="48" selected>`) || !strings.Contains(page, `<option value="6" selected>`) {
		t.Fatalf("the resource's form: %s", page)
	}

	// on a website (GO24DEV-688): the page in a frame — no go24 menu even for an employee, everything stays embedded
	day2 := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	page, _ = do(boss, "GET", "/bk/"+tok+"?embed=1&date="+day2, nil, 200)
	if !strings.Contains(page, "bkembed.js") || !strings.Contains(page, "bk-embed") || strings.Contains(page, "data-bell") ||
		!strings.Contains(page, "&t=09%3a00&amp;embed=1#form") {
		t.Fatalf("the embedded page: %s", page)
	}
	if page, _ := do("", "GET", "/bk/"+tok+"?date="+day2, nil, 200); strings.Contains(page, "bkembed.js") || strings.Contains(page, "embed=1") {
		t.Fatal("the page out of a frame is embedded")
	}
	if page, _ := do("", "GET", "/bk/"+tok+"?embed=1&date="+day2+"&t=09:00", nil, 200); !strings.Contains(page, `name="embed" value="1"`) {
		t.Fatal("the form in a frame does not keep it")
	}
	emb := url.Values{"r": {doc}, "date": {day2}, "t": {"09:00"}, "name": {"Ольга"}, "phone": {"+7 900 555-00-00"}, "embed": {"1"}}
	_, loc = do("", "POST", "/bk/"+tok, emb, 303)
	if !strings.HasPrefix(loc, "/bk/b/") || !strings.HasSuffix(loc, "?embed=1") {
		t.Fatalf("after booking in a frame: %s", loc)
	}
	if page, _ := do("", "GET", loc, nil, 200); !strings.Contains(page, "bkembed.js") || !strings.Contains(page, `name="embed" value="1"`) {
		t.Fatalf("the client's page in a frame: %s", page)
	}
	if _, l2 := do("", "POST", "/bk/"+tok, emb, 303); !strings.Contains(l2, "embed=1") || !strings.HasPrefix(l2, "/bk/"+tok+"?") {
		t.Fatalf("a refusal in a frame: %s", l2)
	}
	cli2 := strings.TrimSuffix(strings.TrimPrefix(loc, "/bk/b/"), "?embed=1")
	if _, l2 := do("", "POST", "/bk/b/"+cli2+"/status", url.Values{"status": {"cancelled"}, "embed": {"1"}}, 303); l2 != "/bk/b/"+cli2+"?embed=1" {
		t.Fatalf("cancelled in a frame: %s", l2)
	}
	if page, _ := do("", "GET", "/bk/nope?embed=1", nil, 404); strings.Contains(page, "data-bell") {
		t.Fatal("a gone link in a frame shows go24's menu")
	}
	// «Код для сайта» and the QR code
	if page, _ := do(ann, "GET", "/booking/links", nil, 200); !strings.Contains(page, "https://go24.test/bk/"+tok+"?embed=1") ||
		!strings.Contains(page, "data-go24-booking") || !strings.Contains(page, "/booking/links/"+links[0].ID+"/qr") {
		t.Fatalf("no code for a website: %s", page)
	}
	if png, _ := do(ann, "GET", "/booking/links/"+links[0].ID+"/qr", nil, 200); !strings.HasPrefix(png, "\x89PNG") {
		t.Fatal("the QR code is not a PNG")
	}
	do(ann, "GET", "/booking/links/00000000-0000-0000-0000-000000000000/qr", nil, 404)
	do("", "GET", "/booking/links/"+links[0].ID+"/qr", nil, 302)

	// switched off — the page is gone
	link.Del("active")
	do(boss, "POST", "/booking/links/"+links[0].ID, link, 200)
	do("", "GET", "/bk/"+tok, nil, 404)
	do("", "POST", "/bk/"+tok, form, 404)
}
