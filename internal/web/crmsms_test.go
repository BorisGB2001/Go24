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
	"go24/internal/sms"
	"go24/internal/tasks"
)

// TestCRMSMSHTTP: only an administrator connects a provider (an own gateway here, a real HTTP one) and tests it; the
// card offers «SMS» with the client's phones once there is a provider, the message goes to the gateway and lands in
// the timeline, a refusal too; the robot «Отправить SMS клиенту» writes to the client's first phone; REST.
func TestCRMSMSHTTP(t *testing.T) {
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
	var got []map[string]string
	refuse := false
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		defer mu.Unlock()
		m["auth"] = r.Header.Get("Authorization")
		got = append(got, m)
		if refuse {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"no balance"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":42}`))
	}))
	defer gw.Close()
	last := func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			return nil
		}
		return got[len(got)-1]
	}

	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), SMS: sms.NewStore(pool, "secret"), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.CRM.SendSMS = func(ctx context.Context, userID, entity, entityID, phone, text string) error {
		_, err := srv.SMS.Send(ctx, sms.Out{Phone: phone, Text: text, AuthorID: userID, Entity: entity, EntityID: entityID})
		return err
	}
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Name: "Олег", Phone: "8 900 555-44-33", Responsible: crm.Ref{ID: anna}})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(cookie *http.Cookie, path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := client.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Location")
	}
	admin, worker := cookieOf(anna, true), cookieOf(petr, false)
	card := "/crm/leads/" + lead

	if _, body, _ := do(admin, card, nil); strings.Contains(body, `action="`+card+`/sms"`) {
		t.Fatal("no provider — no «SMS» in the card")
	}
	add := url.Values{"op": {"save"}, "kind": {"gateway"}, "name": {"Шлюз"}, "login": {gw.URL}, "secret": {"TOKEN"}, "sender": {"Shop"}, "active": {"1"}}
	do(worker, "/crm/sms", add)
	if srv.SMS.Ready(ctx) {
		t.Fatal("providers are the administrators'")
	}
	if c, _, loc := do(admin, "/crm/sms", add); c != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("add a provider: %d %s", c, loc)
	}
	list, _ := srv.SMS.Senders(ctx)
	if len(list) != 1 || !list[0].Default {
		t.Fatalf("the provider: %+v", list)
	}
	if _, body, _ := do(worker, "/crm/sms", nil); !strings.Contains(body, "Шлюз") || strings.Contains(body, "TOKEN") || strings.Contains(body, `name="op" value="delete"`) {
		t.Fatal("everyone sees the providers, without keys; only administrators change them")
	}
	if c, _, loc := do(admin, "/crm/sms", url.Values{"op": {"test"}, "id": {list[0].ID}, "phone": {"+7 911 000-00-01"}}); c != http.StatusSeeOther ||
		strings.Contains(loc, "no+balance") || last()["phone"] != "+79110000001" || last()["auth"] != "Bearer TOKEN" || last()["sender"] != "Shop" {
		t.Fatalf("a test SMS: %d %s %v", c, loc, last())
	}

	if _, body, _ := do(worker, card, nil); !strings.Contains(body, `action="`+card+`/sms"`) || !strings.Contains(body, "8 900 555-44-33") {
		t.Fatal("the card offers «SMS» to the client's phone")
	}
	if c, _, loc := do(worker, card+"/sms", url.Values{"phone": {"8 900 555-44-33"}, "text": {"Ваш заказ готов"}}); c != http.StatusSeeOther ||
		!strings.Contains(loc, "msg=") || last()["phone"] != "+79005554433" || last()["text"] != "Ваш заказ готов" {
		t.Fatalf("SMS from the card: %d %s %v", c, loc, last())
	}
	refuse = true
	if _, _, loc := do(worker, card+"/sms", url.Values{"phone": {"8 900 555-44-33"}, "text": {"Второе"}}); !strings.Contains(loc, "no+balance") {
		t.Fatalf("a refusal is an error: %s", loc)
	}
	refuse = false
	if _, body, _ := do(worker, card, nil); !strings.Contains(body, "Ваш заказ готов") || !strings.Contains(body, "Второе") || !strings.Contains(body, "no balance") {
		t.Fatal("the timeline has both, the refusal with its reason")
	}
	if _, _, loc := do(worker, card+"/sms", url.Values{"phone": {"8 900 555-44-33"}, "text": {""}}); !strings.Contains(loc, "msg=") || last()["text"] == "" {
		t.Fatal("an empty text is refused")
	}

	// the robot of a lead's stage
	stages, err := srv.CRM.LeadStages(ctx)
	must(err)
	must(srv.CRM.AddRobot(ctx, anna, stages[1].ID, "sms", crm.RobotParams{Text: "Здравствуйте, {client}! Заявка «{title}» в работе"}))
	_, err = srv.CRM.MoveLead(ctx, anna, lead, stages[1].ID)
	must(err)
	if m := last(); m["phone"] != "+79005554433" || !strings.Contains(m["text"], "Заявка «Заявка» в работе") {
		t.Fatalf("the robot's SMS: %v", m)
	}

	// REST
	token, err := srv.Keys.Create(ctx, petr, "n8n", apikeys.AllScopes, 0)
	must(err)
	api := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, out := api("GET", "/api/v1/crm/sms/senders", ""); c != http.StatusOK || !strings.Contains(out, `"gateway"`) || strings.Contains(out, "TOKEN") {
		t.Fatalf("REST senders: %d %s", c, out)
	}
	if c, out := api("POST", "/api/v1/crm/leads/"+lead+"/sms", `{"text":"Из n8n"}`); c != http.StatusOK || !strings.Contains(out, `+79005554433`) ||
		last()["text"] != "Из n8n" {
		t.Fatalf("REST sms: %d %s", c, out)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE kind = 'sms' AND entity_id = $1`, lead).Scan(&n))
	if n != 4 {
		t.Fatalf("the timeline's SMS: %d", n)
	}
}
