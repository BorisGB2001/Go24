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
	"go24/internal/openlines"
	"go24/internal/tasks"
)

// TestOLBookingHTTP: «Онлайн-запись» in a dialogue of an open line (GO24DEV-733) — the operator's «Онлайн-запись»
// sends a booking link, the widget shows it with the page inside the chat and the dialogue's key; the page is filled
// with the dialogue's client; the booking gets the dialogue's contact (none — the contact made of the client is bound
// to the dialogue), «Вы записаны» in the dialogue, the operator is told; the client's cancellation is a service line;
// REST sends the link too, an inactive link is refused.
func TestOLBookingHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Booking: booking.NewStore(pool), Keys: apikeys.NewStore(pool),
		BaseURL: "https://go24.test", Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.OpenLines = openlines.NewStore(pool, srv.CRM)
	srv.OpenLines.Tr = func(key string, args ...any) string { return key }
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	admin := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(signed bool, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if signed {
			req.AddCookie(admin)
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
	doc, err := srv.Booking.SaveResource(ctx, boss, booking.Resource{TypeID: types[0].ID, Name: "Врач Иванова", UserID: ann, Slot: 30,
		Days: []int{1, 2, 3, 4, 5, 6, 7}, From: 540, To: 660})
	must(err)
	do(true, "POST", "/booking/links", url.Values{"name": {"Запись к врачу"}, "resources": {doc}, "days_ahead": {"7"}, "active": {"1"}}, 303)
	do(true, "POST", "/booking/links", url.Values{"name": {"Старая ссылка"}, "resources": {doc}, "days_ahead": {"7"}}, 303)
	links, err := srv.Booking.Links(ctx)
	must(err)
	var lid, off, tok string
	for _, l := range links {
		if l.Active {
			lid, tok = l.ID, l.Token
		} else {
			off = l.ID
		}
	}

	wid, err := srv.OpenLines.SaveWidget(ctx, boss, openlines.Widget{Name: "Линия", Active: true})
	must(err)
	sid, vtok, err := srv.OpenLines.Visit(ctx, wid, "", "")
	must(err)
	must(srv.OpenLines.VisitorSay(ctx, wid, vtok, "Хочу к врачу"))
	var contact string
	must(pool.QueryRow(ctx, `INSERT INTO crm_contacts (name, responsible_id, created_by) VALUES ('Ольга Петрова', $1, $1) RETURNING id`, boss).Scan(&contact))
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET name = 'Ольга', phone = '+7 900 111-22-33', contact_id = $2 WHERE id = $1`, sid, contact)
	must(err)

	// the operator's «Онлайн-запись»: the active links only
	page, _ := do(true, "GET", "/openlines?s="+sid, nil, 200)
	if !strings.Contains(page, `name="op" value="booking"`) || !strings.Contains(page, `<option value="`+lid+`">Запись к врачу</option>`) || strings.Contains(page, "Старая ссылка") {
		t.Fatal("no «Онлайн-запись» in the dialogue")
	}
	if _, loc := do(true, "POST", "/openlines/s/"+sid, url.Values{"op": {"booking"}, "link": {off}}, 303); !strings.Contains(loc, "msg=") {
		t.Fatal("an inactive link is sent")
	}
	if _, loc := do(true, "POST", "/openlines/s/"+sid, url.Values{"op": {"booking"}, "link": {lid}}, 303); strings.Contains(loc, "msg=") {
		t.Fatalf("send the link: %s", loc)
	}
	widget := func() []struct {
		Body    string
		Booking *struct {
			ID, URL string
			Done    bool
		}
		BookingDone string `json:"booking_done"`
	} {
		req, _ := http.NewRequest("GET", hs.URL+"/w/"+wid+"/api/messages", nil)
		req.Header.Set("X-Go24-Visitor", vtok)
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		var got struct {
			Messages []struct {
				Body    string
				Booking *struct {
					ID, URL string
					Done    bool
				}
				BookingDone string `json:"booking_done"`
			}
		}
		must(json.NewDecoder(res.Body).Decode(&got))
		return got.Messages
	}
	msgs := widget()
	last := msgs[len(msgs)-1]
	if last.Booking == nil || last.Booking.ID != lid || last.Body != "ol.booking_body" || !strings.Contains(last.Booking.URL, "/bk/"+tok+"?embed=1&ol=") {
		t.Fatalf("the widget's booking card: %+v", last)
	}
	inChat, _ := url.Parse(last.Booking.URL)
	key := inChat.Query().Get("ol")

	// the page in the chat: the dialogue's client filled in, the key in the links and the form
	day := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	page, _ = do(false, "GET", inChat.RequestURI()+"&date="+day+"&t=10:00", nil, 200)
	if !strings.Contains(page, `value="Ольга"`) || !strings.Contains(page, `value="&#43;7 900 111-22-33"`) || !strings.Contains(page, `name="ol" value="`+key+`"`) ||
		!strings.Contains(page, "&amp;ol="+key) {
		t.Fatal("the page of the dialogue's link")
	}
	if page, _ := do(false, "GET", "/bk/"+tok+"?ol=bad&date="+day+"&t=10:00", nil, 200); strings.Contains(page, `value="Ольга"`) {
		t.Fatal("a wrong key shows the dialogue's client")
	}
	_, loc := do(false, "POST", "/bk/"+tok, url.Values{"r": {doc}, "date": {day}, "t": {"10:00"}, "name": {"Ольга"}, "phone": {"+7 900 111-22-33"},
		"email": {"olga@t"}, "embed": {"1"}, "ol": {key}}, 303)
	if !strings.HasPrefix(loc, "/bk/b/") || !strings.Contains(loc, "embed=1") {
		t.Fatalf("booked: %s", loc)
	}
	var bid, bContact, bSession, sEmail string
	must(pool.QueryRow(ctx, `SELECT b.id::text, coalesce(b.contact_id::text, ''), coalesce(b.ol_session_id::text, ''), s.email
		FROM bookings b, ol_sessions s WHERE s.id = $1`, sid).Scan(&bid, &bContact, &bSession, &sEmail))
	if bContact != contact || bSession != sid || sEmail != "olga@t" {
		t.Fatalf("the booking is not the dialogue's: %q %q %q", bContact, bSession, sEmail)
	}
	var events, told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'booking'`, contact).Scan(&events))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'openline' AND body = 'ol.booking_done'`, boss).Scan(&told))
	if events != 1 || told != 1 {
		t.Fatalf("CRM events %d, the operator told %d", events, told)
	}
	msgs = widget()
	if l := msgs[len(msgs)-1]; l.BookingDone != lid || msgs[len(msgs)-2].Booking == nil || !msgs[len(msgs)-2].Booking.Done {
		t.Fatalf("the widget after booking: %+v", msgs)
	}
	if page, _ := do(true, "GET", "/openlines?s="+sid, nil, 200); !strings.Contains(page, "ol.booking_booked") && !strings.Contains(page, "ol-formdone") {
		t.Fatal("the operator does not see the booking")
	}

	// the client cancels: a service line of the dialogue
	do(false, "POST", strings.SplitN(loc, "?", 2)[0]+"/status", url.Values{"status": {"cancelled"}}, 303)
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ol_messages WHERE session_id = $1 AND kind = 'note' AND body = 'ol.note_booking_cancel'`, sid).Scan(&notes))
	if notes != 1 {
		t.Fatalf("cancel notes: %d", notes)
	}

	// a dialogue without a contact: the contact made of the client is bound to it; REST sends the link
	sid2, vtok2, err := srv.OpenLines.Visit(ctx, wid, "", "")
	must(err)
	must(srv.OpenLines.VisitorSay(ctx, wid, vtok2, "Запишите"))
	wtok, err := srv.Keys.Create(ctx, boss, "ol", []string{"openlines:read", "openlines:write"}, 0)
	must(err)
	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/openlines/dialogs/"+sid2+"/booking", strings.NewReader(`{"booking_link_id": "`+lid+`"}`))
	req.Header.Set("Authorization", "Bearer "+wtok)
	res, err := client.Do(req)
	must(err)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("REST booking: %d", res.StatusCode)
	}
	must(pool.QueryRow(ctx, `SELECT form_secret FROM ol_messages WHERE session_id = $1 AND kind = 'booking'`, sid2).Scan(&key))
	do(false, "POST", "/bk/"+tok, url.Values{"r": {doc}, "date": {day}, "t": {"10:30"}, "name": {"Пётр"}, "phone": {"+7 900 222-33-44"}, "ol": {key}}, 303)
	var c2, bc2 string
	must(pool.QueryRow(ctx, `SELECT coalesce(s.contact_id::text, ''), coalesce(b.contact_id::text, '') FROM ol_sessions s
		JOIN bookings b ON b.ol_session_id = s.id WHERE s.id = $1`, sid2).Scan(&c2, &bc2))
	if c2 == "" || c2 != bc2 || c2 == contact {
		t.Fatalf("the new contact: session %q booking %q", c2, bc2)
	}
	req, _ = http.NewRequest("GET", hs.URL+"/api/v1/openlines/dialogs/"+sid2+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+wtok)
	res, err = client.Do(req)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var flat bytes.Buffer
	must(json.Compact(&flat, raw))
	if raw = flat.Bytes(); !strings.Contains(string(raw), `"kind":"booking"`) || !strings.Contains(string(raw), `"booking":{"id":"`+lid+`","done_at":`) {
		t.Fatalf("REST messages: %s", raw)
	}
}
