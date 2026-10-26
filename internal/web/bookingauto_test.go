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
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestBookingTriggersHTTP: «Онлайн-запись» in the CRM automation — a booking bound to a deal fires «Клиент
// записался», its statuses fire their triggers (in the grid, by REST, by the client on the booking's page), the deal
// goes only forward; the robot «Отправить ссылку для записи» writes to the client with the link's address, a link
// switched off is the robot's failure.
func TestBookingTriggersHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), CRM: crm.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	type letter struct{ to, subject, text string }
	var sent []letter
	srv.CRM.SendMail = func(_ context.Context, _, to, subject, text string) error {
		sent = append(sent, letter{to, subject, text})
		return nil
	}
	srv.CRM.BookingURL = func(ctx context.Context, id string) (string, error) {
		l, err := srv.Booking.Link(ctx, id)
		if err == nil && !l.Active {
			err = i18n.E("err.robot_booking")
		}
		return "https://go24.test/bk/" + l.Token, err
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values, auth bool) string {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(form.Encode()))
		if auth {
			req.AddCookie(cookie)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST %s: %d: %.300s", path, res.StatusCode, raw)
		}
		return res.Header.Get("Location")
	}

	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	st := pipes[0].Stages // new, preparation, prepayment, executing, final_invoice, won, lose
	must(srv.CRM.AddTrigger(ctx, boss, st[1].ID, "booking"))
	must(srv.CRM.AddTrigger(ctx, boss, st[2].ID, "booking_confirmed"))
	must(srv.CRM.AddTrigger(ctx, boss, st[3].ID, "booking_came"))
	must(srv.CRM.AddTrigger(ctx, boss, st[4].ID, "booking_cancelled"))
	stageOf := func(id string) string {
		t.Helper()
		d, err := srv.CRM.Deal(ctx, id)
		must(err)
		return d.Stage.ID
	}
	contact, err := srv.CRM.SaveContact(ctx, boss, crm.Contact{Name: "Ольга", Email: "olga@x.test", Responsible: crm.Ref{ID: boss}})
	must(err)
	newDeal := func(title string) string {
		id, err := srv.CRM.SaveDeal(ctx, boss, crm.Deal{Title: title, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss},
			Contacts: []crm.Ref{{ID: contact}}})
		must(err)
		return id
	}
	types, err := srv.Booking.Types(ctx)
	must(err)
	room, err := srv.Booking.SaveResource(ctx, boss, booking.Resource{TypeID: types[1].ID, Name: "Кабинет 1", Slot: 30,
		Days: []int{1, 2, 3, 4, 5, 6, 7}, From: 540, To: 1080})
	must(err)
	day := time.Now().AddDate(0, 0, 1).Format("2006-01-02")

	// booked for the deal → «Клиент записался»; confirmed → on; came → on; cancelled after — the deal is further already
	deal := newDeal("Стрижка")
	post("/booking", url.Values{"date": {day}, "time": {"10:00"}, "minutes": {"30"}, "resource": {room}, "deal_id": {deal}}, true)
	if stageOf(deal) != st[1].ID {
		t.Fatal("«Клиент записался» does not move the deal")
	}
	list, err := srv.Booking.OfCard(ctx, "deal", deal)
	must(err)
	b := list[0]
	post("/booking/item/"+b.ID+"/status", url.Values{"status": {"confirmed"}}, true)
	if stageOf(deal) != st[2].ID {
		t.Fatal("«Клиент подтвердил запись» does not move the deal")
	}
	// saved again with the same deal and status — nothing fires again
	must(srv.CRM.AddTrigger(ctx, boss, st[5].ID, "booking"))
	post("/booking/item/"+b.ID, url.Values{"date": {day}, "time": {"10:30"}, "minutes": {"30"}, "resource": {room},
		"deal_id": {deal}, "status": {"confirmed"}}, true)
	if stageOf(deal) != st[2].ID {
		t.Fatal("the same booking saved again fires its triggers again")
	}
	must(srv.CRM.DeleteTrigger(ctx, triggerOf(t, srv, st[5].ID)))
	post("/booking/item/"+b.ID+"/status", url.Values{"status": {"came"}}, true)
	if stageOf(deal) != st[3].ID {
		t.Fatal("«Клиент пришёл» does not move the deal")
	}

	// the client cancels from the booking's page → «Запись отменена»
	deal2 := newDeal("Окрашивание")
	post("/booking", url.Values{"date": {day}, "time": {"12:00"}, "minutes": {"30"}, "resource": {room}, "deal_id": {deal2}}, true)
	list, err = srv.Booking.OfCard(ctx, "deal", deal2)
	must(err)
	post("/bk/b/"+list[0].ClientToken+"/status", url.Values{"status": {"cancelled"}}, false)
	if stageOf(deal2) != st[4].ID {
		t.Fatal("the client's cancelling does not move the deal")
	}

	// the robot sends the booking link; {booking} is where the address goes
	link, err := srv.Booking.SaveLink(ctx, boss, booking.Link{Name: "Запись к мастеру", DaysAhead: 7, Active: true})
	must(err)
	l, err := srv.Booking.Link(ctx, link)
	must(err)
	if err := srv.CRM.AddRobot(ctx, boss, st[1].ID, "booking", crm.RobotParams{Text: "Запишитесь", Link: "nope"}); err == nil {
		t.Fatal("a robot with no link")
	}
	must(srv.CRM.AddRobot(ctx, boss, st[1].ID, "booking", crm.RobotParams{Text: "Запишитесь, {client}",
		Details: "Выберите время: {booking}", Link: link}))
	deal3 := newDeal("Маникюр")
	_, err = srv.CRM.MoveDeal(ctx, boss, deal3, st[1].ID)
	must(err)
	if len(sent) != 1 || sent[0].to != "olga@x.test" || sent[0].subject != "Запишитесь, Ольга" ||
		sent[0].text != "Выберите время: https://go24.test/bk/"+l.Token {
		t.Fatalf("the robot's letter: %+v", sent)
	}
	// the link switched off — the robot fails, the history says so
	l.Active = false
	_, err = srv.Booking.SaveLink(ctx, boss, l)
	must(err)
	deal4 := newDeal("Педикюр")
	_, err = srv.CRM.MoveDeal(ctx, boss, deal4, st[1].ID)
	must(err)
	var fails int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id::text = $1 AND kind = 'robot'
		AND body = 'fail:booking:err.robot_booking'`, deal4).Scan(&fails))
	if len(sent) != 1 || fails != 1 {
		t.Fatalf("a link switched off: %d letters, %d failures", len(sent), fails)
	}
}

func triggerOf(t *testing.T, srv *Server, stage string) string {
	t.Helper()
	trs, err := srv.CRM.Triggers(context.Background(), []string{stage})
	if err != nil || len(trs) != 1 {
		t.Fatalf("the trigger of %s: %v %+v", stage, err, trs)
	}
	return trs[0].ID
}
