package web

import (
	"bytes"
	"context"
	"encoding/json"
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
	"go24/internal/booking"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBookingAPI: «Онлайн-запись» through REST API v1, as booking.v1.* — types and resources (administrators
// only), free slots, bookings with more resources, a contact and its status, the overlap, the waiting list there and
// back, the statistics; the key's scopes are kept.
func TestBookingAPI(t *testing.T) {
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
	user := func(name string, groups ...string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ($1, $1, $1 || '@t', $2) RETURNING id`,
			name, append([]string{}, groups...)).Scan(&id))
		return id
	}
	boss, anna := user("Босс", "admins"), user("Анна")
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	bossKey, annaKey, readOnly := key(boss, "booking:read", "booking:write"), key(anna, "booking:read", "booking:write"), key(anna, "booking:read")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), CRM: crm.NewStore(pool), Keys: keys,
		AdminGroups: []string{"admins"}, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(method, path, token string, body any, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.StatusCode, raw, want)
		}
		return raw
	}
	decode := func(raw []byte, v any) {
		t.Helper()
		must(json.Unmarshal(raw, v))
	}

	// types: the standard ones, one of its own — by an administrator only
	var types []apiBkType
	decode(call("GET", "/api/v1/booking/types", readOnly, nil, 200), &types)
	if len(types) != 3 || types[0].Code != "employee" || types[0].Name != "Сотрудник" {
		t.Fatalf("types: %+v", types)
	}
	call("POST", "/api/v1/booking/types", annaKey, map[string]string{"name": "Тренажёры"}, 403)
	var made map[string]string
	decode(call("POST", "/api/v1/booking/types", bossKey, map[string]string{"name": "Тренажёры"}, 201), &made)
	call("PUT", "/api/v1/booking/types/"+made["id"], bossKey, map[string]string{"name": "Тренажёры и снаряды"}, 200)
	call("DELETE", "/api/v1/booking/types/"+made["id"], bossKey, nil, 204)

	// resources: a new one with the defaults and its own hours, a change of some fields
	call("POST", "/api/v1/booking/resources", annaKey, map[string]any{"name": "Мастер"}, 403)
	var master, room apiBkResource
	decode(call("POST", "/api/v1/booking/resources", bossKey, map[string]any{"name": "Мастер", "user_id": anna,
		"days": []int{1, 2, 3, 4, 5, 6, 7}, "from": "10:00", "to": "20:00", "slot_minutes": 60}, 201), &master)
	if master.TypeCode != "employee" || master.From != "10:00" || master.To != "20:00" || master.Slot != 60 || master.UserName != "Анна" ||
		!master.NotifyMail || master.RemindHours != 24 || len(master.Days) != 7 {
		t.Fatalf("master: %+v", master)
	}
	decode(call("POST", "/api/v1/booking/resources", bossKey, map[string]any{"name": "Кабинет", "type_id": types[1].ID,
		"days": []int{1, 2, 3, 4, 5, 6, 7}, "to": "00:00"}, 201), &room)
	if room.TypeCode != "room" || room.From != "09:00" || room.To != "24:00" || room.Slot != 30 {
		t.Fatalf("room: %+v", room)
	}
	decode(call("PUT", "/api/v1/booking/resources/"+room.ID, bossKey, map[string]any{"to": "18:00", "description": "2 этаж"}, 200), &room)
	if room.To != "18:00" || room.Description != "2 этаж" || room.Name != "Кабинет" || room.Slot != 30 {
		t.Fatalf("room changed: %+v", room)
	}
	call("PUT", "/api/v1/booking/resources/"+room.ID, bossKey, map[string]any{"from": "19:00"}, 422)
	var list []apiBkResource
	decode(call("GET", "/api/v1/booking/resources?type_id="+types[1].ID, readOnly, nil, 200), &list)
	if len(list) != 1 || list[0].ID != room.ID {
		t.Fatalf("resources of a type: %+v", list)
	}

	// bookings: the master and the room at once, with a contact; the overlap; a status
	contact, err := srv.CRM.SaveContact(ctx, boss, crm.Contact{Name: "Ольга Петрова", Phone: "+7 900 555-11-22", Responsible: crm.Ref{ID: boss}})
	must(err)
	day := time.Now().AddDate(0, 0, 2)
	at := func(h int) time.Time { return time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, time.Local) }
	date := day.Format("2006-01-02")
	call("POST", "/api/v1/booking/bookings", readOnly, map[string]any{"resource_id": master.ID, "start": at(11), "client_name": "К"}, 403)
	call("POST", "/api/v1/booking/bookings", bossKey, map[string]any{"resource_id": master.ID, "client_name": "К"}, 422)
	var b apiBkBooking
	decode(call("POST", "/api/v1/booking/bookings", bossKey, map[string]any{"resource_id": master.ID, "extra_resource_ids": []string{room.ID},
		"start": at(11), "contact_id": contact, "note": "Стрижка"}, 201), &b)
	if b.ClientName != "Ольга Петрова" || b.ContactID != contact || !b.End.Equal(at(12)) || b.Status != "unconfirmed" ||
		len(b.Extras) != 1 || b.Extras[0] != room.ID || !strings.HasPrefix(b.ClientURL, "https://go24.test/bk/b/") {
		t.Fatalf("booking: %+v", b)
	}
	var tl int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1`, contact).Scan(&tl))
	if tl == 0 {
		t.Fatal("no «Запись клиента» in the contact's timeline")
	}
	call("POST", "/api/v1/booking/bookings", annaKey, map[string]any{"resource_id": room.ID, "start": at(11).Add(30 * time.Minute),
		"client_name": "Иван"}, 409)
	var slots struct {
		Free []time.Time `json:"free"`
	}
	decode(call("GET", "/api/v1/booking/resources/"+master.ID+"/slots?date="+date, readOnly, nil, 200), &slots)
	if len(slots.Free) != 9 || slots.Free[0].Hour() != 10 || slots.Free[1].Hour() != 12 {
		t.Fatalf("slots: %+v", slots.Free)
	}
	decode(call("PUT", "/api/v1/booking/bookings/"+b.ID, annaKey, map[string]any{"start": at(14)}, 200), &b)
	if !b.Start.Equal(at(14)) || !b.End.Equal(at(15)) || b.ClientName != "Ольга Петрова" || len(b.Extras) != 1 {
		t.Fatalf("moved: %+v", b)
	}
	call("POST", "/api/v1/booking/bookings/"+b.ID+"/status", annaKey, map[string]string{"status": "lost"}, 422)
	decode(call("POST", "/api/v1/booking/bookings/"+b.ID+"/status", annaKey, map[string]string{"status": "came"}, 200), &b)
	var all []apiBkBooking
	decode(call("GET", "/api/v1/booking/bookings?from="+date+"&to="+date+"&resource_id="+room.ID, readOnly, nil, 200), &all)
	if len(all) != 1 || all[0].Status != "came" {
		t.Fatalf("bookings of the room: %+v", all)
	}
	decode(call("GET", "/api/v1/booking/bookings?from="+date+"&to="+date+"&status=confirmed", readOnly, nil, 200), &all)
	if len(all) != 0 {
		t.Fatalf("confirmed: %+v", all)
	}

	// the waiting list: a client on it, booked from it, a booking back on it
	call("POST", "/api/v1/booking/waitlist", annaKey, map[string]any{"note": "любое время"}, 422)
	var wt apiBkWait
	decode(call("POST", "/api/v1/booking/waitlist", annaKey, map[string]any{"client_name": "Иван", "client_phone": "+7 900 1",
		"resource_id": master.ID}, 201), &wt)
	decode(call("PUT", "/api/v1/booking/waitlist/"+wt.ID, annaKey, map[string]any{"note": "после обеда"}, 200), &wt)
	if wt.ResourceID != master.ID || wt.Note != "после обеда" || wt.ClientName != "Иван" {
		t.Fatalf("wait: %+v", wt)
	}
	var b2 apiBkBooking
	decode(call("POST", "/api/v1/booking/waitlist/"+wt.ID+"/book", annaKey, map[string]any{"resource_id": master.ID, "start": at(16)}, 201), &b2)
	if b2.ClientName != "Иван" || !b2.End.Equal(at(17)) {
		t.Fatalf("booked from the list: %+v", b2)
	}
	call("GET", "/api/v1/booking/waitlist/"+wt.ID, readOnly, nil, 404)
	call("POST", "/api/v1/booking/bookings/"+b2.ID+"/status", annaKey, map[string]string{"status": "no_show"}, 200)

	// the statistics of the day: the master 2 of 10 hours, a half of no-shows; the room 1 of 9
	var st struct {
		Resources []apiBkStat `json:"resources"`
		Total     apiBkStat   `json:"total"`
	}
	decode(call("GET", "/api/v1/booking/stats?from="+date+"&to="+date, readOnly, nil, 200), &st)
	by := map[string]apiBkStat{}
	for _, x := range st.Resources {
		by[x.ResourceID] = x
	}
	if m, r := by[master.ID], by[room.ID]; len(st.Resources) != 2 || m.Work != 600 || m.Busy != 120 || m.Load != 20 ||
		m.NoShowShare == nil || *m.NoShowShare != 50 || r.Busy != 60 || st.Total.Bookings != 2 || st.Total.Statuses["came"] != 1 {
		t.Fatalf("stats: %+v", st)
	}

	var back apiBkWait
	decode(call("POST", "/api/v1/booking/bookings/"+b2.ID+"/wait", annaKey, nil, 201), &back)
	if back.ClientName != "Иван" || back.ResourceID != master.ID {
		t.Fatalf("back on the list: %+v", back)
	}
	call("GET", "/api/v1/booking/bookings/"+b2.ID, readOnly, nil, 404)
	var wl []apiBkWait
	decode(call("GET", "/api/v1/booking/waitlist", readOnly, nil, 200), &wl)
	if len(wl) != 1 {
		t.Fatalf("waitlist: %+v", wl)
	}
	call("DELETE", "/api/v1/booking/waitlist/"+back.ID, annaKey, nil, 204)
	call("DELETE", "/api/v1/booking/bookings/"+b.ID, annaKey, nil, 204)
	call("DELETE", "/api/v1/booking/resources/"+room.ID, annaKey, nil, 403)
	call("DELETE", "/api/v1/booking/resources/"+room.ID, bossKey, nil, 204)
}
